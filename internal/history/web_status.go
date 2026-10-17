package history

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

const webProbeInterval = 2 * time.Minute

type webAuthState struct {
	mu                sync.Mutex
	latest            envelope.WebStatus
	reported          time.Time
	fresh             time.Time
	probeUnsupported  bool
	reportUnsupported bool
	// limitsCheckedAt is when the relay's web_limits capability was last
	// read, and limitsOK what it said.
	limitsCheckedAt time.Time
	limitsOK        bool
}

// limitsRecheck is how long a read of the relay's web_limits capability
// is trusted before it is read again (the relay may be upgraded).
const limitsRecheck = 10 * time.Minute

// observeAuth records what a site request said about the session: signed
// out, or signed in. Any other failure says nothing about it. Signed in
// while the site's usage limit runs is reported as limited.
func (w *WebAgent) observeAuth(err error) {
	if err != nil && !errors.Is(err, ErrNotLoggedIn) {
		return
	}
	state := envelope.WebAuthenticated
	if err != nil {
		state = envelope.WebSignedOut
	}
	var until time.Time
	if err == nil && w.Native != nil {
		if until = w.Native.cooldown().LimitedUntil(w.Site); !until.IsZero() {
			state = envelope.WebLimited
		}
	}
	w.auth.mu.Lock()
	defer w.auth.mu.Unlock()
	now := w.clk().Now()
	if !now.After(w.auth.latest.ObservedAt) {
		now = w.auth.latest.ObservedAt.Add(time.Millisecond)
	}
	if state != w.auth.latest.State || !until.Equal(w.auth.latest.Until) {
		switch {
		case state == envelope.WebSignedOut:
			w.logf("signed out of %s; sign in there in Chrome without restarting Chrome", w.site().host)
		case state == envelope.WebLimited:
			w.logf("%s is out of messages on this account until %s; not sending to it until then", w.site().host, until.Local().Format(time.RFC3339))
		case w.auth.latest.State == envelope.WebLimited:
			w.logf("usage limit on %s has passed", w.site().host)
		case w.auth.latest.State != "":
			w.logf("authenticated session restored on %s", w.site().host)
		}
	}
	w.auth.latest = envelope.WebStatus{Site: w.site().host, State: state, ObservedAt: now, Until: until}
	if err == nil {
		w.auth.fresh = now
	}
}

// noteLimit records that the site is out of messages until until: nothing
// is sent to it before then, and the agent reports itself limited.
func (w *WebAgent) noteLimit(until time.Time) {
	w.Native.cooldown().NoteLimit(w.Site, until)
	w.observeAuth(nil)
}

// sendLimit reads a usage limit from a failed send: the site's limit text
// in the page, or a 429 that said when to come back (a bare 429 is a short
// rate limit and keeps its cooldown). It notes the limit and returns its
// reset time.
func (w *WebAgent) sendLimit(err error) (time.Time, bool) {
	ue, ok := errors.AsType[*UnavailableError](err)
	if !ok {
		return time.Time{}, false
	}
	now := w.Native.cooldown().now()
	until, limited := detectLimit(w.Site, ue.Notice, now)
	if !limited && ue.Kind == ErrRateLimited && ue.RetryAfter > 0 && ue.Detail != cooldownDetail {
		until, limited = now.Add(ue.RetryAfter), true
	}
	if limited {
		w.noteLimit(until)
	}
	return until, limited
}

// observeSent reads a usage limit from the page after a send that went
// through, so the next request is held back.
func (w *WebAgent) observeSent(res SendResult) {
	if until, ok := detectLimit(w.Site, res.Notice, w.Native.cooldown().now()); ok {
		w.noteLimit(until)
	}
}

// relayTakesLimits reports whether the relay advertises web_limits, read
// at most every limitsRecheck. A failed read counts as no and is not kept,
// so the next report reads again.
func (w *WebAgent) relayTakesLimits(ctx context.Context) bool {
	now := w.clk().Now()
	w.auth.mu.Lock()
	if !w.auth.limitsCheckedAt.IsZero() && now.Sub(w.auth.limitsCheckedAt) < limitsRecheck {
		ok := w.auth.limitsOK
		w.auth.mu.Unlock()
		return ok
	}
	w.auth.mu.Unlock()
	caps, err := w.Relay.Capabilities(ctx)
	if err != nil {
		return false
	}
	w.auth.mu.Lock()
	defer w.auth.mu.Unlock()
	w.auth.limitsCheckedAt, w.auth.limitsOK = now, caps.WebLimits
	return caps.WebLimits
}

