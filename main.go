package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	tele "gopkg.in/telebot.v3"
)

const (
	maxFileSize         = "50M"
	defaultDownloadTimeout  = 10 * time.Minute
	defaultProgressInterval = 3 * time.Second
	defaultCleanupInterval  = 5 * time.Minute
	stateTTL            = 30 * time.Minute
	tempDir             = "/tmp/linkdrop"
	maxConcurrent       = 10
)

var progressRegex = regexp.MustCompile(`(\d+\.\d+)%`)

// telegramBot is the subset of tele.Bot used by Bot. Enables mocking in tests.
type telegramBot interface {
	Send(to tele.Recipient, what interface{}, opts ...interface{}) (*tele.Message, error)
	Edit(msg tele.Editable, what interface{}, opts ...interface{}) (*tele.Message, error)
	Delete(msg tele.Editable) error
	Handle(endpoint interface{}, h tele.HandlerFunc, m ...tele.MiddlewareFunc)
	Start()
	Stop()
}

type pendingURL struct {
	url       string
	expiresAt time.Time
}

type Bot struct {
	tele             telegramBot
	userState        sync.Map     // map[int64]pendingURL
	activeUsers      sync.Map     // map[int64]struct{}
	workerPool       chan struct{} // global concurrency cap
	selector         *tele.ReplyMarkup
	downloadTimeout  time.Duration
	progressInterval time.Duration
	cleanupInterval  time.Duration
	newCmd           func(ctx context.Context, name string, args ...string) *exec.Cmd
}

