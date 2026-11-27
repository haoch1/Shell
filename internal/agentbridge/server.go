// Package agentbridge exposes Shell to external AI Agents (Antigravity, Claude Code, Cursor, Codex, etc.)
// over Model Context Protocol (MCP Streamable HTTP JSON-RPC 2.0) and a localhost REST API,
// protected by loopback-only binding, Origin validation, DPAPI Bearer tokens, command policy, redaction, and audit logs.
package agentbridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ToolSpec defines one MCP / Agent callable tool.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolCallOutcome carries the result returned by Shell's unified guarded tool dispatcher.
type ToolCallOutcome struct {
	Text          string `json:"text"`
	IsError       bool   `json:"isError"`
	Risk          string `json:"risk,omitempty"`
	Decision      string `json:"decision,omitempty"`
	RedactedCount int    `json:"redactedCount,omitempty"`
}

// Handler bridges MCP / REST calls into the running Shell App instance.
type Handler interface {
	ListTools() []ToolSpec
	CallTool(ctx context.Context, source, clientName, toolName string, args map[string]any) ToolCallOutcome
}

// Server serves MCP Streamable HTTP (/mcp) and REST (/api/v1/*) strictly on 127.0.0.1.
type Server struct {
	mu      sync.Mutex
	srv     *http.Server
	ln      net.Listener
	port    int
	token   string
	handler Handler
}

func New(handler Handler) *Server {
	return &Server{handler: handler}
}

func (s *Server) Start(port int, token string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.srv != nil {
		if s.port == port {
			s.token = token
			return s.port, nil
		}
		_ = s.stopLocked()
	}
	if port < 1024 || port > 65535 {
		port = 17822
	}
	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return 0, fmt.Errorf("无法在 127.0.0.1:%d 启动本地 MCP 服务：%w", port, err)
	}
	s.ln = ln
	s.port = ln.Addr().(*net.TCPAddr).Port
	s.token = token

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/tools", s.handleRESTTools)
	mux.HandleFunc("/api/v1/call", s.handleRESTCall)

	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func(srv *http.Server, l net.Listener) {
		_ = srv.Serve(l)
	}(s.srv, ln)
	return s.port, nil
}

func (s *Server) SetToken(token string) {
	s.mu.Lock()
	s.token = token
	s.mu.Unlock()
}

func (s *Server) Running() (bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil, s.port
}

func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.stopLocked()
}

func (s *Server) stopLocked() error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.srv.Shutdown(ctx)
	s.srv = nil
	s.ln = nil
	s.port = 0
	return err
}

// validateSecurity enforces MCP spec Origin validation (DNS rebinding protection) and Bearer token authentication.
func (s *Server) validateSecurity(w http.ResponseWriter, r *http.Request) bool {
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		u, err := url.Parse(origin)
		host := ""
		if err == nil {
			host = strings.ToLower(u.Hostname())
		}
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			http.Error(w, "Forbidden Origin", http.StatusForbidden)
			return false
		}
	}
	s.mu.Lock()
	expected := s.token
	s.mu.Unlock()
	if expected == "" {
		http.Error(w, "Bridge token unavailable", http.StatusServiceUnavailable)
		return false
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	provided := ""
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		provided = strings.TrimSpace(auth[7:])
	}
	if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="Shell MCP"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !s.validateSecurity(w, r) {
		return
	}
	_, port := s.Running()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"service":  "shell-mcp-bridge",
		"protocol": "2025-03-26",
		"port":     port,
	})
}

