package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"
)

// ── mockTeleBot ───────────────────────────────────────────────────────────────

type mockTeleBot struct {
	sendMsg  *tele.Message
	sendErr  error
	edits    []string
	deleted  bool
}

func (m *mockTeleBot) Send(to tele.Recipient, what interface{}, opts ...interface{}) (*tele.Message, error) {
	return m.sendMsg, m.sendErr
}
func (m *mockTeleBot) Edit(msg tele.Editable, what interface{}, opts ...interface{}) (*tele.Message, error) {
	if s, ok := what.(string); ok {
		m.edits = append(m.edits, s)
	}
	return nil, nil
}
func (m *mockTeleBot) Delete(msg tele.Editable) error                                      { m.deleted = true; return nil }
func (m *mockTeleBot) Handle(ep interface{}, h tele.HandlerFunc, m2 ...tele.MiddlewareFunc) {}
func (m *mockTeleBot) Start()                                                               {}
func (m *mockTeleBot) Stop()                                                                {}

// ── mockContext ───────────────────────────────────────────────────────────────

type mockContext struct {
	sender  *tele.User
	text    string
	sendErr error
	sent    []interface{}
}

func (m *mockContext) Sender() *tele.User            { return m.sender }
func (m *mockContext) Recipient() tele.Recipient     { return m.sender }
func (m *mockContext) Text() string                  { return m.text }
func (m *mockContext) Send(what interface{}, opts ...interface{}) error {
	m.sent = append(m.sent, what)
	return m.sendErr
}

// Remaining tele.Context methods – unused, return zero values.
func (m *mockContext) Bot() *tele.Bot                                          { return nil }
func (m *mockContext) Update() tele.Update                                     { return tele.Update{} }
func (m *mockContext) Message() *tele.Message                                  { return nil }
func (m *mockContext) Callback() *tele.Callback                                { return nil }
func (m *mockContext) Query() *tele.Query                                      { return nil }
func (m *mockContext) InlineResult() *tele.InlineResult                        { return nil }
func (m *mockContext) ShippingQuery() *tele.ShippingQuery                      { return nil }
func (m *mockContext) PreCheckoutQuery() *tele.PreCheckoutQuery                { return nil }
func (m *mockContext) ChatMember() *tele.ChatMemberUpdate                     { return nil }
func (m *mockContext) ChatJoinRequest() *tele.ChatJoinRequest                  { return nil }
func (m *mockContext) Migration() (int64, int64)                               { return 0, 0 }
func (m *mockContext) Topic() *tele.Topic                                      { return nil }
func (m *mockContext) Boost() *tele.BoostUpdated                               { return nil }
func (m *mockContext) BoostRemoved() *tele.BoostRemoved                        { return nil }
func (m *mockContext) Poll() *tele.Poll                                        { return nil }
func (m *mockContext) PollAnswer() *tele.PollAnswer                            { return nil }
func (m *mockContext) Chat() *tele.Chat                                        { return nil }
func (m *mockContext) Entities() tele.Entities                                 { return nil }
func (m *mockContext) Caption() string                                         { return "" }
func (m *mockContext) CaptionEntities() tele.Entities                          { return nil }
func (m *mockContext) Data() string                                            { return "" }
func (m *mockContext) Args() []string                                          { return nil }
func (m *mockContext) SendAlbum(a tele.Album, opts ...interface{}) error        { return nil }
func (m *mockContext) Reply(what interface{}, opts ...interface{}) error        { return nil }
func (m *mockContext) Forward(msg tele.Editable, opts ...interface{}) error     { return nil }
func (m *mockContext) ForwardTo(to tele.Recipient, opts ...interface{}) error   { return nil }
func (m *mockContext) Edit(what interface{}, opts ...interface{}) error         { return nil }
func (m *mockContext) EditCaption(caption string, opts ...interface{}) error    { return nil }
func (m *mockContext) EditOrSend(what interface{}, opts ...interface{}) error   { return nil }
func (m *mockContext) EditOrReply(what interface{}, opts ...interface{}) error  { return nil }
func (m *mockContext) Delete() error                                            { return nil }
func (m *mockContext) DeleteAfter(d time.Duration) *time.Timer                 { return nil }
func (m *mockContext) Notify(action tele.ChatAction) error                     { return nil }
func (m *mockContext) Ship(what ...interface{}) error                          { return nil }
func (m *mockContext) Accept(errorMessage ...string) error                     { return nil }
func (m *mockContext) Answer(resp *tele.QueryResponse) error                   { return nil }
func (m *mockContext) Respond(resp ...*tele.CallbackResponse) error            { return nil }
func (m *mockContext) RespondText(text string) error                           { return nil }
func (m *mockContext) RespondAlert(text string) error                          { return nil }
func (m *mockContext) Get(key string) interface{}                              { return nil }
func (m *mockContext) Set(key string, val interface{})                         {}

