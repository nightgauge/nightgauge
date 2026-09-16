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

	"github.com/nightgauge/nightgauge/internal/gittest"
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

// guardGitInit makes dir a one-commit git repository — every real dispatch's
// WorktreeDir is one (Manager.RunStage's own worktree setup), unlike a bare
// t.TempDir(). opencode 1.18.30 resolves Instance.worktree, what edit/write/
// read patterns are matched relative to, from `git rev-parse --show-toplevel`
// when dir is a git repository, and "/" (the whole filesystem) when it is
// not — the two forms this file's probes tell apart (#1638 fix round finding
// 1/7).
func guardGitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.email=probe@example.invalid", "-c", "user.name=probe", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "base"},
	} {
		gittest.Run(t, dir, args...)
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

// TestOpenCodeIncludesEditDeniedInGitWorktree is AC2's probe in the shape
// every real dispatch actually runs in: a git worktree (guardGitInit), not
// TestOpenCodeIncludesReadAllowed's bare t.TempDir(). opencode 1.18.30
// matches edit's own deny patterns against path.relative(Instance.worktree,
// file), and Instance.worktree is the git top-level for a git repository —
// not "/", which is the only case an absolute, leading-slash-stripped
// pattern (the pre-fix openCodeEditDenyPatterns) matches. #1638 fix round
// finding 1/7: before the fix, this edit succeeded and the include file was
// overwritten.
func TestOpenCodeIncludesEditDeniedInGitWorktree(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "skill")
	includeFile := filepath.Join(skillDir, "_includes", "note.md")
	guardWriteFile(t, includeFile, "PROBE-1638-INCLUDE-CONTENT\n")

	dir := t.TempDir()
	guardWriteFile(t, filepath.Join(dir, "README.md"), "probe worktree\n")
	guardGitInit(t, dir)

	opts := RunOptions{AllowedTools: []string{"Read", "Edit"}, SkillPath: filepath.Join(skillDir, "SKILL.md"), WorktreeDir: dir}
	permission := openCodePermissionMap(opts, "")

	stub := newGuardStubServer(t, []guardStubTurn{
		{ToolCall: &guardStubToolCall{Name: "edit", Arguments: map[string]any{"filePath": includeFile, "oldString": "PROBE-1638-INCLUDE-CONTENT", "newString": "TAMPERED"}}},
		{Content: "done"},
	})
	exitCode, stdout, stderr := guardDirectRun(t, permission, stub, dir)
	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)

	var sawEditError bool
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		tool, status, _, ok := guardToolUseEvent(line)
		if ok && tool == "edit" && status == "error" {
			sawEditError = true
		}
	}
	if !sawEditError {
		t.Errorf("no errored tool_use for \"edit\" of a skill-dir file in a git worktree:\n%s", stdout)
	}
	if got, _ := os.ReadFile(includeFile); strings.Contains(string(got), "TAMPERED") {
		t.Errorf("AC2 broken in a git worktree: the edit of NIGHTGAUGE_SKILL_DIR/_includes/note.md succeeded, content is now %q", got)
	}
}

