package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/haoch1/Shell/internal/local"
	"github.com/haoch1/Shell/internal/remote"
)

func TestVisibleConnectPropagatesResult(t *testing.T) {
	app := newApp()
	first := local.Profile{ID: "one", Host: "example.test", Port: 22, User: "alice"}
	other := local.Profile{ID: "two", Host: first.Host, Port: 2222, User: "bob"}
	wantErr := errors.New("authentication rejected")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err := app.waitVisibleConnect(ctx, first, func() {
		app.CancelVisibleConnect(other.ID)
		app.finishVisibleConnect(other, "wrong", nil)
		app.finishVisibleConnect(first, "failed", wantErr)
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("wanted original SSH error, got %v", err)
	}
	if len(app.visibleWaiters) != 0 {
		t.Fatal("waiter not removed")
	}
	_, _, err = app.waitVisibleConnect(ctx, first, func() { app.CancelVisibleConnect(first.ID) })
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing prompt cancellation: %v", err)
	}
	entry := &liveSession{session: &remote.Session{}, profile: first}
	id, got, err := app.waitVisibleConnect(ctx, first, func() { app.live["gui"] = entry; app.finishVisibleConnect(first, "gui", nil) })
	if err != nil || id != "gui" || got != entry {
		t.Fatalf("incorrect GUI result: %s, %v", id, err)
	}
}

func TestChooseProfileRequiresExactTarget(t *testing.T) {
	profiles := []local.Profile{{ID: "one", Name: "web", Host: "same.example", Port: 22, User: "a"}, {ID: "two", Name: "web", Host: "same.example", Port: 2222, User: "b"}}
	for _, ambiguous := range []string{"same.example", "web"} {
		if _, err := matchSavedProfile(profiles, ambiguous); err == nil {
			t.Fatalf("unexpected match for %s", ambiguous)
		}
	}
	p, err := matchSavedProfile(profiles, "two")
	if err != nil || p.ID != "two" {
		t.Fatal("exact id was not preferred")
	}
	app := newApp()
	app.live["wrong"] = &liveSession{profile: profiles[0], session: &remote.Session{}}
	if _, got, _ := app.findExistingSessionForProfile(*p); got != nil {
		t.Fatal("reused another account on the same host")
	}
	changed := profiles[0]
	changed.Port++
	if _, got, _ := app.findExistingSessionForProfile(changed); got != nil {
		t.Fatal("reused profile after endpoint change")
	}
}

func TestExplicitOrImplicitSessionHonoursAccess(t *testing.T) {
	app := newApp()
	app.live["only"] = &liveSession{session: &remote.Session{}, profile: local.Profile{AgentAccess: "deny"}}
	for _, selected := range []string{"only", ""} {
		if _, _, _, err := app.resolveOrConnectSession(context.Background(), selected, "", local.AgentConfig{}, false); err == nil {
			t.Errorf("used denied session %q", selected)
		}
	}
}

func TestVisibleConnectSharesOneWindowRequest(t *testing.T) {
	app := newApp()
	profile := local.Profile{ID: "shared", Host: "loopback.test", Port: 22}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	opened := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, err := app.waitVisibleConnect(ctx, profile, func() { close(opened) })
		done <- err
	}()
	<-opened
	duplicate := make(chan struct{}, 1)
	go func() {
		_, _, err := app.waitVisibleConnect(ctx, profile, func() { duplicate <- struct{}{} })
		done <- err
	}()
	for {
		app.mu.Lock()
		pending := len(app.visibleWaiters)
		app.mu.Unlock()
		if pending == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("second waiter did not register")
		case <-time.After(time.Millisecond):
		}
	}
	want := errors.New("the visible attempt ended")
	app.finishVisibleConnect(profile, "example", want)
	for i := 0; i < 2; i++ {
		if err := <-done; !errors.Is(err, want) {
			t.Fatalf("missing shared result: %v", err)
		}
	}
	select {
	case <-duplicate:
		t.Fatal("opened duplicate windows")
	default:
	}
}
