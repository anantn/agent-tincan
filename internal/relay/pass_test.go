package relay_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/pass"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/testrelay"
)

// passConfig is a relay config with the pass parser tincan relay sets.
func passConfig() relay.Config {
	return relay.Config{ParsePass: func(body string) (relay.PassRef, error) {
		r, err := pass.ParseRequest(body)
		return relay.PassRef{Source: string(r.Source), ConversationID: r.ID, To: r.To}, err
	}}
}

// heldPass is a held listing entry as tincan held reads it.
type heldPass struct {
	envelope.Request
	ToKind string `json:"to_kind"`
	Pass   *struct {
		Source string `json:"source"`
		ID     string `json:"conversation_id"`
		To     string `json:"to"`
	} `json:"pass"`
	PassError string `json:"pass_error"`
}

// AE5: an agent's ask to a pass-kind agent is held with no approval.json,
// and the owner's held listing shows which chat would move where; a
// request that does not parse says why instead.
func TestPassAskHeldWithParsedListing(t *testing.T) {
	m := testrelay.New(t, passConfig())
	if ok, err := m.Store.SetAgentKind(t.Context(), "muse", "pass"); !ok || err != nil {
		t.Fatalf("set kind: %v %v", ok, err)
	}
	sender := m.Client(t, "grokbot")
	good, err := sender.Send(t.Context(), "muse", "from: https://claude.ai/chat/abc-3\nto: chatgpt-web", envelope.KindAsk, "", false)
	if err != nil || good.Status != envelope.StatusHeld {
		t.Fatalf("pass ask = %+v %v, want held", good, err)
	}
	bad, err := sender.Send(t.Context(), "muse", "pass my latest chat please", envelope.KindAsk, "", false)
	if err != nil || bad.Status != envelope.StatusHeld {
		t.Fatalf("bad pass ask = %+v %v, want held", bad, err)
	}
	var held []heldPass
	if err := m.Client(t, "admin").Raw(t.Context(), "GET", "/v1/admin/held", nil, &held); err != nil || len(held) != 2 {
		t.Fatalf("held list: %+v %v", held, err)
	}
	for _, h := range held {
		switch h.ID {
		case good.ID:
			if h.ToKind != "pass" || h.Pass == nil || h.Pass.Source != "claude-ai" || h.Pass.ID != "abc-3" || h.Pass.To != "chatgpt-web" || h.PassError != "" {
				t.Fatalf("good entry = %+v pass %+v", h, h.Pass)
			}
		case bad.ID:
			if h.Pass != nil || !strings.Contains(h.PassError, "from:") {
				t.Fatalf("bad entry = %+v pass %+v", h, h.Pass)
			}
		}
	}
}

// A seed sent under a pass group is audited as pass_sent with ids and
// byte counts only; when it continues an ask to a pass agent, the chat's
// source and id come from that ask.
func TestPassSentAudited(t *testing.T) {
	m := testrelay.New(t, passConfig())
	if ok, err := m.Store.SetAgentKind(t.Context(), "muse", "pass"); !ok || err != nil {
		t.Fatalf("set kind: %v %v", ok, err)
	}
	ask, err := m.Client(t, "grokbot").Send(t.Context(), "muse", "from: grok abc\nto: instinct", envelope.KindAsk, "", false)
	if err != nil {
		t.Fatal(err)
	}
	admin := m.Client(t, "admin")
	if err := admin.Raw(t.Context(), "POST", "/v1/admin/requests/"+ask.ID+"/approve", nil, nil); err != nil {
		t.Fatal(err)
	}
	muse := m.Client(t, "muse")
	if _, err := muse.Claim(t.Context(), ask.ID); err != nil {
		t.Fatal(err)
	}
	seed := "new chat\nsecret chat text"
	sent, err := muse.SendGrouped(t.Context(), "instinct", seed, envelope.KindAsk, ask.ID, "pass-"+ask.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := m.Store.AuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		if e.Event != "pass_sent" {
			continue
		}
		if strings.Contains(e.Detail, "secret") {
			t.Fatalf("pass_sent carries text: %s", e.Detail)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
			t.Fatal(err)
		}
		if e.RequestID != sent.ID || e.Actor != "muse" || d["to"] != "instinct" || d["bytes"] != float64(len(seed)) || d["source"] != "grok" || d["conversation_id"] != "abc" {
			t.Fatalf("pass_sent = %+v %v", e, d)
		}
		got = append(got, e.RequestID)
	}
	if len(got) != 1 {
		t.Fatalf("pass_sent events = %v, want one", got)
	}
}

