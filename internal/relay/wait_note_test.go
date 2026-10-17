package relay

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

// peekAs peeks as agent at addr from session, advertising features.
func peekAs(h *harness, addr, session, features string) {
	h.t.Helper()
	req := httptest.NewRequest("GET", "/v1/poll?peek=1&hold=0&replies=keep", nil)
	req.RemoteAddr = addr
	req.Header.Set(client.SessionHeader, session)
	req.Header.Set(client.FeaturesHeader, features)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	if rec.Code >= 300 {
		h.t.Fatalf("peek from %s: status %d: %s", addr, rec.Code, rec.Body.String())
	}
}

// sendTarget sends from grokbot to to and returns the target the relay
// sent back with the queued request.
func sendTarget(h *harness, to string) *envelope.Target {
	h.t.Helper()
	var out envelope.SendResponse
	h.do(grokAddr, "POST", "/v1/send", `{"to":"`+to+`","body":"hi"}`, 201, &out)
	return out.Target
}

// AE4: a channel agent whose session peeks advertising socket wake is
// reached by socket, and is listening while its peeks are recent.
func TestSendTargetSocketWakeListening(t *testing.T) {
	h, _, fw := wakeHarness(t)
	fw.methods["muse"] = "channel"
	peekAs(h, museAddr, "s1", "ping,socket-wake")
	got := sendTarget(h, "muse")
	if got == nil || got.WakeVia != "socket" || !got.Listening {
		t.Fatalf("socket-wake muse: target %+v", got)
	}
	res := client.Result{Request: envelope.Request{ID: "r1", To: "muse"}, Status: envelope.StatusQueued, Target: got}
	if out := client.FormatResult(res); !strings.Contains(out, "muse is listening now; a session will be prompted when it is idle.") {
		t.Fatalf("rendered = %q", out)
	}
}

// A socket-wake session whose last peek is older than the poll hold plus
// slack is still the way the agent wakes, but it is not listening. The
// record lapses after SessionGrace, and a peek without the advert drops it
// at once, so the configured method shows again.
func TestSendTargetSocketWakeStale(t *testing.T) {
	h, clk, fw := wakeHarness(t)
	fw.methods["muse"] = "channel"
	peekAs(h, museAddr, "s1", "ping,socket-wake")
	clk.advance(h.srv.cfg.PollHold + 31*time.Second)
	if got := sendTarget(h, "muse"); got == nil || got.WakeVia != "socket" || got.Listening {
		t.Fatalf("quiet socket-wake muse: target %+v", got)
	}
	clk.advance(SessionGrace)
	if got := sendTarget(h, "muse"); got == nil || got.WakeVia != "channel" || got.Listening {
		t.Fatalf("socket-wake session gone: target %+v", got)
	}
	peekAs(h, museAddr, "s1", "ping,socket-wake")
	peekAs(h, museAddr, "s1", "ping")
	if got := sendTarget(h, "muse"); got == nil || got.WakeVia != "channel" || !got.Listening {
		t.Fatalf("session stopped advertising: target %+v", got)
	}
}

// AE5: an agent on wake method none that has not polled for a day is told
// to the asker as checking only when its owner prompts it.
func TestSendTargetWakeNone(t *testing.T) {
	h, clk, fw := wakeHarness(t)
	fw.methods["instinct"] = "none"
	clk.advance(24 * time.Hour)
	got := sendTarget(h, "instinct")
	if got == nil || got.WakeVia != "none" || got.Listening {
		t.Fatalf("none instinct: target %+v", got)
	}
	res := client.Result{Request: envelope.Request{ID: "r1", To: "instinct"}, Status: envelope.StatusQueued, Target: got}
	if out := client.FormatResult(res); !strings.Contains(out, "instinct checks only when its owner next prompts it.") {
		t.Fatalf("rendered = %q", out)
	}
}

// A wait agent is listening while it polls, and its webhook and email
// teammates get their method too, beside the wake facts they already had.
func TestSendTargetConfiguredMethod(t *testing.T) {
	h, _, _ := wakeHarness(t)
	h.do(museAddr, "GET", "/v1/poll?hold=0", "", 204, nil)
	if got := sendTarget(h, "muse"); got == nil || got.WakeVia != "wait" || !got.Listening {
		t.Fatalf("polling wait muse: target %+v", got)
	}
	if got := sendTarget(h, "instinct"); got == nil || got.WakeVia != "email" || got.Listening {
		t.Fatalf("email instinct: target %+v", got)
	}
}

// A relay with no wake config knows no method, so it adds no wake facts.
func TestSendTargetNoWakeConfig(t *testing.T) {
	h := newHarness(t, Config{})
	if got := sendTarget(h, "muse"); got != nil {
		t.Fatalf("no wake config: target %+v", got)
	}
}