// ── helpers ───────────────────────────────────────────────────────────────────

// errReader is a reader that always returns an error, used to trigger scanner.Err().
type errReader struct{ err error }

func (r *errReader) Read(p []byte) (int, error) { return 0, r.err }

func testBot(tb *mockTeleBot) *Bot {
	b := newBot(tb)
	b.downloadTimeout = 5 * time.Second
	b.progressInterval = 0 // every matching line fires immediately in tests
	b.cleanupInterval = 10 * time.Millisecond
	return b
}

func testCtx(userID int64, text ...string) *mockContext {
	t := ""
	if len(text) > 0 {
		t = text[0]
	}
	return &mockContext{sender: &tele.User{ID: userID, Username: "u"}, text: t}
}

func storeURL(b *Bot, userID int64, rawURL string) {
	b.userState.Store(userID, pendingURL{url: rawURL, expiresAt: time.Now().Add(time.Hour)})
}

// cmdThatCreatesFile returns a newCmd factory whose command creates outputFile and exits 0.
// It also prints a progress line so scanProgress is exercised.
func cmdThatCreatesFile(outputFile string) func(context.Context, string, ...string) *exec.Cmd {
	return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		script := fmt.Sprintf(`printf '[download]  50.00%%%% of\n'; touch '%s'`, outputFile)
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
}

// cmdThatFails returns a newCmd factory whose command exits with code 1.
func cmdThatFails() func(context.Context, string, ...string) *exec.Cmd {
	return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}
}

// ── isValidURL ────────────────────────────────────────────────────────────────

