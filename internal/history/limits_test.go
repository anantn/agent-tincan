package history

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

// The page text the extension reports for a site at its usage limit, as
// the site words it. Only claude.ai's wording is documented (its help
// center); the other sites' fixtures are captured live before their rules
// are written.
const (
	claudeFiveHourFixture = "5-hour limit reached - resets 2:00 PM"
	claudeWeeklyFixture   = "You've reached your weekly limit. It resets Monday at 9:00 AM."
	claudeNoResetFixture  = "5-hour limit reached"
)

// The text a site shows when it moves the owner to a smaller model and
// keeps answering: not a limit.
var downgradeFixtures = map[Source]string{
	SourceChatGPT: "You've hit the Free plan limit for GPT-4o. Responses will use another model until your limit resets after 3:12 PM.",
	SourceGemini:  "You've reached your limit with 2.5 Pro. Responses will use 2.5 Flash until it resets.",
}

// R6: Claude's 5-hour limit text yields limited until the time it names,
// in the owner's own zone.
func TestClaudeLimitTextResetTime(t *testing.T) {
	now := time.Date(2026, 10, 17, 10, 30, 0, 0, time.Local)
	until, ok := detectLimit(SourceClaudeAI, claudeFiveHourFixture, now)
	if want := time.Date(2026, 10, 17, 14, 0, 0, 0, time.Local); !ok || !until.Equal(want) {
		t.Fatalf("until = %v %v, want %v", until, ok, want)
	}
	// Past 2 PM, the named time is tomorrow's.
	late := time.Date(2026, 10, 17, 15, 0, 0, 0, time.Local)
	if until, ok := detectLimit(SourceClaudeAI, claudeFiveHourFixture, late); !ok || !until.Equal(time.Date(2026, 10, 18, 14, 0, 0, 0, time.Local)) {
		t.Fatalf("late until = %v %v", until, ok)
	}
}

// Claude's weekly limit names a weekday and a time.
func TestClaudeWeeklyLimitResetTime(t *testing.T) {
	now := time.Date(2026, 10, 17, 10, 30, 0, 0, time.Local) // a Saturday
	until, ok := detectLimit(SourceClaudeAI, claudeWeeklyFixture, now)
	if want := time.Date(2026, 10, 19, 9, 0, 0, 0, time.Local); !ok || !until.Equal(want) {
		t.Fatalf("until = %v %v, want %v", until, ok, want)
	}
}

// A limit with no reset time yields limited for 60 minutes.
func TestLimitWithoutResetTime(t *testing.T) {
	now := time.Date(2026, 10, 17, 10, 30, 0, 0, time.Local)
	until, ok := detectLimit(SourceClaudeAI, claudeNoResetFixture, now)
	if !ok || !until.Equal(now.Add(DefaultLimitWait)) || DefaultLimitWait != time.Hour {
		t.Fatalf("until = %v %v", until, ok)
	}
}

// A switch to a smaller model is not a limit, on its own site or any
// other, and ordinary page text is not either.
func TestModelDowngradeIsNotLimited(t *testing.T) {
	now := time.Now()
	for _, site := range WebSites {
		for _, text := range append([]string{"", "Claude can make mistakes."}, downgradeFixtures[SourceChatGPT], downgradeFixtures[SourceGemini]) {
			if until, ok := detectLimit(site, text, now); ok {
				t.Errorf("%s: %q read as limited until %v", site, text, until)
			}
		}
	}
}

// limitRig is a web rig whose cooldown and auth clock share one fake
// clock, so a limit's reset time can be checked exactly.
func limitRig(t *testing.T) (*webRig, *fakeClock) {
	t.Helper()
	rig := newWebRig(t)
	clk := newFakeClock()
	rig.agent.Native = &Client{Channel: rig.browser, Cooldown: &SiteCooldown{Now: clk.Now}}
	rig.agent.clock = clk
	return rig, clk
}

// A 429 on the send whose Retry-After is 900 seconds yields limited for 15
// minutes: the reported status says so and the site is left alone until
// then.
func TestSendRetryAfterYieldsLimited(t *testing.T) {
	rig, clk := limitRig(t)
	rig.browser.sendErr, rig.browser.sendRetryAfter = "rate_limited", 900
	res := rig.ask(t, "grokbot", "hello")
	if res.Status != envelope.StatusFailed {
		t.Fatalf("%s %q", res.Status, res.Reply.Body)
	}
	want := clk.Now().Add(15 * time.Minute)
	if got := rig.agent.auth.latest; got.State != envelope.WebLimited || !got.Until.Equal(want) {
		t.Fatalf("status = %+v, want limited until %v", got, want)
	}
	if left := rig.agent.Native.CooldownRemaining(SourceChatGPT); left != 15*time.Minute {
		t.Fatalf("cooldown = %v", left)
	}
}

