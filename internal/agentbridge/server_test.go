package agentbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
)

type stubHandler struct{}

func (stubHandler) ListTools() []ToolSpec {
	return []ToolSpec{{Name: "shell_ping", Description: "ping", InputSchema: map[string]any{"type": "object"}}}
}

func (stubHandler) CallTool(_ context.Context, source, _ string, toolName string, args map[string]any) ToolCallOutcome {
	return ToolCallOutcome{Text: fmt.Sprintf("%s:%s:%v", source, toolName, args["msg"])}
}

func TestMCPStreamableHTTPAuthOriginAndStdioProxy(t *testing.T) {
	probeLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	freePort := probeLn.Addr().(*net.TCPAddr).Port
	_ = probeLn.Close()

	srv := New(stubHandler{})
	const tok = "sh_unit_test_secret_token_0123456789"
	port, err := srv.Start(freePort, tok)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{}

	// 1. Unauthorized without Bearer token
	resp, err := client.Get(base + "/api/v1/health")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %v, %v", resp, err)
	}
	resp.Body.Close()

	// 2. Forbidden cross-site Origin (DNS rebinding guard)
	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", "https://evil.example.com")
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for remote Origin, got %v, %v", resp, err)
	}
	resp.Body.Close()

	// 3. MCP initialize + tools/list + tools/call via stdio proxy
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"shell_ping","arguments":{"msg":"hello"}}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	err = ProxyStdioToHTTP(strings.NewReader(input), &out, func() (string, string, error) {
		return base + "/mcp", tok, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 JSON-RPC responses (notification omitted), got %d:\n%s", len(lines), out.String())
	}
	var callResp struct {
		ID     int `json:"id"`
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &callResp); err != nil {
		t.Fatal(err)
	}
	if callResp.ID != 3 || len(callResp.Result.Content) != 1 || callResp.Result.Content[0].Text != "mcp:shell_ping:hello" {
		t.Fatalf("unexpected tools/call response: %s", lines[2])
	}
}