func TestIsValidURL(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"https://youtube.com/watch?v=abc", true},
		{"http://x.com/video", true},
		{"ftp://files.example.com", false},
		{"javascript:alert(1)", false},
		{"not a url", false},
		{"", false},
		{"//no-scheme.com", false},
	}
	for _, tc := range cases {
		if got := isValidURL(tc.input); got != tc.want {
			t.Errorf("isValidURL(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// ── buildArgs ─────────────────────────────────────────────────────────────────

func TestBuildArgs_Audio(t *testing.T) {
	args := buildArgs("audio", "/tmp/out.mp3", "https://example.com")
	mustContain(t, args, "-x")
	mustContain(t, args, "mp3")
	mustContain(t, args, maxFileSize)
	mustContain(t, args, "/tmp/out.mp3")
	mustContain(t, args, "https://example.com")
}

func TestBuildArgs_Video(t *testing.T) {
	args := buildArgs("video", "/tmp/out.mp4", "https://example.com")
	mustContain(t, args, "--merge-output-format")
	mustContain(t, args, "mp4")
	mustContain(t, args, maxFileSize)
	mustContain(t, args, "/tmp/out.mp4")
	mustContain(t, args, "https://example.com")
}

func mustContain(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if strings.Contains(a, want) {
			return
		}
	}
	t.Errorf("args %v do not contain %q", args, want)
}

// ── purgeExpired ──────────────────────────────────────────────────────────────

func TestPurgeExpired(t *testing.T) {
	b := testBot(&mockTeleBot{})

	b.userState.Store(int64(1), pendingURL{url: "http://a.com", expiresAt: time.Now().Add(-time.Second)})  // expired
	b.userState.Store(int64(2), pendingURL{url: "http://b.com", expiresAt: time.Now().Add(time.Hour)})     // valid

	b.purgeExpired()

	if _, ok := b.userState.Load(int64(1)); ok {
		t.Error("expired entry should have been deleted")
	}
	if _, ok := b.userState.Load(int64(2)); !ok {
		t.Error("valid entry should still exist")
	}
}

// ── cleanupExpiredState ───────────────────────────────────────────────────────

func TestCleanupExpiredState_ExitsOnCancel(t *testing.T) {
	b := testBot(&mockTeleBot{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	done := make(chan struct{})
	go func() {
		b.cleanupExpiredState(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanupExpiredState did not exit on ctx.Done")
	}
}

func TestCleanupExpiredState_PurgesOnTick(t *testing.T) {
	b := testBot(&mockTeleBot{})
	b.cleanupInterval = 10 * time.Millisecond

	b.userState.Store(int64(99), pendingURL{url: "http://x.com", expiresAt: time.Now().Add(-time.Hour)})

	ctx, cancel := context.WithCancel(context.Background())
	go b.cleanupExpiredState(ctx)
	defer cancel()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := b.userState.Load(int64(99)); !ok {
			return // purged — test passes
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("expired entry was not purged after ticker fired")
}

// ── registerHandlers ─────────────────────────────────────────────────────────

func TestRegisterHandlers_SetsSelector(t *testing.T) {
	b := testBot(&mockTeleBot{})
	b.registerHandlers()
	if b.selector == nil {
		t.Error("selector should be set after registerHandlers")
	}
}

// ── handleStart ───────────────────────────────────────────────────────────────

func TestHandleStart(t *testing.T) {
	b := testBot(&mockTeleBot{})
	c := testCtx(1)
	if err := b.handleStart(c); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(c.sent) == 0 {
		t.Error("expected welcome message to be sent")
	}
}

// ── handleText ────────────────────────────────────────────────────────────────

func TestHandleText_InvalidURL(t *testing.T) {
	b := testBot(&mockTeleBot{})
	b.registerHandlers()
	c := testCtx(1, "not a url")
	if err := b.handleText(c); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(c.sent) != 0 {
		t.Error("no message should be sent for invalid URL")
	}
}

func TestHandleText_ValidURL(t *testing.T) {
	b := testBot(&mockTeleBot{})
	b.registerHandlers()
	c := testCtx(1, "https://youtube.com/watch?v=abc")
	if err := b.handleText(c); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(c.sent) == 0 {
		t.Error("expected selector message to be sent")
	}
	if _, ok := b.userState.Load(int64(1)); !ok {
		t.Error("URL should be stored in userState")
	}
}

// ── handleVideo / handleAudio (delegation) ───────────────────────────────────

func TestHandleVideo_DelegatesToDownload(t *testing.T) {
	tb := &mockTeleBot{}
	b := testBot(tb)
	// No pending URL → hits the "please send link again" path, proving delegation
	c := testCtx(1)
	_ = b.handleVideo(c)
	if len(c.sent) == 0 {
		t.Error("expected a reply from handleVideo")
	}
}

func TestHandleAudio_DelegatesToDownload(t *testing.T) {
	tb := &mockTeleBot{}
	b := testBot(tb)
	c := testCtx(1)
	_ = b.handleAudio(c)
	if len(c.sent) == 0 {
		t.Error("expected a reply from handleAudio")
	}
}

// ── downloadAndSend ───────────────────────────────────────────────────────────

func TestDownloadAndSend_WorkerPoolFull(t *testing.T) {
	b := testBot(&mockTeleBot{})
	// Fill the worker pool completely.
	for i := 0; i < maxConcurrent; i++ {
		b.workerPool <- struct{}{}
	}
	c := testCtx(1)
	if err := b.downloadAndSend(c, "video"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(fmt.Sprintf("%v", c.sent), "busy") {
		t.Error("expected busy message")
	}
}

func TestDownloadAndSend_UserAlreadyActive(t *testing.T) {
	b := testBot(&mockTeleBot{})
	b.activeUsers.Store(int64(1), struct{}{})
	c := testCtx(1)
	_ = b.downloadAndSend(c, "video")
	if !strings.Contains(fmt.Sprintf("%v", c.sent), "progress") {
		t.Error("expected in-progress message")
	}
}

func TestDownloadAndSend_NoPendingURL(t *testing.T) {
	b := testBot(&mockTeleBot{})
	c := testCtx(1)
	_ = b.downloadAndSend(c, "video")
	if !strings.Contains(fmt.Sprintf("%v", c.sent), "link") {
		t.Error("expected 'send link again' message")
	}
}

func TestDownloadAndSend_InitSendError(t *testing.T) {
	tb := &mockTeleBot{sendErr: errors.New("telegram down")}
	b := testBot(tb)
	storeURL(b, 1, "https://youtube.com/watch?v=abc")
	c := testCtx(1)
	err := b.downloadAndSend(c, "video")
	if err == nil {
		t.Error("expected error when init send fails")
	}
}

func TestDownloadAndSend_InitSendReturnsNilMsg(t *testing.T) {
	tb := &mockTeleBot{sendMsg: nil, sendErr: nil}
	b := testBot(tb)
	storeURL(b, 1, "https://youtube.com/watch?v=abc")
	c := testCtx(1)
	// nil msg with nil err should return nil (no error to propagate)
	_ = b.downloadAndSend(c, "video")
	// Verify it didn't proceed past the nil-msg guard (no edits attempted)
	if len(tb.edits) != 0 {
		t.Error("should not have edited a nil message")
	}
}

func TestDownloadAndSend_StdoutPipeError(t *testing.T) {
	tb := &mockTeleBot{sendMsg: &tele.Message{ID: 1}}
	b := testBot(tb)
	storeURL(b, 1, "https://youtube.com/watch?v=abc")
	// Pre-set Stdout on the cmd so StdoutPipe() returns an error.
	b.newCmd = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "true")
		cmd.Stdout = &bytes.Buffer{}
		return cmd
	}
	c := testCtx(1)
	err := b.downloadAndSend(c, "video")
	if err == nil {
		t.Error("expected error from StdoutPipe failure")
	}
	if !hasEdit(tb, "Internal error") {
		t.Error("expected internal error edit")
	}
}

func TestDownloadAndSend_StartError(t *testing.T) {
	tb := &mockTeleBot{sendMsg: &tele.Message{ID: 1}}
	b := testBot(tb)
	storeURL(b, 1, "https://youtube.com/watch?v=abc")
	b.newCmd = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "nonexistent_command_that_does_not_exist_xyz")
	}
	c := testCtx(1)
	err := b.downloadAndSend(c, "video")
	if err == nil {
		t.Error("expected error when command not found")
	}
	if !hasEdit(tb, "Could not start") {
		t.Error("expected 'Could not start' edit")
	}
}

func TestDownloadAndSend_Timeout(t *testing.T) {
	tb := &mockTeleBot{sendMsg: &tele.Message{ID: 1}}
	b := testBot(tb)
	b.downloadTimeout = 50 * time.Millisecond
	storeURL(b, 1, "https://youtube.com/watch?v=abc")
	b.newCmd = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "10")
	}
	c := testCtx(1)
	err := b.downloadAndSend(c, "video")
	if err == nil {
		t.Error("expected timeout error")
	}
	if !hasEdit(tb, "timed out") {
		t.Error("expected timeout edit")
	}
}

func TestDownloadAndSend_DownloadFailed(t *testing.T) {
	tb := &mockTeleBot{sendMsg: &tele.Message{ID: 1}}
	b := testBot(tb)
	storeURL(b, 1, "https://youtube.com/watch?v=abc")
	b.newCmd = cmdThatFails()
	c := testCtx(1)
	err := b.downloadAndSend(c, "video")
	if err == nil {
		t.Error("expected error on download failure")
	}
	if !hasEdit(tb, "download failed") {
		t.Error("expected failure edit")
	}
}

func TestDownloadAndSend_VideoSuccess(t *testing.T) {
	if err := os.MkdirAll(tempDir, 0700); err != nil {
		t.Fatal(err)
	}
	tb := &mockTeleBot{sendMsg: &tele.Message{ID: 1}}
	b := testBot(tb)
	storeURL(b, 1, "https://youtube.com/watch?v=abc")

	var capturedOutput string
	b.newCmd = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		capturedOutput = outputFileFromArgs(args)
		return cmdThatCreatesFile(capturedOutput)(ctx, "", args...)
	}

	c := testCtx(1)
	err := b.downloadAndSend(c, "video")
	if err != nil {
		t.Errorf("unexpected error on video success: %v", err)
	}
	if !tb.deleted {
		t.Error("progress message should have been deleted on success")
	}
	if !hasEdit(tb, "Uploading") {
		t.Error("expected uploading edit")
	}
}