// A bare 429 is a short rate limit, not a usage limit: a cooldown and no
// limited report.
func TestBareRateLimitIsNotLimited(t *testing.T) {
	rig, _ := limitRig(t)
	rig.browser.sendErr = "rate_limited"
	rig.ask(t, "grokbot", "hello")
	if got := rig.agent.auth.latest; got.State == envelope.WebLimited {
		t.Fatalf("status = %+v", got)
	}
	if left := rig.agent.Native.CooldownRemaining(SourceChatGPT); left != DefaultRateLimitCooldown {
		t.Fatalf("cooldown = %v", left)
	}
}

// A send the page refused with the site's limit text in view is a usage
// limit: limited until the named time, and the reply says nothing was
// sent and when the site resets.
func TestSendLimitTextYieldsLimited(t *testing.T) {
	rig, clk := limitRig(t)
	rig.agent.Site = SourceClaudeAI
	rig.browser.sendErr, rig.browser.sendNotice = "send_failed", claudeNoResetFixture
	res := rig.ask(t, "grokbot", "hello")
	until := clk.Now().Add(DefaultLimitWait)
	if got := rig.agent.auth.latest; got.State != envelope.WebLimited || !got.Until.Equal(until) {
		t.Fatalf("status = %+v", got)
	}
	if res.Status != envelope.StatusFailed || !strings.Contains(res.Reply.Body, "out of messages") || !strings.Contains(res.Reply.Body, "Nothing was sent") {
		t.Fatalf("%s %q", res.Status, res.Reply.Body)
	}
}

// The limit text the page shows after a send that went through holds the
// next request back, while this one is answered as usual.
func TestSentWithLimitTextHoldsNextRequest(t *testing.T) {
	rig, _ := limitRig(t)
	rig.agent.Site = SourceClaudeAI
	if got := rig.agent.Native.CooldownRemaining(SourceClaudeAI); got != 0 {
		t.Fatalf("cooldown before = %v", got)
	}
	rig.agent.observeSent(SendResult{Notice: claudeNoResetFixture})
	if got := rig.agent.auth.latest; got.State != envelope.WebLimited {
		t.Fatalf("status = %+v", got)
	}
	if left := rig.agent.Native.CooldownRemaining(SourceClaudeAI); left != DefaultLimitWait {
		t.Fatalf("cooldown = %v", left)
	}
}

// An idle probe while the limit runs reports limited, not authenticated,
// and once it has passed reports authenticated again.
func TestProbeDuringLimitStaysLimited(t *testing.T) {
	clk := newFakeClock()
	w := &WebAgent{Site: SourceClaudeAI, Native: &Client{Channel: &authChannel{}, Cooldown: &SiteCooldown{Now: clk.Now}}, clock: clk, Log: &bytes.Buffer{}}
	w.noteLimit(clk.Now().Add(time.Hour))
	w.observeAuth(nil)
	if got := w.auth.latest; got.State != envelope.WebLimited || !got.Until.Equal(clk.Now().Add(time.Hour)) {
		t.Fatalf("during the limit: %+v", got)
	}
	if err := clk.Sleep(t.Context(), time.Hour); err != nil {
		t.Fatal(err)
	}
	w.probeSession(t.Context())
	if got := w.auth.latest; got.State != "authenticated" || !got.Until.IsZero() {
		t.Fatalf("after the limit: %+v", got)
	}
}

