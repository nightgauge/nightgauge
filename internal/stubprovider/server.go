// Package stubprovider is a deterministic, scripted, OpenAI-compatible chat
// completions server for adapter contract runs. It answers POST
// /v1/chat/completions (streamed and non-streamed) and GET /v1/models from a
// named script embedded in scripts.json, binds loopback only, and has a
// bounded lifetime — see cmd/stub-provider for the CLI.
//
// Turn selection is pure and stateless: the server counts the assistant
// messages already present in the request body to find the next scripted
// turn. It keeps no session state and uses no randomness, so byte-identical
// requests produce byte-identical replies.
package stubprovider

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

//go:embed scripts.json
var scriptsFS embed.FS

// stubCreatedUnix is used as every reply's "created" timestamp instead of
// wall-clock time, so that identical requests produce byte-identical
// replies regardless of when they are sent.
const stubCreatedUnix int64 = 1700000000

// maxBodyBytes bounds how much of a request body the server will read.
const maxBodyBytes = 1 << 20 // 1 MiB

// Defaults for Config, mirrored by the cmd/stub-provider flag defaults.
const (
	DefaultMaxRequests = 50
	DefaultIdleTimeout = 60 * time.Second
)

const (
	scriptKindTurns    = "turns"
	scriptKindOverflow = "overflow"
	scriptKindError    = "error"
)

// ToolCall is one scripted tool call: a name and its arguments.
type ToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// Turn is one scripted assistant reply: either a single tool call or final
// text content.
type Turn struct {
	ToolCall *ToolCall `json:"tool_call,omitempty"`
	Content  string    `json:"content,omitempty"`
}

// Script is one named, deterministic scenario.
type Script struct {
	Models            []string `json:"models"`
	Kind              string   `json:"kind"`
	Turns             []Turn   `json:"turns,omitempty"`
	FirstTokenDelayMS int      `json:"first_token_delay_ms,omitempty"`
}

func loadScripts() (map[string]Script, error) {
	data, err := scriptsFS.ReadFile("scripts.json")
	if err != nil {
		return nil, err
	}
	var scripts map[string]Script
	if err := json.Unmarshal(data, &scripts); err != nil {
		return nil, err
	}
	return scripts, nil
}

// Config configures a Server.
type Config struct {
	// Script is the name of the script to serve (required).
	Script string
	// MaxRequests bounds the number of /v1/chat/completions requests served
	// before Serve stops accepting new connections and returns. Defaults to
	// DefaultMaxRequests when zero or negative.
	MaxRequests int
	// IdleTimeout is how long Serve waits without a request before it stops
	// and returns. Defaults to DefaultIdleTimeout when zero or negative.
	IdleTimeout time.Duration
	// DelayOverride, when non-nil, replaces the script's configured
	// first-token delay (including with zero, to disable it).
	DelayOverride *time.Duration
	// Log receives operational log lines: request sizes and script turn
	// indices only. Request bodies are never logged. Defaults to a discard
	// logger.
	Log *log.Logger
}

// Server serves one script's scenario over HTTP.
type Server struct {
	cfg    Config
	script Script

	httpSrv      *http.Server
	requestCount atomic.Int32
	lastActivity atomic.Int64
	maxReached   chan struct{}
}

