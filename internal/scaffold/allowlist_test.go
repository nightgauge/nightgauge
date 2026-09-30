package scaffold

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// committedPaths are one path per entry of the ADR-024 § 13 allowlist. Each
// must stay visible to git under the template.
var committedPaths = []string{
	".nightgauge/config.yaml",
	".nightgauge/config.schema.json",
	".nightgauge/pattern-mining-config.yaml",
	".nightgauge/.gitignore",
	".nightgauge/audit/r.md",
	".nightgauge/skill-smoke/s.json",
	".nightgauge/skill-evals/baseline.jsonl",
	".nightgauge/model-evals/evidence/run.jsonl",
}

// ignoredPaths must stay out of git: legacy per-clone data that may not have
// migrated yet, a path no rule has ever named, the per-clone config, the
// knowledge tree (ignored unless the team opts in), and the non-allowlisted
// neighbours inside allowlisted directories.
var ignoredPaths = []string{
	".nightgauge/pipeline/x",
	".nightgauge/pipeline/history/x.jsonl",
	".nightgauge/plans/7-x.md",
	".nightgauge/logs/serve.log",
	".nightgauge/worktrees/issue-7/file.go",
	".nightgauge/daemon.sock",
	".nightgauge/knowledge/.recall-cache/index.db",
	".nightgauge/anything-new",
	".nightgauge/config.local.yaml",
	".nightgauge/knowledge/features/1-x/PRD.md",
	".nightgauge/audit/scope-drift-stats.json",
	".nightgauge/skill-evals/run-1.jsonl",
	".nightgauge/model-evals/run.jsonl",
	".nightgauge/model-evals/routing-advice.json",
}

// ignoredOf asks git which of paths it ignores in the repository at root. The
// paths need not exist; check-ignore evaluates each leading directory too.
func ignoredOf(t *testing.T, root string, paths []string) map[string]bool {
	t.Helper()
	cmd := gittest.Command(root, append([]string{"check-ignore", "--"}, paths...)...)
	out, err := cmd.Output()
	var exit *exec.ExitError
	// Exit 1 means none of the paths is ignored; anything else is a failure.
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		t.Fatalf("git check-ignore: %v", err)
	}
	got := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			got[l] = true
		}
	}
	return got
}

func allPaths() []string { return append(append([]string{}, committedPaths...), ignoredPaths...) }

// verdictErrors lists every path whose ignore state differs from the
// allowlist's expectation.
func verdictErrors(ignored map[string]bool) []string {
	var bad []string
	for _, p := range committedPaths {
		if ignored[p] {
			bad = append(bad, p+" is ignored, want committed")
		}
	}
	for _, p := range ignoredPaths {
		if !ignored[p] {
			bad = append(bad, p+" is not ignored")
		}
	}
	return bad
}

func writeIgnoreFile(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".nightgauge", ".gitignore"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// templateRules returns the template's rule lines above Local additions, in
// order, each with the template text that carries it.
func templateRules(t *testing.T) []string {
	t.Helper()
	at := strings.Index(GitignoreTemplate, LocalAdditionsMarker)
	if at < 0 {
		t.Fatal("template carries no Local additions marker")
	}
	return ruleLines(GitignoreTemplate[:at])
}

// #2043, ADR-024 § 13: the template is deny-by-default. `/*` ignores
// everything under .nightgauge/ and `!` rules re-include exactly the committed
// allowlist.
func TestTemplateAllowlist(t *testing.T) {
	t.Run("template: allowlist committed, everything else ignored", func(t *testing.T) {
		root := initializedRepo(t)
		ensure(t, root, IgnoreCreated)
		if bad := verdictErrors(ignoredOf(t, root, allPaths())); len(bad) > 0 {
			t.Fatalf("template verdicts:\n  %s", strings.Join(bad, "\n  "))
		}
		if rules := templateRules(t); len(rules) == 0 || rules[0] != "/*" {
			t.Fatalf("first rule = %q, want /* (deny by default)", rules)
		}
	})

	// Every rule decides at least one case, so dropping any `!` rule (or any
	// re-ignore beneath one) turns its case red, and no dead rule survives.
	t.Run("every rule is load-bearing", func(t *testing.T) {
		root := initializedRepo(t)
		for _, rule := range templateRules(t) {
			var kept []string
			for _, l := range strings.Split(GitignoreTemplate, "\n") {
				if strings.TrimSpace(l) != rule {
					kept = append(kept, l)
				}
			}
			writeIgnoreFile(t, root, strings.Join(kept, "\n"))
			if bad := verdictErrors(ignoredOf(t, root, allPaths())); len(bad) == 0 {
				t.Errorf("dropping %q changes no verdict: the rule is dead or a case is missing", rule)
			}
		}
	})

	t.Run("Local additions opt-in commits the knowledge tree", func(t *testing.T) {
		root := initializedRepo(t)
		writeIgnoreFile(t, root, GitignoreTemplate+"!/knowledge/\n")
		prd := ".nightgauge/knowledge/features/1-x/PRD.md"
		got := ignoredOf(t, root, []string{prd, ".nightgauge/anything-new"})
		if got[prd] || !got[".nightgauge/anything-new"] {
			t.Fatalf("with !/knowledge/ under Local additions: ignored = %v", got)
		}
	})

	// A committed older file defers the rules to info/exclude, re-anchored at
	// the repository root. The verdicts must be the same.
	t.Run("info/exclude form gives the same verdicts", func(t *testing.T) {
		root := initializedRepo(t)
		writeIgnoreFile(t, root, "# nightgauge-gitignore-version: 1\n")
		commitAll(t, root)
		ensure(t, root, IgnoreDeferred)
		ignored := ignoredOf(t, root, allPaths())
		// The tracked config.yaml and .gitignore are committed regardless.
		if bad := verdictErrors(ignored); len(bad) > 0 {
			t.Fatalf("info/exclude verdicts:\n  %s", strings.Join(bad, "\n  "))
		}
	})
}

// buildBinary compiles cmd/nightgauge into a temporary directory.
func buildBinary(t *testing.T) string {
	t.Helper()
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "nightgauge")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", bin, "./cmd/nightgauge")
	cmd.Dir = moduleRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/nightgauge: %v\n%s", err, out)
	}
	return bin
}