// Against a relay without web_limits, a limited observation goes out as
// authenticated (it is signed in), so the older relay never sees a state
// it would refuse; with web_limits it goes out as limited.
func TestLimitedReportNeedsWebLimits(t *testing.T) {
	for _, caps := range []string{`{}`, `{"web_limits":true}`} {
		var mu sync.Mutex
		var sent []envelope.WebStatus
		relay := client.NewRelayHTTP("http://relay", &http.Client{Transport: statusTransport(func(r *http.Request) (*http.Response, error) {
			body := `{}`
			switch r.URL.Path {
			case "/v1/capabilities":
				body = caps
			case "/v1/agents/self/web-status":
				var in envelope.WebStatus
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &in)
				mu.Lock()
				sent = append(sent, in)
				mu.Unlock()
				return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
		clk := newFakeClock()
		w := &WebAgent{Site: SourceClaudeAI, Relay: relay, Native: &Client{Channel: &authChannel{}, Cooldown: &SiteCooldown{Now: clk.Now}}, clock: clk, Log: &bytes.Buffer{}}
		w.noteLimit(clk.Now().Add(time.Hour))
		w.flushWebStatus(t.Context())
		mu.Lock()
		if len(sent) != 1 {
			t.Fatalf("%s: sent %+v", caps, sent)
		}
		got := sent[0]
		mu.Unlock()
		if caps == `{}` && (got.State != "authenticated" || !got.Until.IsZero()) {
			t.Fatalf("old relay got %+v", got)
		}
		if caps != `{}` && (got.State != envelope.WebLimited || !got.Until.Equal(clk.Now().Add(time.Hour))) {
			t.Fatalf("new relay got %+v", got)
		}
	}
}

// handbackRig is a web rig on a fake clock that starts at the real time
// (the relay checks report times against its own), with the agent's kind
// set and a second web chat teammate, claude-web, online to take a moved
// ask.
func handbackRig(t *testing.T) (*webRig, *fakeClock) {
	t.Helper()
	rig := newWebRig(t)
	clk := &fakeClock{t: time.Now()}
	rig.agent.Native = &Client{Channel: rig.browser, Cooldown: &SiteCooldown{Now: clk.Now}}
	rig.agent.clock = clk
	rig.mesh.JoinOnMachineOf(t, "instinct", "claude-web")
	for name, kind := range map[string]string{"chatgpt-web": "chatgpt-web", "claude-web": "claude-web"} {
		if ok, err := rig.mesh.Store.SetAgentKind(t.Context(), name, kind); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	if _, err := rig.mesh.Client(t, "claude-web").Peek(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	return rig, clk
}

// sendAsk sends body from from to chatgpt-web and lets the agent handle it.
func (r *webRig) sendAsk(t *testing.T, from, body string) (*client.Relay, string) {
	t.Helper()
	c := r.mesh.Client(t, from)
	req, err := c.Send(t.Context(), "chatgpt-web", body, envelope.KindAsk, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := r.agent.PollOnce(t.Context()); err != nil || n != 1 {
		t.Fatalf("PollOnce = %d, %v", n, err)
	}
	return c, req.ID
}

// A 429 before the send button was clicked hands the ask back and it moves
// to claude-web: no reply from chatgpt-web, and the asker's request now
// names claude-web.
func TestLimitBeforeClickHandsBack(t *testing.T) {
	rig, _ := handbackRig(t)
	rig.browser.sendErr, rig.browser.sendRetryAfter = "rate_limited", 900
	c, id := rig.sendAsk(t, "codex", "summarize this paper")
	res, err := c.Get(t.Context(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply != nil || res.Request.To != "claude-web" || res.Status != envelope.StatusQueued {
		t.Fatalf("asker sees %+v %s %+v", res.Request, res.Status, res.Reply)
	}
}

// A 429 after the click replies failed: the message may have gone out.
func TestLimitAfterClickFails(t *testing.T) {
	rig, _ := handbackRig(t)
	rig.browser.sendErr, rig.browser.sendRetryAfter, rig.browser.sendClicked = "rate_limited", 900, true
	c, id := rig.sendAsk(t, "codex", "summarize this paper")
	res, err := c.Get(t.Context(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != envelope.StatusFailed || res.Request.To != "chatgpt-web" || !strings.Contains(res.Reply.Body, "may have been sent") {
		t.Fatalf("asker sees %+v %s %+v", res.Request, res.Status, res.Reply)
	}
}

// While the limit runs, the next ask is handed back before anything is
// typed and moves too.
func TestLimitedAgentHandsBackBeforeSending(t *testing.T) {
	rig, clk := handbackRig(t)
	rig.agent.noteLimit(clk.Now().Add(time.Hour))
	c, id := rig.sendAsk(t, "codex", "summarize this paper")
	if res, err := c.Get(t.Context(), id, 0); err != nil || res.Reply != nil || res.Request.To != "claude-web" {
		t.Fatalf("asker sees %+v %v", res, err)
	}
	if len(rig.browser.sent()) != 0 {
		t.Fatalf("typed into the site: %+v", rig.browser.sent())
	}
}

// An ask the relay will not move (409) is answered failed.
func TestRefusedHandbackFails(t *testing.T) {
	rig, clk := handbackRig(t)
	rig.agent.noteLimit(clk.Now().Add(time.Hour))
	c := rig.mesh.Client(t, "codex")
	req, err := c.Send(t.Context(), "chatgpt-web", "conversation: abc123\nsummarize it", envelope.KindAsk, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := rig.agent.PollOnce(t.Context()); err != nil || n != 1 {
		t.Fatalf("PollOnce = %d, %v", n, err)
	}
	res, err := c.Get(t.Context(), req.ID, 0)
	if err != nil || res.Status != envelope.StatusFailed || res.Request.To != "chatgpt-web" {
		t.Fatalf("asker sees %+v %v", res, err)
	}
}
