package cli

import (
	"strings"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/store"
)

func TestTraceExplainsReplyOutcomeAndRejection(t *testing.T) {
	for _, status := range []envelope.Status{envelope.StatusAnswered, envelope.StatusFailed} {
		out := formatTrace(traceResp{TraceID: "r1", Steps: []envelope.Result{{Request: envelope.Request{ID: "r1", From: "grokbot", To: "hermes"}, Status: status, Reply: &envelope.Reply{From: "hermes", Status: status, Body: "result"}}}, Events: []store.AuditEvent{{Event: "rejected", Actor: "hermes", Detail: `{"reason":"request would loop back"}`}, {Event: "replied", Actor: "hermes", Detail: `{"status":"` + string(status) + `"}`}}})
		for _, want := range []string{"[" + string(status) + "]", "status " + string(status), "request would loop back"} {
			if !strings.Contains(out, want) {
				t.Fatalf("missing %q: %s", want, out)
			}
		}
	}
}