// TestOpenCodeBinDirEditDenied is #1638 fix round finding 8's probe: even
// before finding 5/9 (item 4) removed NIGHTGAUGE_BIN from
// openCodeExternalDirectoryAllowList entirely, nothing denied Edit or Write
// of it while external_directory allow-listed it for Read, and every stage
// skill grants both. A stage could plant an executable in the running
// nightgauge binary's own directory — typically a PATH directory — giving
// it persistent code execution outside the per-run isolation. This still
// covers the edit-deny backstop directly (openCodeWorktreeRelativeDirPatterns'
// own defense in depth, kept past the allow-list removal); a write tool
// call now hits BOTH that and external_directory's own "*": "deny" default,
// either one already sufficient — see TestOpenCodeBinDirCpDenied below for
// the bash leg the issue's own item 4 asked for.
func TestOpenCodeBinDirEditDenied(t *testing.T) {
	dir := t.TempDir()
	guardWriteFile(t, filepath.Join(dir, "README.md"), "probe worktree\n")
	guardGitInit(t, dir)

	binDir := t.TempDir()
	planted := filepath.Join(binDir, "gh")

	opts := RunOptions{AllowedTools: []string{"Read", "Edit", "Write"}, WorktreeDir: dir}
	permission := openCodePermissionMap(opts, binDir)

	stub := newGuardStubServer(t, []guardStubTurn{
		{ToolCall: &guardStubToolCall{Name: "write", Arguments: map[string]any{"filePath": planted, "content": "#!/bin/sh\necho PLANTED\n"}}},
		{Content: "done"},
	})
	exitCode, stdout, stderr := guardDirectRun(t, permission, stub, dir)
	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)

	if _, err := os.Stat(planted); err == nil {
		t.Errorf("NIGHTGAUGE_BIN dir is writable: a write tool call planted %s", planted)
	}
}

// TestOpenCodeBinDirCpDenied is #1638 fix round finding 5/9's own closure
// (item 4): a scan of the six stage skills' own committed text found no
// concrete Read, cat or cd of a path under NIGHTGAUGE_BIN — every reference
// is `BINARY="${NIGHTGAUGE_BIN:-}"` followed by running $BINARY or
// `export PATH="$(dirname "$BINARY"):$PATH"`, neither a filesystem read of
// it — so openCodeExternalDirectoryAllowList no longer allow-lists it at
// all (kept only for TestOpenCodeBinDirEditDenied's own edit-deny leg,
// unaffected). This probe is the bash leg the issue's own item 4 asked for:
// a bash `cp` planting a file into NIGHTGAUGE_BIN is refused by opencode's
// own external_directory check, naming that permission in the rejected
// tool_use event.
func TestOpenCodeBinDirCpDenied(t *testing.T) {
	runOpenCodeBinDirCpDeniedProbe(t)
}

// TestOpenCodeBinDirCpDeniedUnderTmpdirTmp is TestOpenCodeBinDirCpDenied's
// own regression leg for TMPDIR=/tmp (ubuntu-latest CI's default, and the
// usual Linux developer setup): #1638 fix round finding. With TMPDIR unset,
// t.TempDir() puts binDir itself under /tmp, nested at least one level;
// before the fix round's "?" narrowing (openCodeTmpDirAllowPatterns), that
// fell inside the old, recursive "/tmp/*" allow and the planted cp
// completed instead of being rejected. See
// TestOpenCodeOutsideReadRejectedUnderTmpdirTmp's own comment for why
// forcing TMPDIR=/tmp here does not depend on which OS runs it.
func TestOpenCodeBinDirCpDeniedUnderTmpdirTmp(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	runOpenCodeBinDirCpDeniedProbe(t)
}

func runOpenCodeBinDirCpDeniedProbe(t *testing.T) {
	dir := t.TempDir()
	guardWriteFile(t, filepath.Join(dir, "README.md"), "probe worktree\n")
	source := filepath.Join(dir, "payload.sh")
	guardWriteFile(t, source, "#!/bin/sh\necho PLANTED\n")
	guardGitInit(t, dir)

	binDir := t.TempDir()
	planted := filepath.Join(binDir, "gh")

	opts := RunOptions{AllowedTools: []string{"Read", "Bash"}, WorktreeDir: dir}
	permission := openCodePermissionMap(opts, binDir)

	stub := newGuardStubServer(t, []guardStubTurn{
		{ToolCall: &guardStubToolCall{Name: "bash", Arguments: map[string]any{"command": fmt.Sprintf("cp %q %q", source, planted)}}},
		{Content: "done"},
	})
	exitCode, stdout, stderr := guardDirectRun(t, permission, stub, dir)
	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)

	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		tool, status, errText, ok := guardToolUseEvent(line)
		if !ok || tool != "bash" {
			continue
		}
		if status != "error" {
			t.Errorf("the bash cp into NIGHTGAUGE_BIN was not rejected (status=%q)", status)
		} else if !strings.Contains(errText, `"permission":"external_directory"`) {
			t.Errorf("the rejection's error text does not name permission external_directory: %s", errText)
		}
	}
	if _, err := os.Stat(planted); err == nil {
		t.Errorf("NIGHTGAUGE_BIN dir is writable via bash: cp planted %s", planted)
	}
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
	runOpenCodeOutsideReadRejectedProbe(t)
}

