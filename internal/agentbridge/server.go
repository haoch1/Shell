// Package agentbridge exposes Shell to external AI Agents (Antigravity, Claude Code, Cursor, Codex, etc.)
// over Model Context Protocol (MCP Streamable HTTP JSON-RPC 2.0) and a localhost REST API,
// protected by loopback-only binding, Origin validation, DPAPI Bearer tokens, command policy, redaction, and audit logs.
package agentbridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
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
	lifecycle sync.Mutex // Serialize listener changes independently of request authentication.
	mu        sync.Mutex
	srv       *http.Server
	port      int
	token     string
	handler   Handler
}

func New(handler Handler) *Server {
	return &Server{handler: handler}
}

func (s *Server) Start(port int, token string) (int, error) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if port < 1024 || port > 65535 {
		port = 17822
	}
	s.mu.Lock()
	if s.srv != nil && s.port == port {
		s.token = token
		s.mu.Unlock()
		return port, nil
	}
	s.mu.Unlock()
	s.stop()
	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return 0, fmt.Errorf("无法在 127.0.0.1:%d 启动本地 MCP 服务：%w", port, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/tools", s.handleRESTTools)
	mux.HandleFunc("/api/v1/call", s.handleRESTCall)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.mu.Lock()
	s.srv, s.port, s.token = srv, port, token
	s.mu.Unlock()
	go func(srv *http.Server, l net.Listener) {
		_ = srv.Serve(l)
	}(srv, ln)
	return port, nil
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
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.stop()
}

func (s *Server) stop() {
	s.mu.Lock()
	srv := s.srv
	s.srv, s.port, s.token = nil, 0, ""
	s.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
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
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
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
		w.Header().Set("Allow", "POST, DELETE")
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

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "failed to read body"}})
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32600, Message: "invalid JSON-RPC 2.0 request"}})
		return
	}

	// JSON-RPC Notification (no ID -> 202 Accepted per MCP Streamable HTTP spec)
	if req.ID == nil {
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
