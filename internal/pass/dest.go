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
	a := rosterAgent(roster, to)
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
	if src := agentSite(a); src != "" {
		return src, nil
	}
	return "", fmt.Errorf("%s's kind %s has no site", to, a.Kind)
}

// rosterAgent is the roster agent named name, nil when there is none.
func rosterAgent(roster []client.AgentInfo, name string) *client.AgentInfo {
	if i := slices.IndexFunc(roster, func(a client.AgentInfo) bool { return a.Name == name }); i >= 0 {
		return &roster[i]
	}
	return nil
}

// agentSite is the site of the web chat agent a, "" when a is nil or not
// one.
func agentSite(a *client.AgentInfo) history.Source {
	if a == nil {
		return ""
	}
	for _, src := range history.WebSites {
		if history.WebAgentName(src) == a.Kind {
			return src
		}
	}
	return ""
}

// Read reads the conversation id from r whole, past the history
// reader's message cap, or with id "" the newest conversation the owner's
// window admits. Pack, not the reader, decides what the seed leaves out.
func Read(ctx context.Context, r history.Reader, id string, w history.Window) (history.Conversation, error) {
	src := r.Source()
	newest := id == ""
	listTitle := ""
	if newest {
		page, err := r.List(ctx, 1, history.Options{Window: w})
		if err != nil {
			return history.Conversation{}, err
		}
		if len(page.Conversations) == 0 {
			return history.Conversation{}, fmt.Errorf("no %s conversations to pass in the lookup window", history.SourceLabel(src))
		}
		id, listTitle = page.Conversations[0].ID, page.Conversations[0].Title
	}
	q := history.Query{Source: src, Mode: history.ModeConversation, ConversationID: id}
	if err := q.Validate(); err != nil {
		return history.Conversation{}, err
	}
	page, err := r.Read(ctx, q, history.Options{Window: w, Whole: true})
	if err != nil {
		return history.Conversation{}, err
	}
	if len(page.Conversations) == 0 {
		return history.Conversation{}, history.ErrNotFound
	}
	c := page.Conversations[0]
	// Some sites' conversation reads carry no title (Gemini's); only the
	// list shows it.
	if strings.TrimSpace(c.Title) == "" {
		if newest {
			c.Title = listTitle
		} else {
			c.Title = listedTitle(ctx, r, c.ID, w)
		}
	}
	// Some lists (Copilot's) carry no dates, so the newest entry is checked
	// against the window once the read has its times. A chat named by id
	// is the owner's explicit choice and is not.
	if newest {
		maxAge := w.MaxAge
		if maxAge <= 0 {
			maxAge = history.DefaultWindow().MaxAge
		}
		if u := lastUpdate(c); !u.IsZero() && u.Before(time.Now().Add(-maxAge)) {
			return history.Conversation{}, fmt.Errorf("no %s conversations to pass in the lookup window: the newest was last updated %s", history.SourceLabel(src), u.UTC().Format("2006-01-02"))
		}
	}
	return c, nil
}

// titleLookup is how many of the newest conversations listedTitle looks
// through: one list request, past which a chat goes untitled.
const titleLookup = 50

// titleAge is the age limit listedTitle lists with. A chat named by id is
// read whatever its age, so its title lookup is bounded by titleLookup
// alone, not by the window's MaxAge.
const titleAge = 100 * 365 * 24 * time.Hour

// listedTitle is the title the list shows for conversation id, "" when it
// is not among the titleLookup newest or the list fails: a title is worth
// one list request, never a failed pass.
func listedTitle(ctx context.Context, r history.Reader, id string, w history.Window) string {
	w.MaxAge = titleAge
	page, err := r.List(ctx, titleLookup, history.Options{Window: w, All: true})
	if err != nil {
		return ""
	}
	for _, c := range page.Conversations {
		if c.ID == id {
			return c.Title
		}
	}
	return ""
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