// TestOpenCodeOutsideReadRejectedUnderTmpdirTmp is
// TestOpenCodeOutsideReadRejected's own regression leg for TMPDIR=/tmp
// (ubuntu-latest CI's default, and the usual Linux developer setup): #1638
// fix round finding. With TMPDIR unset, Go's t.TempDir() (both here and in
// the shared probe below) puts every fixture directory this test treats as
// "outside the allow-list" under /tmp itself — nested at least one level
// (t.TempDir()'s own "<name>/NNN" shape), never the bare, flat "/tmp/*"
// request the allow-list legitimately grants. Before the fix round's "?"
// narrowing (openCodeTmpDirAllowPatterns), that nested fixture fell inside
// the old, recursive "/tmp/*" allow and this probe's own read completed
// instead of erroring; forcing TMPDIR=/tmp here reproduces that on any
// platform, including one whose default temp root is already outside /tmp
// (macOS's /var/folders/...), so this leg does not depend on which OS runs
// it.
func TestOpenCodeOutsideReadRejectedUnderTmpdirTmp(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	runOpenCodeOutsideReadRejectedProbe(t)
}

func runOpenCodeOutsideReadRejectedProbe(t *testing.T) {
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

// TestOpenCodeNestedDotEnvDenied is a routed #1752 request's own probe: a
// Read-granted stage must not be able to read a NESTED secret file, whether
// its name starts with ".env" (apps/web/.env.local) or merely carries
// ".env." as an infix (config/prod.env.local) — neither shape at any depth.
// opencode's own read patterns are matched against the worktree-relative
// path with no implicit "**" prefix, so the issue's own backstop entries
// only ever match a root-level file, or a name that starts with ".env";
// without "**/*.env"/"**/.env*" (#1638 fix round finding) and
// "*.env.*"/"**/*.env.*" (#1638 fix round, a routed #1752 request), the
// map's read "*": allow backstop is the last matching rule for either
// nested file, and it is readable. A control read of config/plain.txt,
// which names no secret shape at all, MUST complete — proving the deny is
// specific to these two filenames, not a broader nested-read regression.
func TestOpenCodeNestedDotEnvDenied(t *testing.T) {
	dir := t.TempDir()
	guardWriteFile(t, filepath.Join(dir, "README.md"), "probe worktree\n")
	guardWriteFile(t, filepath.Join(dir, "apps", "web", ".env.local"), "SECRET=PROBE-1638\n")
	guardWriteFile(t, filepath.Join(dir, "config", "prod.env.local"), "SECRET=PROBE-1752\n")
	guardWriteFile(t, filepath.Join(dir, "config", "plain.txt"), "not a secret\n")
	guardGitInit(t, dir)

	// opencode reports a tool call's filePath resolved through symlinks
	// (ADR-022 § 9's last bullet); t.TempDir() on macOS is itself under a
	// symlink ($TMPDIR -> /private/var/...), so this probe's own tool paths
	// are built from the resolved root too, the same form the real dispatch
	// events below actually carry.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	opts := RunOptions{AllowedTools: []string{"Read"}, WorktreeDir: dir}
	permission := openCodePermissionMap(opts, "")

	envLocal := filepath.Join(resolved, "apps", "web", ".env.local")
	prodEnv := filepath.Join(resolved, "config", "prod.env.local")
	plain := filepath.Join(resolved, "config", "plain.txt")
	stub := newGuardStubServer(t, []guardStubTurn{
		{ToolCall: &guardStubToolCall{Name: "read", Arguments: map[string]any{"filePath": envLocal}}},
		{ToolCall: &guardStubToolCall{Name: "read", Arguments: map[string]any{"filePath": prodEnv}}},
		{ToolCall: &guardStubToolCall{Name: "read", Arguments: map[string]any{"filePath": plain}}},
		{Content: "done"},
	})
	exitCode, stdout, stderr := guardDirectRun(t, permission, stub, dir)
	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)

	var reads []string
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		tool, st, _, ok := guardToolUseEvent(line)
		if !ok || tool != "read" {
			continue
		}
		reads = append(reads, st)
	}
	if len(reads) != 3 {
		t.Fatalf("saw %d \"read\" tool_use events, want 3 (env-local, prod-env, plain):\n%s", len(reads), stdout)
	}
	if reads[0] != "error" {
		t.Errorf("secret widening: apps/web/.env.local was READ under the generated map (status=%q)", reads[0])
	}
	if reads[1] != "error" {
		t.Errorf("secret widening: config/prod.env.local was READ under the generated map (status=%q)", reads[1])
	}
	if reads[2] == "error" {
		t.Errorf("control read failed: config/plain.txt (no secret shape) was denied too, so this is not a targeted deny")
	}
}