// flushWebStatus sends the latest observation now, if it is newer than the
// last one the relay took. A limited observation goes to a relay without
// web_limits as authenticated, which it also is.
func (w *WebAgent) flushWebStatus(ctx context.Context) error {
	w.auth.mu.Lock()
	snapshot := w.auth.latest
	pending := snapshot.ObservedAt.After(w.auth.reported) && !w.auth.reportUnsupported
	w.auth.mu.Unlock()
	if !pending || w.Relay == nil {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if snapshot.State == envelope.WebLimited && !w.relayTakesLimits(rctx) {
		snapshot.State, snapshot.Until = envelope.WebAuthenticated, time.Time{}
	}
	err := w.Relay.ReportWebStatus(rctx, snapshot)
	w.auth.mu.Lock()
	defer w.auth.mu.Unlock()
	switch {
	case err == nil:
		if snapshot.ObservedAt.After(w.auth.reported) {
			w.auth.reported = snapshot.ObservedAt
		}
	case errors.Is(err, client.ErrWebStatusUnsupported):
		w.auth.reportUnsupported = true
		w.logf("%v; continuing to serve", err)
		return nil
	}
	return err
}

func (w *WebAgent) probeSession(ctx context.Context) {
	w.observeAuth(w.sessionError(ctx))
}

func (w *WebAgent) sessionError(ctx context.Context) error {
	w.auth.mu.Lock()
	disabled := w.auth.probeUnsupported
	w.auth.mu.Unlock()
	if disabled {
		return ErrRejected
	}
	timeout := 30 * time.Second
	if w.site().listInTab {
		timeout = TabReadClientTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := w.Native.Request(ctx, w.site().op(opSession), OpArgs{})
	if errors.Is(err, ErrRejected) {
		w.auth.mu.Lock()
		if !w.auth.probeUnsupported {
			w.logf("session probes unavailable; upgrade the Chrome extension; traffic detection remains active")
		}
		w.auth.probeUnsupported = true
		w.auth.mu.Unlock()
	}
	return err
}

// authRequest observes failures before callers convert or swallow them.
// Public or cached detail reads need a fresh session check to clear a failure.
func (w *WebAgent) authRequest(ctx context.Context, op Op, args OpArgs) (json.RawMessage, error) {
	raw, err := w.Native.Request(ctx, op, args)
	if err != nil {
		w.observeAuth(err)
	} else if w.Site == SourceChatGPT || w.Site == SourceDots {
		// These reads fetch chatgptAuth on every operation, including old extensions.
		w.observeAuth(nil)
	} else {
		w.auth.mu.Lock()
		needs := w.auth.latest.State != envelope.WebAuthenticated || w.clk().Now().Sub(w.auth.fresh) >= webProbeInterval
		w.auth.mu.Unlock()
		if needs {
			w.probeSession(ctx)
		}
	}
	return raw, err
}

func (w *WebAgent) authSend(ctx context.Context, src Source, message, conv string, newChat bool) (SendResult, error) {
	result, err := w.Native.Send(ctx, src, message, conv, newChat)
	w.observeAuth(err)
	return result, err
}

// runWebStatus keeps relay I/O outside the site lock. A single reporter sends
// the latest snapshot in order and bounds retries even when the relay is down.
func (w *WebAgent) runWebStatus(ctx context.Context) {
	reporterDone := make(chan struct{})
	go func() { defer close(reporterDone); w.reportWebStatus(ctx) }()
	defer func() { <-reporterDone }()
	next := time.Time{}
	for ctx.Err() == nil {
		if !w.clk().Now().Before(next) {
			next = w.probeIdle(ctx)
		}

		if w.clk().Sleep(ctx, time.Second) != nil {
			return
		}
	}
}

func (w *WebAgent) reportWebStatus(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := w.flushWebStatus(ctx); err != nil {
			backoff = min(backoff*2, time.Minute)
		} else {
			backoff = time.Second
		}
		if w.clk().Sleep(ctx, backoff) != nil {
			return
		}
	}
}

// probeIdle starts only while idle, but never makes a send wait for browser I/O.
func (w *WebAgent) probeIdle(ctx context.Context) time.Time {
	now := w.clk().Now()
	if !w.mu.TryLock() {
		return now.Add(time.Second)
	}
	generation := w.sendGeneration
	w.mu.Unlock()
	if w.Native == nil {
		return now.Add(webProbeInterval)
	}
	if left := w.Native.CooldownRemaining(w.Site); left > 0 {
		return now.Add(left)
	}
	w.auth.mu.Lock()
	fresh := w.auth.fresh
	w.auth.mu.Unlock()
	if !fresh.IsZero() && now.Sub(fresh) < webProbeInterval {
		return fresh.Add(webProbeInterval)
	}
	err := w.sessionError(ctx)
	if w.mu.TryLock() {
		if generation == w.sendGeneration {
			w.observeAuth(err)
		}
		w.mu.Unlock()
	}
	return w.clk().Now().Add(webProbeInterval)
}
