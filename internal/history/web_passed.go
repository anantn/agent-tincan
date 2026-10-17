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

// DefaultWebPassedPath is where the web agents for site list the
// conversations a pass opened, which no asker may continue by id. It is
// kept per site, as the used list is, so every agent serving the site
// refuses a chat any of them recorded.
func DefaultWebPassedPath(site Source) string {
	return configPath("", "web-agent-"+string(site)+"-passed.json")
}

// LegacyWebPassedPath is the passed list an older build kept under the
// agent's name. A web agent merges it into its site's list on first load.
func LegacyWebPassedPath(agent string) string { return configPath("", agent+"-passed.json") }

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

// migratePassed merges the passed list at legacy, if there is one, into
// the list at path and removes it. A legacy list that cannot be read, or
// a site list that cannot be read or written, is an error, so nothing is
// continued until the owner fixes it. A legacy list left behind when the
// removal fails is merged again on the next load.
func migratePassed(path, legacy string) error {
	if legacy == "" || legacy == path {
		return nil
	}
	if _, err := os.Stat(legacy); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	old, err := loadPassed(legacy)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(legacy), err)
	}
	convs, err := loadPassed(path)
	if err != nil {
		return err
	}
	for id, at := range old {
		if prev, ok := convs[id]; !ok || at.Before(prev) {
			convs[id] = at
		}
	}
	if err := writeList(path, webPassed{Conversations: convs}); err != nil {
		return err
	}
	_ = os.Remove(legacy)
	return nil
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

// loadPassed reads the agent's passed list, first merging in the list an
// older build kept under its name.
func (w *WebAgent) loadPassed() (map[string]time.Time, error) {
	if err := migratePassed(w.PassedPath, w.LegacyPassedPath); err != nil {
		return nil, err
	}
	return loadPassed(w.PassedPath)
}

// refuseContinuation says why a conversation: request for id must not be
// sent, or "" when it may: id was opened by a pass, or the passed list
// cannot be read.
func (w *WebAgent) refuseContinuation(id, label string) (string, envelope.Status) {
	if w.PassedPath == "" {
		return "", ""
	}
	passed, err := w.loadPassed()
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
	err := migratePassed(w.PassedPath, w.LegacyPassedPath)
	if err == nil {
		err = recordPassed(w.PassedPath, convID, time.Now())
	}
	if err != nil {
		w.logf("request %s from %s: passed list %s: %v", req.ID, req.From, w.PassedPath, err)
		w.reply(ctx, req, fmt.Sprintf("The chat was opened in %s, but the %s agent could not record it as passed, so its id is not returned. Find it in %s's chat list.", label, w.Name, label), envelope.StatusFailed, nil)
		return false
	}
	return true
}
