package relay

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

func TestWebStatusRouteAndTarget(t *testing.T) {
	h, clk, _ := wakeHarness(t)
	body := fmt.Sprintf(`{"site":"chatgpt.com","state":"signed_out","observed_at":%q}`, clk.Now().Format("2006-01-02T15:04:05.999999999Z07:00"))
	h.do(grokAddr, "PUT", "/v1/agents/self/web-status", body, http.StatusNoContent, nil)
	a := agentInfo(t, h, macAddr, "grokbot")
	if a.SignedOutSite != "chatgpt.com" || a.SignedOutSince.IsZero() {
		t.Fatalf("missing fact: %+v", a)
	}
	var sent envelope.SendResponse
	h.do(instinctAddr, "POST", "/v1/send", `{"to":"grokbot","body":"hello"}`, http.StatusCreated, &sent)
	if sent.Target == nil || sent.Target.SignedOutSite != "chatgpt.com" {
		t.Fatalf("missing target: %+v", sent)
	}
	for _, bad := range []string{
		`{"site":"evil.example","state":"signed_out"}`, `{"site":"chatgpt.com","state":"unknown"}`, body[:len(body)-1] + `,"agent":"muse"}`, body + ` {}`,
	} {
		h.do(grokAddr, "PUT", "/v1/agents/self/web-status", bad, http.StatusBadRequest, nil)
	}
}

type statusPreparer struct{}

func (*statusPreparer) Prepare(context.Context, *envelope.Request) error { return nil }
func (*statusPreparer) NotifyDestination() string                        { return "" }

func TestWebStatusConcurrentPreparerReplacement(t *testing.T) {
	s := &Server{}
	s.SetPreparer(&statusPreparer{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 1000 {
			s.SetPreparer(&statusPreparer{})
		}
	})
	for range 1000 {
		s.notifyWebStatus(t.Context())
	}
	wg.Wait()
}

// webLimit reports, as grokbot, that its site is out of messages until
// until, observed at now.
func webLimit(h *harness, now, until time.Time, want int) {
	h.t.Helper()
	body := fmt.Sprintf(`{"site":"chatgpt.com","state":"limited","until":%q,"observed_at":%q}`, until.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	h.do(grokAddr, "PUT", "/v1/agents/self/web-status", body, want, nil)
}

// R5, R6: a web agent's usage limit shows on the roster as limited until
// its reset, and an authenticated report clears it.
func TestWebLimitOnRoster(t *testing.T) {
	h, clk, _ := statusHarness(t)
	h.do(grokAddr, "GET", "/v1/poll?hold=0", "", http.StatusNoContent, nil)
	until := clk.Now().Add(90 * time.Minute).UTC().Truncate(time.Millisecond)
	webLimit(h, clk.Now(), until, http.StatusNoContent)
	a := agentInfo(t, h, macAddr, "grokbot")
	if a.Status != client.StatusLimited || !a.LimitedUntil.Equal(until) || a.StatusReason != "until "+utcClock(until, clk.Now()) {
		t.Fatalf("limited agent = %+v", a)
	}
	var sent envelope.SendResponse
	h.do(instinctAddr, "POST", "/v1/send", `{"to":"grokbot","body":"hello"}`, http.StatusCreated, &sent)
	if sent.Target == nil || !sent.Target.LimitedUntil.Equal(until) {
		t.Fatalf("send target = %+v", sent.Target)
	}
	clk.advance(time.Second)
	body := fmt.Sprintf(`{"site":"chatgpt.com","state":"authenticated","observed_at":%q}`, clk.Now().Format(time.RFC3339Nano))
	h.do(grokAddr, "PUT", "/v1/agents/self/web-status", body, http.StatusNoContent, nil)
	if a := agentInfo(t, h, macAddr, "grokbot"); a.Status == client.StatusLimited || !a.LimitedUntil.IsZero() {
		t.Fatalf("authenticated report kept the limit: %+v", a)
	}
}

// A limit that has passed no longer shows.
func TestWebLimitLapses(t *testing.T) {
	h, clk, _ := statusHarness(t)
	webLimit(h, clk.Now(), clk.Now().Add(time.Minute), http.StatusNoContent)
	clk.advance(2 * time.Minute)
	h.do(grokAddr, "GET", "/v1/poll?hold=0", "", http.StatusNoContent, nil)
	if a := agentInfo(t, h, macAddr, "grokbot"); a.Status != client.StatusIdle || !a.LimitedUntil.IsZero() {
		t.Fatalf("lapsed limit: %+v", a)
	}
}

// The relay refuses a limit without a reset time, one more than 8 days
// ahead, one already past, and a reset time on any other state.
func TestWebLimitValidation(t *testing.T) {
	h, clk, _ := wakeHarness(t)
	now := clk.Now()
	webLimit(h, now, now.Add(8*24*time.Hour+time.Minute), http.StatusBadRequest)
	webLimit(h, now, now.Add(-time.Minute), http.StatusBadRequest)
	for _, bad := range []string{
		fmt.Sprintf(`{"site":"chatgpt.com","state":"limited","observed_at":%q}`, now.Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"site":"chatgpt.com","state":"authenticated","until":%q,"observed_at":%q}`, now.Add(time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)),
	} {
		h.do(grokAddr, "PUT", "/v1/agents/self/web-status", bad, http.StatusBadRequest, nil)
	}
	webLimit(h, now, now.Add(7*24*time.Hour), http.StatusNoContent)
}

// A relay that takes limit reports says so.
func TestCapabilitiesWebLimits(t *testing.T) {
	h, _, _ := wakeHarness(t)
	var caps client.Capabilities
	h.do(grokAddr, "GET", "/v1/capabilities", "", http.StatusOK, &caps)
	if !caps.WebLimits {
		t.Fatalf("caps = %+v", caps)
	}
}
