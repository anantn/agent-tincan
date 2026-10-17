package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/testrelay"
)

// fakeWindow is one Claude Code window to pushSocket: it records pushes,
// and its idle state and write failures are set by the test.
type fakeWindow struct {
	*recordPusher
	busy atomic.Bool
	fail atomic.Bool
}

func newFakeWindow() *fakeWindow { return &fakeWindow{recordPusher: newRecordPusher()} }

func (w *fakeWindow) idle() bool { return !w.busy.Load() }

func (w *fakeWindow) Push(ctx context.Context, content string, meta map[string]string) error {
	if w.fail.Load() {
		return errors.New("socket closed")
	}
	return w.recordPusher.Push(ctx, content, meta)
}

func (w *fakeWindow) count() int { return len(w.pushes()) }

// startSocket runs pushSocket for one window until the test ends.
func startSocket(t *testing.T, r *client.Relay, w socketTarget, shared socketShared) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); pushSocket(ctx, r, w, shared) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

// museWindow is a muse client with its own session, as one window's MCP
// server has.
func museWindow(t *testing.T, m *testrelay.Mesh, session string) *client.Relay {
	r := m.Client(t, "muse")
	r.SetSession(session)
	return r
}

func totalPushes(ws ...*fakeWindow) int {
	n := 0
	for _, w := range ws {
		n += w.count()
	}
	return n
}

func settle() { time.Sleep(300 * time.Millisecond) }

// One teammate request starts one turn on the machine, however many
// windows are idle.
func TestSocketWakeOneWindowPerRequest(t *testing.T) {
	fastChannel(t, 20*time.Millisecond, 50*time.Millisecond)
	m := testrelay.New(t, relay.Config{PollHold: time.Second})
	shared := socketShared{dir: t.TempDir(), agent: "muse"}
	ws := []*fakeWindow{newFakeWindow(), newFakeWindow(), newFakeWindow()}
	for i, w := range ws {
		startSocket(t, museWindow(t, m, "mcp-"+string(rune('a'+i))), w, shared)
	}
	sent, _ := m.Client(t, "grokbot").Send(t.Context(), "muse", "call the dentist", envelope.KindAsk, "", false)
	settle()
	if n := totalPushes(ws...); n != 1 {
		t.Fatalf("pushes across windows = %d, want 1", n)
	}
	for _, w := range ws {
		for _, p := range w.pushes() {
			if p["request_ids"] != sent.ID {
				t.Fatalf("push = %v, want request %s", p, sent.ID)
			}
		}
	}
}