// NewServer validates cfg and returns an unstarted Server. Call Listen to
// bind an address and Serve to run.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Script == "" {
		return nil, errors.New("stubprovider: script is required")
	}

	scripts, err := loadScripts()
	if err != nil {
		return nil, fmt.Errorf("stubprovider: loading scripts: %w", err)
	}

	script, ok := scripts[cfg.Script]
	if !ok {
		names := make([]string, 0, len(scripts))
		for n := range scripts {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("stubprovider: unknown script %q (available: %s)", cfg.Script, strings.Join(names, ", "))
	}
	switch script.Kind {
	case scriptKindTurns, scriptKindOverflow, scriptKindError:
	default:
		return nil, fmt.Errorf("stubprovider: script %q has unknown kind %q (want one of %q, %q, %q)",
			cfg.Script, script.Kind, scriptKindTurns, scriptKindOverflow, scriptKindError)
	}

	if cfg.MaxRequests <= 0 {
		cfg.MaxRequests = DefaultMaxRequests
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.Log == nil {
		cfg.Log = log.New(io.Discard, "", 0)
	}

	return &Server{
		cfg:        cfg,
		script:     script,
		maxReached: make(chan struct{}, 1),
	}, nil
}

// Listen binds addr, refusing anything that is not a loopback address
// (127.0.0.1 or ::1). It validates the host before ever calling net.Listen,
// so a rejected address never has an open socket.
func Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("stubprovider: invalid listen address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("stubprovider: refusing to bind non-loopback address %q; use 127.0.0.1 or ::1", addr)
	}
	return net.Listen("tcp", addr)
}

// Serve mounts the script's handlers on ln and blocks until ctx is
// cancelled, the idle timeout elapses with no requests, or MaxRequests
// requests have been served. It always returns nil on a graceful stop.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/v1/models", s.handleModels)

	httpSrv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          s.cfg.Log,
	}
	s.httpSrv = httpSrv
	s.touch()

	serveErr := make(chan error, 1)
	go func() {
		err := httpSrv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			serveErr <- nil
			return
		}
		serveErr <- err
	}()

	ticker := time.NewTicker(idleCheckInterval(s.cfg.IdleTimeout))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return s.shutdown()
		case err := <-serveErr:
			return err
		case <-s.maxReached:
			return s.shutdown()
		case <-ticker.C:
			last := time.Unix(0, s.lastActivity.Load())
			if time.Since(last) >= s.cfg.IdleTimeout {
				return s.shutdown()
			}
		}
	}
}

func idleCheckInterval(idle time.Duration) time.Duration {
	interval := idle / 10
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval > time.Second {
		interval = time.Second
	}
	return interval
}

func (s *Server) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) touch() {
	s.lastActivity.Store(time.Now().UnixNano())
}

func (s *Server) delay() time.Duration {
	if s.cfg.DelayOverride != nil {
		return *s.cfg.DelayOverride
	}
	return time.Duration(s.script.FirstTokenDelayMS) * time.Millisecond
}

func (s *Server) turnAt(i int) Turn {
	if len(s.script.Turns) == 0 {
		return Turn{}
	}
	if i >= len(s.script.Turns) {
		i = len(s.script.Turns) - 1
	}
	if i < 0 {
		i = 0
	}
	return s.script.Turns[i]
}

// ---- wire types -----------------------------------------------------------

type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireToolCall struct {
	Index    *int             `json:"index,omitempty"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function wireFunctionCall `json:"function"`
}

type chatMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content,omitempty"`
	ToolCalls []wireToolCall  `json:"tool_calls,omitempty"`
}

type streamOptionsWire struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatRequest struct {
	Model         string             `json:"model"`
	Messages      []chatMessage      `json:"messages"`
	Stream        bool               `json:"stream"`
	StreamOptions *streamOptionsWire `json:"stream_options,omitempty"`
}

type chatCompletionMessage struct {
	Role      string         `json:"role"`
	Content   *string        `json:"content"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
}

type chatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      chatCompletionMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   *usage                 `json:"usage,omitempty"`
}

type streamDelta struct {
	Role      string         `json:"role,omitempty"`
	Content   *string        `json:"content,omitempty"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type chatCompletionChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *usage         `json:"usage,omitempty"`
}

type errorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelsResponse struct {
	Object string      `json:"object"`
	Data   []modelInfo `json:"data"`
}

