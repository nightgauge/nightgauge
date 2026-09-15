//go:build opencode_integration

package adapters

// The permission map's empirical leg (ADR-022 § 9; #1638): every claim about
// opencode 1.18.30's own behaviour here is a bounded probe against the real,
// installed, pinned binary (openCodeCatalogBinary, opencode_catalog_integration_test.go),
// never read from documentation. The model server is this file's own
// scripted, in-process, OpenAI-compatible stub (guardStubServer) — no live
// model, no network egress: it binds loopback only and answers a fixed
// sequence of turns keyed by how many assistant messages the request already
// carries, the same turn-selection rule internal/stubprovider/server.go
// uses. A second, parameterized stub is needed here rather than that
// package's named, embedded-JSON scripts, because these probes plant tool
// calls against absolute paths only known at test time (a fresh t.TempDir()
// skill directory), which a static, compiled-in script cannot name.
//
//	go test -tags opencode_integration ./internal/execution/adapters/ -run 'TestOpenCodeIncludesReadAllowed|TestOpenCodeOutsideReadRejected' -count=1 -v

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// guardStubModel is the provider/model every probe in this file dispatches:
// a declared "guard" provider block pointed at the stub, never at OpenCode's
// bundled catalog or a real endpoint.
const guardStubModel = "guard/stub-model"

// guardStubToolCall is one scripted tool call.
type guardStubToolCall struct {
	Name      string
	Arguments map[string]any
}

// guardStubTurn is one scripted assistant reply: a tool call or final text.
type guardStubTurn struct {
	ToolCall *guardStubToolCall
	Content  string
}

// --- OpenAI-compatible wire shapes (a parameterized subset of
// internal/stubprovider/server.go's own, whose contract is already proven
// against this pinned binary by the #1618/#1639 suites) ---

type guardWireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type guardWireToolCall struct {
	ID       string                `json:"id,omitempty"`
	Type     string                `json:"type,omitempty"`
	Function guardWireFunctionCall `json:"function"`
}

type guardChatMessage struct {
	Role string `json:"role"`
}

type guardChatRequest struct {
	Model    string             `json:"model"`
	Messages []guardChatMessage `json:"messages"`
	Stream   bool               `json:"stream"`
}

type guardCompletionMessage struct {
	Role      string              `json:"role"`
	Content   *string             `json:"content"`
	ToolCalls []guardWireToolCall `json:"tool_calls,omitempty"`
}

type guardCompletionChoice struct {
	Index        int                    `json:"index"`
	Message      guardCompletionMessage `json:"message"`
	FinishReason string                 `json:"finish_reason"`
}

type guardCompletionResponse struct {
	ID      string                  `json:"id"`
	Object  string                  `json:"object"`
	Created int64                   `json:"created"`
	Model   string                  `json:"model"`
	Choices []guardCompletionChoice `json:"choices"`
}

// guardStubServer serves guardTurns over /v1/chat/completions, non-streamed
// and streamed alike, selecting the turn by how many assistant messages the
// request already carries — stateless and deterministic, the same rule
// internal/stubprovider/server.go uses. It refuses every request past
// len(turns) with a fixed error, so a run stuck in a loop cannot spin it
// forever.
type guardStubServer struct {
	*httptest.Server
	mu    sync.Mutex
	turns []guardStubTurn
	seen  int
}