// A reply to an ask a window sent goes to that window only.
func TestSocketWakeOwnReplyToAskingWindow(t *testing.T) {
	fastChannel(t, 20*time.Millisecond, 50*time.Millisecond)
	m := testrelay.New(t, relay.Config{PollHold: time.Second})
	shared := socketShared{dir: t.TempDir(), agent: "muse"}
	a, b := newFakeWindow(), newFakeWindow()
	ra, rb := museWindow(t, m, "mcp-a"), museWindow(t, m, "mcp-b")
	startSocket(t, ra, a, shared)
	startSocket(t, rb, b, shared)
	res, err := rb.Ask(t.Context(), "grokbot", "is the build green?", "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	grok := m.Client(t, "grokbot")
	in, _ := grok.Poll(t.Context(), 0)
	if len(in.Requests) != 1 {
		t.Fatalf("grokbot inbox = %+v", in)
	}
	if _, err := grok.Reply(t.Context(), in.Requests[0].ID, "green", envelope.StatusAnswered); err != nil {
		t.Fatal(err)
	}
	settle()
	if a.count() != 0 || b.count() != 1 {
		t.Fatalf("pushes a=%d b=%d, want 0 and 1", a.count(), b.count())
	}
	if got := b.pushes()[0]["request_ids"]; got != res.Request.ID {
		t.Fatalf("b pushed %q, want %s", got, res.Request.ID)
	}
}

// A reply to a request sent with no session is shown to every window, and
// still wakes only one.
func TestSocketWakeSessionlessReplyWakesOne(t *testing.T) {
	fastChannel(t, 20*time.Millisecond, 50*time.Millisecond)
	m := testrelay.New(t, relay.Config{PollHold: time.Second})
	shared := socketShared{dir: t.TempDir(), agent: "muse"}
	if _, err := m.Client(t, "muse").Ask(t.Context(), "grokbot", "is the build green?", "", 0, false); err != nil {
		t.Fatal(err)
	}
	grok := m.Client(t, "grokbot")
	in, _ := grok.Poll(t.Context(), 0)
	if _, err := grok.Reply(t.Context(), in.Requests[0].ID, "green", envelope.StatusAnswered); err != nil {
		t.Fatal(err)
	}
	ws := []*fakeWindow{newFakeWindow(), newFakeWindow(), newFakeWindow()}
	for i, w := range ws {
		startSocket(t, museWindow(t, m, "mcp-"+string(rune('a'+i))), w, shared)
	}
	settle()
	if n := totalPushes(ws...); n != 1 {
		t.Fatalf("pushes across windows = %d, want 1", n)
	}
}

// A busy window never holds a request back from an idle one.
func TestSocketWakeBusyWindowDoesNotBlockIdle(t *testing.T) {
	fastChannel(t, 20*time.Millisecond, 50*time.Millisecond)
	m := testrelay.New(t, relay.Config{PollHold: time.Second})
	shared := socketShared{dir: t.TempDir(), agent: "muse"}
	busy, idle := newFakeWindow(), newFakeWindow()
	busy.busy.Store(true)
	startSocket(t, museWindow(t, m, "mcp-a"), busy, shared)
	startSocket(t, museWindow(t, m, "mcp-b"), idle, shared)
	m.Client(t, "grokbot").Send(t.Context(), "muse", "call the dentist", envelope.KindAsk, "", false)
	settle()
	if busy.count() != 0 || idle.count() != 1 {
		t.Fatalf("pushes busy=%d idle=%d, want 0 and 1", busy.count(), idle.count())
	}
}

// A request is delivered once: the window that pushed it may go away, and
// nobody announces it again, even after reannounceAfter.
func TestSocketWakeDeliversOnce(t *testing.T) {
	fastChannel(t, 20*time.Millisecond, 50*time.Millisecond)
	m := testrelay.New(t, relay.Config{PollHold: time.Second})
	shared := socketShared{dir: t.TempDir(), agent: "muse"}
	first := newFakeWindow()
	stop := startSocket(t, museWindow(t, m, "mcp-a"), first, shared)
	m.Client(t, "grokbot").Send(t.Context(), "muse", "call the dentist", envelope.KindAsk, "", false)
	settle()
	if first.count() != 1 {
		t.Fatalf("first window pushes = %d, want 1", first.count())
	}
	stop()
	second := newFakeWindow()
	startSocket(t, museWindow(t, m, "mcp-b"), second, shared)
	settle()
	if second.count() != 0 || first.count() != 1 {
		t.Fatalf("pushes after the first window left: first=%d second=%d", first.count(), second.count())
	}
}

// A window that loses the lock still tells its own session about a reply
// to its ask, and does not count the waiting request as delivered.
func TestSocketWakeOwnReplyWithoutLock(t *testing.T) {
	fastChannel(t, 20*time.Millisecond, 50*time.Millisecond)
	m := testrelay.New(t, relay.Config{PollHold: time.Second})
	shared := socketShared{dir: t.TempDir(), agent: "muse"}
	release, ok, err := shared.tryLock()
	if err != nil || !ok {
		t.Fatalf("test lock: ok=%v err=%v", ok, err)
	}
	rb := museWindow(t, m, "mcp-b")
	if _, err := rb.Ask(t.Context(), "grokbot", "is the build green?", "", 0, false); err != nil {
		t.Fatal(err)
	}
	grok := m.Client(t, "grokbot")
	in, _ := grok.Poll(t.Context(), 0)
	grok.Reply(t.Context(), in.Requests[0].ID, "green", envelope.StatusAnswered)
	req, _ := grok.Send(t.Context(), "muse", "call the dentist", envelope.KindAsk, "", false)
	b := newFakeWindow()
	startSocket(t, rb, b, shared)
	settle()
	got := b.pushes()
	if len(got) != 1 || got[0]["kind"] != "reply" || strings.Contains(got[0]["request_ids"], req.ID) {
		t.Fatalf("pushes while locked out = %v, want one reply-only notice", got)
	}
	release()
	settle()
	got = b.pushes()
	if len(got) != 2 || got[1]["request_ids"] != req.ID {
		t.Fatalf("pushes after the lock frees = %v, want the request next", got)
	}
}

// Upgrade notices never go through the socket: each socket message spends
// a turn, and the notice reaches the agent on its next tool call anyway.
func TestSocketWakeSkipsUpgradeNotice(t *testing.T) {
	old := client.Version
	client.Version = "0.5.4"
	t.Cleanup(func() { client.Version = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"waiting": 0, "upgrade_available": "93.0.0"})
	}))
	defer srv.Close()
	r, _ := client.NewRelay(srv.URL, "")
	w := newFakeWindow()
	startSocket(t, r, w, socketShared{dir: t.TempDir(), agent: "muse"})
	settle()
	if w.count() != 0 {
		t.Fatalf("pushes = %v, want none", w.pushes())
	}
}

