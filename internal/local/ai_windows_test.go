package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
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
	blob, err := os.ReadFile(filepath.Join(dir, "security", "ai.dpapi"))
	if err != nil || bytes.Contains(blob, []byte(secret)) {
		t.Fatal("settings file missing in security/ or key exposed")
	}
	if _, err := os.Stat(filepath.Join(dir, "ai.dpapi")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected legacy root ai.dpapi created: err=%v", err)
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
		got, err := readStreamOrJSON(t.Context(), resp, "", nil)
		if size == limit && (got != "ok" || err != nil) {
			t.Fatalf("valid stream at limit: %q, %v", got, err)
		}
		if size > limit && (got != "" || err == nil || !strings.Contains(err.Error(), "256 KiB")) {
			t.Fatalf("oversized stream returned as complete: %q, %v", got, err)
		}
	}
}

func TestAIStreamCompletesWithoutWaitingForEOF(t *testing.T) {
	const textEvent = "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
	for _, terminator := range []string{
		"data: [DONE]\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	} {
		body := io.MultiReader(strings.NewReader(textEvent+terminator), iotest.ErrReader(errors.New("read past final event")))
		resp := &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(body)}
		var streamed strings.Builder
		got, err := readStreamOrJSON(t.Context(), resp, "", func(s string) { streamed.WriteString(s) })
		if err != nil || got != "hello" || streamed.String() != got {
			t.Fatalf("terminal event %q: got %q, streamed %q, err %v", terminator, got, streamed.String(), err)
		}
	}
}

func TestCLIOutputDrainsAfterProcessExit(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL_TEST_PIPE_CHILD", "1")
	args := []string{"-test.run=^TestCLIPipeHelper$", "--", "late-error"}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := execLocalAICLIOnce(cancelled, self, "fixture", "fixture", args, "", "", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation before startup: %v", err)
	}
	_, stderr, err := execLocalAICLIOnce(t.Context(), self, "fixture", "fixture", args, "", "", nil)
	if err == nil || !strings.Contains(err.Error(), "late diagnostic") || !strings.Contains(stderr, "late diagnostic") {
		t.Fatalf("missing stderr written after stdout closed: %q, %v", stderr, err)
	}
	args[len(args)-1] = "inherited"
	_, _, err = execLocalAICLIOnce(t.Context(), self, "fixture", "fixture", args, "", "", nil)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("expected WaitDelay for an inherited output pipe, got %v", err)
	}
}

