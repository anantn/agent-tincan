package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
)

// socketTarget is a session pushSocket can wake: a pusher that also says
// whether the session is idle right now.
type socketTarget interface {
	channelPusher
	idle() bool
}

// socketFailLimit is how many writes in a row may fail before pushSocket
// gives up on the socket, so a session that cannot be woken stops looking
// online to teammates.
const socketFailLimit = 3

// socketRecordKeep is how long a delivered id stays in the shared record
// after a peek last listed it. A window's peek can miss an item another
// window's showed (the pending list is capped), so the record does not
// forget an id the moment one peek leaves it out.
var socketRecordKeep = 10 * time.Minute

// errSocketFailing ends pushSocket after socketFailLimit failed writes.
var errSocketFailing = errors.New("session socket keeps failing")

// pushSocket announces waiting items into one Claude Code session through
// its messaging socket. Unlike a channel notice, every socket message starts
// a turn, so it differs from pushWaiting in four ways:
//
//   - It never pushes the upgrade notice, which reaches the agent on its next
//     tool call anyway.
//   - It pushes only while the session is idle.
//   - Each item is pushed once. A reply to an ask this process sent goes to
//     this session alone. Everything else (teammate requests, and replies the
//     relay shows every session of the agent) goes through a lock and record
//     shared by the agent's windows on this machine, so one item starts one
//     turn on the machine.
//   - It returns errSocketFailing after socketFailLimit failed writes in a
//     row, and nil when ctx ends.
//
// Pings are answered as pushWaiting answers them.
func pushSocket(ctx context.Context, r *client.Relay, t socketTarget, shared socketShared) error {
	select {
	case <-ctx.Done():
		return nil
	case <-t.Ready():
	}
	// The peeks below tell the relay this session wakes by socket, so an
	// asker hears it will be prompted. Once the loop ends nothing pushes,
	// so the advert stops with it.
	r.AdvertiseSocketWake(true)
	defer r.AdvertiseSocketWake(false)
	own := map[string]bool{} // own reply keys already pushed and still waiting
	var retries client.PongRetries
	defer retries.Wait()
	backoff, fails := time.Second, 0
	for ctx.Err() == nil {
		w, err := r.Peek(ctx, client.DefaultPollHold)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("tincan socket wake: %v", client.RejoinHint(err, r.Base()))
				sleepCtx(ctx, jitter(backoff))
				backoff = min(backoff*2, 30*time.Second)
			}
			continue
		}
		w, pingFailed := answerChannelPings(ctx, r, w, &retries)
		if pingFailed && w.Total <= 0 {
			sleepCtx(ctx, jitter(backoff))
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		// settle counts failed writes in a row; a write that lands resets
		// the count.
		settle := func(tried bool, err error) bool {
			switch {
			case err != nil:
				fails++
				log.Printf("tincan socket wake: push: %v", err)
			case tried:
				fails = 0
			}
			return fails >= socketFailLimit
		}
		if t.idle() {
			mine, rest := splitOwn(r, w)
			if settle(pushOwn(ctx, t, mine, own)) || settle(pushShared(ctx, t, rest, shared)) {
				return errSocketFailing
			}
		}
		if w.Total > 0 {
			sleepCtx(ctx, waitingRecheck)
		}
	}
	return nil
}

// splitOwn splits a peek into the replies to asks this process sent and
// everything else.
func splitOwn(r *client.Relay, w client.Waiting) (mine []client.Result, rest client.Waiting) {
	rest = w
	rest.Replies = nil
	for _, rep := range w.Replies {
		if r.SentHere(rep.Request.ID) {
			mine = append(mine, rep)
		} else {
			rest.Replies = append(rest.Replies, rep)
		}
	}
	return mine, rest
}

