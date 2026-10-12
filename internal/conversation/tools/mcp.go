package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"time"
)

// Server is one turn's tool server: the Model Context Protocol over HTTP
// ("streamable HTTP", answered as plain JSON) on a loopback port. It answers
// only requests that carry the turn's own bearer token, which exists in
// memory and in the 0600 config file the turn's adapter is handed, so one
// turn's process can reach its own tools and nothing else can reach them. It
// holds no credential: the forge tools resolve theirs in the daemon.
type Server struct {
	set   *Toolset
	token string
	ln    net.Listener
	http  *http.Server
	// base ends every tool call in flight when the server closes.
	base   context.Context
	cancel context.CancelFunc
}

// Bounds of one request.
const (
	maxRequestBytes = 1 << 20
	callTimeout     = 2 * time.Minute
)

// protocolVersions are the MCP revisions this server speaks. It answers an
// initialize in the client's revision when it is one of these, and in the
// newest otherwise.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

// Start serves set on a new loopback port until Close.
func Start(set *Toolset) (*Server, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	base, cancel := context.WithCancel(context.Background())
	s := &Server{set: set, token: hex.EncodeToString(secret), ln: ln, base: base, cancel: cancel}
	s.http = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      callTimeout + 30*time.Second,
		BaseContext:       func(net.Listener) context.Context { return base },
	}
	go func() { _ = s.http.Serve(ln) }()
	return s, nil
}

// URL is the endpoint the adapter posts to.
func (s *Server) URL() string { return "http://" + s.ln.Addr().String() + "/mcp" }

// Close ends the calls in flight and stops the server.
func (s *Server) Close() {
	s.cancel()
	ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
	defer done()
	_ = s.http.Shutdown(ctx)
}

// WriteConfig writes the adapter's MCP config for this server to path, a
// new file readable by its owner only. The token goes in a file, never on
// argv, where every local user could read it.
func (s *Server) WriteConfig(path string) error {
	cfg := map[string]any{"mcpServers": map[string]any{
		ServerName: map[string]any{
			"type":    "http",
			"url":     s.URL(),
			"headers": map[string]string{"Authorization": "Bearer " + s.token},
		},
	}}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
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

// ServeHTTP answers one JSON-RPC message. Anything not addressed to this
// server by its own client is refused before it is read: another host name
// (DNS rebinding), a browser origin, or a missing or wrong token.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp" {
		http.NotFound(w, r)
		return
	}
	if r.Host != s.ln.Addr().String() || r.Header.Get("Origin") != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	want := []byte("Bearer " + s.token)
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		// No server-initiated stream and no session to delete.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '[' {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: -32600, Message: "batches are not supported"}})
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.JSONRPC != "2.0" {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: -32700, Message: "not a JSON-RPC 2.0 message"}})
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		// A notification, or a response to a request this server never
		// makes: accepted, and nothing to answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := s.dispatch(r.Context(), req)
	writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rerr})
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := protocolVersions[0]
		if slices.Contains(protocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": ServerName, "version": "1"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolList()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid tools/call params"}
		}
		if _, ok := lookup(p.Name); !ok {
			return nil, &rpcError{Code: -32602, Message: "unknown tool: " + quoteName(p.Name)}
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		text, terr := s.set.Call(ctx, p.Name, p.Arguments)
		if terr != nil {
			return toolResult(fmt.Sprintf("error %s: %s", terr.Code, terr.Message), true), nil
		}
		return toolResult(text, false), nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found"}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
}

// toolList is tools/list's answer: every registered tool, with read-only
// tools hinted as such.
func toolList() []map[string]any {
	out := make([]map[string]any, 0, len(registry))
	for _, name := range Names() {
		sp, _ := lookup(name)
		out = append(out, map[string]any{
			"name":        sp.name,
			"description": sp.description,
			"inputSchema": sp.schema,
			"annotations": map[string]any{
				"readOnlyHint":    !sp.mutating,
				"destructiveHint": false,
				"openWorldHint":   sp.name == ToolGetIssue || sp.name == ToolSearchIssues || sp.name == ToolGetPullRequest || sp.mutating,
			},
		})
	}
	return out
}
