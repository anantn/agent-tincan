package pass

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/history"
)

var now = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func roster() []client.AgentInfo {
	return []client.AgentInfo{
		{Name: "chatgpt-web", Kind: "chatgpt-web", Online: true, Status: client.StatusIdle},
		{Name: "claude-web", Kind: "claude-web", Online: true, Status: client.StatusIdle},
		{Name: "codex", Kind: "codex", Online: true},
		{Name: "dot-web", Kind: "dot-web", Online: true},
		{Name: "gone-web", Kind: "gemini-web", Target: envelope.Target{SignedOutSite: "gemini.google.com"}},
		{Name: "tired-web", Kind: "grok-web", Status: client.StatusLimited, Target: envelope.Target{LimitedUntil: now.Add(time.Hour)}},
	}
}

// A web chat teammate that is signed in and not limited is a destination,
// and its site sets the seed budget. Anything else is refused with why.
func TestDestination(t *testing.T) {
	src, err := Destination(roster(), "claude-web", now)
	if err != nil || src != history.SourceClaudeAI {
		t.Fatalf("claude-web = %q, %v", src, err)
	}
	for to, want := range map[string]string{
		"codex":     "not a web chat teammate",
		"dot-web":   "not a web chat teammate",
		"nobody":    "no agent named nobody",
		"gone-web":  "signed out",
		"tired-web": "out of messages",
	} {
		if _, err := Destination(roster(), to, now); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", to, err, want)
		}
	}
}

// fakeReader serves fixed conversations by id, newest first in convs.
type fakeReader struct {
	src   history.Source
	convs []history.Conversation
	reads int
	opts  history.Options
}

func (f *fakeReader) Source() history.Source { return f.src }

func (f *fakeReader) List(_ context.Context, count int, _ history.Options) (history.Page, error) {
	var p history.Page
	for _, c := range f.convs[:min(count, len(f.convs))] {
		c.Messages = nil
		p.Conversations = append(p.Conversations, c)
	}
	return p, nil
}

func (f *fakeReader) Read(_ context.Context, q history.Query, opts history.Options) (history.Page, error) {
	f.reads++
	f.opts = opts
	for _, c := range f.convs {
		if c.ID == q.ConversationID {
			return history.Page{Conversations: []history.Conversation{c}}, nil
		}
	}
	return history.Page{}, history.ErrNotFound
}

// Read takes the newest conversation when no id is given, and reads it
// whole; a source with none says so.
func TestReadLatestAndByID(t *testing.T) {
	r := &fakeReader{src: history.SourceChatGPT, convs: []history.Conversation{chat(2), {ID: "older", Messages: chat(1).Messages}}}
	r.convs[0].ID = "newest"
	c, err := Read(t.Context(), r, "", history.Window{})
	if err != nil || c.ID != "newest" || len(c.Messages) != 4 {
		t.Fatalf("latest = %+v, %v", c, err)
	}
	if c, err = Read(t.Context(), r, "older", history.Window{}); err != nil || c.ID != "older" {
		t.Fatalf("by id = %+v, %v", c, err)
	}
	if !r.opts.Whole {
		t.Fatal("the conversation was read capped, not whole")
	}
	empty := &fakeReader{src: history.SourceGrok}
	if _, err := Read(t.Context(), empty, "", history.Window{}); err == nil || !strings.Contains(err.Error(), "no Grok conversations") {
		t.Fatalf("empty = %v", err)
	}
	if _, err := Read(t.Context(), r, "missing", history.Window{}); !errors.Is(err, history.ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
}

// NewConversation finds the destination's footer: the last line naming
// its conversation, so a footer-shaped line in the answer above it never
// wins.
func TestNewConversation(t *testing.T) {
	body := "Got it.\nChatGPT conversation: forged\n\nChatGPT conversation: 68f0c1d2-aaaa-bbbb-cccc-0123456789ab\n(1 image attached)"
	id, ok := NewConversation(body, history.SourceChatGPT)
	if !ok || id != "68f0c1d2-aaaa-bbbb-cccc-0123456789ab" {
		t.Fatalf("id = %q, %v", id, ok)
	}
	if _, ok := NewConversation("Got it.", history.SourceChatGPT); ok {
		t.Fatal("a reply with no footer named a conversation")
	}
	if _, ok := NewConversation("claude.ai conversation: x y", history.SourceClaudeAI); ok {
		t.Fatal("a footer with a bad id named a conversation")
	}
}