// pushOwn pushes one notice for this session's own replies not pushed yet,
// and forgets keys no longer waiting. It reports whether it wrote.
func pushOwn(ctx context.Context, t socketTarget, mine []client.Result, own map[string]bool) (bool, error) {
	still := map[string]bool{}
	var fresh []client.Result
	for _, rep := range mine {
		k := "r:" + rep.Request.ID
		if own[k] {
			still[k] = true
		} else {
			fresh = append(fresh, rep)
		}
	}
	clear(own)
	for k := range still {
		own[k] = true
	}
	if len(fresh) == 0 {
		return false, nil
	}
	n := socketNotice(client.Waiting{Replies: fresh, Total: len(fresh)})
	if err := t.Push(ctx, n.content, n.meta); err != nil {
		return true, err
	}
	for _, k := range n.keys {
		own[k] = true
	}
	return true, nil
}

// pushShared pushes one notice for the items no window on this machine has
// delivered yet. It takes the shared lock only for this one decision, so a
// busy window never holds up an idle one. When another window holds the
// lock it writes nothing and the next recheck tries again.
func pushShared(ctx context.Context, t socketTarget, w client.Waiting, shared socketShared) (bool, error) {
	keys := waitingKeys(w)
	if len(keys) == 0 {
		return false, nil
	}
	release, ok, err := shared.tryLock()
	if err != nil {
		log.Printf("tincan socket wake: lock: %v", err)
		return false, nil
	}
	if !ok {
		return false, nil
	}
	defer release()
	now := time.Now()
	rec := shared.read()
	listed := map[string]bool{}
	for _, k := range keys {
		listed[k] = true
	}
	changed := false
	for k, at := range rec {
		// A count key ("q#N", from a relay that gives only a count) names
		// no request, so a new request can bring back a count already
		// announced. Drop it once the count changes, and do not keep it
		// past socketRecordKeep even while it is listed.
		count := strings.HasPrefix(k, "q#")
		if (count && !listed[k]) || ((count || !listed[k]) && now.Sub(at) >= socketRecordKeep) {
			delete(rec, k)
			changed = true
		}
	}
	save := func() {
		if changed {
			shared.write(rec)
		}
	}
	fresh := w
	fresh.Pending, fresh.Replies = nil, nil
	for _, p := range w.Pending {
		if _, done := rec["q:"+p.ID]; !done {
			fresh.Pending = append(fresh.Pending, p)
		}
	}
	for _, rep := range w.Replies {
		if _, done := rec["r:"+rep.Request.ID]; !done {
			fresh.Replies = append(fresh.Replies, rep)
		}
	}
	if len(w.Pending) == 0 && w.Queued > 0 {
		// A relay that predates pending gives only a count.
		if _, done := rec[fmt.Sprintf("q#%d", w.Queued)]; done {
			fresh.Queued = 0
		}
	} else {
		fresh.Queued = len(fresh.Pending)
	}
	fresh.Total = fresh.Queued + len(fresh.Replies)
	if fresh.Total == 0 {
		save()
		return false, nil
	}
	n := socketNotice(fresh)
	if err := t.Push(ctx, n.content, n.meta); err != nil {
		save()
		return true, err
	}
	for _, k := range n.keys {
		rec[k] = now
	}
	changed = true
	save()
	return true, nil
}

// waitingKeys are the item keys a peek lists, as announcer keys them.
func waitingKeys(w client.Waiting) []string {
	var keys []string
	for _, p := range w.Pending {
		keys = append(keys, "q:"+p.ID)
	}
	if len(w.Pending) == 0 && w.Queued > 0 {
		keys = append(keys, fmt.Sprintf("q#%d", w.Queued))
	}
	for _, rep := range w.Replies {
		keys = append(keys, "r:"+rep.Request.ID)
	}
	return keys
}

// socketNotice is the notice for every item in w, built the way a channel
// notice is.
func socketNotice(w client.Waiting) notice {
	var a announcer
	n, _ := a.next(w, time.Now())
	return n
}

// socketShared is the record the agent's windows on this machine share, so
// that one item is delivered once: dir/<agent>.json maps item keys to when
// they were delivered, guarded by dir/<agent>.lock.
type socketShared struct {
	dir, agent string
}

func (s socketShared) path() string { return filepath.Join(s.dir, agentFileName(s.agent)+".json") }

// read returns the record, empty when it is missing or unreadable.
func (s socketShared) read() map[string]time.Time {
	rec := map[string]time.Time{}
	b, err := os.ReadFile(s.path())
	if err == nil {
		_ = json.Unmarshal(b, &rec)
	}
	return rec
}

