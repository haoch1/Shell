package agentbridge

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const initializeRequest = `{"jsonrpc":"2.0","id":9007199254740993,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`
const discoveryMessages = initializeRequest + "\n" +
	`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
	`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"

func TestStdioDiscoveryReturnsCorrelatedErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"empty", http.StatusAccepted, ""},
		{"wrong response ID", http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{}}`},
		{"truncated JSON", http.StatusOK, "{"},
		{"rotated token unavailable", http.StatusUnauthorized, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			var out bytes.Buffer
			err := ProxyStdioToHTTP(strings.NewReader(initializeRequest+"\n"), &out, func() (Endpoint, error) { return Endpoint{URL: srv.URL, Token: "unchanged"}, nil })
			if err != nil {
				t.Fatal(err)
			}
			var resp rpcResponse
			if err := json.Unmarshal(out.Bytes(), &resp); err != nil || string(resp.ID) != "9007199254740993" || resp.Error == nil || attempts.Load() != 1 {
				t.Fatalf("attempts=%d response=%q err=%v", attempts.Load(), out.String(), err)
			}
		})
	}
}

func TestDiscoveryRotationAndWireFormat(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if r.Header.Get("Accept") != "application/json, text/event-stream" {
			t.Error("missing Accept negotiation")
		}
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "{\n  \"jsonrpc\":\"2.0\", \"id\":9007199254740993, \"result\":{}\n}\n")
	}))
	defer srv.Close()
	resolveCount := 0
	var out bytes.Buffer
	err := ProxyStdioToHTTP(strings.NewReader(initializeRequest+"\n"), &out, func() (Endpoint, error) {
		resolveCount++
		if resolveCount == 1 {
			return Endpoint{URL: srv.URL, Token: "old"}, nil
		}
		return Endpoint{URL: srv.URL, Token: "new"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil || resp.Error != nil || string(resp.ID) != "9007199254740993" || bytes.Count(out.Bytes(), []byte("\n")) != 1 || attempts.Load() != 2 {
		t.Fatalf("attempts=%d, response=%q, error=%v", attempts.Load(), out.String(), err)
	}
}

func TestDiscoveryDeadlineDoesNotReplayTimedOutRequest(t *testing.T) {
	var attempts atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		<-release
	}))
	defer srv.Close()
	defer close(release)
	var out bytes.Buffer
	err := proxyStdio(strings.NewReader(initializeRequest+"\n"), &out, func() (Endpoint, error) { return Endpoint{URL: srv.URL, Token: "test"}, nil }, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil || resp.Error == nil || attempts.Load() != 1 {
		t.Fatalf("attempts=%d, response=%q, error=%v", attempts.Load(), out.String(), err)
	}
}

func TestStdioDoesNotReplayAnAmbiguousToolRequest(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer srv.Close()
	const request = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fixture"}}`
	var out bytes.Buffer
	err := ProxyStdioToHTTP(strings.NewReader(request+"\n"), &out, func() (Endpoint, error) { return Endpoint{URL: srv.URL, Token: "fixture"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 1 || !strings.Contains(out.String(), `"error":`) {
		t.Fatalf("attempts=%d response=%q", attempts.Load(), out.String())
	}
}

func TestBridgeStatusWhileStopping(t *testing.T) {
	started, draining, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.Config.RegisterOnShutdown(func() { close(draining) })
	srv.Start()
	defer srv.Close()
	defer close(release)
	bridge := &Server{srv: srv.Config, port: 12345}
	go func() {
		resp, err := srv.Client().Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
	}()
	<-started
	go bridge.Stop()
	<-draining
	stopped := make(chan bool, 1)
	go func() { running, _ := bridge.Running(); stopped <- !running }()
	select {
	case ok := <-stopped:
		if !ok {
			t.Fatal("bridge still advertises a draining listener")
		}
	case <-time.After(time.Second):
		t.Fatal("status blocked by active HTTP handler during shutdown")
	}
}

func BenchmarkStdioDiscovery(b *testing.B) {
	bridge := New(stubHandler{}, "fixture-version")
	bridge.SetToken("fixture")
	srv := httptest.NewServer(http.HandlerFunc(bridge.handleMCP))
	defer srv.Close()
	benchmarkStdioDiscovery(b, Endpoint{URL: srv.URL, Token: "fixture"})
}

func benchmarkStdioDiscovery(b *testing.B, endpoint Endpoint) {
	resolve := func() (Endpoint, error) { return endpoint, nil }
	var initial bytes.Buffer
	if err := ProxyStdioToHTTP(strings.NewReader(discoveryMessages), &initial, resolve); err != nil || !strings.Contains(initial.String(), "shell_ping") {
		b.Fatalf("discovery failed before timing: %v %s", err, initial.String())
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := ProxyStdioToHTTP(strings.NewReader(discoveryMessages), io.Discard, resolve); err != nil {
			b.Fatal(err)
		}
	}
}

func TestRealBridgeDiscoveryPreservesLargeID(t *testing.T) {
	bridge := New(stubHandler{}, "fixture-version")
	bridge.SetToken("fixture")
	srv := httptest.NewServer(http.HandlerFunc(bridge.handleMCP))
	defer srv.Close()
	var out bytes.Buffer
	err := ProxyStdioToHTTP(strings.NewReader(discoveryMessages), &out, func() (Endpoint, error) { return Endpoint{URL: srv.URL, Token: "fixture"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected exactly two responses, got %q", out.String())
	}
	for i, want := range []string{"9007199254740993", "2"} {
		var response rpcResponse
		if err := json.Unmarshal(lines[i], &response); err != nil || response.Error != nil || string(response.ID) != want {
			t.Fatalf("unexpected discovery response: %s", lines[i])
		}
	}
}
