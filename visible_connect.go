package main

import (
	"context"
	"errors"

	"github.com/haoch1/Shell/internal/local"
)

type visibleResult struct {
	id    string
	entry *liveSession
	err   error
}

// Register under the same lock as the live-session check so a fast GUI result cannot be missed.
func (a *App) waitVisibleConnect(ctx context.Context, p local.Profile, open func()) (string, *liveSession, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	result := make(chan visibleResult, 1)
	a.mu.Lock()
	for id, e := range a.live {
		if e != nil && e.session != nil && !e.session.IsClosed() && sameSessionTarget(p, e.profile) {
			a.mu.Unlock()
			return id, e, nil
		}
	}
	alreadyOpening := false
	for _, pending := range a.visibleWaiters {
		if sameSessionTarget(pending, p) {
			alreadyOpening = true
			break
		}
	}
	a.visibleWaiters[result] = p
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.visibleWaiters, result); a.mu.Unlock() }()
	if !alreadyOpening {
		open()
	}
	select {
	case r := <-result:
		return r.id, r.entry, r.err
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
}

func (a *App) finishVisibleConnect(p local.Profile, id string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.live[id]
	if err == nil && (e == nil || e.session == nil || e.session.IsClosed() || !sameSessionTarget(p, e.profile)) {
		err = errors.New("前台会话已关闭")
	}
	for result, want := range a.visibleWaiters {
		if sameSessionTarget(p, want) {
			delete(a.visibleWaiters, result)
			result <- visibleResult{id: id, entry: e, err: err}
		}
	}
}

// CancelVisibleConnect ends the MCP wait when its credential dialog is dismissed.
func (a *App) CancelVisibleConnect(profileID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for result, want := range a.visibleWaiters {
		if want.ID == profileID {
			delete(a.visibleWaiters, result)
			result <- visibleResult{err: errors.New("前台连接已取消或主机资料不可用")}
		}
	}
}
