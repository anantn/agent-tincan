package client_test

import (
	"encoding/json"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

// AgentInfo embeds Target with its fields at the top level, so the wake
// facts a send carries must not share a JSON name with the roster's own
// wake and online fields. A round trip keeps all four apart.
func TestAgentInfoJSONKeepsWakeAndOnline(t *testing.T) {
	in := client.AgentInfo{Name: "muse", Wake: "channel", Online: true, Target: envelope.Target{WakeVia: "socket", Listening: false}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out client.AgentInfo
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Wake != "channel" || !out.Online || out.WakeVia != "socket" || out.Listening {
		t.Fatalf("round trip of %s = %+v", b, out)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["wake"] != "channel" || fields["online"] != true || fields["wake_via"] != "socket" {
		t.Fatalf("fields = %v", fields)
	}
}