func TestDownloadAndSend_AudioSuccess(t *testing.T) {
	if err := os.MkdirAll(tempDir, 0700); err != nil {
		t.Fatal(err)
	}
	tb := &mockTeleBot{sendMsg: &tele.Message{ID: 1}}
	b := testBot(tb)
	storeURL(b, 2, "https://youtube.com/watch?v=abc")

	b.newCmd = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		out := outputFileFromArgs(args)
		return cmdThatCreatesFile(out)(ctx, "", args...)
	}

	c := testCtx(2)
	err := b.downloadAndSend(c, "audio")
	if err != nil {
		t.Errorf("unexpected error on audio success: %v", err)
	}
	if !tb.deleted {
		t.Error("progress message should have been deleted on success")
	}
}

func TestDownloadAndSend_VideoUploadFailed(t *testing.T) {
	if err := os.MkdirAll(tempDir, 0700); err != nil {
		t.Fatal(err)
	}
	tb := &mockTeleBot{sendMsg: &tele.Message{ID: 1}}
	b := testBot(tb)
	storeURL(b, 3, "https://youtube.com/watch?v=abc")
	b.newCmd = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		out := outputFileFromArgs(args)
		return cmdThatCreatesFile(out)(ctx, "", args...)
	}
	c := testCtx(3)
	c.sendErr = errors.New("file too large")
	err := b.downloadAndSend(c, "video")
	if err == nil {
		t.Error("expected error on upload failure")
	}
	if !hasEdit(tb, "upload failed") {
		t.Error("expected upload failure edit")
	}
}