// A pass seed and its reply hold the passed chat's text. The agent that
// asked the pass service shares the seed's chain but sees neither in a
// trace, search or get; the pass service, the destination and the admin
// still do.
func TestPassSeedHiddenFromAsker(t *testing.T) {
	m := testrelay.New(t, passConfig())
	if ok, err := m.Store.SetAgentKind(t.Context(), "muse", "pass"); !ok || err != nil {
		t.Fatalf("set kind: %v %v", ok, err)
	}
	ask, err := m.Client(t, "grokbot").Send(t.Context(), "muse", "from: grok abc\nto: instinct", envelope.KindAsk, "", false)
	if err != nil {
		t.Fatal(err)
	}
	admin := m.Client(t, "admin")
	if err := admin.Raw(t.Context(), "POST", "/v1/admin/requests/"+ask.ID+"/approve", nil, nil); err != nil {
		t.Fatal(err)
	}
	muse, inst := m.Client(t, "muse"), m.Client(t, "instinct")
	if _, err := muse.Claim(t.Context(), ask.ID); err != nil {
		t.Fatal(err)
	}
	seed, err := muse.SendGrouped(t.Context(), "instinct", "new chat\nsecretseed chat text", envelope.KindAsk, ask.ID, "pass-"+ask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Claim(t.Context(), seed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Reply(t.Context(), seed.ID, "secretreply from the new chat", envelope.StatusAnswered); err != nil {
		t.Fatal(err)
	}
	trace := func(c *client.Relay) string {
		var out json.RawMessage
		if err := c.Raw(t.Context(), "GET", "/v1/trace/"+ask.TraceID, nil, &out); err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if got := trace(m.Client(t, "grokbot")); strings.Contains(got, "secretseed") || strings.Contains(got, "secretreply") || !strings.Contains(got, envelope.PassRedacted) || !strings.Contains(got, seed.ID) {
		t.Fatalf("asker trace leaks or drops the seed: %s", got)
	}
	for name, c := range map[string]*client.Relay{"muse": muse, "instinct": inst, "admin": admin} {
		if got := trace(c); !strings.Contains(got, "secretseed") || !strings.Contains(got, "secretreply") {
			t.Fatalf("%s trace lost the seed: %s", name, got)
		}
	}
	for _, q := range []string{"secretseed", "secretreply"} {
		if hits, err := m.Client(t, "grokbot").Search(t.Context(), q, 20); err != nil || len(hits) != 0 {
			t.Fatalf("asker search %q = %+v %v, want none", q, hits, err)
		}
		for name, c := range map[string]*client.Relay{"muse": muse, "instinct": inst, "admin": admin} {
			if hits, err := c.Search(t.Context(), q, 20); err != nil || len(hits) != 1 || hits[0].RequestID != seed.ID {
				t.Fatalf("%s search %q = %+v %v, want the seed", name, q, hits, err)
			}
		}
	}
	if res, err := m.Client(t, "grokbot").Get(t.Context(), seed.ID, 0); err == nil {
		raw, _ := json.Marshal(res)
		if strings.Contains(string(raw), "secretseed") || strings.Contains(string(raw), "secretreply") {
			t.Fatalf("asker get leaks the seed: %s", raw)
		}
	}
	for name, c := range map[string]*client.Relay{"muse": muse, "instinct": inst} {
		res, err := c.Get(t.Context(), seed.ID, 0)
		if err != nil || !strings.Contains(res.Request.Body, "secretseed") || res.Reply == nil || !strings.Contains(res.Reply.Body, "secretreply") {
			t.Fatalf("%s get = %+v %v", name, res, err)
		}
	}
}