// write replaces the record. The caller holds the lock.
func (s socketShared) write(rec map[string]time.Time) {
	b, _ := json.Marshal(rec)
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("tincan socket wake: record: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		log.Printf("tincan socket wake: record: %v", err)
	}
}

// socketWakeTiming is how socketWake keeps a session live while the socket
// is failing, and how long it waits before checking the socket again.
type socketWakeTiming struct {
	keepalive, retry time.Duration
}

// socketWake runs pushSocket until ctx ends. When the socket keeps failing,
// it keeps the session live with keepSession, which does not make the agent
// look online, and after timing.retry asks reverify for a fresh pusher; it
// resumes pushing once one checks out.
func socketWake(ctx context.Context, r *client.Relay, t socketTarget, shared socketShared, timing socketWakeTiming, reverify func() (socketTarget, error)) {
	for ctx.Err() == nil {
		if t != nil {
			if err := pushSocket(ctx, r, t, shared); err == nil {
				return
			}
			log.Printf("tincan socket wake: %v; keeping the session live and trying again in %s", errSocketFailing, timing.retry)
		}
		wait, cancel := context.WithTimeout(ctx, timing.retry)
		keepSession(wait, r, timing.keepalive)
		cancel()
		if ctx.Err() != nil {
			return
		}
		next, err := reverify()
		if err != nil {
			log.Printf("tincan socket wake: %v", err)
			t = nil
			continue
		}
		t = next
	}
}

// idle reports whether the session that owns the socket is idle now. It
// re-reads the registry each time and checks that the registered process
// is still the one verified at start, so a pid reused by another program
// never reads as an idle session. statusUpdatedAt is when the status last
// changed, not a heartbeat, so its age is not checked: a window idle for
// hours is the one most in need of waking.
func (p *claudeSocketPusher) idle() bool {
	if !p.verified {
		return false
	}
	s, err := p.session()
	if err != nil {
		return false
	}
	return s.Status == "idle" && s.PID == p.ppid && s.ProcStart == p.procStart && processAlive(s.PID)
}

// socketWakeDir is where the shared delivery records live: beside the
// agent's config, so each agent on the machine has its own.
func socketWakeDir() string {
	return filepath.Join(filepath.Dir(client.ConfigPath()), "socket-wake")
}

// wakePath is how a tincan mcp server keeps its session reachable.
type wakePath int

const (
	wakeByKeepalive wakePath = iota // nothing pushes; whoami keeps the session live
	wakeByChannel                   // the Claude Code channel transport
	wakeBySocket                    // the session's own messaging socket
)

func (w wakePath) String() string {
	return [...]string{"keepalive", "channel", "socket"}[w]
}

// pickWake decides the wake path from the flags. --channel wins, and the
// session socket is read only when socket wake is on, so --wake-socket=false
// neither reads the registry nor logs about it. fromEnv returns nil when
// there is no session socket that checks out; the server then runs the
// keepalive so the window keeps its replies.
func pickWake(channel, socket bool, fromEnv func() *claudeSocketPusher) (wakePath, *claudeSocketPusher) {
	if channel {
		return wakeByChannel, nil
	}
	if !socket {
		return wakeByKeepalive, nil
	}
	if p := fromEnv(); p != nil {
		return wakeBySocket, p
	}
	return wakeByKeepalive, nil
}

// claudeSocketFromEnv returns the socket pusher for the Claude Code session
// that started this process, or nil when there is none. It says why only
// when the session's socket is set but does not check out, so agents that
// are not Claude Code log nothing.
func claudeSocketFromEnv() *claudeSocketPusher {
	if os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET") == "" {
		return nil
	}
	p, err := newClaudeSocketPusher(os.Getenv, os.Getppid())
	if err != nil {
		log.Printf("tincan mcp: socket wake off: %v", err)
		return nil
	}
	return p
}

// agentFileName keeps an agent name safe as a file name.
func agentFileName(name string) string {
	if name == "" {
		return "agent"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, name)
}
