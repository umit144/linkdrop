package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"
)

var (
	b             *tele.Bot
	userState     = make(map[int64]string)
	progressRegex = regexp.MustCompile(`(\d+\.\d+)%`)
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		slog.Error("TELEGRAM_BOT_TOKEN missing")
		os.Exit(1)
	}

	var err error
	b, err = tele.NewBot(tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		slog.Error("Start failed", "err", err)
		return
	}

	selector := &tele.ReplyMarkup{}
	btnVideo := selector.Data("🎬 Video (1080p)", "menu_vid")
	btnAudio := selector.Data("🎵 Audio (MP3)", "menu_aud")

	b.Handle("/start", func(c tele.Context) error {
		instructions := "👋 **Welcome to LinkDrop!**\n\n" +
			"I can download videos and audio from:\n" +
			"• YouTube & YT Shorts\n" +
			"• Instagram (Reels & Posts)\n" +
			"• X (Twitter)\n\n" +
			"🚀 **How to use:** Just paste a link here and I'll do the rest!"
		return c.Send(instructions, tele.ModeMarkdown)
	})

	b.Handle(tele.OnText, func(c tele.Context) error {
		url := strings.TrimSpace(c.Text())
		if !strings.Contains(url, "http") {
			return nil
		}
		u := c.Sender()
		slog.Info("Link", "user_id", u.ID, "user", u.Username, "url", url)
		userState[u.ID] = url
		selector.Inline(selector.Row(btnVideo, btnAudio))
		return c.Send("Select format:", selector)
	})

	b.Handle(&btnVideo, func(c tele.Context) error { return downloadAndSend(c, "video") })
	b.Handle(&btnAudio, func(c tele.Context) error { return downloadAndSend(c, "audio") })

	slog.Info("Bot started")
	b.Start()
}

func downloadAndSend(c tele.Context, mode string) error {
	u := c.Sender()
	url, ok := userState[u.ID]
	if !ok {
		return c.Send("Please send the link again.")
	}

	msg, _ := b.Send(c.Recipient(), fmt.Sprintf("⏳ Initializing %s...", mode))

	outputFile := fmt.Sprintf("dl_%d_%d", u.ID, time.Now().Unix())
	if mode == "audio" {
		outputFile += ".mp3"
	} else {
		outputFile += ".mp4"
	}

	defer os.Remove(outputFile)
	defer delete(userState, u.ID)

	var args []string
	if mode == "audio" {
		args = []string{
			"-x",
			"--audio-format", "mp3",
			"--max-filesize", "50M",
			"-o", outputFile,
			"--newline",
			url,
		}
	} else {
		args = []string{
			"-f", "bestvideo[height<=1080][vcodec^=avc1]+bestaudio[ext=m4a]/best[height<=1080][vcodec^=avc1]/best",
			"--merge-output-format", "mp4",
			"--max-filesize", "50M",
			"-o", outputFile,
			"--newline",
			url,
		}
	}

	cmd := exec.Command("yt-dlp", args...)
	stdout, _ := cmd.StdoutPipe()
	cmd.Start()

	scanner := bufio.NewScanner(stdout)
	lastUpdate := time.Now()

	for scanner.Scan() {
		line := scanner.Text()
		matches := progressRegex.FindStringSubmatch(line)
		if len(matches) > 1 {
			if time.Since(lastUpdate) > 3*time.Second {
				b.Edit(msg, fmt.Sprintf("⏳ Downloading %s: %s%%", mode, matches[1]))
				lastUpdate = time.Now()
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		slog.Error("yt-dlp error", "err", err)
		b.Edit(msg, "❌ File exceeds 50MB limit or download failed.")
		return err
	}

	b.Edit(msg, "📤 Uploading to Telegram...")

	var sendErr error
	if mode == "audio" {
		sendErr = c.Send(&tele.Audio{File: tele.FromDisk(outputFile)})
	} else {
		sendErr = c.Send(&tele.Video{File: tele.FromDisk(outputFile)})
	}

	if sendErr != nil {
		slog.Error("Upload error", "err", sendErr)
		b.Edit(msg, "❌ Telegram upload failed (Max 50MB).")
	} else {
		b.Delete(msg)
	}

	return sendErr
}
