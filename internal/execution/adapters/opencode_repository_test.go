package adapters

import (
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// What an OpenCode stage is given from its repository (ADR-022 § 8, § 11,
// § 15, #1626), through PrepareOpenCodeRun, the preparation the adapter and
// `nightgauge opencode config` share.

// openCodeFixtureRepo commits files as the base branch of a repository,
// publishes it as origin, and returns a clone: the worktree a stage runs in.
func openCodeFixtureRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	seed := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	for path, content := range files {
		writeRepoFile(t, filepath.Join(seed, path), content)
	}
	gittest.Run(t, seed, "add", "-A")
	gittest.Run(t, seed, "commit", "-qm", "base")
	origin := filepath.Join(t.TempDir(), "origin.git")
	gittest.Run(t, seed, "clone", "-q", "--bare", seed, origin)
	parent := t.TempDir()
	gittest.Run(t, parent, "clone", "-q", origin, "wt")
	return filepath.Join(parent, "wt")
}

func writeRepoFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// worktreeFiles maps every path under dir but .git to its mode and content.
func worktreeFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := info.Mode().String()
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			entry += "\n" + string(b)
		}
		out[p] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestPrepareOpenCodeRunGivesTheRepositorysSteeringAndMcp: in a repository
// whose only steering is a CLAUDE.md that imports a file, and whose base
// branch defines a local and a remote MCP server, a prepared run's config
// names CLAUDE.md, the file it imports and the run's own steering file as
// instructions, none a URL, and holds exactly the base branch's servers: a
// server the stage added to its worktree's .mcp.json is not there, and the
// remote server's credential is the reference {env:MCP_FIXTURE_TOKEN}, whose
// value is in neither the config nor the verb's JSON. Nothing is written into
// the worktree, and stderr says what the stage is given and what it is not.
func TestPrepareOpenCodeRunGivesTheRepositorysSteeringAndMcp(t *testing.T) {
	const token = "fake-mcp-credential-1626"
	t.Setenv("MCP_FIXTURE_TOKEN", token)
	wt := openCodeFixtureRepo(t, map[string]string{
		"CLAUDE.md":        "@AGENTS.md\n\n# Rules\n\nSENTINEL-7Q: every change carries a changelog entry. See @docs/imported.md for more\n",
		"docs/imported.md": "IMPORTED-RULE\n",
		".mcp.json":        `{"mcpServers": {"a": {"command": "/usr/bin/true"}, "r": {"type": "http", "url": "https://mcp.example.test/mcp", "headers": {"Authorization": "Bearer ${MCP_FIXTURE_TOKEN}"}}}}`,
	})
	writeRepoFile(t, filepath.Join(wt, ".mcp.json"), `{"mcpServers": {"a": {"command": "/usr/bin/true"}, "r": {"type": "http", "url": "https://mcp.example.test/mcp", "headers": {"Authorization": "Bearer ${MCP_FIXTURE_TOKEN}"}}, "evil": {"command": "/bin/sh", "args": ["-c", "exit 0"]}}}`)
	before := worktreeFiles(t, wt)

	home := t.TempDir()
	var run *OpenCodeRun
	var err error
	stderr := captureAdapterStderr(t, func() {
		run, err = PrepareOpenCodeRun(OpenCodeRunRequest{
			Home:               home,
			ID:                 testRunID,
			MachineConfigDir:   filepath.Join(home, ".nightgauge"),
			Run:                RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b", WorktreeDir: wt},
			Settings:           lmStudioSettings(),
			Lookup:             envLookup(nil),
			GOOS:               "linux",
			ManagedConfigFiles: []string{},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Instructions []string                  `json:"instructions"`
		MCP          map[string]map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(run.ConfigContent), &cfg); err != nil {
		t.Fatal(err)
	}

	root, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	steeringFile := filepath.Join(run.RunDir, "nightgauge", "steering.md")
	want := []string{filepath.Join(root, "CLAUDE.md"), filepath.Join(root, "docs", "imported.md"), steeringFile}
	if !slices.Equal(cfg.Instructions, want) {
		t.Errorf("instructions = %q\nwant %q", cfg.Instructions, want)
	}
	url := regexp.MustCompile(`^[a-z]+://`)
	for _, entry := range cfg.Instructions {
		if url.MatchString(entry) {
			t.Errorf("instructions entry %q is a URL", entry)
		}
	}
	fi, err := os.Lstat(steeringFile)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the steering file %s: %v, %v; want a regular 0600 file", steeringFile, fi, err)
	}
	steering, _ := os.ReadFile(steeringFile)
	for _, w := range []string{"# Nightgauge Pipeline Steering (OpenCode)", "SENTINEL-7Q", "Never push directly to main"} {
		if !strings.Contains(string(steering), w) {
			t.Errorf("the steering file does not hold %q:\n%s", w, steering)
		}
	}

	if names := slices.Sorted(maps.Keys(cfg.MCP)); !slices.Equal(names, []string{"a", "r"}) {
		t.Errorf("mcp servers = %v, want exactly the base branch's a and r", names)
	}
	if got := cfg.MCP["r"]["headers"].(map[string]any)["Authorization"]; got != "Bearer {env:MCP_FIXTURE_TOKEN}" {
		t.Errorf("r's Authorization = %v, want the reference Bearer {env:MCP_FIXTURE_TOKEN}", got)
	}
	verb, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(verb), token) || strings.Contains(string(steering), token) {
		t.Error("the credential's value is in the verb's output or the steering file")
	}

	if after := worktreeFiles(t, wt); !maps.Equal(before, after) {
		t.Errorf("preparing the run changed the worktree:\nbefore %v\nafter  %v", slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
	}
	for _, w := range []string{"the repository's CLAUDE.md and 1 file(s) it imports", "the MCP servers origin/main defines: a, r", "only the worktree defines, not origin/main, are not started: evil"} {
		if !strings.Contains(stderr, w) {
			t.Errorf("stderr does not say %q:\n%s", w, stderr)
		}
	}
}

// TestPrepareOpenCodeRunRefusesADispatchWithoutAWorktree: the repository's
// steering and MCP servers are read from the stage's worktree, so a dispatch
// with none is refused before anything is created, rather than run with
// neither.
func TestPrepareOpenCodeRunRefusesADispatchWithoutAWorktree(t *testing.T) {
	home := t.TempDir()
	run, err := PrepareOpenCodeRun(OpenCodeRunRequest{
		Home:               home,
		ID:                 testRunID,
		MachineConfigDir:   filepath.Join(home, ".nightgauge"),
		Run:                RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b"},
		Settings:           lmStudioSettings(),
		Lookup:             envLookup(nil),
		GOOS:               "linux",
		ManagedConfigFiles: []string{},
	})
	if err == nil || !strings.Contains(err.Error(), "no worktree") {
		t.Fatalf("PrepareOpenCodeRun without a worktree = %+v, %v; want a refusal naming the missing worktree", run, err)
	}
	if _, statErr := os.Lstat(OpenCodeRunsDir(home)); !os.IsNotExist(statErr) {
		t.Errorf("the refused dispatch created %s", OpenCodeRunsDir(home))
	}
}

// TestPrepareOpenCodeRunLeavesOutAnMcpValueOpenCodeCannotPaste: OpenCode
// pastes a {env:VAR}'s value into its config text unescaped, so the value of
// every variable an MCP server names is checked in the environment the
// dispatch is prepared against (req.Lookup). A remote server whose credential
// variable holds a backslash is left out of the config, which would otherwise
// fail to parse and print the credentials in it, and stderr names the server
// and the variable, never the value.
func TestPrepareOpenCodeRunLeavesOutAnMcpValueOpenCodeCannotPaste(t *testing.T) {
	const value = `fixture\credential`
	wt := openCodeFixtureRepo(t, map[string]string{
		".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}, "r": {"type": "http", "url": "https://mcp.example.test/mcp", "headers": {"Authorization": "Bearer ${MCP_FIXTURE_TOKEN}"}}}}`,
	})
	home := t.TempDir()
	var run *OpenCodeRun
	var err error
	stderr := captureAdapterStderr(t, func() {
		run, err = PrepareOpenCodeRun(OpenCodeRunRequest{
			Home:               home,
			ID:                 testRunID,
			MachineConfigDir:   filepath.Join(home, ".nightgauge"),
			Run:                RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b", WorktreeDir: wt},
			Settings:           lmStudioSettings(),
			Lookup:             envLookup(map[string]string{"MCP_FIXTURE_TOKEN": value}),
			GOOS:               "linux",
			ManagedConfigFiles: []string{},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCP map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(run.ConfigContent), &cfg); err != nil {
		t.Fatal(err)
	}
	if names := slices.Sorted(maps.Keys(cfg.MCP)); !slices.Equal(names, []string{"a"}) {
		t.Errorf("mcp servers = %v, want a alone: r's variable holds a backslash", names)
	}
	if !strings.Contains(stderr, `MCP server "r" is not started: the value of MCP_FIXTURE_TOKEN holds`) {
		t.Errorf("stderr does not name the server and the variable:\n%s", stderr)
	}
	if strings.Contains(stderr, value) {
		t.Errorf("stderr quotes the variable's value:\n%s", stderr)
	}
}
