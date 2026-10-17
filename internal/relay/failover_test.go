package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/store"
)

// In these tests grokbot is claude-web (claude.ai) and muse is gemini-web
// (gemini.google.com), both online; instinct asks.
func failoverHarness(t *testing.T) (*harness, *fakeClock) {
	t.Helper()
	h, clk, _ := statusHarness(t)
	for name, kind := range map[string]string{"grokbot": "claude-web", "muse": "gemini-web"} {
		if ok, err := h.st.SetAgentKind(t.Context(), name, kind); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	listen(h, grokAddr, museAddr)
	return h, clk
}

// listen marks the agents at addrs online with a peek, which delivers
// nothing.
func listen(h *harness, addrs ...string) {
	h.t.Helper()
	for _, addr := range addrs {
		req := httptest.NewRequest("GET", "/v1/poll?hold=0&peek=1", nil)
		req.RemoteAddr = addr
		h.h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

var webSite = map[string]string{grokAddr: "claude.ai", museAddr: "gemini.google.com"}

// limitAt reports, as the agent at addr, that its site is out of messages
// for d, and returns the reset time as the relay stores it.
func limitAt(h *harness, clk *fakeClock, addr string, d time.Duration) time.Time {
	h.t.Helper()
	until := clk.Now().Add(d).UTC().Truncate(time.Millisecond)
	body := fmt.Sprintf(`{"site":%q,"state":"limited","until":%q,"observed_at":%q}`, webSite[addr], until.Format(time.RFC3339Nano), clk.Now().Format(time.RFC3339Nano))
	h.do(addr, "PUT", "/v1/agents/self/web-status", body, http.StatusNoContent, nil)
	clk.advance(time.Millisecond)
	return until
}

// clearLimit reports, as the agent at addr, that it is signed in again.
func clearLimit(h *harness, clk *fakeClock, addr string) {
	h.t.Helper()
	body := fmt.Sprintf(`{"site":%q,"state":"authenticated","observed_at":%q}`, webSite[addr], clk.Now().Format(time.RFC3339Nano))
	h.do(addr, "PUT", "/v1/agents/self/web-status", body, http.StatusNoContent, nil)
	clk.advance(time.Millisecond)
}

func handback(h *harness, addr, id string, until time.Time, want int) client.HandbackResult {
	h.t.Helper()
	var out client.HandbackResult
	var dst any
	if want == http.StatusOK {
		dst = &out
	}
	h.do(addr, "POST", "/v1/requests/"+id+"/handback", fmt.Sprintf(`{"reason":"limited","until":%q}`, until.Format(time.RFC3339Nano)), want, dst)
	return out
}

func getAs(h *harness, addr, id string) store.Result {
	h.t.Helper()
	var res store.Result
	h.do(addr, "GET", "/v1/requests/"+id, "", http.StatusOK, &res)
	return res
}

func auditEvents(h *harness, event string) []store.AuditEvent {
	h.t.Helper()
	all, err := h.st.AuditEvents(h.t.Context())
	if err != nil {
		h.t.Fatal(err)
	}
	return slices.DeleteFunc(all, func(e store.AuditEvent) bool { return e.Event != event })
}

// AE1: an ask to limited claude-web moves to gemini-web, keeps its id,
// sender and chain, and the asker sees one note naming who took it.
func TestFailoverMovesQueuedAsk(t *testing.T) {
	h, clk := failoverHarness(t)
	until := limitAt(h, clk, grokAddr, 90*time.Minute)
	var sent envelope.SendResponse
	h.do(instinctAddr, "POST", "/v1/send", `{"to":"grokbot","body":"summarize this paper"}`, http.StatusCreated, &sent)
	if sent.To != "muse" || !slices.Equal(sent.MovedFrom, []string{"grokbot"}) {
		t.Fatalf("send response = %+v", sent.Request)
	}
	res := getAs(h, instinctAddr, sent.ID)
	if res.Request.To != "muse" || res.Request.From != "instinct" || !slices.Equal(res.Request.Chain, []string{"instinct"}) || res.Status != envelope.StatusQueued {
		t.Fatalf("request = %+v %s", res.Request, res.Status)
	}
	want := "grokbot is out of messages until " + utcClock(until, clk.Now()) + "; muse took your request"
	if res.RelayNote == nil || !strings.Contains(res.RelayNote.Note, want) || !strings.Contains(res.RelayNote.Note, "without any earlier conversation") {
		t.Fatalf("note = %+v", res.RelayNote)
	}
	if n := relayNotices(h, "instinct"); len(n) != 1 || !strings.Contains(n[0].Body, want) {
		t.Fatalf("notices = %+v", n)
	}
	if m := auditEvents(h, "moved"); len(m) != 1 || m[0].RequestID != sent.ID || !strings.Contains(m[0].Detail, `"from":"grokbot"`) || !strings.Contains(m[0].Detail, `"to":"muse"`) {
		t.Fatalf("moved audit = %+v", m)
	}
	h.do(grokAddr, "GET", "/v1/poll?hold=0", "", http.StatusNoContent, nil)
	var got pollResult
	h.do(museAddr, "GET", "/v1/poll?hold=0", "", http.StatusOK, &got)
	if len(got.Requests) != 1 || got.Requests[0].ID != sent.ID {
		t.Fatalf("muse got %+v", got)
	}
}

// AE3: a claimed ask handed back unsent moves; gemini-web's reply reaches
// the asker on the same id.
func TestFailoverHandbackMoves(t *testing.T) {
	h, clk := failoverHarness(t)
	sent := h.send(instinctAddr, "grokbot", "summarize this paper")
	h.do(grokAddr, "POST", "/v1/requests/"+sent.ID+"/claim", "", http.StatusOK, nil)
	until := limitAt(h, clk, grokAddr, time.Hour)
	out := handback(h, grokAddr, sent.ID, until, http.StatusOK)
	if out.Outcome != client.HandbackMoved || out.To != "muse" {
		t.Fatalf("handback = %+v", out)
	}
	h.do(grokAddr, "POST", "/v1/requests/"+sent.ID+"/reply", `{"body":"too late"}`, http.StatusNotFound, nil)
	h.do(museAddr, "POST", "/v1/requests/"+sent.ID+"/claim", "", http.StatusOK, nil)
	h.do(museAddr, "POST", "/v1/requests/"+sent.ID+"/reply", `{"body":"the summary"}`, http.StatusOK, nil)
	res := getAs(h, instinctAddr, sent.ID)
	if res.Status != envelope.StatusAnswered || res.Reply == nil || res.Reply.From != "muse" || res.Reply.Body != "the summary" {
		t.Fatalf("asker sees %+v", res)
	}
	if hb := auditEvents(h, "handed_back"); len(hb) != 1 || !strings.Contains(hb[0].Detail, `"outcome":"moved"`) {
		t.Fatalf("handed_back audit = %+v", hb)
	}
}

// AE2: a council member ask does not move; its handback gets 409 and it is
// still the limited agent's to answer, which fails it as before.
func TestFailoverCouncilAskStays(t *testing.T) {
	h, clk := failoverHarness(t)
	if ok, err := h.st.SetAgentKind(t.Context(), "instinct", "council"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	until := limitAt(h, clk, grokAddr, time.Hour)
	sent := h.send(instinctAddr, "grokbot", "a council question")
	if sent.To != "grokbot" {
		t.Fatalf("council ask moved: %+v", sent)
	}
	h.srv.Sweep(t.Context())
	h.do(grokAddr, "POST", "/v1/requests/"+sent.ID+"/claim", "", http.StatusOK, nil)
	handback(h, grokAddr, sent.ID, until, http.StatusConflict)
	h.do(grokAddr, "POST", "/v1/requests/"+sent.ID+"/reply", `{"body":"out of messages","status":"failed"}`, http.StatusOK, nil)
	if res := getAs(h, instinctAddr, sent.ID); res.Status != envelope.StatusFailed || res.Reply.From != "grokbot" {
		t.Fatalf("asker sees %+v", res)
	}
}

// Asks that were held, are notifies, are in a group, carry attachments,
// name a conversation, have a clarification exchange, may have been sent
// already, or involve dot-web, a council or a non-web target never move.
func TestFailoverIneligible(t *testing.T) {
	ask := envelope.Request{Kind: envelope.KindAsk, Body: "summarize this paper"}
	with := func(f func(*envelope.Request)) envelope.Request { r := ask; f(&r); return r }
	if why := failoverIneligible(ask, false, "codex", "claude-web"); why != "" {
		t.Fatalf("plain ask: %s", why)
	}
	for name, tc := range map[string]struct {
		req      envelope.Request
		claimed  bool
		from, to string
	}{
		"held":           {req: with(func(r *envelope.Request) { r.WasHeld, r.Approved = true, true })},
		"notify":         {req: with(func(r *envelope.Request) { r.Kind = envelope.KindNotify })},
		"group":          {req: with(func(r *envelope.Request) { r.Group = "g1" })},
		"attachments":    {req: with(func(r *envelope.Request) { r.Attachments = []envelope.Attachment{{ID: "a"}} })},
		"conversation":   {req: with(func(r *envelope.Request) { r.Body = "  Conversation: abc123\nand more" })},
		"exchange":       {req: with(func(r *envelope.Request) { r.Exchanges = []envelope.Exchange{{Question: "which?"}} })},
		"resumed":        {req: with(func(r *envelope.Request) { r.Resumed = true })},
		"maybe sent":     {req: ask, claimed: true},
		"to dot-web":     {req: ask, to: "dot-web"},
		"from dot-web":   {req: ask, from: "dot-web"},
		"from council":   {req: ask, from: "council"},
		"to a CLI":       {req: ask, to: "codex"},
		"to no kind":     {req: ask, to: "-"},
		"to the council": {req: ask, to: "council"},
	} {
		from, to := tc.from, tc.to
		if from == "" {
			from = "codex"
		}
		switch to {
		case "":
			to = "claude-web"
		case "-":
			to = ""
		}
		if failoverIneligible(tc.req, tc.claimed, from, to) == "" {
			t.Errorf("%s: would move", name)
		}
	}
}

// When no teammate can take it, the ask stays, is held back until the
// reset, and the asker is told it waits; the limited agent is not handed
// it before then.
func TestFailoverWaitsWithNoTarget(t *testing.T) {
	h, clk := failoverHarness(t)
	limitAt(h, clk, museAddr, 2*time.Hour)
	until := limitAt(h, clk, grokAddr, 30*time.Minute)
	sent := h.send(instinctAddr, "grokbot", "summarize this paper")
	res := getAs(h, instinctAddr, sent.ID)
	if res.Request.To != "grokbot" || res.RelayNote == nil || !strings.Contains(res.RelayNote.Note, "it waits") {
		t.Fatalf("asker sees %+v %+v", res.Request, res.RelayNote)
	}
	h.srv.Sweep(t.Context())
	if n := relayNotices(h, "instinct"); len(n) != 1 {
		t.Fatalf("notices = %+v", n)
	}
	h.do(grokAddr, "GET", "/v1/poll?hold=0", "", http.StatusNoContent, nil)
	clk.advance(until.Sub(clk.Now()) + time.Second)
	var got pollResult
	h.do(grokAddr, "GET", "/v1/poll?hold=0", "", http.StatusOK, &got)
	if len(got.Requests) != 1 || got.Requests[0].ID != sent.ID {
		t.Fatalf("after the reset grokbot got %+v", got)
	}
}

// A held-back ask moves on a later sweep once a teammate is free.
func TestFailoverSweepMovesLater(t *testing.T) {
	h, clk := failoverHarness(t)
	limitAt(h, clk, museAddr, time.Hour)
	limitAt(h, clk, grokAddr, time.Hour)
	sent := h.send(instinctAddr, "grokbot", "summarize this paper")
	clearLimit(h, clk, museAddr)
	h.srv.Sweep(t.Context())
	if res := getAs(h, instinctAddr, sent.ID); res.Request.To != "muse" {
		t.Fatalf("not moved by the sweep: %+v", res.Request)
	}
	if n := relayNotices(h, "instinct"); len(n) != 2 {
		t.Fatalf("notices = %+v", n)
	}
}

// An ask moved from claude-web to gemini-web never moves back.
func TestFailoverNeverMovesBack(t *testing.T) {
	h, clk := failoverHarness(t)
	limitAt(h, clk, grokAddr, time.Hour)
	sent := h.send(instinctAddr, "grokbot", "summarize this paper")
	clearLimit(h, clk, grokAddr)
	limitAt(h, clk, museAddr, time.Hour)
	listen(h, grokAddr, museAddr)
	h.srv.Sweep(t.Context())
	if res := getAs(h, instinctAddr, sent.ID); res.Request.To != "muse" {
		t.Fatalf("moved back: %+v", res.Request)
	}
}

// refusingPreparer refuses every move to refuse, as an approval.json gate
// on it would.
type refusingPreparer struct{ refuse string }

func (refusingPreparer) Prepare(ctx context.Context, req *envelope.Request) error {
	return newChain{}.Prepare(ctx, req)
}

func (p refusingPreparer) CheckMove(_ context.Context, _ envelope.Request, to string) error {
	if to == p.refuse {
		return errors.New("gated")
	}
	return nil
}

// A move the policy refuses (a target gated by approval.json) does not
// happen; the ask stays.
func TestFailoverRefusedMoveStays(t *testing.T) {
	h, clk := failoverHarness(t)
	h.srv.SetPreparer(refusingPreparer{refuse: "muse"})
	limitAt(h, clk, grokAddr, time.Hour)
	sent := h.send(instinctAddr, "grokbot", "summarize this paper")
	if res := getAs(h, instinctAddr, sent.ID); res.Request.To != "grokbot" {
		t.Fatalf("moved to a gated target: %+v", res.Request)
	}
}

// A target listed in failover.json is never chosen.
func TestFailoverExcludedTarget(t *testing.T) {
	h, clk := failoverHarness(t)
	path := filepath.Join(t.TempDir(), FailoverFile)
	if err := os.WriteFile(path, []byte(`{"exclude":["muse"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.srv.cfg.FailoverPath = path
	limitAt(h, clk, grokAddr, time.Hour)
	sent := h.send(instinctAddr, "grokbot", "summarize this paper")
	if res := getAs(h, instinctAddr, sent.ID); res.Request.To != "grokbot" {
		t.Fatalf("moved to an excluded target: %+v", res.Request)
	}
	// A bad file excludes nothing.
	if err := os.WriteFile(path, []byte(`{"exclude":"muse"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.srv.Sweep(t.Context())
	if res := getAs(h, instinctAddr, sent.ID); res.Request.To != "muse" {
		t.Fatalf("a bad file still excluded: %+v", res.Request)
	}
}

// After a move the old target cannot claim, reply to, ask a question on,
// post progress on or read the ask: each is not found.
func TestFailoverOldTargetLosesTheAsk(t *testing.T) {
	h, clk := failoverHarness(t)
	sent := h.send(instinctAddr, "grokbot", "summarize this paper")
	h.do(grokAddr, "POST", "/v1/requests/"+sent.ID+"/claim", "", http.StatusOK, nil)
	handback(h, grokAddr, sent.ID, limitAt(h, clk, grokAddr, time.Hour), http.StatusOK)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/claim", ""},
		{"POST", "/reply", `{"body":"x"}`},
		{"POST", "/reply", `{"body":"which paper?","status":"needs_input"}`},
		{"POST", "/progress", `{"note":"working"}`},
		{"GET", "", ""},
	} {
		h.do(grokAddr, c.method, "/v1/requests/"+sent.ID+c.path, c.body, http.StatusNotFound, nil)
	}
}

// Handback rules: only the claimant hands back, the reset time is capped
// at the limit the agent reported, and an agent that reported none gets
// 409.
func TestHandbackRules(t *testing.T) {
	h, clk := failoverHarness(t)
	limitAt(h, clk, museAddr, 3*time.Hour)
	sent := h.send(instinctAddr, "grokbot", "summarize this paper")
	h.do(grokAddr, "POST", "/v1/requests/"+sent.ID+"/claim", "", http.StatusOK, nil)
	handback(h, grokAddr, sent.ID, clk.Now().Add(time.Hour), http.StatusConflict)
	handback(h, instinctAddr, sent.ID, clk.Now().Add(time.Hour), http.StatusNotFound)
	h.do(grokAddr, "POST", "/v1/requests/"+sent.ID+"/handback", `{"reason":"tired","until":"`+clk.Now().Add(time.Hour).Format(time.RFC3339)+`"}`, http.StatusBadRequest, nil)
	reported := limitAt(h, clk, grokAddr, 20*time.Minute)
	out := handback(h, grokAddr, sent.ID, clk.Now().Add(2*time.Hour), http.StatusOK)
	if out.Outcome != client.HandbackRequeued || !out.Until.Equal(reported) {
		t.Fatalf("handback = %+v, want requeued until %v", out, reported)
	}
	h.do(grokAddr, "GET", "/v1/poll?hold=0", "", http.StatusNoContent, nil)
	clk.advance(reported.Sub(clk.Now()) + time.Second)
	var got pollResult
	h.do(grokAddr, "GET", "/v1/poll?hold=0", "", http.StatusOK, &got)
	if len(got.Requests) != 1 {
		t.Fatalf("not redelivered after the capped reset: %+v", got)
	}
}

// queueRecorder records the requests the relay queued, for wakes.
type queueRecorder struct {
	mu  sync.Mutex
	got []envelope.Request
}

func (q *queueRecorder) Queued(_ context.Context, req envelope.Request) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.got = append(q.got, req)
}

// A moved ask wakes its new target, not the limited one.
func TestFailoverWakesNewTarget(t *testing.T) {
	h, clk := failoverHarness(t)
	rec := &queueRecorder{}
	h.srv.SetEvents(rec)
	limitAt(h, clk, grokAddr, time.Hour)
	h.send(instinctAddr, "grokbot", "summarize this paper")
	rec.mu.Lock()
	defer rec.mu.Unlock()
	// The asker's relay notice is queued too; the ask itself goes to muse
	// only.
	asks := slices.DeleteFunc(slices.Clone(rec.got), func(r envelope.Request) bool { return r.From == client.RelaySender })
	if len(asks) != 1 || asks[0].To != "muse" {
		t.Fatalf("woken for %+v", rec.got)
	}
}

// A watch on a limited web agent does not fire until the limit clears.
func TestWatchOnLimitedAgentWaits(t *testing.T) {
	h, clk := failoverHarness(t)
	until := limitAt(h, clk, grokAddr, 10*time.Minute)
	watch(h, instinctAddr, "grokbot", http.StatusCreated)
	h.srv.Sweep(t.Context())
	if n := relayNotices(h, "instinct"); len(n) != 0 {
		t.Fatalf("fired while limited: %+v", n)
	}
	clk.advance(until.Sub(clk.Now()) + time.Second)
	listen(h, grokAddr)
	h.srv.Sweep(t.Context())
	if n := relayNotices(h, "instinct"); len(n) != 1 || !strings.Contains(n[0].Body, "grokbot is idle now") {
		t.Fatalf("notices = %+v", n)
	}
}

// A relay that takes handbacks says so.
func TestCapabilitiesHandback(t *testing.T) {
	h, _ := failoverHarness(t)
	var caps client.Capabilities
	h.do(grokAddr, "GET", "/v1/capabilities", "", http.StatusOK, &caps)
	if !caps.Handback {
		t.Fatalf("caps = %+v", caps)
	}
}