// A socket that keeps failing ends the loop, so the agent stops peeking and
// stops looking online; socketWake then keeps the session live without
// polling until the socket checks out again. Its peeks advertise socket
// wake and the keepalive after them does not.
func TestSocketWakeStopsPeekingWhenSocketFails(t *testing.T) {
	fastChannel(t, 10*time.Millisecond, time.Hour)
	var polls, whoami atomic.Int32
	var peekFeatures, keepFeatures atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/poll":
			peekFeatures.Store(r.Header.Get(client.FeaturesHeader))
			polls.Add(1)
			n := polls.Load()
			_ = json.NewEncoder(w).Encode(map[string]any{"waiting": 1, "queued": 1, "pending": []map[string]string{{"id": "q" + string(rune('0'+n%10)), "from": "grokbot"}}})
		case "/v1/whoami":
			keepFeatures.Store(r.Header.Get(client.FeaturesHeader))
			whoami.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"agent": "muse"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r, _ := client.NewRelay(srv.URL, "")
	w := newFakeWindow()
	w.fail.Store(true)
	var mu sync.Mutex
	reverified := 0
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		socketWake(ctx, r, w, socketShared{dir: t.TempDir(), agent: "muse"}, socketWakeTiming{keepalive: 20 * time.Millisecond, retry: time.Hour}, func() (socketTarget, error) {
			mu.Lock()
			reverified++
			mu.Unlock()
			return nil, errors.New("still failing")
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for whoami.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	before := polls.Load()
	time.Sleep(200 * time.Millisecond)
	after := polls.Load()
	cancel()
	<-done
	if whoami.Load() == 0 {
		t.Fatal("socketWake never fell back to the keepalive")
	}
	if after != before {
		t.Fatalf("peeks went on after the socket failed: %d then %d", before, after)
	}
	if before > socketFailLimit {
		t.Fatalf("peeks before giving up = %d, want at most %d", before, socketFailLimit)
	}
	if got := peekFeatures.Load(); got != "ping,socket-wake" {
		t.Fatalf("peek features = %v, want ping,socket-wake", got)
	}
	if got := keepFeatures.Load(); got != "ping" {
		t.Fatalf("keepalive features = %v, want ping", got)
	}
}

// The idle gate reads the session registry each time and ignores how long
// ago the status last changed: a window idle for hours is the one to wake.
func TestClaudeSocketIdleIgnoresStatusAge(t *testing.T) {
	f := newClaudeSockFixture(t)
	sock, cfg := f.socket, f.configDir
	pid := os.Getpid() // a live process, as the gate checks
	reg := filepath.Join(cfg, "sessions")
	os.MkdirAll(reg, 0o700)
	write := func(status string) {
		b, _ := json.Marshal(map[string]any{
			"pid": pid, "status": status, "procStart": "Sat Oct 17 04:55:23 2026",
			"statusUpdatedAt": time.Now().Add(-13 * time.Hour).UnixMilli(), "messagingSocketPath": sock,
		})
		os.WriteFile(filepath.Join(reg, strconv.Itoa(pid)+".json"), b, 0o600)
	}
	write("idle")
	env := map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": sock, "CLAUDE_CONFIG_DIR": cfg}
	p, err := newClaudeSocketPusher(func(k string) string { return env[k] }, pid)
	if err != nil {
		t.Fatal(err)
	}
	if !p.idle() {
		t.Fatal("idle session 13 hours since its last status change is not idle")
	}
	write("busy")
	if p.idle() {
		t.Fatal("busy session reads idle")
	}
}
