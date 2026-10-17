package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

// DefaultWebPassedPath is where a web agent lists the conversations a
// pass opened, which no asker may continue by id.
func DefaultWebPassedPath(agent string) string { return configPath("", agent+"-passed.json") }

// webPassed is the passed list: conversation id -> when a pass opened it.
// It holds ids and times only, and is never pruned: an entry dropped
// would let any asker continue that chat again.
type webPassed struct {
	Conversations map[string]time.Time `json:"conversations"`
}

// isPassSeed reports whether req, read as wr, is a pass's seed: a new
// chat grouped pass-<id>. Any asker may group an ask that way; it only
// makes the chat it opens one nobody can continue.
func isPassSeed(req envelope.Request, wr webRequest) bool {
	return wr.mode == threadNew && strings.HasPrefix(req.Group, envelope.PassGroupPrefix)
}

// loadPassed reads the passed list at path. A missing file is an empty
// list; any other failure is an error, so a list that cannot be read
// refuses continuations rather than allowing them.
func loadPassed(path string) (map[string]time.Time, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]time.Time{}, nil
	}
	if err != nil {
		return nil, err
	}
	var p webPassed
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return nil, fmt.Errorf("invalid: %w", err)
	}
	if p.Conversations == nil {
		p.Conversations = map[string]time.Time{}
	}
	return p.Conversations, nil
}

// recordPassed adds id to the passed list at path (0600 in a 0700
// directory).
func recordPassed(path, id string, now time.Time) error {
	if !validNativeID(id) {
		return fmt.Errorf("invalid conversation id %q", id)
	}
	convs, err := loadPassed(path)
	if err != nil {
		return err
	}
	convs[id] = now.UTC()
	return writeList(path, webPassed{Conversations: convs})
}

// refuseContinuation says why a conversation: request for id must not be
// sent, or "" when it may: id was opened by a pass, or the passed list
// cannot be read.
func (w *WebAgent) refuseContinuation(id, label string) (string, envelope.Status) {
	if w.PassedPath == "" {
		return "", ""
	}
	passed, err := loadPassed(w.PassedPath)
	if err != nil {
		w.logf("passed list %s: %v", w.PassedPath, err)
		return fmt.Sprintf("Nothing was sent to %s: the %s agent could not read its list of passed chats (%s), so it continues no conversation by id until the owner fixes it.", label, w.Name, filepath.Base(w.PassedPath)), envelope.StatusFailed
	}
	if _, ok := passed[id]; ok {
		return fmt.Sprintf("Nothing was sent to %s: the conversation %s was opened by a pass of the owner's chat, so agents cannot continue it. The owner continues it in the browser.", label, id), envelope.StatusDeclined
	}
	return "", ""
}

// markPassed records a pass seed's conversation in the passed list
// before its id goes to anyone. It reports false, having replied, when
// it cannot: the id is then withheld.
func (w *WebAgent) markPassed(ctx context.Context, req envelope.Request, convID, label string) bool {
	if err := recordPassed(w.PassedPath, convID, time.Now()); err != nil {
		w.logf("request %s from %s: passed list %s: %v", req.ID, req.From, w.PassedPath, err)
		w.reply(ctx, req, fmt.Sprintf("The chat was opened in %s, but the %s agent could not record it as passed, so its id is not returned. Find it in %s's chat list.", label, w.Name, label), envelope.StatusFailed, nil)
		return false
	}
	return true
}
