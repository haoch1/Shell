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
	"path/filepath"
	"slices"
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
