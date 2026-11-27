package main

import (
	"context"

	"github.com/haoch1/Shell/internal/local"
)

type headlessAttempt struct {
	profile local.Profile
	done    chan struct{}
	sid     string
	entry   *liveSession
	err     error
	retry   bool
}

func (a *App) headlessAttemptFor(p local.Profile) (attempt *headlessAttempt, owner bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for pending := range a.headlessAttempts {
		if sameSessionTarget(p, pending.profile) {
			return pending, false
		}
	}
	attempt = &headlessAttempt{profile: p, done: make(chan struct{})}
	a.headlessAttempts[attempt] = struct{}{}
	return attempt, true
}

func (a *App) connectHeadlessOnce(ctx context.Context, p local.Profile, dial func(context.Context, local.Profile) (string, *liveSession, error)) (string, *liveSession, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		attempt, owner := a.headlessAttemptFor(p)
		if owner {
			// Check the live maps after claiming the attempt: a previous dial may just have finished.
			attempt.sid, attempt.entry, _ = a.findExistingSessionForProfile(p)
			if attempt.entry == nil {
				attempt.sid, attempt.entry, attempt.err = dial(ctx, p)
			}
			attempt.retry = attempt.err != nil && ctx.Err() != nil
			a.mu.Lock()
			delete(a.headlessAttempts, attempt)
			close(attempt.done)
			a.mu.Unlock()
		} else {
			select {
			case <-ctx.Done():
				return "", nil, ctx.Err()
			case <-attempt.done:
			}
			if attempt.retry {
				continue
			}
		}
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		return attempt.sid, attempt.entry, attempt.err
	}
}