func newGuardStubServer(t *testing.T, turns []guardStubTurn) *guardStubServer {
	t.Helper()
	s := &guardStubServer{turns: turns}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *guardStubServer) handle(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/models") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"stub-model","object":"model"}]}`))
		return
	}
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	var req guardChatRequest
	_ = json.Unmarshal(body, &req)

	s.mu.Lock()
	s.seen++
	seen := s.seen
	s.mu.Unlock()

	n := 0
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			n++
		}
	}
	if n >= len(s.turns) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":{"message":"guard stub: turn %d exceeds the %d scripted (request #%d)","type":"invalid_request_error"}}`, n, len(s.turns), seen)
		return
	}
	turn := s.turns[n]
	id := fmt.Sprintf("guard-%d", n)

	if req.Stream {
		s.writeStream(w, id, n, turn)
		return
	}

	msg := guardCompletionMessage{Role: "assistant"}
	finish := "stop"
	if turn.ToolCall != nil {
		args, _ := json.Marshal(turn.ToolCall.Arguments)
		msg.ToolCalls = []guardWireToolCall{{
			ID: fmt.Sprintf("call-%d", n), Type: "function",
			Function: guardWireFunctionCall{Name: turn.ToolCall.Name, Arguments: string(args)},
		}}
		finish = "tool_calls"
	} else {
		content := turn.Content
		msg.Content = &content
	}

	resp := guardCompletionResponse{
		ID: id, Object: "chat.completion", Created: 1700000000, Model: "guard/stub-model",
		Choices: []guardCompletionChoice{{Index: 0, Message: msg, FinishReason: finish}},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// guardStreamDelta / guardStreamChoice / guardStreamChunk are the SSE chunk
// shapes: opencode 1.18.30 requests "stream": true, observed by this probe
// itself (a non-streamed reply left the run looping with no tool_use event —
// see this file's package doc comment), so writeStream, not a single JSON
// body, is what a real dispatch needs.
type guardStreamDelta struct {
	Role      string              `json:"role,omitempty"`
	Content   *string             `json:"content,omitempty"`
	ToolCalls []guardWireToolCall `json:"tool_calls,omitempty"`
}

type guardStreamChoice struct {
	Index        int              `json:"index"`
	Delta        guardStreamDelta `json:"delta"`
	FinishReason *string          `json:"finish_reason"`
}

type guardStreamChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []guardStreamChoice `json:"choices"`
}

func (s *guardStubServer) writeStream(w http.ResponseWriter, id string, n int, turn guardStubTurn) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	write := func(c guardStreamChunk) {
		data, _ := json.Marshal(c)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}

	write(guardStreamChunk{ID: id, Object: "chat.completion.chunk", Created: 1700000000, Model: "guard/stub-model",
		Choices: []guardStreamChoice{{Index: 0, Delta: guardStreamDelta{Role: "assistant"}}}})

	finish := "stop"
	if turn.ToolCall != nil {
		args, _ := json.Marshal(turn.ToolCall.Arguments)
		write(guardStreamChunk{ID: id, Object: "chat.completion.chunk", Created: 1700000000, Model: "guard/stub-model",
			Choices: []guardStreamChoice{{Index: 0, Delta: guardStreamDelta{ToolCalls: []guardWireToolCall{{
				ID: fmt.Sprintf("call-%d", n), Type: "function",
				Function: guardWireFunctionCall{Name: turn.ToolCall.Name, Arguments: string(args)},
			}}}}}})
		finish = "tool_calls"
	} else {
		content := turn.Content
		write(guardStreamChunk{ID: id, Object: "chat.completion.chunk", Created: 1700000000, Model: "guard/stub-model",
			Choices: []guardStreamChoice{{Index: 0, Delta: guardStreamDelta{Content: &content}}}})
	}

	write(guardStreamChunk{ID: id, Object: "chat.completion.chunk", Created: 1700000000, Model: "guard/stub-model",
		Choices: []guardStreamChoice{{Index: 0, Delta: guardStreamDelta{}, FinishReason: &finish}}})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// guardRunID is a fixed, valid run identity (runstate.IsIdentity's shape),
// the same literal style as opencode_canary_test.go's own fixture id.
const guardRunID = "0199a8b3-0000-7000-8000-000000001638"