func (s *Server) handleRESTTools(w http.ResponseWriter, r *http.Request) {
	if !s.validateSecurity(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": s.handler.ListTools()})
}

func (s *Server) handleRESTCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validateSecurity(w, r) {
		return
	}
	var body struct {
		Name       string         `json:"name"`
		Arguments  map[string]any `json:"arguments"`
		ClientName string         `json:"clientName"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid tool call payload"})
		return
	}
	client := strings.TrimSpace(body.ClientName)
	if client == "" {
		client = strings.TrimSpace(r.Header.Get("X-Client-Name"))
	}
	if client == "" {
		client = "HTTP Agent"
	}
	out := s.handler.CallTool(r.Context(), "http", client, body.Name, body.Arguments)
	status := http.StatusOK
	if out.IsError {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, out)
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !s.validateSecurity(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		// Streamable HTTP optional GET probe -> cleanly indicate stateless POST JSON-RPC support.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "failed to read body"}})
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.Method == "" {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32600, Message: "invalid JSON-RPC 2.0 request"}})
		return
	}

	// JSON-RPC Notification (no ID -> 202 Accepted per MCP Streamable HTTP spec)
	if req.ID == nil && strings.HasPrefix(req.Method, "notifications/") {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	clientName := strings.TrimSpace(r.Header.Get("X-MCP-Client"))
	if clientName == "" {
		clientName = strings.TrimSpace(r.Header.Get("User-Agent"))
	}
	if clientName == "" {
		clientName = "MCP Agent"
	}

	resp := s.dispatchMCP(r.Context(), req, clientName)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) dispatchMCP(ctx context.Context, req rpcRequest, clientName string) rpcResponse {
	switch req.Method {
	case "initialize":
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(req.Params, &initParams)
		proto := "2025-03-26"
		if initParams.ProtocolVersion == "2024-11-05" {
			proto = "2024-11-05"
		}
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": proto,
				"capabilities": map[string]any{
					"tools": map[string]any{"listChanged": false},
				},
				"serverInfo": map[string]any{
					"name":    "Shell SSH Client",
					"version": "1.2.0",
				},
				"instructions": "Shell SSH Client MCP Bridge. You can pass `host` (saved host name, IP, or ID) directly to shell_exec, shell_sftp_*, or shell_server_stats for automatic zero-friction connection & persistent-channel execution. Outputs are automatically redacted for secrets/PII.",
			},
		}

	case "ping":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}

	case "tools/list":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{"tools": s.handler.ListTools()},
		}

	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "missing tool name"}}
		}
		out := s.handler.CallTool(ctx, "mcp", clientName, p.Name, p.Arguments)
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": out.Text},
				},
				"isError": out.IsError,
			},
		}

	default:
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
}

// ProxyStdioToHTTP implements `Shell.exe --mcp` stdio JSON-RPC 2.0 mode by forwarding cleanly
// to the running Shell desktop window's local MCP HTTP endpoint with keep-alive pooling and token caching.
func ProxyStdioToHTTP(in io.Reader, out io.Writer, resolveEndpoint func() (string, string, error)) error {
	scanner := bufioNewScanner(in)
	transport := &http.Transport{
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	}
	httpClient := &http.Client{Timeout: 180 * time.Second, Transport: transport}
	enc := json.NewEncoder(out)

	var cachedEndpoint, cachedToken string
	getEndpoint := func(forceRefresh bool) (string, string, error) {
		if !forceRefresh && cachedEndpoint != "" && cachedToken != "" {
			return cachedEndpoint, cachedToken, nil
		}
		ep, tok, err := resolveEndpoint()
		if err != nil {
			cachedEndpoint, cachedToken = "", ""
			return "", "", err
		}
		cachedEndpoint, cachedToken = ep, tok
		return ep, tok, nil
	}

	sendOnce := func(endpoint, token string, line []byte) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(line)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-MCP-Client", "Shell stdio MCP")
		return httpClient.Do(req)
	}

	for scanner.Scan() {
		line := bytesTrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var probe struct {
			ID     any    `json:"id,omitempty"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(line, &probe)

		endpoint, token, err := getEndpoint(false)
		if err != nil {
			if probe.ID != nil {
				_ = enc.Encode(rpcResponse{
					JSONRPC: "2.0",
					ID:      probe.ID,
					Error:   &rpcError{Code: -32000, Message: "Shell 未启动或未开启本地 MCP 服务：" + err.Error()},
				})
			}
			continue
		}

		resp, err := sendOnce(endpoint, token, line)
		if err != nil || (resp != nil && resp.StatusCode == http.StatusUnauthorized) {
			if resp != nil {
				resp.Body.Close()
			}
			// Re-resolve endpoint/token once (e.g. if Shell restarted on a new port or rotated token).
			if ep2, tok2, err2 := getEndpoint(true); err2 == nil {
				resp, err = sendOnce(ep2, tok2, line)
			}
		}
		if err != nil {
			if probe.ID != nil {
				_ = enc.Encode(rpcResponse{
					JSONRPC: "2.0",
					ID:      probe.ID,
					Error:   &rpcError{Code: -32001, Message: "无法连接到运行中的 Shell 窗口，请确认 Shell 已启动并在「设置 -> Agent 与安全」中开启了 MCP 服务：" + err.Error()},
				})
			}
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()

		if resp.StatusCode == http.StatusAccepted || len(bytesTrimSpace(respBody)) == 0 {
			continue
		}
		if resp.StatusCode >= 400 && probe.ID != nil {
			_ = enc.Encode(rpcResponse{
				JSONRPC: "2.0",
				ID:      probe.ID,
				Error:   &rpcError{Code: -32002, Message: fmt.Sprintf("Shell MCP 拒绝请求 (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))},
			})
			continue
		}
		_, _ = out.Write(append(bytesTrimSpace(respBody), '\n'))
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