func newBot(tb telegramBot) *Bot {
	return &Bot{
		tele:             tb,
		workerPool:       make(chan struct{}, maxConcurrent),
		downloadTimeout:  defaultDownloadTimeout,
		progressInterval: defaultProgressInterval,
		cleanupInterval:  defaultCleanupInterval,
		newCmd:           exec.CommandContext,
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		slog.Error("TELEGRAM_BOT_TOKEN missing")
		os.Exit(1)
	}

	if err := os.RemoveAll(tempDir); err != nil {
		slog.Warn("failed to remove stale temp dir", "err", err)
	}
	if err := os.MkdirAll(tempDir, 0700); err != nil {
		slog.Error("failed to create temp dir", "err", err)
		os.Exit(1)
	}

	teleBot, err := tele.NewBot(tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		slog.Error("failed to create bot", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bot := newBot(teleBot)
	bot.registerHandlers()
	go bot.cleanupExpiredState(ctx)

	go func() {
		slog.Info("bot started")
		bot.tele.Start()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down")
	cancel()
	bot.tele.Stop()
}

// purgeExpired deletes userState entries whose TTL has elapsed.
func (bot *Bot) purgeExpired() {
	now := time.Now()
	bot.userState.Range(func(key, value any) bool {
		if value.(pendingURL).expiresAt.Before(now) {
			bot.userState.Delete(key)
		}
		return true
	})
}

// cleanupExpiredState runs purgeExpired on every cleanupInterval tick until ctx is cancelled.
func (bot *Bot) cleanupExpiredState(ctx context.Context) {
	ticker := time.NewTicker(bot.cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			bot.purgeExpired()
		}
	}
}

func (bot *Bot) registerHandlers() {
	selector := &tele.ReplyMarkup{}
	btnVideo := selector.Data("🎬 Video (1080p)", "menu_vid")
	btnAudio := selector.Data("🎵 Audio (MP3)", "menu_aud")
	selector.Inline(selector.Row(btnVideo, btnAudio))
	bot.selector = selector

	bot.tele.Handle("/start", bot.handleStart)
	bot.tele.Handle(tele.OnText, bot.handleText)
	bot.tele.Handle(&btnVideo, bot.handleVideo)
	bot.tele.Handle(&btnAudio, bot.handleAudio)
}

func (bot *Bot) handleStart(c tele.Context) error {
	msg := "👋 **Welcome to LinkDrop!**\n\n" +
		"I can download videos and audio from:\n" +
		"• YouTube & YT Shorts\n" +
		"• Instagram (Reels & Posts)\n" +
		"• X (Twitter)\n\n" +
		"🚀 **How to use:** Just paste a link here and I'll do the rest!"
	return c.Send(msg, tele.ModeMarkdown)
}

func (bot *Bot) handleText(c tele.Context) error {
	rawURL := strings.TrimSpace(c.Text())
	if !isValidURL(rawURL) {
		return nil
	}
	u := c.Sender()
	slog.Info("link received", "user_id", u.ID, "username", u.Username, "url", rawURL)
	bot.userState.Store(u.ID, pendingURL{
		url:       rawURL,
		expiresAt: time.Now().Add(stateTTL),
	})
	return c.Send("Select format:", bot.selector)
}

func (bot *Bot) handleVideo(c tele.Context) error { return bot.downloadAndSend(c, "video") }
func (bot *Bot) handleAudio(c tele.Context) error { return bot.downloadAndSend(c, "audio") }

func isValidURL(raw string) bool {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func (bot *Bot) downloadAndSend(c tele.Context, mode string) error {
	u := c.Sender()

	// Global concurrency cap: reject when all worker slots are full.
	select {
	case bot.workerPool <- struct{}{}:
		defer func() { <-bot.workerPool }()
	default:
		return c.Send("⚙️ Server is busy. Please try again in a moment.")
	}

	// Per-user rate limiting: one active download at a time.
	if _, alreadyActive := bot.activeUsers.LoadOrStore(u.ID, struct{}{}); alreadyActive {
		return c.Send("⏳ You already have a download in progress. Please wait.")
	}
	defer bot.activeUsers.Delete(u.ID)

	pending, ok := bot.userState.LoadAndDelete(u.ID)
	if !ok {
		return c.Send("Please send the link again.")
	}
	downloadURL := pending.(pendingURL).url

	msg, err := bot.tele.Send(c.Recipient(), fmt.Sprintf("⏳ Initializing %s download...", mode))
	if err != nil || msg == nil {
		slog.Error("failed to send init message", "err", err)
		return err
	}

	ext := "mp4"
	if mode == "audio" {
		ext = "mp3"
	}
	outputFile := filepath.Join(tempDir, fmt.Sprintf("dl_%d_%d.%s", u.ID, time.Now().Unix(), ext))
	defer os.Remove(outputFile)

	args := buildArgs(mode, outputFile, downloadURL)

	ctx, cancel := context.WithTimeout(context.Background(), bot.downloadTimeout)
	defer cancel()

	cmd := bot.newCmd(ctx, "yt-dlp", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		slog.Error("stdout pipe error", "err", err)
		bot.tele.Edit(msg, "❌ Internal error. Please try again.")
		return err
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		slog.Error("failed to start yt-dlp", "err", err)
		bot.tele.Edit(msg, "❌ Internal error. Could not start download.")
		return err
	}

	bot.scanProgress(stdout, mode, func(text string) {
		bot.tele.Edit(msg, text)
	})

	if err := cmd.Wait(); err != nil {
		slog.Error("yt-dlp failed", "err", err, "stderr", stderrBuf.String(), "user_id", u.ID)
		if ctx.Err() == context.DeadlineExceeded {
			bot.tele.Edit(msg, "❌ Download timed out (10 min limit).")
		} else {
			bot.tele.Edit(msg, "❌ File exceeds 50MB limit or download failed.")
		}
		return err
	}

	bot.tele.Edit(msg, "📤 Uploading to Telegram...")

	var sendErr error
	if mode == "audio" {
		sendErr = c.Send(&tele.Audio{File: tele.FromDisk(outputFile)})
	} else {
		sendErr = c.Send(&tele.Video{File: tele.FromDisk(outputFile)})
	}

	if sendErr != nil {
		slog.Error("upload failed", "user_id", u.ID, "err", sendErr)
		bot.tele.Edit(msg, "❌ Telegram upload failed (Max 50MB).")
	} else {
		slog.Info("download complete", "user_id", u.ID, "mode", mode)
		bot.tele.Delete(msg)
	}

	return sendErr
}

// scanProgress reads yt-dlp stdout and calls editFn when a progress percentage is found,
// throttled by bot.progressInterval. Initialising lastUpdate in the past ensures the
// first matching line always fires immediately.
func (bot *Bot) scanProgress(r io.Reader, mode string, editFn func(string)) {
	scanner := bufio.NewScanner(r)
	lastUpdate := time.Now().Add(-bot.progressInterval)
	for scanner.Scan() {
		matches := progressRegex.FindStringSubmatch(scanner.Text())
		if len(matches) > 1 && time.Since(lastUpdate) >= bot.progressInterval {
			editFn(fmt.Sprintf("⏳ Downloading %s: %s%%", mode, matches[1]))
			lastUpdate = time.Now()
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("scanner error reading yt-dlp output", "err", err)
	}
}

func buildArgs(mode, outputFile, downloadURL string) []string {
	if mode == "audio" {
		return []string{
			"-x",
			"--audio-format", "mp3",
			"--max-filesize", maxFileSize,
			"-o", outputFile,
			"--newline",
			downloadURL,
		}
	}
	return []string{
		"-f", "bestvideo[height<=1080][vcodec^=avc1]+bestaudio[ext=m4a]/best[height<=1080][vcodec^=avc1]/best",
		"--merge-output-format", "mp4",
		"--max-filesize", maxFileSize,
		"-o", outputFile,
		"--newline",
		downloadURL,
	}
}
