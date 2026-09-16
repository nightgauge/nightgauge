//go:build opencode_integration || canary

package execution

// Repository steering and MCP servers (ADR-022 § 8, § 11, #1626) observed
// against the real opencode binary, pinned to the version the observations
// were made on:
//
//	go test -tags opencode_integration ./internal/execution/ -run 'OpenCodeClaudeMd|OpenCodeIntegrationMcp' -count=1
//
// A stage is dispatched through Manager.RunStage, so the config and the
// environment under test are the ones a real stage runs under. The model is
// the #1618 stub provider on 127.0.0.1, behind a recording front that keeps
// the system messages of every request in memory, so no hosted provider takes
// part and no request body is written anywhere.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/stubprovider"
)

// systemRecorder is an OpenAI-compatible endpoint: the stub provider serving
// its "slow" script with no delay, one final text reply, behind a front that
// records the system messages each request carries.
type systemRecorder struct {
	URL     string
	mu      sync.Mutex
	systems []string
}

func (r *systemRecorder) system() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.systems, "\n")
}

func startSystemRecorder(t *testing.T) *systemRecorder {
	t.Helper()
	zero := time.Duration(0)
	stub, err := stubprovider.NewServer(stubprovider.Config{Script: "slow", DelayOverride: &zero, MaxRequests: 20, IdleTimeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := stubprovider.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- stub.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("the stub provider stopped with %v", err)
		}
	})

	target, err := url.Parse("http://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	rec := &systemRecorder{}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var chat struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &chat) == nil {
			for _, m := range chat.Messages {
				if m.Role != "system" {
					continue
				}
				var text string
				if json.Unmarshal(m.Content, &text) != nil {
					var parts []struct {
						Text string `json:"text"`
					}
					_ = json.Unmarshal(m.Content, &parts)
					for _, p := range parts {
						text += p.Text
					}
				}
				rec.mu.Lock()
				rec.systems = append(rec.systems, text)
				rec.mu.Unlock()
			}
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		proxy.ServeHTTP(w, req)
	}))
	t.Cleanup(front.Close)
	rec.URL = front.URL + "/v1"
	return rec
}

