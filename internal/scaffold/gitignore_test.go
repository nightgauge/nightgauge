package scaffold

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// The embedded template is the repository's own committed copy. The
// extension's ensureGitignore.test.ts pins GITIGNORE_CONTENT to this same
// template file, so a change on either side alone turns one of them red.
func TestGitignoreTemplateMatchesCommittedCopy(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join("..", "..", ".nightgauge", ".gitignore"))
	if err != nil {
		t.Fatalf("read committed .nightgauge/.gitignore: %v", err)
	}
	if string(committed) != GitignoreTemplate {
		t.Fatal("internal/scaffold/nightgauge.gitignore differs from .nightgauge/.gitignore; " +
			"change the template, the extension's GITIGNORE_CONTENT and the committed copy together")
	}
	if GitignoreVersionMarker() == "" {
		t.Fatal("template carries no nightgauge-gitignore-version marker")
	}
	if !strings.Contains(GitignoreTemplate, LocalAdditionsMarker) {
		t.Fatal("template carries no Local additions marker")
	}
}

func initializedRepo(t *testing.T) string {
	t.Helper()
	root := gittest.InitRepo(t, t.TempDir(), "-q")
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"), []byte("owner: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func commitAll(t *testing.T, root string) {
	t.Helper()
	gittest.Run(t, root, "add", "-A")
	gittest.Run(t, root, "commit", "-q", "-m", "init")
}

func writeRuntimeExhaust(t *testing.T, root string) {
	t.Helper()
	for _, rel := range []string{
		".nightgauge/logs/serve.log",
		".nightgauge/pipeline/issue-7.json",
		".nightgauge/pipeline/history/x.jsonl",
		".nightgauge/complexity-model.lock",
		".nightgauge/attention/card.json",
		".nightgauge/plans/7-x.md",
		".nightgauge/worktrees/issue-7/file.go",
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ensure(t *testing.T, root string, want IgnoreAction) IgnoreResult {
	t.Helper()
	res, err := EnsureIgnoreRules(root)
	if err != nil {
		t.Fatalf("EnsureIgnoreRules: %v", err)
	}
	if res.Action != want {
		t.Fatalf("action = %q (%s), want %q", res.Action, res.Note, want)
	}
	return res
}

// #2042: the knowledge tree is committed by default. A scaffolded PRD shows as a
// new file to commit, the derived recall cache stays ignored, and a team opts
// out in its root .gitignore.
func TestKnowledgeTreeIsCommittedByDefault(t *testing.T) {
	root := initializedRepo(t)
	ensure(t, root, IgnoreCreated)
	kdir := filepath.Join(root, ".nightgauge", "knowledge", "features", "7-x")
	cache := filepath.Join(root, ".nightgauge", "knowledge", ".recall-cache")
	for _, d := range []string{kdir, cache} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(kdir, "PRD.md"), filepath.Join(cache, "index.jsonl")} {
		if err := os.WriteFile(f, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	status := func() string {
		return gittest.Run(t, root, "status", "--porcelain", "--untracked-files=all")
	}
	got := status()
	if !strings.Contains(got, ".nightgauge/knowledge/features/7-x/PRD.md") {
		t.Fatalf("PRD.md is ignored, want it shown as a new file:\n%s", got)
	}
	if strings.Contains(got, ".recall-cache") {
		t.Fatalf("recall cache is not ignored:\n%s", got)
	}

	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/.nightgauge/knowledge/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := status(); strings.Contains(got, "knowledge/") {
		t.Fatalf("root .gitignore opt-out did not hide the knowledge tree:\n%s", got)
	}
}

func TestEnsureIgnoreRules(t *testing.T) {
	t.Run("untracked: writes the template, then does nothing", func(t *testing.T) {
		root := initializedRepo(t)
		ignorePath := filepath.Join(root, ".nightgauge", ".gitignore")

		ensure(t, root, IgnoreCreated)
		if got := read(t, ignorePath); got != GitignoreTemplate {
			t.Fatal("written .nightgauge/.gitignore differs from the embedded template")
		}
		for _, sub := range gitkeepDirs {
			if _, err := os.Stat(filepath.Join(root, ".nightgauge", sub, ".gitkeep")); err != nil {
				t.Errorf("missing %s/.gitkeep: %v", sub, err)
			}
		}

		writeRuntimeExhaust(t, root)
		status := gittest.Run(t, root, "status", "--porcelain", "--untracked-files=all")
		for _, line := range strings.Split(status, "\n") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "??"))
			switch path {
			case ".nightgauge/config.yaml", ".nightgauge/.gitignore",
				".nightgauge/pipeline/.gitkeep", ".nightgauge/pipeline/history/.gitkeep",
				".nightgauge/plans/.gitkeep", ".nightgauge/logs/.gitkeep":
			default:
				t.Errorf("runtime path not ignored: %q", path)
			}
		}

		ensure(t, root, IgnoreCurrent)
		if got := read(t, ignorePath); got != GitignoreTemplate {
			t.Fatal("second call changed the file")
		}
		if _, err := os.Stat(filepath.Join(root, ".git", "info", "exclude")); err == nil {
			if strings.Contains(read(t, filepath.Join(root, ".git", "info", "exclude")), ExcludeBlockID) {
				t.Fatal("untracked case wrote an info/exclude block")
			}
		}
	})

	t.Run("tracked and older: committed file untouched, rules in info/exclude", func(t *testing.T) {
		root := initializedRepo(t)
		ignorePath := filepath.Join(root, ".nightgauge", ".gitignore")
		committed := "# nightgauge-gitignore-version: 10\n/knowledge/\n!/knowledge/\n"
		if err := os.WriteFile(ignorePath, []byte(committed), 0o644); err != nil {
			t.Fatal(err)
		}
		excludePath := filepath.Join(root, ".git", "info", "exclude")
		if err := os.WriteFile(excludePath, []byte("# operator's own rule\n*.swp\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commitAll(t, root)

		res := ensure(t, root, IgnoreDeferred)
		if got := read(t, ignorePath); got != committed {
			t.Fatal("tracked .nightgauge/.gitignore was edited")
		}
		if res.Path != excludePath {
			resolved, _ := filepath.EvalSymlinks(res.Path)
			want, _ := filepath.EvalSymlinks(excludePath)
			if resolved != want {
				t.Fatalf("Path = %q, want %q", res.Path, excludePath)
			}
		}
		exclude := read(t, excludePath)
		if !strings.HasPrefix(exclude, "# operator's own rule\n*.swp\n") {
			t.Fatalf("operator's info/exclude lines were not kept:\n%s", exclude)
		}
		if !strings.Contains(exclude, excludeBlockBegin(ExcludeBlockID)) ||
			!strings.Contains(exclude, "/.nightgauge/pipeline/*\n") {
			t.Fatalf("info/exclude lacks the rules block:\n%s", exclude)
		}

		writeRuntimeExhaust(t, root)
		if status := gittest.Run(t, root, "status", "--porcelain", "--untracked-files=all"); status != "" {
			t.Fatalf("checkout not clean:\n%s", status)
		}

		ensure(t, root, IgnoreDeferred)
		if got := read(t, excludePath); got != exclude {
			t.Fatal("second call changed info/exclude")
		}
		if got := read(t, ignorePath); got != committed {
			t.Fatal("second call edited the tracked file")
		}
	})

	t.Run("linked worktree: rules go to the common dir's info/exclude", func(t *testing.T) {
		root := initializedRepo(t)
		ignorePath := filepath.Join(root, ".nightgauge", ".gitignore")
		if err := os.WriteFile(ignorePath, []byte("# nightgauge-gitignore-version: 10\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commitAll(t, root)
		wt := filepath.Join(t.TempDir(), "wt")
		gittest.Run(t, root, "worktree", "add", "-q", wt)

		ensure(t, wt, IgnoreDeferred)
		if !strings.Contains(read(t, filepath.Join(root, ".git", "info", "exclude")), excludeBlockBegin(ExcludeBlockID)) {
			t.Fatal("block not written to the common dir's info/exclude")
		}
		writeRuntimeExhaust(t, wt)
		if status := gittest.Run(t, wt, "status", "--porcelain", "--untracked-files=all"); status != "" {
			t.Fatalf("worktree not clean:\n%s", status)
		}
	})

	t.Run("untracked and older: rewritten, local additions kept", func(t *testing.T) {
		root := initializedRepo(t)
		ignorePath := filepath.Join(root, ".nightgauge", ".gitignore")
		old := strings.Replace(GitignoreTemplate, GitignoreVersionMarker(), "# nightgauge-gitignore-version: 1", 1) +
			"!/knowledge/\n"
		if err := os.WriteFile(ignorePath, []byte(old), 0o644); err != nil {
			t.Fatal(err)
		}
		ensure(t, root, IgnoreUpdated)
		if got := read(t, ignorePath); got != GitignoreTemplate+"!/knowledge/\n" {
			t.Fatalf("rewrite lost local additions:\n%s", got)
		}
		ensure(t, root, IgnoreCurrent)
	})

	t.Run("outside a git work tree: writes nothing", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		res := ensure(t, root, IgnoreSkipped)
		if !strings.Contains(res.Note, "not a git work tree") {
			t.Fatalf("note = %q", res.Note)
		}
		if _, err := os.Stat(filepath.Join(root, ".nightgauge", ".gitignore")); !os.IsNotExist(err) {
			t.Fatal("wrote .gitignore outside a git work tree")
		}
	})

	t.Run("not initialized: writes nothing", func(t *testing.T) {
		root := gittest.InitRepo(t, t.TempDir(), "-q")
		ensure(t, root, IgnoreSkipped)
		if _, err := os.Stat(filepath.Join(root, ".nightgauge")); !os.IsNotExist(err) {
			t.Fatal("created .nightgauge/ in a repository nobody onboarded")
		}
	})

	t.Run("symlinked info/exclude is refused", func(t *testing.T) {
		root := initializedRepo(t)
		if err := os.WriteFile(filepath.Join(root, ".nightgauge", ".gitignore"), []byte("# old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commitAll(t, root)
		outside := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.WriteFile(outside, []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		excludePath := filepath.Join(root, ".git", "info", "exclude")
		_ = os.Remove(excludePath)
		if err := os.Symlink(outside, excludePath); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureIgnoreRules(root); err == nil || !strings.Contains(err.Error(), "symlinked") {
			t.Fatalf("err = %v, want a symlink refusal", err)
		}
		if got := read(t, outside); got != "keep\n" {
			t.Fatal("wrote through the symlink")
		}
	})
}

// The same inputs the extension's toRootPatterns test uses, so the two ports
// are held to one expected output.
func TestToRootPatterns(t *testing.T) {
	got := ToRootPatterns(".nightgauge", "# c\n/knowledge/\npipeline/*\n!pipeline/.gitkeep\n*.tmp\n")
	want := []string{
		"/.nightgauge/knowledge/",
		"/.nightgauge/pipeline/*",
		"!/.nightgauge/pipeline/.gitkeep",
		"/.nightgauge/**/*.tmp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestReplaceBlock(t *testing.T) {
	first := replaceBlock("*.swp", "x", []string{"/a"})
	if want := "*.swp\n" + excludeBlockBegin("x") + "\n/a\n# nightgauge:end x\n"; first != want {
		t.Fatalf("append: got %q, want %q", first, want)
	}
	second := replaceBlock(first+"tail\n", "x", []string{"/b"})
	if want := "*.swp\n" + excludeBlockBegin("x") + "\n/b\n# nightgauge:end x\ntail\n"; second != want {
		t.Fatalf("replace: got %q, want %q", second, want)
	}
	if again := replaceBlock(second, "x", []string{"/b"}); again != second {
		t.Fatal("replacing with the same patterns changed the content")
	}
}

// Both writers must delimit the info/exclude block identically, or each
// would append its own copy beside the other's.
func TestExcludeBlockDelimitersMatchExtension(t *testing.T) {
	src := read(t, filepath.Join("..", "..", "packages", "nightgauge-vscode", "src", "utils", "localGitExclude.ts"))
	for _, want := range []string{
		"`" + strings.Replace(excludeBlockBegin("X"), "X", "${id}", 1) + "`",
		"`" + strings.Replace(excludeBlockEnd("X"), "X", "${id}", 1) + "`",
		"`# nightgauge:begin ${id} `",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("localGitExclude.ts no longer contains %s", want)
		}
	}
}

func withVersion(v string) string {
	return strings.Replace(GitignoreTemplate, GitignoreVersionMarker(), "# nightgauge-gitignore-version: "+v, 1)
}

// A newer file or block is a newer writer's, never downgraded by this one.
func TestEnsureIgnoreRulesVersionOrdering(t *testing.T) {
	newer := strconv.Itoa(GitignoreVersion() + 1)

	t.Run("newer untracked file is left alone", func(t *testing.T) {
		root := initializedRepo(t)
		ignorePath := filepath.Join(root, ".nightgauge", ".gitignore")
		body := withVersion(newer) + "/from-the-future/\n"
		if err := os.WriteFile(ignorePath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		res := ensure(t, root, IgnoreCurrent)
		if res.Changed || !strings.Contains(res.Note, "newer") {
			t.Fatalf("res = %+v", res)
		}
		if read(t, ignorePath) != body {
			t.Fatal("a newer file was rewritten")
		}
	})

	t.Run("newer info/exclude block is left alone", func(t *testing.T) {
		root := initializedRepo(t)
		if err := os.WriteFile(filepath.Join(root, ".nightgauge", ".gitignore"), []byte("# nightgauge-gitignore-version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commitAll(t, root)
		excludePath := filepath.Join(root, ".git", "info", "exclude")
		future := replaceBlock("", ExcludeBlockID, []string{"# nightgauge-gitignore-version: " + newer, "/.nightgauge/x/"})
		if err := os.WriteFile(excludePath, []byte(future), 0o644); err != nil {
			t.Fatal(err)
		}
		res := ensure(t, root, IgnoreDeferred)
		if res.Changed || read(t, excludePath) != future {
			t.Fatalf("a newer block was replaced: %+v", res)
		}
	})

	t.Run("older block is replaced and carries the version", func(t *testing.T) {
		root := initializedRepo(t)
		if err := os.WriteFile(filepath.Join(root, ".nightgauge", ".gitignore"), []byte("# nightgauge-gitignore-version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commitAll(t, root)
		excludePath := filepath.Join(root, ".git", "info", "exclude")
		// A block from a writer before blocks carried a version: unparseable.
		if err := os.WriteFile(excludePath, []byte(replaceBlock("", ExcludeBlockID, []string{"/.nightgauge/old/"})), 0o644); err != nil {
			t.Fatal(err)
		}
		if res := ensure(t, root, IgnoreDeferred); !res.Changed {
			t.Fatal("older block not replaced")
		}
		block, err := ReadExcludeBlock(root, ExcludeBlockID)
		if err != nil || len(block) == 0 || block[0] != GitignoreVersionMarker() {
			t.Fatalf("block = %q, %v", block, err)
		}
		if strings.Contains(read(t, excludePath), "/.nightgauge/old/") {
			t.Fatal("old block survived")
		}
	})

	t.Run("unparseable marker is treated as older", func(t *testing.T) {
		root := initializedRepo(t)
		ignorePath := filepath.Join(root, ".nightgauge", ".gitignore")
		if err := os.WriteFile(ignorePath, []byte(withVersion("x1")), 0o644); err != nil {
			t.Fatal(err)
		}
		ensure(t, root, IgnoreUpdated)
		if read(t, ignorePath) != GitignoreTemplate {
			t.Fatal("unparseable marker not rewritten to the template")
		}
	})
}

// #4 of the review: a hand-extended untracked file with no Local additions
// marker kept nothing on rewrite.
func TestEnsureIgnoreRulesCarriesCustomRules(t *testing.T) {
	root := initializedRepo(t)
	ignorePath := filepath.Join(root, ".nightgauge", ".gitignore")
	old := "# nightgauge-gitignore-version: 3\npipeline/*\n!/release-watch/\n/my-own/\n# a comment\n*.bak\n"
	if err := os.WriteFile(ignorePath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	res := ensure(t, root, IgnoreUpdated)
	if !reflect.DeepEqual(res.Carried, []string{"/my-own/", "*.bak"}) {
		t.Fatalf("carried = %q", res.Carried)
	}
	if got := read(t, ignorePath); got != GitignoreTemplate+"/my-own/\n*.bak\n" {
		t.Fatalf("rewrite:\n%s", got)
	}
	// Idempotent: the carried rules are now local additions.
	ensure(t, root, IgnoreCurrent)

	// A marker-bearing file: rules above the marker are carried after the
	// existing local additions, without duplicates.
	withMarker := strings.Replace(withVersion("3"), "/improvement-runs/\n", "/improvement-runs/\n/above/\n/dup/\n", 1) +
		"/below/\n/dup/\n"
	if err := os.WriteFile(ignorePath, []byte(withMarker), 0o644); err != nil {
		t.Fatal(err)
	}
	res = ensure(t, root, IgnoreUpdated)
	if got := read(t, ignorePath); got != GitignoreTemplate+"/below/\n/dup/\n/above/\n" {
		t.Fatalf("rewrite with marker:\n%s (carried %q)", got, res.Carried)
	}
}

func TestExcludeBlockCRLF(t *testing.T) {
	crlf := "*.swp\r\n" + excludeBlockBegin("x") + "\r\n# nightgauge-gitignore-version: 9\r\n/a\r\n" + excludeBlockEnd("x") + "\r\ntail\r\n"
	if got := blockLines(crlf, "x"); !reflect.DeepEqual(got, []string{"# nightgauge-gitignore-version: 9", "/a"}) {
		t.Fatalf("blockLines = %q", got)
	}
	next := replaceBlock(crlf, "x", []string{"/b"})
	if strings.Count(next, "# nightgauge:begin x ") != 1 || !strings.Contains(next, "/b\n") || strings.Contains(next, "/a\r") {
		t.Fatalf("CRLF block not replaced in place:\n%q", next)
	}
}