func TestDownloadAndSend_AudioUploadFailed(t *testing.T) {
	if err := os.MkdirAll(tempDir, 0700); err != nil {
		t.Fatal(err)
	}
	tb := &mockTeleBot{sendMsg: &tele.Message{ID: 1}}
	b := testBot(tb)
	storeURL(b, 4, "https://youtube.com/watch?v=abc")
	b.newCmd = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		out := outputFileFromArgs(args)
		return cmdThatCreatesFile(out)(ctx, "", args...)
	}
	c := testCtx(4)
	c.sendErr = errors.New("file too large")
	err := b.downloadAndSend(c, "audio")
	if err == nil {
		t.Error("expected error on audio upload failure")
	}
	if !hasEdit(tb, "upload failed") {
		t.Error("expected upload failure edit")
	}
}

// ── scanProgress ─────────────────────────────────────────────────────────────

func TestScanProgress_MatchFires(t *testing.T) {
	b := testBot(&mockTeleBot{})
	b.progressInterval = 0

	var edits []string
	b.scanProgress(strings.NewReader("[download]  42.50% of\n"), "video", func(s string) {
		edits = append(edits, s)
	})
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if !strings.Contains(edits[0], "42.50") {
		t.Errorf("edit should contain progress percentage, got: %s", edits[0])
	}
}

func TestScanProgress_NoMatch(t *testing.T) {
	b := testBot(&mockTeleBot{})
	var edits []string
	b.scanProgress(strings.NewReader("some random output\n"), "video", func(s string) {
		edits = append(edits, s)
	})
	if len(edits) != 0 {
		t.Errorf("expected no edits for non-progress line, got %d", len(edits))
	}
}

func TestScanProgress_Throttled(t *testing.T) {
	b := testBot(&mockTeleBot{})
	b.progressInterval = time.Hour // long throttle — only first line fires

	var edits []string
	input := "[download]  10.00% of\n[download]  20.00% of\n"
	b.scanProgress(strings.NewReader(input), "video", func(s string) {
		edits = append(edits, s)
	})
	if len(edits) != 1 {
		t.Errorf("expected exactly 1 edit due to throttle, got %d", len(edits))
	}
}

func TestScanProgress_ScannerError(t *testing.T) {
	b := testBot(&mockTeleBot{})
	var edits []string
	// errReader immediately returns an error, triggering scanner.Err() non-nil path.
	b.scanProgress(&errReader{err: errors.New("broken pipe")}, "video", func(s string) {
		edits = append(edits, s)
	})
	if len(edits) != 0 {
		t.Errorf("expected no edits when reader errors, got %d", len(edits))
	}
}

// ── test helpers ──────────────────────────────────────────────────────────────

func hasEdit(tb *mockTeleBot, substr string) bool {
	for _, e := range tb.edits {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

func outputFileFromArgs(args []string) string {
	for i, a := range args {
		if a == "-o" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
