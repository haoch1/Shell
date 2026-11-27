package main

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/haoch1/Shell/internal/local"
	"github.com/haoch1/Shell/internal/remote"
)

func TestConcurrentHeadlessConnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		app := newApp()
		profile := local.Profile{ID: "one", Host: "host.test", User: "alice"}
		other := profile
		other.User = "bob" // Same profile ID, but the saved account changed.
		release := make(chan struct{})
		entry := &liveSession{profile: profile, session: &remote.Session{}}
		attempts := 0
		dial := func(ctx context.Context, p local.Profile) (string, *liveSession, error) {
			if p.User == other.User {
				return "other", &liveSession{profile: p}, nil
			}
			attempts++
			<-release
			return "one", entry, nil
		}
		connect := func(ctx context.Context) <-chan error {
			done := make(chan error, 1)
			go func() {
				id, got, err := app.connectHeadlessOnce(ctx, profile, dial)
				if err == nil && (id != "one" || got != entry) {
					err = errors.New("unexpected shared session")
				}
				done <- err
			}()
			return done
		}
		owner := connect(t.Context())
		synctest.Wait()
		waiter := connect(t.Context())
		waitCtx, cancel := context.WithCancel(t.Context())
		canceled := connect(waitCtx)
		synctest.Wait()
		cancel()
		if err := <-canceled; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter cancellation: %v", err)
		}
		if id, _, err := app.connectHeadlessOnce(t.Context(), other, dial); err != nil || id != "other" {
			t.Fatalf("independent account: %q %v", id, err)
		}
		close(release)
		if err := <-owner; err != nil {
			t.Fatal(err)
		}
		if err := <-waiter; err != nil {
			t.Fatal(err)
		}
		if attempts != 1 {
			t.Fatalf("got %d dials for the same account", attempts)
		}
		app.live["gui"] = entry
		if id, got, err := app.connectHeadlessOnce(t.Context(), profile, dial); err != nil || id != "gui" || got != entry || attempts != 1 {
			t.Fatalf("existing session not reused: %q %v", id, err)
		}
	})
}

func TestHeadlessConnectRetriesAfterOwnerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		app := newApp()
		profile := local.Profile{ID: "one", Host: "host.test"}
		ownerCtx, cancel := context.WithCancel(t.Context())
		attempts := 0
		authErr := errors.New("authentication rejected")
		dial := func(ctx context.Context, _ local.Profile) (string, *liveSession, error) {
			attempts++
			if attempts == 1 {
				<-ctx.Done()
				return "", nil, ctx.Err()
			}
			return "", nil, authErr
		}
		connect := func(ctx context.Context) <-chan error {
			done := make(chan error, 1)
			go func() { _, _, err := app.connectHeadlessOnce(ctx, profile, dial); done <- err }()
			return done
		}
		owner := connect(ownerCtx)
		synctest.Wait()
		waiter := connect(t.Context())
		synctest.Wait()
		cancel()
		if err := <-owner; !errors.Is(err, context.Canceled) {
			t.Fatalf("owner cancellation: %v", err)
		}
		if err := <-waiter; !errors.Is(err, authErr) || attempts != 2 {
			t.Fatalf("waiter retry: %v, %d dials", err, attempts)
		}
		if err := <-connect(t.Context()); !errors.Is(err, authErr) || attempts != 3 {
			t.Fatalf("failed attempt was cached: %v, %d dials", err, attempts)
		}
	})
}