// ---- handlers ---------------------------------------------------------

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "stub-provider: method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.touch()

	resp := modelsResponse{Object: "list"}
	for _, id := range s.script.Models {
		resp.Data = append(resp.Data, modelInfo{ID: id, Object: "model", Created: stubCreatedUnix, OwnedBy: "stub-provider"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "stub-provider: method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		http.Error(w, "stub-provider: reading body", http.StatusBadRequest)
		return
	}

	s.touch()

	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "stub-provider: invalid JSON body", http.StatusBadRequest)
		return
	}

	// A single atomic increment-then-compare: the accept/reject decision and
	// the counter update are the same atomic step, so concurrent requests
	// arriving while the counter is one below the cap cannot all pass the
	// check before any of them has incremented it. This runs only after the
	// body has been read and parsed, so a malformed or oversized body never
	// consumes a --max-requests slot: the budget tracks requests the stub
	// actually serves.
	count := s.requestCount.Add(1)
	if int(count) > s.cfg.MaxRequests {
		http.Error(w, "stub-provider: max-requests reached", http.StatusServiceUnavailable)
		return
	}

	turnIndex := countAssistantMessages(req.Messages)

	// Only sizes and the derived turn index are ever logged — never the
	// request body itself.
	s.cfg.Log.Printf("stub-provider: served script=%q turn=%d bytes=%d stream=%v total=%d",
		s.cfg.Script, turnIndex, len(body), req.Stream, count)

	switch s.script.Kind {
	case scriptKindTurns:
		if d := s.delay(); d > 0 {
			time.Sleep(d)
		}
		turn := s.turnAt(turnIndex)
		if req.Stream {
			s.writeStream(w, turnIndex, turn, req)
		} else {
			s.writeComplete(w, turnIndex, turn, req)
		}
	case scriptKindOverflow:
		writeOverflowError(w)
	case scriptKindError:
		writeServerError(w)
	default:
		// Unreachable in practice: NewServer validates Kind against the
		// known set before a Server is ever constructed. Kept as a
		// defensive fail-closed branch rather than silently serving turns.
		http.Error(w, fmt.Sprintf("stub-provider: unknown script kind %q", s.script.Kind), http.StatusInternalServerError)
	}

	if int(count) >= s.cfg.MaxRequests {
		select {
		case s.maxReached <- struct{}{}:
		default:
		}
	}
}

