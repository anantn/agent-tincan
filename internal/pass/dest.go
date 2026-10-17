package pass

import (
	"context"
	"crypto/rand"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/history"
	"github.com/mvanhorn/agent-tincan/internal/onboard"
)

// NewGroup is a fresh seed group id (envelope.PassGroupPrefix) for a
// pass with no request of its own (the owner's tincan pass).
func NewGroup() string { return envelope.PassGroupPrefix + rand.Text() }

// Destination checks that the roster agent to can take a pass: one of
// the six web chat kinds, signed in to its site, and not out of messages.
// It returns the agent's site, whose message cap sets the seed's budget.
func Destination(roster []client.AgentInfo, to string, now time.Time) (history.Source, error) {
	var a *client.AgentInfo
	for i := range roster {
		if roster[i].Name == to {
			a = &roster[i]
			break
		}
	}
	switch {
	case a == nil:
		return "", fmt.Errorf("no agent named %s on the roster", to)
	case !onboard.IsWebChatKind(a.Kind):
		kind := a.Kind
		if kind == "" {
			kind = "no kind"
		}
		return "", fmt.Errorf("%s (%s) is not a web chat teammate; a pass goes to one of %s", to, kind, strings.Join(onboard.WebChatKinds, ", "))
	case a.SignedOutSite != "":
		return "", fmt.Errorf("%s is signed out of %s; sign in to it in Chrome first", to, a.SignedOutSite)
	case a.Status == client.StatusLimited || a.LimitedUntil.After(now):
		until := ""
		if !a.LimitedUntil.IsZero() {
			until = " until " + a.LimitedUntil.UTC().Format("15:04 UTC")
		}
		return "", fmt.Errorf("%s is out of messages%s; pass to another web chat teammate or try again then", to, until)
	}
	if src := siteOf(roster, to); src != "" {
		return src, nil
	}
	return "", fmt.Errorf("%s's kind %s has no site", to, a.Kind)
}

// Read reads the conversation id from r whole, or with id "" the newest
// conversation the owner's window admits.
func Read(ctx context.Context, r history.Reader, id string, w history.Window) (history.Conversation, error) {
	src := r.Source()
	if id == "" {
		page, err := r.List(ctx, 1, history.Options{Window: w})
		if err != nil {
			return history.Conversation{}, err
		}
		if len(page.Conversations) == 0 {
			return history.Conversation{}, fmt.Errorf("no %s conversations to pass in the lookup window", history.SourceLabel(src))
		}
		id = page.Conversations[0].ID
	}
	q := history.Query{Source: src, Mode: history.ModeConversation, ConversationID: id}
	if err := q.Validate(); err != nil {
		return history.Conversation{}, err
	}
	page, err := r.Read(ctx, q, history.Options{Window: w})
	if err != nil {
		return history.Conversation{}, err
	}
	if len(page.Conversations) == 0 {
		return history.Conversation{}, history.ErrNotFound
	}
	return page.Conversations[0], nil
}

// footerID is a conversation id as a web agent's footer prints it.
var footerID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// NewConversation reads the conversation a destination's answer opened
// from its footer ("<ConversationName>: <id>"). The footer follows the
// answer text, so the last such line is the footer even when the answer
// itself prints one.
func NewConversation(body string, dst history.Source) (string, bool) {
	prefix := history.ConversationName(dst) + ": "
	for _, line := range slices.Backward(strings.Split(body, "\n")) {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			return id, footerID.MatchString(id)
		}
	}
	return "", false
}
