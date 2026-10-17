package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

// trace names the original target of a request that moved.
func TestTraceShowsMove(t *testing.T) {
	res := envelope.Result{Status: envelope.StatusAnswered, Request: envelope.Request{ID: "r1", From: "codex", To: "gemini-web", Hop: 1, Kind: envelope.KindAsk, Body: "summarize this paper", MovedFrom: []string{"claude-web"}}}
	got := formatTrace(traceResp{TraceID: "r1", Steps: []envelope.Result{res}})
	if !strings.Contains(got, "codex -> gemini-web (moved from claude-web)") {
		t.Fatalf("trace = %q", got)
	}
	res.Request.MovedFrom = nil
	if got := formatTrace(traceResp{TraceID: "r1", Steps: []envelope.Result{res}}); strings.Contains(got, "moved") {
		t.Fatalf("unmoved trace = %q", got)
	}
}

// doctor prints each limited web teammate with its reset time.
func TestWebLimitCheck(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.Local)
	until := now.Add(2 * time.Hour)
	for _, tc := range []struct {
		name, roster, status string
		want                 []string
	}{
		{"limited", fmt.Sprintf(`{"agents":[{"name":"claude-web","wake":"wait","status":"limited","limited_until":%q},{"name":"gemini-web","wake":"wait","status":"idle"}]}`, until.Format(time.RFC3339)),
			"warn", []string{"claude-web is out of messages until 14:00", "moves"}},
		{"none", `{"agents":[{"name":"gemini-web","wake":"wait","status":"idle"}]}`, "ok", []string{"no web teammate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tc.roster)
			}))
			defer srv.Close()
			r, err := client.NewRelayFor(client.Config{Relay: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			c := webLimitCheck(t.Context(), r, now)
			text := c.Detail + "\n" + c.Fix
			if c.Name != "web limits" || c.Status != tc.status {
				t.Fatalf("check = %+v", c)
			}
			for _, w := range tc.want {
				if !strings.Contains(text, w) {
					t.Errorf("missing %q in %q", w, text)
				}
			}
		})
	}
}
