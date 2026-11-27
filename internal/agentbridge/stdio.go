package agentbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const discoveryTimeout = 5 * time.Second
const maxRPCResponse = 4 << 20

// Endpoint is a snapshot of the authenticated local desktop bridge.
type Endpoint struct {
	URL, Token, PipeID string
	PID                int
}

func (e Endpoint) client() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:     e.dial,
			IdleConnTimeout: 90 * time.Second,
		},
		// The credentials belong to one local desktop endpoint.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// ProxyStdioToHTTP forwards newline-delimited JSON-RPC over the desktop's local HTTP transport.
func ProxyStdioToHTTP(in io.Reader, out io.Writer, resolve func() (Endpoint, error)) error {
	return proxyStdio(in, out, resolve, discoveryTimeout)
}

func proxyStdio(in io.Reader, out io.Writer, resolve func() (Endpoint, error), discoveryLimit time.Duration) error {
	scanner := bufioNewScanner(in)
	enc := json.NewEncoder(out)
	var endpoint Endpoint
	var client *http.Client
	setEndpoint := func(e Endpoint) {
		if client != nil {
			client.CloseIdleConnections()
		}
		endpoint = e
		client = e.client()
	}
	defer func() {
		if client != nil {
			client.CloseIdleConnections()
		}
	}()
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
		}
		if err := json.Unmarshal(line, &req); err != nil || req.Method == "" || req.JSONRPC != "2.0" {
			if err := enc.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{-32600, "无效的 JSON-RPC 请求"}}); err != nil {
				return err
			}
			continue
		}
		var response json.RawMessage
		var err error
		if endpoint.URL == "" {
			var next Endpoint
			next, err = resolve()
			if err == nil {
				setEndpoint(next)
			}
		}
		if err == nil {
			isDiscovery := req.Method == "initialize" || req.Method == "notifications/initialized" || req.Method == "ping" || req.Method == "tools/list"
			const toolTimeout = 180 * time.Second
			timeout := toolTimeout
			if isDiscovery {
				timeout = discoveryLimit
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			var status int
			response, status, err = postRPC(ctx, client, endpoint.URL, endpoint.Token, line, req.ID)
			// A rejected token has not reached the dispatcher. For ambiguous transport
			// failures only discovery is replayable: a tool could have already run.
			if err != nil && ctx.Err() == nil && (status == http.StatusUnauthorized || (status == 0 && isDiscovery)) {
				next, resolveErr := resolve()
				if resolveErr != nil {
					err = resolveErr
				} else if next != endpoint {
					setEndpoint(next)
					response, _, err = postRPC(ctx, client, endpoint.URL, endpoint.Token, line, req.ID)
				}
			}
			cancel()
		}
		if err != nil {
			endpoint = Endpoint{}
		}
		if req.ID == nil {
			continue
		}
		if err != nil {
			if err := enc.Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32001, "Shell MCP 桥接失败：" + err.Error()}}); err != nil {
				return err
			}
			continue
		}
		// Re-encoding RawMessage compacts pretty-printed HTTP JSON to one stdio line.
		if err := enc.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func postRPC(ctx context.Context, client *http.Client, endpoint, token string, line []byte, id json.RawMessage) (json.RawMessage, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(line))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-MCP-Client", "Shell stdio MCP")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not forward arbitrary HTML or reflected credentials to the stdio client.
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRPCResponse+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(body) > maxRPCResponse {
		return nil, resp.StatusCode, fmt.Errorf("HTTP 响应超过 %d 字节", maxRPCResponse)
	}
	if id == nil {
		return nil, resp.StatusCode, nil
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.JSONRPC != "2.0" || !sameRPCID(id, envelope.ID) || (envelope.Result == nil) == (envelope.Error == nil) {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d 未返回有效的 JSON-RPC 响应", resp.StatusCode)
	}
	return body, resp.StatusCode, nil
}

func sameRPCID(want, got json.RawMessage) bool {
	if bytes.Equal(want, got) {
		return true
	}
	if len(want) == 0 || len(got) == 0 || want[0] != '"' || got[0] != '"' {
		return false
	}
	// Equivalent string IDs can use different JSON escaping in an HTTP response.
	var a, b string
	return json.Unmarshal(want, &a) == nil && json.Unmarshal(got, &b) == nil && a == b
}