// guardDirectRun spawns the real, installed, pinned opencode binary directly
// (never through Manager.RunStage): the #1616 isolation env
// (OpenCodeIsolationEnv), a throwaway HOME and a config declaring the
// "guard" provider pointed at stub, with permission as its `permission` key
// — the exact map openCodePermissionMap would build for the dispatch under
// probe. dir is the project directory opencode resolves paths against
// (--dir); it need not hold the files a scripted tool call reads, since
// those are outside it by construction here.
func guardDirectRun(t *testing.T, permission *openCodePermissionJSON, stub *guardStubServer, dir string) (exitCode int, stdout, stderr string) {
	t.Helper()
	bin := openCodeCatalogBinary(t)
	home := t.TempDir()
	root, _, err := EnsureOpenCodeRunRoot(home, guardRunID, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	env, err := OpenCodeIsolationEnv(OpenCodeIsolation{
		Root: root, Home: home, GOOS: runtime.GOOS,
		Lookup:           func(string) (string, bool) { return "", false },
		MachineConfigDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	config := map[string]any{
		"$schema":    "https://opencode.ai/config.json",
		"share":      "disabled",
		"autoupdate": false,
		"provider": map[string]any{
			"guard": map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"options": map[string]any{"baseURL": stub.URL + "/v1"},
				"models":  map[string]any{"stub-model": map[string]any{}},
			},
		},
		"agent":      map[string]any{"title": map[string]any{"disable": true}},
		"permission": permission,
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}

	envSlice := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "OPENCODE_CONFIG_CONTENT=" + string(raw)}
	for k, v := range env {
		envSlice = append(envSlice, k+"="+v)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "--format", "json", "--print-logs", "--log-level", "ERROR",
		"-m", guardStubModel, "--dir", dir)
	cmd.Env = envSlice
	cmd.Stdin = strings.NewReader("probe prompt")
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	stdout, stderr = outBuf.String(), errBuf.String()
	switch e := runErr.(type) {
	case nil:
		exitCode = 0
	case *exec.ExitError:
		exitCode = e.ExitCode()
	default:
		t.Fatalf("running opencode directly: %v\nstdout:\n%s\nstderr:\n%s", runErr, stdout, stderr)
	}
	return exitCode, stdout, stderr
}

// guardWriteFile writes content to path, making its directory.
func guardWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOpenCodeIncludesReadAllowed is AC2's own probe: a stage whose
// permission map allows Read and denies Edit, with NIGHTGAUGE_SKILL_DIR
// allow-listed for external_directory and denied for edit, reads a file
// under <skillDir>/_includes/ successfully and is denied editing the same
// file. Both are real opencode 1.18.30 tool calls against the real binary;
// nothing here is asserted from reading ADR-022 alone.
func TestOpenCodeIncludesReadAllowed(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "skill")
	includeFile := filepath.Join(skillDir, "_includes", "note.md")
	guardWriteFile(t, includeFile, "PROBE-1638-INCLUDE-CONTENT\n")

	dir := t.TempDir()
	guardWriteFile(t, filepath.Join(dir, "README.md"), "probe worktree\n")

	opts := RunOptions{AllowedTools: []string{"Read", "Edit"}, SkillPath: filepath.Join(skillDir, "SKILL.md"), WorktreeDir: dir}
	permission := openCodePermissionMap(opts, "")

	stub := newGuardStubServer(t, []guardStubTurn{
		{ToolCall: &guardStubToolCall{Name: "read", Arguments: map[string]any{"filePath": includeFile}}},
		{ToolCall: &guardStubToolCall{Name: "edit", Arguments: map[string]any{"filePath": includeFile, "oldString": "PROBE-1638-INCLUDE-CONTENT", "newString": "TAMPERED"}}},
		{Content: "done"},
	})

	exitCode, stdout, stderr := guardDirectRun(t, permission, stub, dir)
	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)

	// No "; auto-rejecting" stderr notice is expected here: that notice is
	// specific to a permission that resolves to "ask" (ADR-022 § 9), and
	// this map never generates "ask" (AC1). A "deny" match instead fails the
	// tool call directly, observed only as an errored tool_use event in
	// stdout — a bounded probe (this test, before this comment was written)
	// found asserting an "auto-rejecting" notice here always failed, even
	// once the edit call was genuinely denied.
	var sawReadSuccess, sawEditError bool
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		tool, status, _, ok := guardToolUseEvent(line)
		if !ok {
			continue
		}
		switch tool {
		case "read":
			if status != "error" {
				sawReadSuccess = true
			}
		case "edit":
			if status == "error" {
				sawEditError = true
			}
		}
	}
	if !sawReadSuccess {
		t.Errorf("no successful tool_use for \"read\" in stdout:\n%s", stdout)
	}
	if !sawEditError {
		t.Errorf("no errored tool_use for \"edit\" in stdout:\n%s", stdout)
	}
}