func TestAIConnectionTestRequiresAValidReply(t *testing.T) {
	const key = "custom-demo-key-789"
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"valid", `{"choices":[{"message":{"content":"OK"}}]}`, true},
		{"login page", "<html>Sign in</html>", false},
		{"provider error", `{"error":{"message":"invalid credential ` + key + `"}}`, false},
		{"empty answer", `{"choices":[{"message":{"content":""}}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			err := TestAI(t.Context(), AISettings{Endpoint: srv.URL, Model: "demo", APIKey: key})
			if (err == nil) != tc.ok || (err != nil && strings.Contains(err.Error(), key)) {
				t.Fatalf("bad connection result or exposed credential: success=%t", err == nil)
			}
		})
	}

	headersSent := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(headersSent)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- TestAI(ctx, AISettings{Endpoint: srv.URL, Model: "demo"}) }()
	select {
	case <-headersSent:
	case err := <-done:
		t.Fatalf("request failed before cancellation: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("connection test did not reach the local server")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("response body cancellation lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled connection test did not return")
	}
}

func TestCLIPipeHelper(t *testing.T) {
	if os.Getenv("SHELL_TEST_PIPE_CHILD") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "late-error":
		_ = os.Stdout.Close()
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(os.Stderr, "late diagnostic")
		os.Exit(1)
	case "inherited":
		child := exec.Command(os.Args[0], "-test.run=^TestCLIPipeHelper$", "--", "hold")
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	case "hold":
		time.Sleep(8 * time.Second)
		os.Exit(0)
	}
}

func TestCLIOutputLimitAtUTF8Boundary(t *testing.T) {
	var emitted strings.Builder
	w := cliStreamOutput{remaining: 5, onDelta: func(s string) { emitted.WriteString(s) }}
	p := []byte("你好")
	for _, b := range [][]byte{p[:1], p[1:4], p[4:]} {
		n, err := w.Write(b)
		if err != nil || n != len(b) {
			t.Fatalf("did not drain output: n=%d, err=%v", n, err)
		}
	}
	if got := w.text.String(); got != "你" || emitted.String() != got {
		t.Fatalf("truncated UTF-8 stream: text=%q, chunks=%q", got, emitted.String())
	}
}

func TestLocalAICLIExecutionAndPersistence(t *testing.T) {
	isolated(t)

	// Verify preset arg builders for claude, codex, agy, gemini, extra flag dedup, and custom {prompt}.
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "claude", CLIModel: "sonnet"}, "hello"); stdin != "hello" || strings.Join(args, " ") != "--output-format text --no-session-persistence --model sonnet -p" {
		t.Fatalf("unexpected claude args=%v stdin=%q", args, stdin)
	}
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "claude", CLIModel: "sonnet", CLIArgs: "--output-format json --no-session-persistence --max-turns 2"}, "hello"); stdin != "hello" || strings.Join(args, " ") != "--model sonnet --output-format json --no-session-persistence --max-turns 2 -p" {
		t.Fatalf("unexpected claude custom args=%v stdin=%q", args, stdin)
	}
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "codex", CLIModel: "o3"}, "hello"); stdin != "hello" || strings.Join(args, " ") != `exec --skip-git-repo-check --ephemeral -c history.persistence="none" -c notify=[] --color never -s read-only -m o3 -` {
		t.Fatalf("unexpected codex args=%v stdin=%q", args, stdin)
	}
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "codex", CLIModel: "o3", CLIArgs: `-s workspace-write --color always -c 'history.persistence="save-all"' --config='notify=["bell"]'`}, "hello"); stdin != "hello" || strings.Join(args, " ") != `exec --skip-git-repo-check --ephemeral -m o3 -s workspace-write --color always -c history.persistence="save-all" --config=notify=["bell"] -` {
		t.Fatalf("unexpected codex custom args=%v stdin=%q", args, stdin)
	}
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "agy", CLIModel: "gemini-2.5-pro"}, "hello"); stdin != "hello" || strings.Join(args, " ") != "--output-format text --disable-slash-commands --model gemini-2.5-pro" {
		t.Fatalf("unexpected agy args=%v stdin=%q", args, stdin)
	}
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "agy", CLIModel: "gemini-2.5-pro", CLIArgs: "--output-format=json --effort high"}, "hello"); stdin != "hello" || strings.Join(args, " ") != "--disable-slash-commands --model gemini-2.5-pro --output-format=json --effort high" {
		t.Fatalf("unexpected agy custom args=%v stdin=%q", args, stdin)
	}
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "gemini", CLIModel: "gemini-2.5-pro"}, "hello"); stdin != "hello" || strings.Join(args, " ") != "-m gemini-2.5-pro" {
		t.Fatalf("unexpected gemini args=%v stdin=%q", args, stdin)
	}
	if _, args, stdin := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "mycli", CLIArgs: `--prompt "{prompt}"`}, "ping"); stdin != "" || len(args) != 2 || args[1] != "ping" {
		t.Fatalf("unexpected placeholder args=%v stdin=%q", args, stdin)
	}

	// Execute a real hidden-window .cmd shim (matching npm-installed claude.cmd / codex.cmd / gemini.cmd on Windows).
	shimDir := t.TempDir()
	shimPath := filepath.Join(shimDir, "mock-ai.cmd")
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

	// Verify codex -o <tmpFile> clean-output fallback strips stdout banner noise and removes the temp file.
	codexShim := filepath.Join(shimDir, "codex.cmd")
	outPathRecord := filepath.Join(shimDir, "recorded-out-path.txt")
	codexShimBody := strings.Join([]string{
		"@echo off",
		"setlocal enabledelayedexpansion",
		"set OUTFILE=",
		":loop",
		"if \"%~1\"==\"\" goto done",
		"if \"%~1\"==\"-o\" (",
		"  set \"OUTFILE=%~2\"",
		"  shift",
		")",
		"shift",
		"goto loop",
		":done",
		"echo [Codex Banner] Reading prompt from stdin...",
		"if not \"!OUTFILE!\"==\"\" (",
		"  >\"!OUTFILE!\" echo {\"command\":\"df -h\",\"explanation\":\"查看磁盘占用\",\"risk\":\"safe\"}",
		"  >\"" + outPathRecord + "\" echo !OUTFILE!",
		")",
		"exit /b 0",
		"",
	}, "\r\n")
	if err := os.WriteFile(codexShim, []byte(codexShimBody), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveAI(AISettings{Mode: "cli", CLICommand: codexShim}); err != nil {
		t.Fatal(err)
	}
	codexRes, err := SuggestStructured(context.Background(), "磁盘空间", "Ubuntu", nil)
	if err != nil {
		t.Fatalf("codex -o fallback failed: %v", err)
	}
	if codexRes.Command != "df -h" || codexRes.Risk != "safe" {
		t.Fatalf("unexpected codex -o AIResult: %+v", codexRes)
	}
	if recBytes, rErr := os.ReadFile(outPathRecord); rErr != nil {
		t.Fatalf("expected codex shim to receive -o tmp file path: %v", rErr)
	} else {
		tmpPath := strings.TrimSpace(string(recBytes))
		if tmpPath == "" {
			t.Fatal("codex shim recorded empty -o path")
		}
		if _, statErr := os.Stat(tmpPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("expected codex temp file %q to be deleted after run, statErr=%v", tmpPath, statErr)
		}
	}

	// Verify Windows Job Object cleanly cancels a .cmd -> child process tree without hanging.
	hangShim := filepath.Join(shimDir, "hang-tree.cmd")
	hangBody := "@echo off\r\necho started\r\nping -n 30 127.0.0.1 >nul\r\n"
	if err := os.WriteFile(hangShim, []byte(hangBody), 0600); err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(250 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err = runLocalAICLI(cancelCtx, AISettings{Mode: "cli", CLICommand: hangShim}, "", "ping", nil)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from job-canceled CLI tree, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("CLI tree cancellation took too long (%v); child process may have leaked", elapsed)
	}
}

type rewriteHostTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt *rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = rt.target.Scheme
	cloned.URL.Host = rt.target.Host
	cloned.Host = rt.target.Host
	return rt.base.RoundTrip(cloned)
}

func redirectAIClientToServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	prev := aiClient.Transport
	aiClient.Transport = &rewriteHostTransport{target: target, base: srv.Client().Transport}
	t.Cleanup(func() {
		aiClient.Transport = prev
	})
}

func TestFetchAIModelsOpenAIAndNoiseFiltering(t *testing.T) {
	isolated(t)
	const secret = "sk-openai-models-test"
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if r.URL.Path == "/only-noise/models" {
			io.WriteString(w, `{"data":[{"id":"text-embedding-3-large"},{"id":"whisper-1"}]}`)
			return
		}
		io.WriteString(w, `{"data":[
			{"id":"gpt-4o-mini"},
			{"id":"text-embedding-3-large"},
			{"id":"whisper-1"},
			{"id":"tts-1-hd"},
			{"id":"dall-e-3"},
			{"id":"omni-moderation-latest"},
			{"id":"babbage-002"},
			{"id":"davinci-002"},
			{"id":"gpt-image-1"},
			{"id":"sora-2"},
			{"id":"gpt-4o-realtime-preview"},
			{"id":"gpt-4o-transcribe"},
			{"id":"gpt-4o-audio-preview"},
			{"id":"gpt-6.1-terra"},
			{"id":"gpt-6.2-sol"},
			{"id":"gpt-6.2-sol"},
			{"id":"gpt-4o"}
		]}`)
	}))
	defer srv.Close()

	// Endpoint with trailing /chat/completions should be stripped cleanly before /models.
	models, err := FetchAIModels(context.Background(), AIConfigInput{
		Endpoint: srv.URL + "/v1/chat/completions",
		APIKey:   secret,
	})
	if err != nil {
		t.Fatalf("FetchAIModels OpenAI failed: %v", err)
	}
	if gotPath != "/v1/models" || gotAuth != "Bearer "+secret {
		t.Fatalf("unexpected OpenAI request: path=%q auth=%q", gotPath, gotAuth)
	}
	want := []string{"gpt-6.2-sol", "gpt-6.1-terra", "gpt-4o", "gpt-4o-mini"}
	if !slices.Equal(models, want) {
		t.Fatalf("unexpected OpenAI models: got=%v want=%v", models, want)
	}

	// KeepKey should load the saved DPAPI key when APIKey is empty and endpoint matches.
	if err := SaveAI(AISettings{Endpoint: srv.URL + "/v1", Model: "gpt-6.2-sol", APIKey: secret}); err != nil {
		t.Fatal(err)
	}
	gotAuth = ""
	models, err = FetchAIModels(context.Background(), AIConfigInput{
		Endpoint: srv.URL + "/v1/",
		KeepKey:  true,
	})
	if err != nil || gotAuth != "Bearer "+secret || len(models) != len(want) {
		t.Fatalf("KeepKey fetch failed: auth=%q models=%v err=%v", gotAuth, models, err)
	}

	// When all returned models match non-chat filters, keep raw IDs instead of returning 0.
	onlyNoise, err := FetchAIModels(context.Background(), AIConfigInput{
		Endpoint: srv.URL + "/only-noise",
		APIKey:   secret,
	})
	if err != nil || len(onlyNoise) != 2 {
		t.Fatalf("expected raw fallback for noise-only response, got=%v err=%v", onlyNoise, err)
	}
}

func TestFetchAIModelsAnthropicHeadersAndOrder(t *testing.T) {
	isolated(t)
	var gotKey, gotVer, gotAuth, gotPath, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVer = r.Header.Get("anthropic-version")
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotLimit = r.URL.Query().Get("limit")
		io.WriteString(w, `{"data":[
			{"id":"claude-sonnet-5-5"},
			{"id":"claude-opus-5-5"},
			{"id":"claude-fable-5-2"},
			{"id":"claude-haiku-5-5"}
		]}`)
	}))
	defer srv.Close()
	redirectAIClientToServer(t, srv)

	models, err := FetchAIModels(context.Background(), AIConfigInput{
		Endpoint: "https://api.anthropic.com/v1",
		APIKey:   "sk-ant-test-key",
	})
	if err != nil {
		t.Fatalf("FetchAIModels Anthropic failed: %v", err)
	}
	if gotPath != "/v1/models" || gotLimit != "100" || gotKey != "sk-ant-test-key" || gotVer != "2023-06-01" || gotAuth != "" {
		t.Fatalf("unexpected Anthropic request: path=%q limit=%q key=%q ver=%q auth=%q", gotPath, gotLimit, gotKey, gotVer, gotAuth)
	}
	want := []string{"claude-sonnet-5-5", "claude-opus-5-5", "claude-fable-5-2", "claude-haiku-5-5"}
	if !slices.Equal(models, want) {
		t.Fatalf("expected Anthropic recency order preserved: got=%v want=%v", models, want)
	}
}

func TestFetchAIModelsGeminiNativeFallback(t *testing.T) {
	isolated(t)
	var openaiHits, nativeHits int
	var gotOpenAIAuth, gotNativeKey, gotPageSize string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1beta/openai/models":
			openaiHits++
			gotOpenAIAuth = r.Header.Get("Authorization")
			http.Error(w, "not supported", http.StatusNotFound)
		case "/v1beta/models":
			nativeHits++
			gotNativeKey = r.Header.Get("x-goog-api-key")
			gotPageSize = r.URL.Query().Get("pageSize")
			io.WriteString(w, `{"models":[
				{"name":"models/embedding-001","supportedGenerationMethods":["embedContent"]},
				{"name":"models/aqa","supportedGenerationMethods":["generateAnswer"]},
				{"name":"models/imagen-3.0-generate-002","supportedGenerationMethods":["generateContent"]},
				{"name":"models/veo-2.0","supportedGenerationMethods":["generateContent"]},
				{"name":"models/gemini-3.5-flash-lite","supportedGenerationMethods":["generateContent"]},
				{"name":"models/gemini-4-neon","supportedGenerationMethods":["generateContent","countTokens"]},
				{"name":"models/gemini-4-carbon","supportedGenerationMethods":["generateContent"]}
			]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	redirectAIClientToServer(t, srv)

	models, err := FetchAIModels(context.Background(), AIConfigInput{
		Endpoint: "https://generativelanguage.googleapis.com/v1beta/openai",
		APIKey:   "gk-test-secret",
	})
	if err != nil {
		t.Fatalf("FetchAIModels Gemini fallback failed: %v", err)
	}
	if openaiHits != 1 || nativeHits != 1 || gotOpenAIAuth != "Bearer gk-test-secret" || gotNativeKey != "gk-test-secret" || gotPageSize != "100" {
		t.Fatalf("unexpected Gemini calls: openai=%d native=%d auth=%q key=%q pageSize=%q", openaiHits, nativeHits, gotOpenAIAuth, gotNativeKey, gotPageSize)
	}
	want := []string{"gemini-4-carbon", "gemini-4-neon", "gemini-3.5-flash-lite"}
	if !slices.Equal(models, want) {
		t.Fatalf("unexpected Gemini models: got=%v want=%v", models, want)
	}
}

func TestFetchAIModelsOllamaFallbackAndErrors(t *testing.T) {
	isolated(t)
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/ollama/models":
			io.WriteString(w, `{"models":[
				{"name":"nomic-embed-text:latest"},
				{"name":"qwen3:8b"},
				{"name":"llama3.3:latest"}
			]}`)
		case "/unauth/models":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case "/missing/models":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	models, err := FetchAIModels(context.Background(), AIConfigInput{
		Endpoint: srv.URL + "/ollama",
	})
	if err != nil {
		t.Fatalf("FetchAIModels Ollama failed: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("expected no Authorization header for keyless Ollama, got %q", gotAuth)
	}
	want := []string{"llama3.3:latest", "qwen3:8b"}
	if !slices.Equal(models, want) {
		t.Fatalf("unexpected Ollama models: got=%v want=%v", models, want)
	}

	if _, err := FetchAIModels(context.Background(), AIConfigInput{Endpoint: srv.URL + "/unauth"}); err == nil || !strings.Contains(err.Error(), "API 密钥无效或未填写") {
		t.Fatalf("expected 401 Chinese error, got: %v", err)
	}
	if _, err := FetchAIModels(context.Background(), AIConfigInput{Endpoint: srv.URL + "/missing"}); err == nil || !strings.Contains(err.Error(), "该接口不支持自动拉取模型列表") {
		t.Fatalf("expected 404 Chinese error, got: %v", err)
	}
	if _, err := FetchAIModels(context.Background(), AIConfigInput{Endpoint: "http://192.0.2.10/v1"}); err == nil {
		t.Fatal("expected non-loopback HTTP endpoint to be rejected")
	}
}

func TestAIStoragePathSecurityOnly(t *testing.T) {
	dir := isolated(t)
	// Seed a legacy root ai.dpapi file to ensure LoadAI does NOT fall back to root.
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeProtected(filepath.Join(dir, "ai.dpapi"), AISettings{
		Mode:       "cli",
		CLICommand: "legacy-cli",
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAI()
	if err != nil {
		t.Fatalf("LoadAI failed with only legacy file present: %v", err)
	}
	if loaded.CLICommand != "" {
		t.Fatalf("expected zero legacy fallback from root ai.dpapi, got %+v", loaded)
	}

	want := AISettings{Mode: "cli", CLICommand: "codex", CLIModel: "gpt-6.2-sol"}
	if err := SaveAI(want); err != nil {
		t.Fatalf("SaveAI failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "security", "ai.dpapi")); err != nil {
		t.Fatalf("expected security/ai.dpapi to exist: %v", err)
	}
	got, err := LoadAI()
	if err != nil || got != want {
		t.Fatalf("LoadAI round-trip mismatch: got=%+v want=%+v err=%v", got, want, err)
	}
}

func TestParseAgyModelsOutput(t *testing.T) {
	raw := strings.Join([]string{
		"\x1b[2K\rFetching available models...",
		"Usage: agy models [flags]",
		"Error: transient warning",
		"ID\tDisplay Name",
		"",
		"\x1b[36mgemini-4-neon-high\x1b[0m\tGemini 4 Neon (High)",
		"gemini-4-neon-medium\tGemini 4 Neon (Medium)",
		"claude-sonnet-5-5-high\tClaude Sonnet 5.5 (High)",
		"gemini-4-neon-high\tGemini 4 Neon (High)",
		"gpt-oss-120b-medium\tGPT-OSS 120B (Medium)",
		"Not logged in, please retry",
	}, "\r\n")

	got := parseAgyModelsOutput(raw)
	want := []string{
		"gemini-4-neon-high",
		"gemini-4-neon-medium",
		"claude-sonnet-5-5-high",
		"gpt-oss-120b-medium",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("parseAgyModelsOutput mismatch: got=%v want=%v", got, want)
	}
}

func TestParseCodexModelsCacheAndConfig(t *testing.T) {
	cacheJSON := []byte(`{
		"models": [
			{"slug": "gpt-6.1-sol", "visibility": "list", "priority": 5},
			{"slug": "gpt-reserve", "visibility": "hide", "priority": 2},
			{"slug": "gpt-6.2-sol", "visibility": "list", "priority": 1},
			{"id": "gpt-legacy-unranked", "visibility": ""},
			{"slug": "gpt-6.2-astra", "visibility": "list", "priority": 2},
			{"slug": "gpt-6.2-sol", "visibility": "list", "priority": 3},
			{"slug": "codex-auto-review", "visibility": "HIDE", "priority": 4},
			{"id": "gpt-second-unranked"}
		]
	}`)
	got := parseCodexModelsCache(cacheJSON)
	want := []string{
		"gpt-6.2-sol",
		"gpt-6.2-astra",
		"gpt-6.1-sol",
		"gpt-legacy-unranked",
		"gpt-second-unranked",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("parseCodexModelsCache mismatch: got=%v want=%v", got, want)
	}

	tomlCases := []struct {
		name string
		data string
		want string
	}{
		{
			name: "top-level double-quoted with comment",
			data: "\xef\xbb\xbf# codex config\nmodel = \"gpt-6.2-sol\" # preferred\n[profiles.alt]\nmodel = \"other\"\n",
			want: "gpt-6.2-sol",
		},
		{
			name: "top-level single-quoted",
			data: "service_tier = 'priority'\nmodel = 'o3-mini'\n",
			want: "o3-mini",
		},
		{
			name: "nested only should be ignored",
			data: "[profiles.fast]\nmodel = \"nested-only\"\n",
			want: "",
		},
	}
	for _, tc := range tomlCases {
		if got := parseCodexConfigModel([]byte(tc.data)); got != tc.want {
			t.Fatalf("parseCodexConfigModel(%s): got=%q want=%q", tc.name, got, tc.want)
		}
	}
}

func TestParseGenericCLIModelsOutput(t *testing.T) {
	if got := parseGenericCLIModelsOutput(`{"models":["m-1","m-2","m-1"]}`); !slices.Equal(got, []string{"m-1", "m-2"}) {
		t.Fatalf("JSON string list mismatch: %v", got)
	}
	if got := parseGenericCLIModelsOutput(`{"data":[{"id":"alpha-1"},{"id":"beta-2"}]}`); !slices.Equal(got, []string{"alpha-1", "beta-2"}) {
		t.Fatalf("JSON data list mismatch: %v", got)
	}
	helpOut := "Usage: mycli [options] [command]\n\nOptions:\n  -h, --help   display help\n\nCommands:\n  chat         start chat\n  login        authenticate\n"
	if got := parseGenericCLIModelsOutput(helpOut); len(got) != 0 {
		t.Fatalf("expected help output to be rejected, got=%v", got)
	}
}

func TestFetchAIModelsCLICodexAndConfigPrepend(t *testing.T) {
	isolated(t)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("PATH", t.TempDir())

	cacheBody := `{
		"models": [
			{"slug": "gpt-6.1-terra", "visibility": "list", "priority": 3},
			{"slug": "gpt-reserve", "visibility": "hide", "priority": 1},
			{"slug": "gpt-6.2-sol", "visibility": "list", "priority": 1}
		]
	}`
	if err := os.WriteFile(filepath.Join(codexHome, "models_cache.json"), []byte(cacheBody), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("model = \"custom-default-model\"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	models, err := FetchAIModels(context.Background(), AIConfigInput{
		Mode:       "cli",
		CLICommand: "codex",
	})
	if err != nil {
		t.Fatalf("FetchAIModels codex failed: %v", err)
	}
	want := []string{"custom-default-model", "gpt-6.2-sol", "gpt-6.1-terra"}
	if !slices.Equal(models, want) {
		t.Fatalf("codex models mismatch: got=%v want=%v", models, want)
	}

	// When config.toml model is already in cache, it should not be duplicated.
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("model = \"gpt-6.1-terra\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	models, err = FetchAIModels(context.Background(), AIConfigInput{
		Mode:       "cli",
		CLICommand: "codex",
	})
	if err != nil || !slices.Equal(models, []string{"gpt-6.2-sol", "gpt-6.1-terra"}) {
		t.Fatalf("codex dedupe mismatch: got=%v err=%v", models, err)
	}

	// Installed codex without models_cache.json falls back to config.toml, or clean error if empty.
	shimDir := t.TempDir()
	codexShim := filepath.Join(shimDir, "codex.cmd")
	if err := os.WriteFile(codexShim, []byte("@echo off\r\nexit /b 0\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	emptyCodexHome := t.TempDir()
	t.Setenv("CODEX_HOME", emptyCodexHome)
	if _, err := FetchAIModels(context.Background(), AIConfigInput{
		Mode:       "cli",
		CLICommand: codexShim,
	}); err == nil || !strings.Contains(err.Error(), "未找到 Codex 本地模型缓存") {
		t.Fatalf("expected missing codex cache error, got: %v", err)
	}
	if err := os.WriteFile(filepath.Join(emptyCodexHome, "config.toml"), []byte("model = \"gpt-6.2-sol\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	models, err = FetchAIModels(context.Background(), AIConfigInput{
		Mode:       "cli",
		CLICommand: codexShim,
	})
	if err != nil || !slices.Equal(models, []string{"gpt-6.2-sol"}) {
		t.Fatalf("expected config.toml fallback model, got=%v err=%v", models, err)
	}
}

func TestFetchAIModelsCLIClaudeLiveAndAliases(t *testing.T) {
	isolated(t)
	var gotKey, gotVer, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVer = r.Header.Get("anthropic-version")
		gotPath = r.URL.Path
		io.WriteString(w, `{"data":[{"id":"claude-sonnet-5-5"},{"id":"claude-opus-5-5"}]}`)
	}))
	defer srv.Close()

	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("PATH", t.TempDir())

	settingsBody := `{"model":"claude-custom-pin","env":{"ANTHROPIC_BASE_URL":"` + srv.URL + `","ANTHROPIC_API_KEY":"sk-ant-cli-test"}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settingsBody), 0600); err != nil {
		t.Fatal(err)
	}

	models, err := FetchAIModels(context.Background(), AIConfigInput{
		Mode:       "cli",
		CLICommand: "claude",
	})
	if err != nil {
		t.Fatalf("FetchAIModels claude failed: %v", err)
	}
	if gotPath != "/v1/models" || gotKey != "sk-ant-cli-test" || gotVer != "2023-06-01" {
		t.Fatalf("unexpected Anthropic request from claude CLI: path=%q key=%q ver=%q", gotPath, gotKey, gotVer)
	}
	want := []string{"claude-custom-pin", "claude-sonnet-5-5", "claude-opus-5-5", "sonnet", "opus", "haiku"}
	if !slices.Equal(models, want) {
		t.Fatalf("claude models mismatch: got=%v want=%v", models, want)
	}

	// OAuth-only Claude config (no API key) still returns configured model + official aliases.
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"model":"opus"}`), 0600); err != nil {
		t.Fatal(err)
	}
	models, err = FetchAIModels(context.Background(), AIConfigInput{
		Mode:       "cli",
		CLICommand: "claude",
	})
	if err != nil || !slices.Equal(models, []string{"opus", "sonnet", "haiku"}) {
		t.Fatalf("claude OAuth fallback mismatch: got=%v err=%v", models, err)
	}
}

func TestFetchAIModelsCLIShimAndErrors(t *testing.T) {
	isolated(t)

	// Mock .cmd shim outputting TSV model list just like `agy models`.
	shimDir := t.TempDir()
	tsvShim := filepath.Join(shimDir, "mock-models.cmd")
	tsvBody := strings.Join([]string{
		"@echo off",
		"if \"%~1\"==\"models\" (",
		"  echo Fetching available models... 1>&2",
		"  echo gemini-4-neon-high\tGemini 4 Neon ^(High^)",
		"  echo claude-sonnet-5-5-high\tClaude Sonnet 5.5 ^(High^)",
		"  echo gemini-4-neon-high\tDuplicate",
		"  exit /b 0",
		")",
		"exit /b 1",
		"",
	}, "\r\n")
	if err := os.WriteFile(tsvShim, []byte(tsvBody), 0600); err != nil {
		t.Fatal(err)
	}

	models, err := FetchAIModels(context.Background(), AIConfigInput{
		Mode:       "cli",
		CLICommand: tsvShim,
	})
	if err != nil {
		t.Fatalf("FetchAIModels TSV shim failed: %v", err)
	}
	if !slices.Equal(models, []string{"gemini-4-neon-high", "claude-sonnet-5-5-high"}) {
		t.Fatalf("TSV shim models mismatch: got=%v", models)
	}

	// Same shim named agy.cmd exercises the dedicated `agy` branch.
	agyShim := filepath.Join(shimDir, "agy.cmd")
	if err := os.WriteFile(agyShim, []byte(tsvBody), 0600); err != nil {
		t.Fatal(err)
	}
	agyModels, err := FetchAIModels(context.Background(), AIConfigInput{
		Mode:       "cli",
		CLICommand: agyShim,
	})
	if err != nil || !slices.Equal(agyModels, []string{"gemini-4-neon-high", "claude-sonnet-5-5-high"}) {
		t.Fatalf("agy shim models mismatch: got=%v err=%v", agyModels, err)
	}

	// Unsupported custom shim printing help screen returns clean error.
	helpShim := filepath.Join(shimDir, "no-models.cmd")
	helpBody := "@echo off\r\necho Usage: no-models [options]\r\necho Options:\r\necho   --help  show help\r\n"
	if err := os.WriteFile(helpShim, []byte(helpBody), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchAIModels(context.Background(), AIConfigInput{
		Mode:       "cli",
		CLICommand: helpShim,
	}); err == nil || !strings.Contains(err.Error(), "暂不支持自动读取模型列表") {
		t.Fatalf("expected unsupported custom CLI error, got: %v", err)
	}

	// Gemini CLI shim without `models` subcommand falls back cleanly to official presets.
	geminiShim := filepath.Join(shimDir, "gemini.cmd")
	if err := os.WriteFile(geminiShim, []byte("@echo off\r\nexit /b 1\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	geminiModels, err := FetchAIModels(context.Background(), AIConfigInput{
		Mode:       "cli",
		CLICommand: geminiShim,
	})
	if err != nil || !slices.Equal(geminiModels, []string{"gemini-2.5-pro", "gemini-2.5-flash"}) {
		t.Fatalf("gemini preset fallback mismatch: got=%v err=%v", geminiModels, err)
	}

	// LOCALAPPDATA fallback for `agy` resolves %LOCALAPPDATA%\agy\bin\agy.exe when not on PATH.
	fakeLocalAppData := t.TempDir()
	fakeAgyExe := filepath.Join(fakeLocalAppData, "agy", "bin", "agy.exe")
	if err := os.MkdirAll(filepath.Dir(fakeAgyExe), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakeAgyExe, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("LOCALAPPDATA", fakeLocalAppData)
	if resolved, err := resolveCLIBinary("agy"); err != nil || resolved != fakeAgyExe {
		t.Fatalf("expected LOCALAPPDATA agy fallback %q, got %q err=%v", fakeAgyExe, resolved, err)
	}

	// Missing CLI error cases (isolate PATH, LOCALAPPDATA, CODEX_HOME, CLAUDE_CONFIG_DIR).
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	if _, err := FetchAIModels(context.Background(), AIConfigInput{Mode: "cli"}); err == nil || !strings.Contains(err.Error(), "请先选择或填写本地 AI CLI 命令") {
		t.Fatalf("expected empty CLI error, got: %v", err)
	}
	if _, err := FetchAIModels(context.Background(), AIConfigInput{Mode: "cli", CLICommand: "agy"}); err == nil || !strings.Contains(err.Error(), "Antigravity CLI") {
		t.Fatalf("expected missing agy error, got: %v", err)
	}
	if _, err := FetchAIModels(context.Background(), AIConfigInput{Mode: "cli", CLICommand: "codex"}); err == nil || !strings.Contains(err.Error(), "Codex CLI") {
		t.Fatalf("expected missing codex error, got: %v", err)
	}
	if _, err := FetchAIModels(context.Background(), AIConfigInput{Mode: "cli", CLICommand: "claude"}); err == nil || !strings.Contains(err.Error(), "Claude Code") {
		t.Fatalf("expected missing claude error, got: %v", err)
	}
	if _, err := FetchAIModels(context.Background(), AIConfigInput{Mode: "cli", CLICommand: "nonexistent-cli-xyz"}); err == nil || !strings.Contains(err.Error(), "未在本机 PATH 中找到命令") {
		t.Fatalf("expected missing custom CLI error, got: %v", err)
	}
}

func TestCompletionURLNormalization(t *testing.T) {
	cases := []struct {
		endpoint string
		want     string
	}{
		// OpenAI
		{"https://api.openai.com", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/v1", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/v1/", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/v1/chat/completions", "https://api.openai.com/v1/chat/completions"},
		// DeepSeek
		{"https://api.deepseek.com", "https://api.deepseek.com/v1/chat/completions"},
		{"https://api.deepseek.com/", "https://api.deepseek.com/v1/chat/completions"},
		{"https://api.deepseek.com/v1", "https://api.deepseek.com/v1/chat/completions"},
		{"https://api.deepseek.com/chat/completions", "https://api.deepseek.com/chat/completions"},
		// Anthropic
		{"https://api.anthropic.com", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com/", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com/v1", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com/v1/", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com/v1/chat/completions", "https://api.anthropic.com/v1/messages"},
		// Gemini OpenAI-compatible
		{"https://generativelanguage.googleapis.com", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"},
		{"https://generativelanguage.googleapis.com/v1beta", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"},
		{"https://generativelanguage.googleapis.com/v1beta/", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"},
		{"https://generativelanguage.googleapis.com/v1beta/openai", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"},
		{"https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"},
		// Ollama & custom relays
		{"http://localhost:11434/v1", "http://localhost:11434/v1/chat/completions"},
		{"http://127.0.0.1:11434/v1/chat/completions", "http://127.0.0.1:11434/v1/chat/completions"},
		{"https://relay.example.com/v1", "https://relay.example.com/v1/chat/completions"},
		{"https://relay.example.com/v1/chat/completions", "https://relay.example.com/v1/chat/completions"},
		{"https://relay.example.com/v1/messages", "https://relay.example.com/v1/messages"},
	}
	for _, tc := range cases {
		got, err := completionURL(AISettings{Endpoint: tc.endpoint, Model: "test-model"})
		if err != nil {
			t.Fatalf("completionURL(%q) unexpected error: %v", tc.endpoint, err)
		}
		if got != tc.want {
			t.Fatalf("completionURL(%q) = %q, want %q", tc.endpoint, got, tc.want)
		}
	}
}

func TestAnthropicNativeAPIFlowsAndErrors(t *testing.T) {
	isolated(t)
	const secret = "sk-ant-test-secret-998877"

	var lastSystem string
	var mode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected Anthropic request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-api-key") != secret || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Anthropic headers: x-api-key=%q ver=%q auth=%q",
				r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"), r.Header.Get("Authorization"))
		}
		var reqBody struct {
			Model     string    `json:"model"`
			System    string    `json:"system"`
			Messages  []message `json:"messages"`
			MaxTokens int       `json:"max_tokens"`
			Stream    bool      `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("invalid Anthropic JSON body: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if reqBody.MaxTokens != 4096 || len(reqBody.Messages) == 0 {
			t.Errorf("expected max_tokens=4096 and non-empty messages, got %+v", reqBody)
		}
		for _, m := range reqBody.Messages {
			if m.Role == "system" {
				t.Errorf("system role must be lifted to top-level system field, found in messages: %+v", reqBody.Messages)
			}
		}
		lastSystem = reqBody.System

		switch mode {
		case "test":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn"}`)
		case "suggest":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg_2","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"journalctl -u "},{"type":"text","text":"nginx -n 50"}],"stop_reason":"end_turn"}`)
		case "structured-sse":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_3\"}}\n\n")
			io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"secret thought\"}}\n\n")
			io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"{\\\"command\\\":\\\"Get-Process | Sort-Object CPU -Descending | Select-Object -First 5\\\",\"}}\n\n")
			io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"\\\"explanation\\\":\\\"查看 CPU 占用前五的进程\\\",\\\"risk\\\":\\\"safe\\\"}\"}}\n\n")
			io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		case "explain-sse-error":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Anthropic servers overloaded\"}}\n\n")
		case "http-400":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"model: claude-missing not found for `+secret+`"}}`)
		}
	}))
	defer srv.Close()
	redirectAIClientToServer(t, srv)

	cfg := AISettings{
		Endpoint: "https://api.anthropic.com/v1",
		Model:    "claude-sonnet-4-5",
		APIKey:   secret,
	}
	if err := SaveAI(cfg); err != nil {
		t.Fatal(err)
	}

	// 1. TestAI against https://api.anthropic.com/v1
	mode = "test"
	if err := TestAI(context.Background(), cfg); err != nil {
		t.Fatalf("Anthropic TestAI failed: %v", err)
	}

	// 2. Suggest non-streaming JSON (multi-block text + thinking ignored)
	mode = "suggest"
	cmd, err := Suggest(context.Background(), "查看 nginx 最近日志")
	if err != nil || cmd != "journalctl -u nginx -n 50" {
		t.Fatalf("Anthropic Suggest mismatch: cmd=%q err=%v", cmd, err)
	}
	if lastSystem == "" {
		t.Fatal("expected Anthropic top-level system prompt to be sent")
	}

	// 3. SuggestStructured SSE streaming + Windows osHint adaptation
	mode = "structured-sse"
	var streamed strings.Builder
	res, err := SuggestStructured(context.Background(), "查看 CPU 最高进程", "Windows 11 PowerShell", func(d string) {
		streamed.WriteString(d)
	})
	if err != nil {
		t.Fatalf("Anthropic SuggestStructured failed: %v", err)
	}
	if res.Command != "Get-Process | Sort-Object CPU -Descending | Select-Object -First 5" || res.Risk != "safe" {
		t.Fatalf("unexpected Anthropic structured result: %+v", res)
	}
	if strings.Contains(streamed.String(), "secret thought") || streamed.Len() == 0 {
		t.Fatalf("unexpected Anthropic SSE deltas: %q", streamed.String())
	}
	if !strings.Contains(lastSystem, "Windows PowerShell") || strings.Contains(lastSystem, "POSIX") {
		t.Fatalf("expected Windows system prompt adaptation, got: %q", lastSystem)
	}

	// 4. ExplainStream SSE mid-stream error event
	mode = "explain-sse-error"
	if _, err := ExplainStream(context.Background(), "exit status 1", "why?", nil); err == nil || !strings.Contains(err.Error(), "Anthropic servers overloaded") {
		t.Fatalf("expected Anthropic SSE error propagation, got: %v", err)
	}

	// 5. HTTP 400 error extraction + API key redaction
	mode = "http-400"
	_, err = Suggest(context.Background(), "ping")
	if err == nil || !strings.Contains(err.Error(), "model: claude-missing not found") || strings.Contains(err.Error(), secret) {
		t.Fatalf("expected sanitized Anthropic HTTP 400 detail without secret, got: %v", err)
	}
}