// TestOpenCodeTmpDirAllowLetsAStageReadAndCatFromTmp is AC4's own
// real-binary closure (#1638 fix round): opencode 1.18.30 asks
// external_directory for dirname(file)+"/*", never the file's own path, so a
// per-file allow entry (the pre-fix openCodeTmpAllowList) could never match
// a real tool call against exactly that file — this probe is what found
// that, and what proves the directory-level fix
// (openCodeTmpDirAllowPatterns) now lets a stage skill's own /tmp use
// complete: a Read tool call and a bash `cat`, both on /tmp/automerge.err, a
// literal the nightgauge-pr-merge skill itself uses. A bash `mv` OUT of
// /tmp — the issue's other named shape — was ALSO probed here and found
// still refused even with an explicit allow for the source directory:
// opencode's own lexical scan of a multi-path bash command does not reduce
// to the same dirname(file)+"/*" request a single-path read/edit/bash call
// does, consistent with this ADR's own "the external_directory check is a
// lexical backstop; bash redirection is not covered by it anyway" — a
// finding for the record, not a regression this fix claims to close.
func TestOpenCodeTmpDirAllowLetsAStageReadAndCatFromTmp(t *testing.T) {
	dir := t.TempDir()
	guardWriteFile(t, filepath.Join(dir, "README.md"), "probe worktree\n")
	guardGitInit(t, dir)

	// A real, FLAT file directly under /tmp (never t.TempDir(), which is
	// under macOS's $TMPDIR, not /tmp itself): this probe means to exercise
	// openCodePermissionMap's own DEFAULT /tmp allow entries exactly as a
	// real dispatch gets them, not a hand-patched one this test adds itself.
	tmpFile, err := os.CreateTemp("/tmp", "ocp1638-automerge-*.err")
	if err != nil {
		t.Fatal(err)
	}
	automergeErr := tmpFile.Name()
	t.Cleanup(func() { os.Remove(automergeErr) })
	if _, err := tmpFile.WriteString("PROBE-1638-AUTOMERGE-ERR\n"); err != nil {
		t.Fatal(err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatal(err)
	}

	opts := RunOptions{AllowedTools: []string{"Read", "Bash"}, WorktreeDir: dir}
	permission := openCodePermissionMap(opts, "")

	stub := newGuardStubServer(t, []guardStubTurn{
		{ToolCall: &guardStubToolCall{Name: "read", Arguments: map[string]any{"filePath": automergeErr}}},
		{ToolCall: &guardStubToolCall{Name: "bash", Arguments: map[string]any{"command": fmt.Sprintf("cat %q", automergeErr)}}},
		{Content: "done"},
	})
	exitCode, stdout, stderr := guardDirectRun(t, permission, stub, dir)
	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)

	var sawReadSuccess, sawBashSuccess bool
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		tool, status, errText, ok := guardToolUseEvent(line)
		if !ok {
			continue
		}
		switch tool {
		case "read":
			if status != "error" {
				sawReadSuccess = true
			} else {
				t.Errorf("the read of /tmp/automerge.err was rejected: %s", errText)
			}
		case "bash":
			if status != "error" {
				sawBashSuccess = true
			} else {
				t.Errorf("the bash cat of /tmp/automerge.err was rejected: %s", errText)
			}
		}
	}
	if !sawReadSuccess {
		t.Errorf("no successful tool_use for \"read\" in stdout:\n%s", stdout)
	}
	if !sawBashSuccess {
		t.Errorf("no successful tool_use for \"bash\" in stdout:\n%s", stdout)
	}
}

