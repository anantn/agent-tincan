package pass

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/mvanhorn/agent-tincan/internal/history"
)

// Request is what an agent's ask to the pass teammate names: one chat,
// by source and id, and the web chat teammate to open it on.
type Request struct {
	Source history.Source `json:"source"`
	ID     string         `json:"conversation_id"`
	To     string         `json:"to"`
}

// RequestForm is the form a pass request takes, for replies and errors.
const RequestForm = `"from: <chat URL>" (or "from: <source> <conversation id>") and "to: <web agent>", each on its own line`

var agentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ParseRequest reads a pass request: a "from:" line naming the chat (a
// chat URL, or a source and a conversation id) and a "to:" line naming
// the destination, in either order, with nothing else but blank lines.
// It never takes "latest": an agent names the chat it means.
func ParseRequest(body string) (Request, error) {
	r, err := parseRequest(body)
	if err != nil {
		return Request{}, fmt.Errorf("%w. A pass request is %s", err, RequestForm)
	}
	return r, nil
}

func parseRequest(body string) (Request, error) {
	var from, to string
	var seenFrom, seenTo bool
	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		key = strings.ToLower(strings.TrimSpace(key))
		switch {
		case ok && key == "from":
			if seenFrom {
				return Request{}, errors.New("from: appears more than once")
			}
			seenFrom, from = true, strings.TrimSpace(val)
		case ok && key == "to":
			if seenTo {
				return Request{}, errors.New("to: appears more than once")
			}
			seenTo, to = true, strings.TrimSpace(val)
		default:
			return Request{}, fmt.Errorf("unexpected line %q", capRunes(line, 60))
		}
	}
	switch {
	case !seenFrom:
		return Request{}, errors.New("there is no from: line naming the chat")
	case !seenTo:
		return Request{}, errors.New("there is no to: line naming the web chat teammate")
	case !agentName.MatchString(to):
		return Request{}, fmt.Errorf("%q is not an agent name", capRunes(to, 60))
	}
	src, id, err := chatRef(from)
	if err != nil {
		return Request{}, err
	}
	return Request{Source: src, ID: id, To: to}, nil
}

// chatRef reads a from: value: a chat URL, or a source and an id.
func chatRef(from string) (history.Source, string, error) {
	if strings.Contains(from, "://") {
		return history.ResolveChatURL(from)
	}
	fields := strings.Fields(from)
	if len(fields) == 0 {
		return "", "", errors.New("from: names no chat")
	}
	src := history.Source(strings.ToLower(fields[0]))
	if !history.IsLiveSource(src) {
		return "", "", fmt.Errorf("%q is not a chat site a pass reads (%s)", capRunes(fields[0], 40), history.LiveSourcesLabel())
	}
	if len(fields) != 2 {
		return "", "", fmt.Errorf("from: %s needs a conversation id after the source", src)
	}
	id := fields[1]
	if strings.EqualFold(id, "latest") {
		return "", "", errors.New(`a pass names one conversation, never "latest"`)
	}
	q := history.Query{Source: src, Mode: history.ModeConversation, ConversationID: id}
	if err := q.Validate(); err != nil {
		return "", "", err
	}
	return src, id, nil
}

// capRunes cuts s to n runes, marking a cut with "…".
func capRunes(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j] + "…"
		}
		i++
	}
	return s
}