func countAssistantMessages(messages []chatMessage) int {
	n := 0
	for _, m := range messages {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

func countWords(s string) int {
	return len(strings.Fields(s))
}

func countTokens(messages []chatMessage) int {
	total := 0
	for _, m := range messages {
		var content string
		if len(m.Content) > 0 {
			var s string
			if err := json.Unmarshal(m.Content, &s); err == nil {
				content = s
			} else {
				content = string(m.Content)
			}
		}
		total += countWords(content)
		for _, tc := range m.ToolCalls {
			total += countWords(tc.Function.Name) + countWords(tc.Function.Arguments)
		}
	}
	if total == 0 {
		total = 1
	}
	return total
}

func (s *Server) modelFor(req chatRequest) string {
	if req.Model != "" {
		return req.Model
	}
	if len(s.script.Models) > 0 {
		return s.script.Models[0]
	}
	return ""
}

func completionID(script string, turnIndex int) string {
	return fmt.Sprintf("chatcmpl-stub-%s-%d", script, turnIndex)
}

func toolCallID(script string, turnIndex int) string {
	return fmt.Sprintf("call-stub-%s-%d", script, turnIndex)
}

func (s *Server) writeComplete(w http.ResponseWriter, turnIndex int, turn Turn, req chatRequest) {
	model := s.modelFor(req)
	promptTokens := countTokens(req.Messages)

	msg := chatCompletionMessage{Role: "assistant"}
	finishReason := "stop"
	completionTokens := 0

	if turn.ToolCall != nil {
		args, _ := json.Marshal(turn.ToolCall.Arguments)
		msg.ToolCalls = []wireToolCall{{
			ID:       toolCallID(s.cfg.Script, turnIndex),
			Type:     "function",
			Function: wireFunctionCall{Name: turn.ToolCall.Name, Arguments: string(args)},
		}}
		finishReason = "tool_calls"
		completionTokens = countWords(turn.ToolCall.Name) + countWords(string(args))
	} else {
		content := turn.Content
		msg.Content = &content
		completionTokens = countWords(content)
	}
	if completionTokens == 0 {
		completionTokens = 1
	}

	resp := chatCompletionResponse{
		ID:      completionID(s.cfg.Script, turnIndex),
		Object:  "chat.completion",
		Created: stubCreatedUnix,
		Model:   model,
		Choices: []chatCompletionChoice{{Index: 0, Message: msg, FinishReason: finishReason}},
		Usage: &usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) writeStream(w http.ResponseWriter, turnIndex int, turn Turn, req chatRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	model := s.modelFor(req)
	id := completionID(s.cfg.Script, turnIndex)

	writeSSEChunk(w, chatCompletionChunk{
		ID: id, Object: "chat.completion.chunk", Created: stubCreatedUnix, Model: model,
		Choices: []streamChoice{{Index: 0, Delta: streamDelta{Role: "assistant"}}},
	})
	if flusher != nil {
		flusher.Flush()
	}

	promptTokens := countTokens(req.Messages)
	completionTokens := 0
	finishReason := "stop"

	if turn.ToolCall != nil {
		args, _ := json.Marshal(turn.ToolCall.Arguments)
		zero := 0
		writeSSEChunk(w, chatCompletionChunk{
			ID: id, Object: "chat.completion.chunk", Created: stubCreatedUnix, Model: model,
			Choices: []streamChoice{{Index: 0, Delta: streamDelta{
				ToolCalls: []wireToolCall{{
					Index:    &zero,
					ID:       toolCallID(s.cfg.Script, turnIndex),
					Type:     "function",
					Function: wireFunctionCall{Name: turn.ToolCall.Name, Arguments: string(args)},
				}},
			}}},
		})
		finishReason = "tool_calls"
		completionTokens = countWords(turn.ToolCall.Name) + countWords(string(args))
	} else {
		content := turn.Content
		writeSSEChunk(w, chatCompletionChunk{
			ID: id, Object: "chat.completion.chunk", Created: stubCreatedUnix, Model: model,
			Choices: []streamChoice{{Index: 0, Delta: streamDelta{Content: &content}}},
		})
		completionTokens = countWords(content)
	}
	if flusher != nil {
		flusher.Flush()
	}
	if completionTokens == 0 {
		completionTokens = 1
	}

	writeSSEChunk(w, chatCompletionChunk{
		ID: id, Object: "chat.completion.chunk", Created: stubCreatedUnix, Model: model,
		Choices: []streamChoice{{Index: 0, Delta: streamDelta{}, FinishReason: &finishReason}},
	})
	if flusher != nil {
		flusher.Flush()
	}

	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		u := usage{PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: promptTokens + completionTokens}
		writeSSEChunk(w, chatCompletionChunk{
			ID: id, Object: "chat.completion.chunk", Created: stubCreatedUnix, Model: model,
			Choices: []streamChoice{},
			Usage:   &u,
		})
		if flusher != nil {
			flusher.Flush()
		}
	}

	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func writeSSEChunk(w io.Writer, payload chatCompletionChunk) {
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// writeOverflowError responds with both the OpenAI-hosted
// "context_length_exceeded" wording and the "n_ctx" wording used by local
// OpenAI-compatible servers, so a consumer's detection regex for either form
// can be exercised against one script.
func writeOverflowError(w http.ResponseWriter) {
	body := errorResponse{Error: errorDetail{
		Message: "This model's maximum context length is 8192 tokens, however the messages resulted in more tokens: context_length_exceeded. " +
			"local server: n_ctx = 8192 exceeded by the request; reduce the prompt and try again.",
		Type: "invalid_request_error",
		Code: "context_length_exceeded",
	}}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(body)
}

func writeServerError(w http.ResponseWriter) {
	body := errorResponse{Error: errorDetail{
		Message: "stub-provider: internal error",
		Type:    "server_error",
		Code:    "internal_error",
	}}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(body)
}