func TestAnthropicAgentLoopNativeToolUse(t *testing.T) {
	isolated(t)
	const secret = "sk-ant-agent-key-12345"
	var turn int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn++
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != secret || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
			t.Errorf("turn %d: unexpected Anthropic agent headers/path: path=%q key=%q ver=%q auth=%q",
				turn, r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"), r.Header.Get("Authorization"))
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode turn %d: %v", turn, err)
		}
		toolsList, ok := req["tools"].([]any)
		if !ok || len(toolsList) != 1 {
			t.Fatalf("turn %d: expected 1 Anthropic tool, got %#v", turn, req["tools"])
		}
		tool0 := toolsList[0].(map[string]any)
		if _, hasInputSchema := tool0["input_schema"]; !hasInputSchema {
			t.Fatalf("turn %d: Anthropic tool missing input_schema: %#v", turn, tool0)
		}
		if _, hasFunc := tool0["function"]; hasFunc {
			t.Fatalf("turn %d: Anthropic tool must not use OpenAI function wrapper: %#v", turn, tool0)
		}

		w.Header().Set("Content-Type", "application/json")
		switch turn {
		case 1:
			io.WriteString(w, `{
				"id":"msg_a1","type":"message","role":"assistant","stop_reason":"tool_use",
				"content":[
					{"type":"text","text":"先检查磁盘空间和拒绝测试"},
					{"type":"tool_use","id":"toolu_ok","name":"shell_exec","input":{"command":"df -h"}},
					{"type":"tool_use","id":"toolu_err","name":"shell_exec","input":{"command":"rm -rf /"}}
				]
			}`)
		case 2:
			msgs := req["messages"].([]any)
			if len(msgs) != 3 {
				t.Fatalf("turn 2: expected 3 messages (user, assistant, user tool_results), got %d", len(msgs))
			}
			userTurn := msgs[2].(map[string]any)
			if userTurn["role"] != "user" {
				t.Fatalf("turn 2: expected role=user for tool_result message, got %v", userTurn["role"])
			}
			blocks := userTurn["content"].([]any)
			if len(blocks) != 2 {
				t.Fatalf("turn 2: expected 2 tool_result blocks, got %d", len(blocks))
			}
			b0 := blocks[0].(map[string]any)
			b1 := blocks[1].(map[string]any)
			if b0["type"] != "tool_result" || b0["tool_use_id"] != "toolu_ok" || !strings.Contains(b0["content"].(string), "/dev/sda1") {
				t.Fatalf("turn 2: unexpected first tool_result: %#v", b0)
			}
			if b1["type"] != "tool_result" || b1["tool_use_id"] != "toolu_err" || b1["is_error"] != true {
				t.Fatalf("turn 2: unexpected error tool_result: %#v", b1)
			}
			io.WriteString(w, `{
				"id":"msg_a2","type":"message","role":"assistant","stop_reason":"end_turn",
				"content":[{"type":"text","text":"根分区使用率 42%，危险命令已被安全策略拦截。"}]
			}`)
		default:
			t.Fatalf("unexpected extra turn %d", turn)
		}
	}))
	defer srv.Close()
	redirectAIClientToServer(t, srv)

	if err := SaveAI(AISettings{
		Endpoint: "https://api.anthropic.com",
		Model:    "claude-sonnet-4-5",
		APIKey:   secret,
	}); err != nil {
		t.Fatal(err)
	}

	var events []AgentStepEvent
	summary, err := RunAgentLoop(
		context.Background(),
		"检查磁盘状态",
		"prod-web (root@10.0.0.1:22)",
		nil,
		[]ToolDefinition{{
			Name:        "shell_exec",
			Description: "Execute shell command",
			Parameters:  map[string]any{"properties": map[string]any{"command": map[string]any{"type": "string"}}},
		}},
		func(_ context.Context, toolName string, args map[string]any) (string, string, string, int, error) {
			if args["command"] == "rm -rf /" {
				return "", "danger", "deny", 0, errors.New("policy denied")
			}
			return "/dev/sda1 42% /", "safe", "allow", 0, nil
		},
		func(ev AgentStepEvent) {
			events = append(events, ev)
		},
	)
	if err != nil {
		t.Fatalf("Anthropic RunAgentLoop failed: %v", err)
	}
	if !strings.Contains(summary, "42%") {
		t.Fatalf("unexpected Anthropic agent summary: %q", summary)
	}
	if turn != 2 {
		t.Fatalf("expected 2 Anthropic turns, got %d", turn)
	}
	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	wantKinds := []string{"thought", "tool_call", "tool_result", "tool_call", "tool_result", "final"}
	if !slices.Equal(kinds, wantKinds) {
		t.Fatalf("unexpected Anthropic agent step sequence: got=%v want=%v", kinds, wantKinds)
	}
}

