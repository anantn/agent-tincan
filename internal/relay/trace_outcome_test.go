package relay_test

import (
	"strings"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/policy"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/testrelay"
)

func TestRejectedChildDoesNotFailAnsweredParent(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	m.Server.SetPreparer(policy.New(m.Store, policy.Config{}))
	sender, target := m.Client(t, "grokbot"), m.Client(t, "muse")
	parent, err := sender.Send(t.Context(), "muse", "test wake", envelope.KindAsk, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Claim(t.Context(), parent.ID); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_, err := target.Send(t.Context(), "grokbot", "ask back", envelope.KindAsk, parent.ID, false)
		if err == nil || !strings.Contains(err.Error(), "loop back") {
			t.Fatalf("loop: %v", err)
		}
	}
	if _, err := target.Reply(t.Context(), parent.ID, "received", envelope.StatusAnswered); err != nil {
		t.Fatal(err)
	}
	var trace struct {
		Steps []envelope.Result `json:"steps"`
	}
	if err := sender.Raw(t.Context(), "GET", "/v1/trace/"+parent.TraceID, nil, &trace); err != nil {
		t.Fatal(err)
	}
	if len(trace.Steps) != 1 || trace.Steps[0].Status != envelope.StatusAnswered || trace.Steps[0].Reply == nil || trace.Steps[0].Reply.Status != envelope.StatusAnswered {
		t.Fatalf("outcome: %+v", trace.Steps)
	}
}