// openCodeGitWorktree makes the issue-1612 worktree of a fresh workspace a
// clone whose origin's main holds files, and returns the workspace and the
// worktree.
func openCodeGitWorktree(t *testing.T, files map[string]string) (workspace, worktree string) {
	t.Helper()
	seed := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	for path, content := range files {
		full := filepath.Join(seed, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Run(t, seed, "add", "-A")
	gittest.Run(t, seed, "commit", "-qm", "base")
	origin := filepath.Join(t.TempDir(), "origin.git")
	gittest.Run(t, seed, "clone", "-q", "--bare", seed, origin)
	workspace = t.TempDir()
	parent := filepath.Join(workspace, ".nightgauge", "worktrees")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, parent, "clone", "-q", origin, "nightgauge-issue-1612")
	return workspace, filepath.Join(parent, "nightgauge-issue-1612")
}

// TestOpenCodeClaudeMdReachesSystemPrompt: in a repository whose only
// steering is a CLAUDE.md, a sentinel rule from it reaches the model's system
// prompt, although OPENCODE_DISABLE_CLAUDE_CODE_PROMPT hides the CLAUDE.md
// fallback from OpenCode, because the per-run config names the file as an
// instructions entry; the file it imports reaches the prompt too. In a
// repository with an AGENTS.md, which OpenCode also finds by itself while
// project config loads, the rule reaches the prompt once, because the entry
// names the same resolved path.
func TestOpenCodeClaudeMdReachesSystemPrompt(t *testing.T) {
	realOpenCode(t)
	for name, tc := range map[string]struct {
		files map[string]string
		file  string
	}{
		"CLAUDE.md only": {map[string]string{
			"CLAUDE.md":        "@AGENTS.md\n\n# Rules\n\nSENTINEL-7Q: every change carries a changelog entry. See @docs/imported.md for the rest\n",
			"docs/imported.md": "IMPORTED-SENTINEL-3K\n",
		}, "CLAUDE.md"},
		"AGENTS.md": {map[string]string{
			"AGENTS.md":        "# Rules\n\nSENTINEL-7Q: every change carries a changelog entry. See @docs/imported.md for the rest\n",
			"docs/imported.md": "IMPORTED-SENTINEL-3K\n",
		}, "AGENTS.md"},
	} {
		t.Run(name, func(t *testing.T) {
			isolateOpenCodeHome(t)
			t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
			rec := startSystemRecorder(t)
			writeOpenCodeMachineConfig(t, strings.Replace(openCodeMachineConfig, "http://127.0.0.1:1234/v1", rec.URL, 1))
			workspace, worktree := openCodeGitWorktree(t, tc.files)

			var result *adapters.RunResult
			var err error
			stderr := captureStderr(t, func() {
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				defer cancel()
				opts := openCodeStageOptions("lmstudio/stub/stub-model", nil)
				opts.Timeout = 120 * time.Second
				result, err = NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
			})
			if err != nil {
				t.Fatalf("RunStage: %v\n%s", err, stderr)
			}
			if result.ExitCode != 0 {
				t.Fatalf("the stage exited %d:\n%s", result.ExitCode, stderr)
			}
			system := rec.system()
			if system == "" {
				t.Fatalf("the stub provider recorded no system prompt, so the run never reached the model:\n%s", stderr)
			}
			resolved, err := filepath.EvalSymlinks(worktree)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(system, "SENTINEL-7Q") {
				t.Errorf("the sentinel rule from %s is not in the system prompt", tc.file)
			}
			if !strings.Contains(system, "Instructions from: "+filepath.Join(resolved, tc.file)) {
				t.Errorf("the system prompt does not load %s from the worktree", tc.file)
			}
			if !strings.Contains(system, "IMPORTED-SENTINEL-3K") {
				t.Error("the file the steering imports is not in the system prompt")
			}
			if !strings.Contains(system, "Nightgauge Pipeline Steering (OpenCode)") {
				t.Error("the baseline steering is not in the system prompt")
			}
			// Each model request loads the steering file once: the entry and
			// the file OpenCode finds itself name the same resolved path.
			requests := strings.Count(system, "Nightgauge Pipeline Steering (OpenCode)")
			if n := strings.Count(system, "Instructions from: "+filepath.Join(resolved, tc.file)+"\n"); n != requests {
				t.Errorf("%s is loaded %d times over %d request(s); want once per request", tc.file, n, requests)
			}
		})
	}
}

// TestOpenCodeIntegrationMcpFromBaseBranchReachesOpenCode: the real binary
// resolves the per-run config's mcp block, in the environment a stage runs
// in, to exactly the servers the forge serves for the run's repository (a
// fixture forge here) in the shape opencode 1.18.30's
// schema defines: a server the stage added to its worktree's .mcp.json is
// absent. OpenCode resolves the remote server's {env:VAR} credential in its
// own process, while OPENCODE_CONFIG_CONTENT, which every tool of the stage
// can read, holds only the reference. A server whose variable holds a
// backslash, which OpenCode would paste into the config text unescaped, is
// left out: with it the config would not parse, and OpenCode's error would
// print the credential. A shim runs `opencode debug config` in the stage's
// environment instead of the stage, so no server is started and no request
// is sent.
func TestOpenCodeIntegrationMcpFromBaseBranchReachesOpenCode(t *testing.T) {
	real := realOpenCode(t)
	isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	const token = "fake-mcp-credential-1626"
	t.Setenv("MCP_FIXTURE_TOKEN", token)
	t.Setenv("MCP_FIXTURE_PATH", `C:\fixture\home`)
	writeOpenCodeMachineConfig(t, strings.Replace(openCodeMachineConfig, "127.0.0.1:1234", "127.0.0.1:9", 1))
	out := openCodeMcpShim(t, real)

	servers := `{"mcpServers": {"a": {"command": "/usr/bin/true", "args": ["base"], "env": {"LEVEL": "debug"}}, "p": {"command": "/usr/bin/true", "env": {"HOME_DIR": "${MCP_FIXTURE_PATH}"}}, "r": {"type": "http", "url": "http://127.0.0.1:9/mcp", "headers": {"Authorization": "Bearer ${MCP_FIXTURE_TOKEN}"}}}}`
	workspace, worktree := openCodeGitWorktree(t, map[string]string{".mcp.json": servers})
	t.Cleanup(adapters.SwapOpenCodeMcpForgeForTest(openCodeMapForge{files: map[string]string{".mcp.json": servers}}))
	if err := os.WriteFile(filepath.Join(worktree, ".mcp.json"), []byte(strings.Replace(servers, `"r":`, `"evil": {"command": "/bin/sh"}, "r":`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	stderr := captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		opts := openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", nil)
		opts.Timeout = 120 * time.Second
		if _, err := NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts); err != nil {
			t.Fatalf("RunStage: %v", err)
		}
	})
	raw := readShimFile(t, out, "config.json")
	var cfg struct {
		MCP map[string]struct {
			Type        string            `json:"type"`
			Command     []string          `json:"command"`
			Environment map[string]string `json:"environment"`
			URL         string            `json:"url"`
			Headers     map[string]string `json:"headers"`
			OAuth       *bool             `json:"oauth"`
			Enabled     bool              `json:"enabled"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("`opencode debug config` in the stage's environment printed no config: %v\n%s\n%s", err, raw, readShimFile(t, out, "config.err"))
	}
	if len(cfg.MCP) != 2 {
		t.Errorf("OpenCode resolved %d MCP servers, want the forge's a and r: %+v", len(cfg.MCP), cfg.MCP)
	}
	if _, ok := cfg.MCP["evil"]; ok {
		t.Error("the server the stage added to its worktree reached OpenCode")
	}
	a := cfg.MCP["a"]
	if a.Type != "local" || strings.Join(a.Command, " ") != "/usr/bin/true base" || a.Environment["LEVEL"] != "debug" || !a.Enabled {
		t.Errorf("a resolved to %+v", a)
	}
	r := cfg.MCP["r"]
	if r.Type != "remote" || r.URL != "http://127.0.0.1:9/mcp" || r.OAuth == nil || *r.OAuth || !r.Enabled {
		t.Errorf("r resolved to %+v", r)
	}
	if r.Headers["Authorization"] != "Bearer "+token {
		t.Errorf("OpenCode did not resolve r's {env:MCP_FIXTURE_TOKEN} in its own process: %q", r.Headers["Authorization"])
	}
	if !strings.Contains(stderr, "not started: evil") {
		t.Errorf("stderr does not name the server left out:\n%s", stderr)
	}
	if !strings.Contains(stderr, `MCP server "p" is not started: the value of MCP_FIXTURE_PATH holds`) {
		t.Errorf("stderr does not name the server whose value OpenCode cannot paste:\n%s", stderr)
	}
	if strings.Contains(string(readShimFile(t, out, "config.err")), token) {
		t.Error("OpenCode's stderr holds the credential's value")
	}
	content := string(readShimFile(t, out, "content.json"))
	if !strings.Contains(content, `"Authorization":"Bearer {env:MCP_FIXTURE_TOKEN}"`) || strings.Contains(content, token) {
		t.Errorf("OPENCODE_CONFIG_CONTENT does not hold r's credential as a reference alone:\n%s", content)
	}
}

// openCodeMcpShim installs, first on PATH, an opencode that runs the real
// binary's `debug config` in the stage's environment, keeps the
// OPENCODE_CONFIG_CONTENT it was given, and exits 0 without running the
// stage. Any call other than `run`, such as the version preflight's
// `--version`, goes straight to the real binary (the same rule
// openCodeShim follows), so it cannot answer for the shim. It returns the
// directory it writes to.
func openCodeMcpShim(t *testing.T, real string) string {
	t.Helper()
	bin, out := t.TempDir(), t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
[ "$1" = run ] || exec "%[1]s" "$@"
"%[1]s" debug config < /dev/null > "%[2]s/config.json" 2> "%[2]s/config.err"
printf '%%s' "$OPENCODE_CONFIG_CONTENT" > "%[2]s/content.json"
cat > /dev/null
exit 0
`, real, out)
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return out
}
