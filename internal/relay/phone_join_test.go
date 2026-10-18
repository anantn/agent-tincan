package relay_test

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/identity"
	"github.com/mvanhorn/agent-tincan/internal/onboard"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/testrelay"
)

// pageCall is one call from the phone page on the admin device, as the
// browser makes it: same-origin headers, JSON for a body, and the phone's
// agent name once it has one.
func pageCall(t *testing.T, base, method, path, agent string, in, out any) (int, string) {
	t.Helper()
	hdr := map[string]string{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors"}
	body := ""
	if in != nil {
		raw, _ := json.Marshal(in)
		body = string(raw)
		maps.Copy(hdr, browserJSON)
	}
	if agent != "" {
		hdr[client.AgentHeader] = agent
	}
	code, resp, _ := browserCall(t, method, base, path, "127.0.0.1", body, hdr)
	if out != nil && code < 300 {
		if err := json.Unmarshal([]byte(resp), out); err != nil {
			t.Fatalf("%s %s: %v in %s", method, path, err, resp)
		}
	}
	return code, resp
}

// An unjoined admin phone cannot ask; it joins itself from the page
// (invite, then join, both from the page), shows on the roster as kind
// phone, asks a teammate and reads the reply.
func TestPhoneJoinsAndAsks(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	page := hardened(t, m, "admin")

	if code, body := pageCall(t, page, "GET", "/v1/whoami", "", nil, nil); code != http.StatusForbidden || !strings.Contains(body, identity.ErrNotJoined.Error()) {
		t.Fatalf("whoami before joining: %d %s, want not joined", code, body)
	}
	if code, body := pageCall(t, page, "POST", "/v1/send", "", map[string]string{"to": "muse", "body": "hi", "kind": "ask"}, nil); code != http.StatusForbidden || !strings.Contains(body, identity.ErrNotJoined.Error()) {
		t.Fatalf("ask before joining: %d %s, want not joined", code, body)
	}

	var inv struct{ Code string }
	if code, body := pageCall(t, page, "POST", "/v1/admin/invite", "", map[string]string{"name": "phone", "kind": onboard.KindPhone}, &inv); code != http.StatusOK || inv.Code == "" {
		t.Fatalf("invite from the page: %d %s", code, body)
	}
	var joined struct{ Name string }
	if code, body := pageCall(t, page, "POST", "/v1/join", "", map[string]string{"code": inv.Code}, &joined); code != http.StatusOK || joined.Name != "phone" {
		t.Fatalf("join from the page: %d %s", code, body)
	}
	var roster client.Roster
	if code, body := pageCall(t, page, "GET", "/v1/agents", "phone", nil, &roster); code != http.StatusOK {
		t.Fatalf("roster: %d %s", code, body)
	}
	var found bool
	for _, a := range roster.Agents {
		if a.Name == "phone" {
			found = true
			if a.Kind != onboard.KindPhone || a.GoodAt != "the owner's phone; asks only, never answers" {
				t.Fatalf("phone on the roster: %+v", a)
			}
		}
	}
	if !found {
		t.Fatalf("phone not on the roster: %+v", roster.Agents)
	}

	var sent envelope.SendResponse
	if code, body := pageCall(t, page, "POST", "/v1/send", "phone", map[string]string{"to": "muse", "body": "what is on my calendar?", "kind": "ask"}, &sent); code != http.StatusCreated || sent.ID == "" {
		t.Fatalf("ask from the phone: %d %s", code, body)
	}
	muse := m.Client(t, "muse")
	in, err := muse.Poll(t.Context(), time.Millisecond)
	if err != nil || len(in.Requests) != 1 || in.Requests[0].From != "phone" {
		t.Fatalf("muse inbox: %+v %v", in.Requests, err)
	}
	if _, err := muse.Claim(t.Context(), sent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := muse.Reply(t.Context(), sent.ID, "<i>dentist at 3</i>", envelope.StatusAnswered); err != nil {
		t.Fatal(err)
	}
	var res envelope.Result
	if code, body := pageCall(t, page, "GET", "/v1/requests/"+sent.ID, "phone", nil, &res); code != http.StatusOK || res.Status != envelope.StatusAnswered || res.Reply == nil || res.Reply.Body != "<i>dentist at 3</i>" {
		t.Fatalf("reading the reply: %d %s", code, body)
	}

	// The page rebuilds its sent list from the relay's recent chains.
	var recent struct {
		Traces []envelope.Result `json:"traces"`
	}
	if code, body := pageCall(t, page, "GET", "/v1/trace?limit=50&exclude_pings=true", "phone", nil, &recent); code != http.StatusOK {
		t.Fatalf("recent chains: %d %s", code, body)
	}
	var listed bool
	for _, r := range recent.Traces {
		listed = listed || (r.Request.ID == sent.ID && r.Request.From == "phone" && r.Status == envelope.StatusAnswered)
	}
	if !listed {
		t.Fatalf("the phone's ask is not in the recent chains: %+v", recent.Traces)
	}
}

// The phone answers a teammate's clarifying question from the page.
func TestPhoneAnswersNeedsInput(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	page := hardened(t, m, "admin")
	m.JoinOnMachineOf(t, "admin", "phone")
	var sent envelope.SendResponse
	if code, body := pageCall(t, page, "POST", "/v1/send", "phone", map[string]string{"to": "muse", "body": "book a table", "kind": "ask"}, &sent); code != http.StatusCreated {
		t.Fatalf("ask: %d %s", code, body)
	}
	muse := m.Client(t, "muse")
	if _, err := muse.Claim(t.Context(), sent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := muse.Reply(t.Context(), sent.ID, "for how many?", envelope.StatusNeedsInput); err != nil {
		t.Fatal(err)
	}
	var res envelope.Result
	if code, body := pageCall(t, page, "GET", "/v1/requests/"+sent.ID, "phone", nil, &res); code != http.StatusOK || res.Status != envelope.StatusNeedsInput || len(res.Exchanges) != 1 || res.Exchanges[0].Question != "for how many?" {
		t.Fatalf("needs input: %d %s", code, body)
	}
	if code, body := pageCall(t, page, "POST", "/v1/requests/"+sent.ID+"/answer", "phone", map[string]string{"body": "two"}, nil); code != http.StatusOK {
		t.Fatalf("answer from the page: %d %s", code, body)
	}
}

// The page asks for its own agent's chains with from, which filters before
// the limit, so newer team traffic cannot push an open phone ask out.
func TestRecentTracesFromFiltersBeforeLimit(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	mine, err := m.Client(t, "grokbot").Send(t.Context(), "muse", "mine", envelope.KindAsk, "", false)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := m.Client(t, "instinct").Send(t.Context(), "muse", "theirs", envelope.KindAsk, "", false); err != nil {
			t.Fatal(err)
		}
	}
	var out struct {
		Traces []envelope.Result `json:"traces"`
	}
	if err := m.Client(t, "admin").Raw(t.Context(), "GET", "/v1/trace?limit=1&exclude_pings=true&from=grokbot", nil, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Traces) != 1 || out.Traces[0].Request.ID != mine.ID {
		t.Fatalf("traces from grokbot: %+v", out.Traces)
	}
}

// The page mints its invite with new_only, so a name another device's
// agent holds is refused instead of moved to the phone, whatever the page's
// cached roster said. A free name and the page's own agent on this device
// still get a code, and an invite without the flag (the CLI's re-invite)
// still moves an existing agent.
func TestPageInviteNewOnly(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	page := hardened(t, m, "admin")
	invite := func(name string, newOnly bool) (int, string) {
		t.Helper()
		in := map[string]any{"name": name, "kind": onboard.KindPhone}
		if newOnly {
			in["new_only"] = true
		}
		return pageCall(t, page, "POST", "/v1/admin/invite", "", in, nil)
	}

	if code, body := invite("muse", true); code != http.StatusConflict || !strings.Contains(body, "muse") {
		t.Fatalf("new_only invite for another device's agent: %d %s, want 409", code, body)
	}
	if a, _, err := m.Dir.Agent(t.Context(), "muse"); err != nil || a.NodeID != "nMUSE" {
		t.Fatalf("muse after the refused invite: %+v %v", a, err)
	}
	if code, body := invite("phone", true); code != http.StatusOK {
		t.Fatalf("new_only invite for a free name: %d %s", code, body)
	}
	m.JoinOnMachineOf(t, "admin", "phone")
	if code, body := invite("phone", true); code != http.StatusOK {
		t.Fatalf("new_only invite for this device's own agent: %d %s", code, body)
	}
	// The local admin socket has no machine of its own to match.
	if _, err := m.Dir.InviteNew(t.Context(), identity.LocalAdmin, "phone", onboard.KindPhone); !errors.Is(err, identity.ErrNameTaken) {
		t.Fatalf("new_only invite from the admin socket for an existing agent: %v, want ErrNameTaken", err)
	}
	if code, body := invite("muse", false); code != http.StatusOK {
		t.Fatalf("invite without new_only for an existing agent: %d %s", code, body)
	}
	if _, err := m.Client(t, "admin").Invite(t.Context(), "grokbot"); err != nil {
		t.Fatalf("CLI re-invite: %v", err)
	}
}

// new_only is checked again when the code is redeemed: a name that another
// device's agent took between mint and redeem is refused with the same 409
// instead of moved to the phone, and the other agent keeps it. A code
// minted without new_only (the CLI's re-invite) still moves the agent.
func TestPageInviteNewOnlyRecheckedAtRedeem(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	page := hardened(t, m, "admin")
	var inv struct{ Code string }
	if code, body := pageCall(t, page, "POST", "/v1/admin/invite", "", map[string]any{"name": "phone", "kind": onboard.KindPhone, "new_only": true}, &inv); code != http.StatusOK || inv.Code == "" {
		t.Fatalf("new_only invite for a free name: %d %s", code, body)
	}
	// Another device's agent comes to hold the name before the page redeems
	// (a rebind, or a join that raced the page's mint).
	if err := m.Store.PutAgent(t.Context(), identity.Agent{Name: "phone", NodeID: "nMUSE", NodeName: "muse", JoinedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if code, body := pageCall(t, page, "POST", "/v1/join", "", map[string]string{"code": inv.Code}, nil); code != http.StatusConflict || !strings.Contains(body, identity.ErrNameTaken.Error()) {
		t.Fatalf("redeeming a new_only code for a name another device took: %d %s, want 409", code, body)
	}
	if a, _, err := m.Dir.Agent(t.Context(), "phone"); err != nil || a.NodeID != "nMUSE" {
		t.Fatalf("phone after the refused join: %+v %v", a, err)
	}

	// A new_only code for this device's own agent still redeems.
	m.JoinOnMachineOf(t, "admin", "mine")
	if code, body := pageCall(t, page, "POST", "/v1/admin/invite", "", map[string]any{"name": "mine", "kind": onboard.KindPhone, "new_only": true}, &inv); code != http.StatusOK {
		t.Fatalf("new_only invite for this device's own agent: %d %s", code, body)
	}
	if code, body := pageCall(t, page, "POST", "/v1/join", "", map[string]string{"code": inv.Code}, nil); code != http.StatusOK {
		t.Fatalf("redeeming a new_only code for this device's own agent: %d %s", code, body)
	}

	// A plain re-invite moves an existing agent on join, as before.
	code, err := m.Client(t, "admin").Invite(t.Context(), "phone")
	if err != nil {
		t.Fatal(err)
	}
	if code, body := pageCall(t, page, "POST", "/v1/join", "", map[string]string{"code": code}, nil); code != http.StatusOK {
		t.Fatalf("redeeming a plain re-invite: %d %s", code, body)
	}
	if a, _, err := m.Dir.Agent(t.Context(), "phone"); err != nil || a.NodeID != "nMAC" {
		t.Fatalf("phone after the plain re-invite: %+v %v", a, err)
	}
}

// A phone agent asks and never answers, so the relay refuses a teammate's
// send to it (ask or notify) at send time. Replies to the phone's own asks
// still reach it.
func TestSendToPhoneRefused(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	page := hardened(t, m, "admin")
	var inv struct{ Code string }
	if code, body := pageCall(t, page, "POST", "/v1/admin/invite", "", map[string]any{"name": "phone", "kind": onboard.KindPhone, "new_only": true}, &inv); code != http.StatusOK {
		t.Fatalf("invite: %d %s", code, body)
	}
	if code, body := pageCall(t, page, "POST", "/v1/join", "", map[string]string{"code": inv.Code}, nil); code != http.StatusOK {
		t.Fatalf("join: %d %s", code, body)
	}
	muse := m.Client(t, "muse")
	for _, kind := range []envelope.Kind{envelope.KindAsk, envelope.KindNotify, envelope.KindPing} {
		body := "are you there?"
		if kind == envelope.KindPing {
			body = ""
		}
		_, err := muse.Send(t.Context(), "phone", body, kind, "", false)
		var se *client.APIError
		if !errors.As(err, &se) || se.Code != http.StatusBadRequest || !strings.Contains(err.Error(), "phone agents ask; they do not take requests") {
			t.Fatalf("%s to the phone: %v, want 400 refusal", kind, err)
		}
	}
	if n := len(auditEvents(t, m, "queued")); n != 0 {
		t.Fatalf("queued audit rows after refused sends: %d", n)
	}

	var sent envelope.SendResponse
	if code, body := pageCall(t, page, "POST", "/v1/send", "phone", map[string]string{"to": "muse", "body": "what time is it?", "kind": "ask"}, &sent); code != http.StatusCreated {
		t.Fatalf("ask from the phone: %d %s", code, body)
	}
	if _, err := muse.Claim(t.Context(), sent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := muse.Reply(t.Context(), sent.ID, "noon", envelope.StatusAnswered); err != nil {
		t.Fatalf("reply to the phone's ask: %v", err)
	}
	var res envelope.Result
	if code, body := pageCall(t, page, "GET", "/v1/requests/"+sent.ID, "phone", nil, &res); code != http.StatusOK || res.Reply == nil || res.Reply.Body != "noon" {
		t.Fatalf("reading the reply: %d %s", code, body)
	}
}
