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

func TestAIStreamSizeLimit(t *testing.T) {
	const limit = 256 << 10
	const chunk = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"
	for _, size := range []int{limit, limit + 1} {
		padding := strings.Repeat(":\n", size/2)
		raw := chunk + padding[:size-len(chunk)]
		resp := &http.Response{
			Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body:   io.NopCloser(strings.NewReader(raw)),
		}
		got, err := readStreamOrJSON(t.Context(), resp, nil)
		if size == limit && (got != "ok" || err != nil) {
			t.Fatalf("valid stream at limit: %q, %v", got, err)
		}
		if size > limit && (got != "" || err == nil || !strings.Contains(err.Error(), "256 KiB")) {
			t.Fatalf("oversized stream returned as complete: %q, %v", got, err)
		}
	}
}

func TestLocalAICLIExecutionAndPersistence(t *testing.T) {
	isolated(t)

	// Verify preset arg builders for claude, codex, gemini, and custom {prompt}.
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "claude", CLIModel: "sonnet"}, "hello"); stdin != "hello" || strings.Join(args, " ") != "-p --output-format text --model sonnet" {
		t.Fatalf("unexpected claude args=%v stdin=%q", args, stdin)
	}
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "codex", CLIModel: "o3"}, "hello"); stdin != "hello" || strings.Join(args, " ") != "exec --skip-git-repo-check -m o3 -" {
		t.Fatalf("unexpected codex args=%v stdin=%q", args, stdin)
	}
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "agy", CLIModel: "gemini-2.5-pro"}, "hello"); stdin != "hello" || strings.Join(args, " ") != "-p --output-format text --disable-slash-commands --model gemini-2.5-pro" {
		t.Fatalf("unexpected agy args=%v stdin=%q", args, stdin)
	}
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "gemini", CLIModel: "gemini-2.5-pro"}, "hello"); stdin != "hello" || strings.Join(args, " ") != "-m gemini-2.5-pro" {
		t.Fatalf("unexpected gemini args=%v stdin=%q", args, stdin)
	}
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "mycli", CLIArgs: `--prompt "{prompt}"`}, "ping"); stdin != "" || len(args) != 2 || args[1] != "ping" {
		t.Fatalf("unexpected placeholder args=%v stdin=%q", args, stdin)
	}

	// Execute a real hidden-window .cmd shim (matching npm-installed claude.cmd / codex.cmd / gemini.cmd on Windows).
	shimPath := filepath.Join(t.TempDir(), "mock-ai.cmd")
	shimBody := "@echo off\r\necho {\"command\":\"ss -tulpn\",\"explanation\":\"查看监听端口\",\"risk\":\"safe\"}\r\n"
	if err := os.WriteFile(shimPath, []byte(shimBody), 0600); err != nil {
		t.Fatal(err)
	}
	cliCfg := AISettings{
		Mode:       "cli",
		CLICommand: shimPath,
	}
	if err := SaveAI(cliCfg); err != nil {
		t.Fatalf("SaveAI CLI failed: %v", err)
	}
	loaded, err := LoadAI()
	if err != nil || !loaded.IsCLI() || loaded.CLICommand != shimPath {
		t.Fatalf("LoadAI CLI mismatch: %+v, err=%v", loaded, err)
	}
	var streamed strings.Builder
	res, err := SuggestStructured(context.Background(), "查看监听端口", "Ubuntu", func(d string) {
		streamed.WriteString(d)
	})
	if err != nil {
		t.Fatalf("SuggestStructured via CLI failed: %v", err)
	}
	if res.Command != "ss -tulpn" || res.Risk != "safe" || streamed.Len() == 0 {
		t.Fatalf("unexpected CLI AIResult: %+v, streamed=%q", res, streamed.String())
	}
	if err := TestAI(context.Background(), cliCfg); err != nil {
		t.Fatalf("TestAI via CLI failed: %v", err)
	}
}