// hermeticEnv is the git-isolated test environment with every Nightgauge,
// GitHub and XDG variable from the caller's shell removed and every machine
// root pointed into scratch, so the binary reads and writes nothing of the
// operator's.
func hermeticEnv(scratch string) []string {
	var env []string
	for _, kv := range gittest.Env() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "NIGHTGAUGE_"), strings.HasPrefix(k, "XDG_"),
			strings.HasPrefix(k, "GH_"), strings.HasPrefix(k, "GITHUB_"),
			k == "HOME", k == "USERPROFILE", k == "APPDATA", k == "LOCALAPPDATA", k == "CI":
			continue
		}
		env = append(env, kv)
	}
	dir := func(name string) string { return filepath.Join(scratch, name) }
	return append(env,
		"HOME="+dir("home"), "USERPROFILE="+dir("home"),
		"APPDATA="+dir("appdata"), "LOCALAPPDATA="+dir("localappdata"),
		"XDG_CONFIG_HOME="+dir("xdg-config"), "XDG_STATE_HOME="+dir("xdg-state"),
		"XDG_CACHE_HOME="+dir("xdg-cache"), "XDG_RUNTIME_DIR="+dir("xdg-runtime"),
		"NIGHTGAUGE_CONFIG_HOME="+dir("config"), "NIGHTGAUGE_STATE_HOME="+dir("state"),
		"NIGHTGAUGE_CACHE_HOME="+dir("cache"), "NIGHTGAUGE_RUNTIME_DIR="+dir("runtime"),
	)
}

// porcelain is `git status --porcelain --untracked-files=all` as paths.
func porcelain(t *testing.T, dir string) []string {
	t.Helper()
	out, err := gittest.Command(dir, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	var paths []string
	for _, l := range strings.Split(string(out), "\n") {
		if len(l) > 3 {
			paths = append(paths, l[3:])
		}
	}
	sort.Strings(paths)
	return paths
}

// #2043: a clone driven only by the CLI stays clean. After `config init` and
// the commands that write state, git sees only allowlisted files; once those
// are committed, running the commands again leaves no change at all.
func TestCLIOnlyCloneStaysClean(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the nightgauge binary")
	}
	bin := buildBinary(t)
	scratch := t.TempDir()
	for _, d := range []string{"home", "appdata", "localappdata", "origin"} {
		if err := os.MkdirAll(filepath.Join(scratch, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := hermeticEnv(scratch)

	origin := gittest.InitRepo(t, filepath.Join(scratch, "origin"), "-q")
	gittest.Run(t, origin, "commit", "-q", "--allow-empty", "-m", "init")
	clone := filepath.Join(scratch, "clone")
	gittest.Run(t, scratch, "clone", "-q", origin, clone)

	run := func(stdin string, args ...string) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = clone
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("nightgauge %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	stateCommands := func() {
		t.Helper()
		run("", "focus", "set", "quality")
		run("", "careful", "on")
		run("", "outcome", "init")
		run("", "knowledge", "new", "runbook", "deploy")
		run("", "knowledge", "index")
		run("# plan\n", "layout", "write", "plans", "7-x.md")
		run("{}\n", "layout", "write", "pipeline", "issue-7.json")
		run("{}\n", "layout", "append", "logs", "7_session.log")
	}

	run("", "config", "init", "--owner", "acme", "--out", filepath.Join(".nightgauge", "config.yaml"))
	stateCommands()

	got := porcelain(t, clone)
	want := []string{".nightgauge/.gitignore", ".nightgauge/config.yaml"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("git status after config init and state-writing commands:\n  %s\nwant only:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	// No command appends its own rules to a deny-by-default file.
	if got := read(t, filepath.Join(clone, ".nightgauge", ".gitignore")); got != GitignoreTemplate {
		t.Fatalf(".nightgauge/.gitignore changed after the state-writing commands:\n%s", got)
	}

	gittest.Run(t, clone, "add", "-A")
	gittest.Run(t, clone, "commit", "-q", "-m", "nightgauge config")
	stateCommands()
	if got := porcelain(t, clone); len(got) > 0 {
		t.Fatalf("second round of commands dirtied the committed clone:\n  %s", strings.Join(got, "\n  "))
	}
}
