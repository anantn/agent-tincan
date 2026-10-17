package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
)

// recordReporter records the working-on calls a titleSharer makes.
type recordReporter struct {
	mu    sync.Mutex
	calls []string // "PUT <text>" or "DELETE"
}

func (r *recordReporter) ReportWorkingOn(_ context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "PUT "+text)
	return nil
}

func (r *recordReporter) ClearWorkingOn(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "DELETE")
	return nil
}

func (r *recordReporter) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func wantReports(t *testing.T, r *recordReporter, want ...string) {
	t.Helper()
	got := r.got()
	if len(got) != len(want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("calls = %q, want %q", got, want)
		}
	}
}

const (
	sessionA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	sessionB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	titlePID = 4242
)

// claudeDir is a Claude Code config dir with a transcript per session id
// whose title records are as given, so SessionTitle can read them.
func claudeDir(t *testing.T, titles map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	proj := filepath.Join(dir, "projects", "-Users-me-code-tincan")
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, records := range titles {
		if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(records), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// registerSession writes the registry file for titlePID naming sessionID.
func registerSession(t *testing.T, dir, sessionID string) {
	t.Helper()
	body := `{"pid":` + strconv.Itoa(titlePID) + `,"sessionId":"` + sessionID + `","status":"idle"}`
	if err := os.WriteFile(filepath.Join(dir, "sessions", strconv.Itoa(titlePID)+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// The sharer reads the title of the session the registry names now, so a
// session that /clear or a resume replaced is followed on the next tick,
// and an unchanged title is reported again each tick to stay fresh.
func TestShareTitleFollowsRegistrySession(t *testing.T) {
	dir := claudeDir(t, map[string]string{
		sessionA: `{"type":"user","message":{"role":"user","content":"please fix the flaky test"}}` + "\n" +
			`{"type":"ai-title","aiTitle":"Fix flaky relay test","sessionId":"` + sessionA + `"}` + "\n",
		sessionB: `{"type":"ai-title","aiTitle":"Draft notes","sessionId":"` + sessionB + `"}` + "\n" +
			`{"type":"custom-title","customTitle":"Review the protocol doc","sessionId":"` + sessionB + `"}` + "\n",
	})
	registerSession(t, dir, sessionA)
	rep := &recordReporter{}
	s := newClaudeTitleSharer(rep, envOf(map[string]string{"CLAUDE_CONFIG_DIR": dir, "CLAUDE_CODE_SESSION_ID": sessionB}), titlePID)
	s.refresh(t.Context())
	s.refresh(t.Context())
	registerSession(t, dir, sessionB)
	s.refresh(t.Context())
	wantReports(t, rep, "PUT Fix flaky relay test", "PUT Fix flaky relay test", "PUT Review the protocol doc")
}

// With no registry file for the parent, as under a channel server started
// some other way, the session id comes from CLAUDE_CODE_SESSION_ID.
func TestShareTitleFallsBackToSessionEnv(t *testing.T) {
	dir := claudeDir(t, map[string]string{
		sessionA: `{"type":"ai-title","aiTitle":"Fix flaky relay test"}` + "\n",
	})
	rep := &recordReporter{}
	s := newClaudeTitleSharer(rep, envOf(map[string]string{"CLAUDE_CONFIG_DIR": dir, "CLAUDE_CODE_SESSION_ID": sessionA}), titlePID)
	s.refresh(t.Context())
	wantReports(t, rep, "PUT Fix flaky relay test")
}

// A session with no title yet reports nothing, and one whose title goes
// away removes the line it reported.
func TestShareTitleEmptyTitle(t *testing.T) {
	title := ""
	rep := &recordReporter{}
	s := &titleSharer{r: rep, sessionID: func() string { return sessionA }, title: func(string) string { return title }}
	s.refresh(t.Context())
	wantReports(t, rep)
	title = "Fix flaky relay test"
	s.refresh(t.Context())
	title = ""
	s.refresh(t.Context())
	s.refresh(t.Context())
	wantReports(t, rep, "PUT Fix flaky relay test", "DELETE")
}

// The loop reports at once and on every tick, and stopping it removes the
// session's line, as a clean exit of tincan mcp does.
func TestShareTitleStopRemovesLine(t *testing.T) {
	rep := &recordReporter{}
	s := &titleSharer{r: rep, sessionID: func() string { return sessionA }, title: func(string) string { return "Fix flaky relay test" }}
	stop := startTitleSharing(t.Context(), s, 5*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for len(rep.got()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()
	got := rep.got()
	if len(got) < 3 || got[0] != "PUT Fix flaky relay test" || got[1] != "PUT Fix flaky relay test" || got[len(got)-1] != "DELETE" {
		t.Fatalf("calls = %q, want repeated PUTs then one DELETE", got)
	}
}

// Against a relay that predates working-on lines the first PUT gets a 404:
// the sharer goes quiet for the rest of the process, sending no more PUTs
// and no DELETE at exit.
func TestShareTitleOldRelayGoesQuiet(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		http.NotFound(w, r)
	}))
	defer ts.Close()
	r, err := client.NewRelay(ts.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	r.SetSession("mcp-1")
	s := &titleSharer{r: r, sessionID: func() string { return sessionA }, title: func(string) string { return "Fix flaky relay test" }}
	for range 3 {
		s.refresh(t.Context())
	}
	s.clear(t.Context())
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("relay saw %d calls, want 1", calls)
	}
}

// Sharing is off unless the owner passes --share-title.
func TestShareTitleOffByDefault(t *testing.T) {
	f := mcpCmd().Flags().Lookup("share-title")
	if f == nil || f.DefValue != "false" {
		t.Fatalf("--share-title flag = %+v, want one that defaults to false", f)
	}
}
