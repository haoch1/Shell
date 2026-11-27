package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSuggestAndProtectedSettings(t *testing.T) {
	dir := isolated(t)
	const secret = "api-test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("invalid model request")
		}
		body, err := io.ReadAll(r.Body)
		var query struct {
			Model    string
			Messages []message
		}
		if err != nil || json.Unmarshal(body, &query) != nil || query.Model != "demo" || len(query.Messages) != 2 ||
			query.Messages[1] != (message{"user", "list files"}) || bytes.Contains(body, []byte(secret)) {
			t.Error("wrong payload or key exposed in body")
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ls -la"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	settings := AISettings{Endpoint: server.URL + "/v1", Model: "demo", APIKey: secret}
	if err := SaveAI(settings); err != nil {
		t.Fatal(err)
	}
	gotSettings, err := LoadAI()
	if err != nil || gotSettings != settings {
		t.Fatal("DPAPI settings didn't round-trip", err)
	}
	blob, err := os.ReadFile(filepath.Join(dir, "ai.dpapi"))
	if err != nil || bytes.Contains(blob, []byte(secret)) {
		t.Fatal("settings file missing or key exposed")
	}
	if cmd, err := Suggest(context.Background(), "list files"); err != nil || cmd != "ls -la" {
		t.Fatalf("wrong suggestion: %q, %v", cmd, err)
	}
}

func TestServiceRestrictions(t *testing.T) {
	isolated(t)
	for _, endpoint := range []string{"http://192.0.2.10/v1", "http://localhost.example", "https://u:p@host/v1", "https://example.com/v1?token=abc"} {
		if err := SaveAI(AISettings{Endpoint: endpoint, Model: "demo"}); err == nil {
			t.Fatalf("accepted invalid endpoint %s", endpoint)
		}
	}
	var forwarded atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	if err := SaveAI(AISettings{Endpoint: redirect.URL + "/v1/chat/completions", Model: "demo", APIKey: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Suggest(context.Background(), "test"); err == nil || forwarded.Load() {
		t.Fatal("followed or ignored redirection")
	}
	for _, content := range []string{"one\ntwo", "one\u202etwo", "one\u009btwo", "one\u2067two"} {
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": message{Role: "assistant", Content: content}}}})
		if _, err := readCommand(data); err == nil {
			t.Fatal("accepted a multiline or direction-changing command")
		}
	}
}

func TestCancelInFlightAndBoundResponse(t *testing.T) {
	isolated(t)
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/large/chat/completions" {
			io.WriteString(w, strings.Repeat("x", 128<<10+1))
			return
		}
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	if err := SaveAI(AISettings{Endpoint: srv.URL + "/large", Model: "demo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Suggest(context.Background(), "test"); err == nil || !strings.Contains(err.Error(), "128 KiB") {
		t.Fatal("response size limit wasn't enforced", err)
	}
	if err := SaveAI(AISettings{Endpoint: srv.URL, Model: "demo"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Suggest(ctx, "test")
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("request never started: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for request")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("request didn't report cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request wasn't canceled")
	}
}
