package agentbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

func startPipeFixture(t testing.TB, handler http.Handler) string {
	t.Helper()
	ln, id, err := listenDesktopPipe()
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return id
}

func TestPipeDiscoveryAndAuthentication(t *testing.T) {
	bridge := New(stubHandler{}, "fixture-version")
	bridge.SetToken("test")
	var tcpRequests atomic.Int32
	tcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tcpRequests.Add(1)
		bridge.handleMCP(w, r)
	}))
	defer tcp.Close()
	ep := Endpoint{URL: tcp.URL, Token: "test", PipeID: startPipeFixture(t, http.HandlerFunc(bridge.handleMCP)), PID: os.Getpid()}
	run := func(ep Endpoint, wantError bool, wantTCP int32) {
		t.Helper()
		var out bytes.Buffer
		if err := ProxyStdioToHTTP(strings.NewReader(initializeRequest+"\n"), &out, func() (Endpoint, error) { return ep, nil }); err != nil {
			t.Fatal(err)
		}
		var response rpcResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil || string(response.ID) != "9007199254740993" || (response.Error != nil) != wantError || tcpRequests.Load() != wantTCP {
			t.Fatalf("error=%v tcp=%d response=%s", wantError, tcpRequests.Load(), out.String())
		}
	}
	run(ep, false, 0)
	bad := ep
	bad.Token = "wrong"
	run(bad, true, 0)
	bad = ep
	bad.PID++
	run(bad, true, 0)
	// A previously advertised pipe can disappear before net/http writes its request.
	ep.PipeID = strings.Repeat("0", 32)
	run(ep, false, 1)
}

func TestPipeBrokenAfterDeliveryDoesNotReplayToolOverTCP(t *testing.T) {
	var pipeRequests, tcpRequests atomic.Int32
	tcp := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { tcpRequests.Add(1) }))
	defer tcp.Close()
	ep := Endpoint{URL: tcp.URL, Token: "test", PID: os.Getpid(), PipeID: startPipeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pipeRequests.Add(1)
		c, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		c.Close()
	}))}
	const call = "{\"jsonrpc\":\"2.0\",\"method\":\"tools/call\",\"id\":1}\n"
	var out bytes.Buffer
	if err := ProxyStdioToHTTP(strings.NewReader(call), &out, func() (Endpoint, error) { return ep, nil }); err != nil {
		t.Fatal(err)
	}
	if pipeRequests.Load() != 1 || tcpRequests.Load() != 0 || !strings.Contains(out.String(), "\"error\":") {
		t.Fatalf("pipe=%d tcp=%d response=%s", pipeRequests.Load(), tcpRequests.Load(), out.String())
	}
}

func TestDesktopStopReleasesPipe(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	bridge := New(stubHandler{}, "fixture-version")
	_, id, err := bridge.Start(port, "test")
	if err != nil || id == "" {
		t.Fatalf("pipe startup failed: %v", err)
	}
	defer bridge.Stop()
	ep := Endpoint{URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", port), Token: "test", PID: os.Getpid(), PipeID: id}
	var out bytes.Buffer
	if err := ProxyStdioToHTTP(strings.NewReader(discoveryMessages), &out, func() (Endpoint, error) { return ep, nil }); err != nil || !strings.Contains(out.String(), "shell_ping") {
		t.Fatalf("discovery failed: %v, %s", err, out.String())
	}
	bridge.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := winio.DialPipeContext(ctx, pipePrefix+id)
	if err == nil {
		conn.Close()
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pipe still present after shutdown: %v", err)
	}
}

func BenchmarkStdioDiscoveryPipe(b *testing.B) {
	bridge := New(stubHandler{}, "fixture-version")
	bridge.SetToken("fixture")
	ep := Endpoint{URL: "http://127.0.0.1:17822/mcp", Token: "fixture", PID: os.Getpid(), PipeID: startPipeFixture(b, http.HandlerFunc(bridge.handleMCP))}
	benchmarkStdioDiscovery(b, ep)
}