func TestOpenAIReasoningContentBlocksErrorsAndAgentLoop(t *testing.T) {
	isolated(t)
	const secret = "sk-openai-live-987654321"
	var mode string
	var agentTurn int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "think-sse":
			w.Header().Set("Content-Type", "text/event-stream")
			// Reasoning block + prose with ${HOME} & unclosed { + JSON containing inner {print $1} braces.
			chunks := []string{
				"<think>先想想 {draft} 和 {\"command\":\"wrong\"}</think>\n",
				"提示：路径在 ${HOME}，未闭合 { 忽略：\n```json\n",
				"{\"command\":\"awk '{print $1}' /etc/hosts\",\"explanation\":\"打印 hosts 首列\",\"risk\":\"safe\"}\n```",
			}
			for _, c := range chunks {
				b, _ := json.Marshal(map[string]any{
					"choices": []any{map[string]any{"delta": map[string]any{"content": c}}},
				})
				io.WriteString(w, "data: "+string(b)+"\n\n")
			}
			io.WriteString(w, "data: [DONE]\n\n")
		case "content-array":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":[{"type":"text","text":"uname "},{"type":"text","text":"-a"}]},"finish_reason":"stop"}]}`)
		case "http-400":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"The model 'gpt-nope' does not exist for key `+secret+`"}}`)
		case "http-429-long":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"`+strings.Repeat("配额不足错误说明", 50)+`"}}`)
		case "agent":
			agentTurn++
			w.Header().Set("Content-Type", "application/json")
			if agentTurn == 1 {
				io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"<think>thinking...</think>检查负载","tool_calls":[{"id":"call_1","type":"function","function":{"name":"shell_exec","arguments":"{\"command\":\"uptime\"}"}}]}}]}`)
			} else {
				io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":[{"type":"text","text":"<think>ok</think>系统负载 0.15，状态正常。"}]}}]}`)
			}
		case "agent-400":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Tools are not supported by this model"}}`)
		}
	}))
	defer srv.Close()

	if err := SaveAI(AISettings{Endpoint: srv.URL + "/v1", Model: "deepseek-r1", APIKey: secret}); err != nil {
		t.Fatal(err)
	}

	// 1. Streaming reasoning <think> + prose braces + balanced JSON extraction
	mode = "think-sse"
	res, err := SuggestStructured(context.Background(), "打印 hosts 首列", "Ubuntu 24.04", nil)
	if err != nil {
		t.Fatalf("SuggestStructured think-sse failed: %v", err)
	}
	if res.Command != "awk '{print $1}' /etc/hosts" || res.Explanation != "打印 hosts 首列" || res.Risk != "safe" {
		t.Fatalf("unexpected reasoning AIResult: %+v", res)
	}

	// 2. Content block array in OpenAI response
	mode = "content-array"
	cmd, err := Suggest(context.Background(), "查看内核版本")
	if err != nil || cmd != "uname -a" {
		t.Fatalf("content-array Suggest mismatch: cmd=%q err=%v", cmd, err)
	}

	// 3. HTTP 400 error body extraction & secret redaction
	mode = "http-400"
	_, err = Suggest(context.Background(), "test")
	if err == nil || !strings.Contains(err.Error(), "The model 'gpt-nope' does not exist") || strings.Contains(err.Error(), secret) {
		t.Fatalf("expected sanitized HTTP 400 detail, got: %v", err)
	}

	// 4. Long error body truncation (<=220 runes detail)
	mode = "http-429-long"
	_, err = Suggest(context.Background(), "test")
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") || !strings.Contains(err.Error(), "…") || len([]rune(err.Error())) > 280 {
		t.Fatalf("expected truncated HTTP 429 error, got: %v", err)
	}

	// 5. OpenAI Agent loop with tool call + content array + <think> stripping
	mode = "agent"
	agentTurn = 0
	var events []AgentStepEvent
	summary, err := RunAgentLoop(
		context.Background(),
		"看下负载",
		"node-1",
		nil,
		[]ToolDefinition{{Name: "shell_exec", Description: "run", Parameters: map[string]any{"type": "object"}}},
		func(_ context.Context, _ string, _ map[string]any) (string, string, string, int, error) {
			return "load average: 0.15", "safe", "allow", 0, nil
		},
		func(ev AgentStepEvent) { events = append(events, ev) },
	)
	if err != nil || summary != "系统负载 0.15，状态正常。" {
		t.Fatalf("OpenAI RunAgentLoop mismatch: summary=%q err=%v", summary, err)
	}
	if len(events) < 4 || events[0].Kind != "thought" || events[0].Output != "检查负载" {
		t.Fatalf("unexpected OpenAI agent events: %+v", events)
	}

	// 6. OpenAI Agent HTTP 400 surfaces upstream detail
	mode = "agent-400"
	if _, err := RunAgentLoop(context.Background(), "test", "", nil, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "Tools are not supported by this model") {
		t.Fatalf("expected agent HTTP 400 detail, got: %v", err)
	}

	// 7. CLI Agent loop with <think> + prose braces + balanced JSON extraction
	cliDir := t.TempDir()
	counterFile := filepath.Join(cliDir, "turn.txt")
	cliAgentShim := filepath.Join(cliDir, "cli-agent.cmd")
	cliAgentBody := strings.Join([]string{
		"@echo off",
		"if exist \"" + counterFile + "\" (",
		"  echo ^<think^>done {tmp}^</think^> {\"final\":\"CLI Agent 检查完成，端口 22 正常监听。\"}",
		") else (",
		"  echo 1>\"" + counterFile + "\"",
		"  echo ^<think^>plan {x}^</think^> 提示 ${HOME} {\"thought\":\"先查端口\",\"tool\":\"shell_exec\",\"arguments\":{\"command\":\"ss -tlnp\"}}",
		")",
		"exit /b 0",
		"",
	}, "\r\n")
	if err := os.WriteFile(cliAgentShim, []byte(cliAgentBody), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveAI(AISettings{Mode: "cli", CLICommand: cliAgentShim}); err != nil {
		t.Fatal(err)
	}
	cliSummary, err := RunAgentLoop(
		context.Background(),
		"检查 22 端口",
		"prod-1",
		nil,
		[]ToolDefinition{{Name: "shell_exec", Description: "run", Parameters: map[string]any{"type": "object"}}},
		func(_ context.Context, toolName string, args map[string]any) (string, string, string, int, error) {
			if toolName != "shell_exec" || args["command"] != "ss -tlnp" {
				t.Fatalf("unexpected CLI agent tool call: %s %v", toolName, args)
			}
			return "LISTEN 0 128 *:22", "safe", "allow", 0, nil
		},
		nil,
	)
	if err != nil || !strings.Contains(cliSummary, "端口 22 正常监听") {
		t.Fatalf("CLI RunAgentLoop failed: summary=%q err=%v", cliSummary, err)
	}
}

