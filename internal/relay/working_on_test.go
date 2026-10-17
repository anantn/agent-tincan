package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
)

// workingOn sends method to the working-on route as the agent at addr from
// session ("" sends no session header) and checks the status.
func workingOn(h *harness, addr, session, method, body string, want int) {
	h.t.Helper()
	req := httptest.NewRequest(method, "/v1/agents/self/working-on", strings.NewReader(body))
	req.RemoteAddr = addr
	if session != "" {
		req.Header.Set(client.SessionHeader, session)
	}
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	if rec.Code != want {
		h.t.Fatalf("%s working-on from %s session %q: status %d, want %d: %s", method, addr, session, rec.Code, want, rec.Body.String())
	}
}

// AE6: a session's title shows on its agent's roster entry. With two
// sessions the most recently refreshed title shows with a count of the
// other, a DELETE removes a session's title at once, and a title nobody
// refreshes lapses after SessionGrace.
func TestWorkingOnRoster(t *testing.T) {
	h, clk, _ := wakeHarness(t)
	workingOn(h, museAddr, "s1", "PUT", `{"text":"Fix flaky relay test"}`, http.StatusNoContent)
	if a := agentInfo(t, h, macAddr, "muse"); a.WorkingOn != "Fix flaky relay test" || a.WorkingOnOthers != 0 {
		t.Fatalf("one session: %+v", a)
	}
	if a := agentInfo(t, h, macAddr, "grokbot"); a.WorkingOn != "" {
		t.Fatalf("grokbot shares nothing: %+v", a)
	}
	clk.advance(time.Second)
	workingOn(h, museAddr, "s2", "PUT", `{"text":"Review the protocol doc"}`, http.StatusNoContent)
	if a := agentInfo(t, h, macAddr, "muse"); a.WorkingOn != "Review the protocol doc" || a.WorkingOnOthers != 1 {
		t.Fatalf("two sessions: %+v", a)
	}
	clk.advance(time.Second)
	workingOn(h, museAddr, "s1", "PUT", `{"text":"Fix flaky relay test"}`, http.StatusNoContent)
	if a := agentInfo(t, h, macAddr, "muse"); a.WorkingOn != "Fix flaky relay test" || a.WorkingOnOthers != 1 {
		t.Fatalf("s1 refreshed last: %+v", a)
	}
	workingOn(h, museAddr, "s1", "DELETE", "", http.StatusNoContent)
	if a := agentInfo(t, h, macAddr, "muse"); a.WorkingOn != "Review the protocol doc" || a.WorkingOnOthers != 0 {
		t.Fatalf("s1 deleted: %+v", a)
	}
	clk.advance(SessionGrace)
	if a := agentInfo(t, h, macAddr, "muse"); a.WorkingOn != "" || a.WorkingOnOthers != 0 {
		t.Fatalf("s2 not refreshed for SessionGrace: %+v", a)
	}
}

// A title is one printable line in a body of exactly one known object, from
// a named session, and nothing is stored when any of that fails.
func TestWorkingOnRejects(t *testing.T) {
	h, _, _ := wakeHarness(t)
	for _, bad := range []string{
		`{"text":"line one\nline two"}`,
		"{\"text\":\"hidden\u200bmark\"}",
		`{"text":"` + strings.Repeat("x", 121) + `"}`,
		`{"text":"   "}`,
		`{"text":"ok","agent":"grokbot"}`,
		`{"text":"ok"} {}`,
		`not json`,
	} {
		workingOn(h, museAddr, "s1", "PUT", bad, http.StatusBadRequest)
	}
	workingOn(h, museAddr, "", "PUT", `{"text":"ok"}`, http.StatusBadRequest)
	workingOn(h, museAddr, "", "DELETE", "", http.StatusBadRequest)
	if a := agentInfo(t, h, macAddr, "muse"); a.WorkingOn != "" {
		t.Fatalf("rejected titles stored: %+v", a)
	}
}

// The writer is always the authenticated caller, so one agent's PUT or
// DELETE never touches another agent's line, even for the same session id.
func TestWorkingOnIsTheCallers(t *testing.T) {
	h, _, _ := wakeHarness(t)
	workingOn(h, museAddr, "s1", "PUT", `{"text":"Fix flaky relay test"}`, http.StatusNoContent)
	workingOn(h, grokAddr, "s1", "DELETE", "", http.StatusNoContent)
	if a := agentInfo(t, h, macAddr, "muse"); a.WorkingOn != "Fix flaky relay test" {
		t.Fatalf("grokbot's DELETE removed muse's line: %+v", a)
	}
	workingOn(h, strangerAddr, "s1", "PUT", `{"text":"x"}`, http.StatusForbidden)
}

// The route is served on the admin socket as well as the tailnet API, as
// the web-status route is.
func TestWorkingOnOnAdminHandler(t *testing.T) {
	h := newHarness(t, Config{})
	req := httptest.NewRequest("PUT", "/v1/agents/self/working-on", strings.NewReader(`{"text":"x"}`))
	req.Header.Set(client.SessionHeader, "s1")
	rec := httptest.NewRecorder()
	h.srv.AdminHandler().ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
		t.Fatalf("admin handler has no working-on route: %d", rec.Code)
	}
}