// guardToolUseEvent decodes one raw `opencode run --format json` stdout line
// as a tool_use event's tool name, status and error text; ok is false for
// any other event type or a line this shape does not parse.
func guardToolUseEvent(line string) (tool, status, errText string, ok bool) {
	var ev struct {
		Type string `json:"type"`
		Part struct {
			Tool  string `json:"tool"`
			State struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			} `json:"state"`
		} `json:"part"`
	}
	if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "tool_use" {
		return "", "", "", false
	}
	return ev.Part.Tool, ev.Part.State.Status, ev.Part.State.Error, true
}

// TestOpenCodeOutsideReadRejected is AC3's own probe: a Read outside the
// allow-list (a file in $HOME, never NIGHTGAUGE_SKILL_DIR, the worktree, the
// NIGHTGAUGE_BIN dir or an allow-listed /tmp path) is rejected by opencode
// itself, naming the permission "external_directory" in the tool_use
// event's own error text.
//
// This probe found that ADR-022 § 9's "auto-rejecting" stderr notice never
// appears here: that notice is specific to a permission that resolves to
// "ask", and this map never generates one (AC1) — a "deny" match instead
// fails the tool call directly, visible only as an errored tool_use event in
// stdout, never as stderr text. Package execution's own failure
// classification (#1624, OpenCodeAutoRejectMarker) reads stderr for that
// notice, so once every dispatch carries a permission map with no "ask" in
// it, that stderr-based path stops firing for a permission-map rejection;
// whether classification needs a second, stdout-based path for this case is
// package execution's own tested concern, not this file's or this ticket's
// file_ownership — noted here as a finding, not fixed here.
func TestOpenCodeOutsideReadRejected(t *testing.T) {
	home := t.TempDir()
	outsideFile := filepath.Join(home, "secret-outside-allowlist.txt")
	guardWriteFile(t, outsideFile, "PROBE-1638-OUTSIDE-CONTENT\n")

	dir := t.TempDir()
	guardWriteFile(t, filepath.Join(dir, "README.md"), "probe worktree\n")

	// No SkillPath: the allow-list holds only the worktree-relative
	// defaults (the /tmp literals) and never outsideFile's directory.
	opts := RunOptions{AllowedTools: []string{"Read"}, WorktreeDir: dir}
	permission := openCodePermissionMap(opts, "")

	stub := newGuardStubServer(t, []guardStubTurn{
		{ToolCall: &guardStubToolCall{Name: "read", Arguments: map[string]any{"filePath": outsideFile}}},
		{Content: "done"},
	})

	exitCode, stdout, stderr := guardDirectRun(t, permission, stub, dir)
	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)

	var sawRejection bool
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		tool, status, errText, ok := guardToolUseEvent(line)
		if !ok || tool != "read" {
			continue
		}
		if status != "error" {
			t.Fatalf("the outside read's tool_use status = %q, want \"error\":\n%s", status, stdout)
		}
		if !strings.Contains(errText, `"permission":"external_directory"`) {
			t.Errorf("the rejection's error text does not name permission external_directory: %s", errText)
		}
		sawRejection = true
	}
	if !sawRejection {
		t.Fatalf("no tool_use event for \"read\" in stdout:\n%s", stdout)
	}
}