func TestLocalAICLIZeroHistoryFlagsEnvAndLegacyFallback(t *testing.T) {
	isolated(t)

	// 1. Verify codex zero-history config override detection & deduplication.
	if !hasCodexConfigOverride([]string{"-c", `history.persistence="none"`}, "history.persistence") {
		t.Fatal("expected -c history.persistence=... to match")
	}
	if !hasCodexConfigOverride([]string{`--config=notify=["bell"]`}, "notify") {
		t.Fatal("expected --config=notify=... to match")
	}
	if hasCodexConfigOverride([]string{"-c", `model="o3"`}, "notify") {
		t.Fatal("did not expect unrelated -c key to match notify")
	}
	_, codexArgs, _ := buildCLICommandArgs(AISettings{Mode: "cli", CLICommand: "codex"}, "hi")
	if !slices.Contains(codexArgs, "--ephemeral") || !slices.Contains(codexArgs, `history.persistence="none"`) || !slices.Contains(codexArgs, "notify=[]") {
		t.Fatalf("codex missing zero-history args: %v", codexArgs)
	}

	// 2. Verify claude injects --no-session-persistence, sets CLAUDE_CODE_SKIP_PROMPT_HISTORY=1,
	// and automatically retries once without --no-session-persistence on legacy CLI error.
	shimDir := t.TempDir()
	claudeShim := filepath.Join(shimDir, "claude.cmd")
	claudeBody := strings.Join([]string{
		"@echo off",
		"setlocal",
		"if not \"%CLAUDE_CODE_SKIP_PROMPT_HISTORY%\"==\"1\" (",
		"  echo missing skip env 1>&2",
		"  exit /b 2",
		")",
		"echo %* | findstr /C:\"no-session-persistence\" >nul",
		"if %errorlevel%==0 (",
		"  echo error: unknown option '--no-session-persistence' 1>&2",
		"  exit /b 1",
		")",
		"echo LEGACY_CLAUDE_OK",
		"exit /b 0",
		"",
	}, "\r\n")
	if err := os.WriteFile(claudeShim, []byte(claudeBody), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := runLocalAICLI(context.Background(), AISettings{Mode: "cli", CLICommand: claudeShim}, "", "ping", nil)
	if err != nil || got != "LEGACY_CLAUDE_OK" {
		t.Fatalf("expected legacy claude fallback with skip-history env to succeed, got=%q err=%v", got, err)
	}
}

func TestRunLocalShellCommandArgsAndExecution(t *testing.T) {
	// Verify per-shell argument builders.
	_, pwshArgs, isCmd := buildLocalShellExecArgs("pwsh.exe", "pwsh", []string{"-NoLogo"}, "Get-Date")
	if isCmd || len(pwshArgs) != 5 || pwshArgs[0] != "-NoLogo" || pwshArgs[3] != "-Command" || !strings.Contains(pwshArgs[4], "UTF8") || !strings.Contains(pwshArgs[4], "Get-Date") {
		t.Fatalf("unexpected pwsh exec args: %v isCmd=%v", pwshArgs, isCmd)
	}
	_, cmdArgs, isCmd := buildLocalShellExecArgs("cmd.exe", "cmd", nil, "echo hi")
	if !isCmd || !slices.Equal(cmdArgs, []string{"/d", "/s", "/c", "echo hi"}) {
		t.Fatalf("unexpected cmd exec args: %v isCmd=%v", cmdArgs, isCmd)
	}
	_, bashArgs, isCmd := buildLocalShellExecArgs("bash.exe", "git-bash", []string{"--login", "-i"}, "uname -a")
	if isCmd || !slices.Equal(bashArgs, []string{"-lc", "uname -a"}) {
		t.Fatalf("unexpected bash exec args: %v", bashArgs)
	}
	_, wslArgs, isCmd := buildLocalShellExecArgs("wsl.exe", "wsl:Ubuntu", []string{"-d", "Ubuntu", "--cd", "~"}, "ls -la")
	if isCmd || !slices.Equal(wslArgs, []string{"-d", "Ubuntu", "--", "sh", "-lc", "ls -la"}) {
		t.Fatalf("unexpected wsl exec args: %v", wslArgs)
	}

	// Execute real hidden cmd.exe command including quoted args & exit code propagation.
	stdout, stderr, code, _, err := RunLocalShellCommand(context.Background(), "cmd.exe", "cmd", nil, t.TempDir(), `echo "HELLO_LOCAL_SHELL" && echo ERR_LINE 1>&2`)
	if err != nil || code != 0 || !strings.Contains(stdout, "HELLO_LOCAL_SHELL") || !strings.Contains(stderr, "ERR_LINE") {
		t.Fatalf("RunLocalShellCommand failed: stdout=%q stderr=%q code=%d err=%v", stdout, stderr, code, err)
	}
	_, _, code, _, err = RunLocalShellCommand(context.Background(), "cmd.exe", "cmd", nil, "", "exit /b 7")
	if err != nil || code != 7 {
		t.Fatalf("expected non-zero exit code 7 without transport error, got code=%d err=%v", code, err)
	}
}

func TestRunAgentLoopMultiTurnHistoryProtocols(t *testing.T) {
	isolated(t)

	// 1. Anthropic alternation sanitization (leading assistant + consecutive user turns).
	var gotAnthropicRoles []string
	var gotFirstContent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode failed: %v", err)
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		for _, m := range req.Messages {
			gotAnthropicRoles = append(gotAnthropicRoles, m.Role)
		}
		if len(req.Messages) > 0 {
			gotFirstContent = req.Messages[0].Content
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_hist","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"好的，继续排查。"}]}`)
	}))
	defer srv.Close()
	redirectAIClientToServer(t, srv)

	if err := SaveAI(AISettings{Endpoint: "https://api.anthropic.com/v1", Model: "claude-sonnet-5-5", APIKey: "sk-ant-hist"}); err != nil {
		t.Fatal(err)
	}
	history := []AgentTurn{
		{Role: "assistant", Content: "孤立的开头助手消息应被跳过"},
		{Role: "user", Content: "第一轮提问"},
		{Role: "user", Content: "连续追问"},
		{Role: "assistant", Content: "第一轮答复"},
	}
	res, err := RunAgentLoop(context.Background(), "第二轮任务", "node-1", history, nil, nil, nil)
	if err != nil || !strings.Contains(res, "继续排查") {
		t.Fatalf("Anthropic multi-turn failed: res=%q err=%v", res, err)
	}
	if !slices.Equal(gotAnthropicRoles, []string{"user", "assistant", "user"}) {
		t.Fatalf("expected strictly alternating roles [user assistant user], got %v", gotAnthropicRoles)
	}
	if !strings.Contains(gotFirstContent, "第一轮提问") || !strings.Contains(gotFirstContent, "连续追问") {
		t.Fatalf("expected merged consecutive user turns, got %q", gotFirstContent)
	}

	// 2. CLI mode includes trimmed history context in stdin prompt.
	shimDir := t.TempDir()
	stdinDump := filepath.Join(shimDir, "stdin.txt")
	cliHistShim := filepath.Join(shimDir, "cli-hist.cmd")
	cliHistBody := "@echo off\r\nfindstr \"^\" >\"" + stdinDump + "\"\r\necho {\"final\":\"CLI 多轮完成\"}\r\n"
	if err := os.WriteFile(cliHistShim, []byte(cliHistBody), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveAI(AISettings{Mode: "cli", CLICommand: cliHistShim}); err != nil {
		t.Fatal(err)
	}
	cliRes, err := RunAgentLoop(context.Background(), "继续查看日志", "prod-2", []AgentTurn{
		{Role: "user", Content: "查下 nginx 状态"},
		{Role: "assistant", Content: "nginx 正在运行"},
	}, nil, nil, nil)
	if err != nil || !strings.Contains(cliRes, "CLI 多轮完成") {
		t.Fatalf("CLI multi-turn failed: res=%q err=%v", cliRes, err)
	}
	dumped, err := os.ReadFile(stdinDump)
	if err != nil || !strings.Contains(string(dumped), "近期对话上下文") || !strings.Contains(string(dumped), "查下 nginx 状态") {
		t.Fatalf("expected CLI prompt to contain recent history context, got=%q err=%v", string(dumped), err)
	}
}