// TestOpenCodeTmpDirAllowDoesNotReachANestedFile is
// TestOpenCodeTmpDirAllowLetsAStageReadAndCatFromTmp's own negative
// companion, and #1638 fix round finding's own closure: opencode 1.18.30's
// Wildcard.match turns a configured "*" into ".*", which crosses "/", so the
// PRE-fix "/tmp/*" allow entry matched a NESTED /tmp file's own
// external_directory request too, not only a flat one — the whole /tmp and
// /private/tmp trees were reachable for read and edit at any depth, wider
// than ADR-022's own recorded cost (every OTHER file directly under /tmp).
// openCodeTmpDirAllowPatterns' "/tmp/?" entry (this fix round) matches only
// the flat request; this probe's read of a two-levels-down file, and write
// of a sibling at the same depth, must both be rejected, naming
// external_directory, and the sibling file's content must be untouched.
func TestOpenCodeTmpDirAllowDoesNotReachANestedFile(t *testing.T) {
	dir := t.TempDir()
	guardWriteFile(t, filepath.Join(dir, "README.md"), "probe worktree\n")
	guardGitInit(t, dir)

	root, err := os.MkdirTemp("/tmp", "ocp1638-nested-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	nested := filepath.Join(root, "sub", "other-process-secret.txt")
	guardWriteFile(t, nested, "SECRET\n")
	sibling := filepath.Join(root, "sub", "victim.txt")
	guardWriteFile(t, sibling, "ORIGINAL\n")

	opts := RunOptions{AllowedTools: []string{"Read", "Write"}, WorktreeDir: dir}
	permission := openCodePermissionMap(opts, "")

	stub := newGuardStubServer(t, []guardStubTurn{
		{ToolCall: &guardStubToolCall{Name: "read", Arguments: map[string]any{"filePath": nested}}},
		{ToolCall: &guardStubToolCall{Name: "write", Arguments: map[string]any{"filePath": sibling, "content": "OVERWRITTEN\n"}}},
		{Content: "done"},
	})
	exitCode, stdout, stderr := guardDirectRun(t, permission, stub, dir)
	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)

	var sawRead, sawWrite bool
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		tool, status, errText, ok := guardToolUseEvent(line)
		if !ok {
			continue
		}
		switch tool {
		case "read":
			sawRead = true
		case "write":
			sawWrite = true
		default:
			continue
		}
		if status != "error" {
			t.Errorf("nested /tmp %s was not rejected (status=%q)", tool, status)
		} else if !strings.Contains(errText, `"permission":"external_directory"`) {
			t.Errorf("the rejection's error text does not name permission external_directory: %s", errText)
		}
	}
	if !sawRead || !sawWrite {
		t.Fatalf("missing tool_use events (read=%v write=%v) in stdout:\n%s", sawRead, sawWrite, stdout)
	}
	if got, _ := os.ReadFile(sibling); string(got) != "ORIGINAL\n" {
		t.Errorf("a nested /tmp sibling file was overwritten via external_directory: got %q", got)
	}
}
