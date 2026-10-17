package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sharedScope is a workspace whose root is the acme/api checkout, holding
// what an operator's machine holds besides the tracked tree: ignored and
// untracked files, .nightgauge runtime state (one file of it force-added), a
// bare repository, a nested clone of another repository (acme/secret), and
// symlinks, tracked and not, that lead out of the repository.
type sharedScope struct {
	root, nested string
	// leaks are the contents no turn may be shown unless acme/secret is
	// readable to it.
	leaks []string
}

func newSharedScope(t *testing.T) sharedScope {
	t.Helper()
	root := gitRepo(t)
	outside := t.TempDir()
	write(t, outside, "secret.txt", "OUTSIDE-ROOT-CONTENT\n")
	write(t, root, ".gitignore", ".env\n*.env\nlocal/\nvendor/\n*.git/\n")
	write(t, root, ".nightgauge/.gitignore", "/*\n!/config.yaml\n!/.gitignore\n")
	write(t, root, "docs/guide.md", "A tracked guide.\n")
	for link, target := range map[string]string{
		"docs/to-readme":  "../README.md",
		"docs/to-local":   "../local/notes.txt",
		"docs/to-vendor":  "../vendor/secret/PLAN.md",
		"docs/to-outside": filepath.Join(outside, "secret.txt"),
		"docs/to-git":     "../.git/config",
		"docs/to-handoff": "../.nightgauge/session-handoff.md",
		"docs/to-nowhere": "../vendor/secret/no-such-file.md",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	// Tracked although the repository's own rules ignore them: runtime state
	// and a local file someone force-added.
	write(t, root, ".nightgauge/run-state.json", `{"FORCED-RUNTIME-STATE":1}`+"\n")
	write(t, root, "local/forced.txt", "FORCED-IGNORED-FILE\n")
	git(t, root, "add", "-f", ".nightgauge/run-state.json", "local/forced.txt")
	git(t, root, "add", "-A")
	git(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "tracked")

	// Everything below is the operator's alone: not one of it is tracked.
	write(t, root, ".env", "ENV-SECRET-VALUE\n")
	write(t, root, "deploy/prod.env", "PROD-ENV-VALUE\n")
	write(t, root, "local/notes.txt", "IGNORED-LOCAL-NOTES\n")
	write(t, root, "scratch.txt", "UNTRACKED-SCRATCH\n")
	write(t, root, "docs/draft.md", "UNTRACKED-DRAFT\n")
	write(t, root, ".nightgauge/session-handoff.md", "RUNTIME-HANDOFF\n")
	write(t, root, ".nightgauge/config.local.yaml", "owner: acme\nnote: LOCAL-CONFIG-NOTE\n")
	write(t, root, "backup.git/config", "BARE-REPO-CONFIG\n")
	if err := os.Symlink("vendor/secret/PLAN.md", filepath.Join(root, "peek")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "vendor", "secret")
	write(t, nested, "PLAN.md", "UNREADABLE-REPO-PLAN\n")
	git(t, nested, "init", "-q", "-b", "main")
	git(t, nested, "add", "-A")
	git(t, nested, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "init")
	return sharedScope{root: root, nested: nested, leaks: []string{
		"ENV-SECRET-VALUE", "PROD-ENV-VALUE", "IGNORED-LOCAL-NOTES", "UNTRACKED-SCRATCH", "UNTRACKED-DRAFT",
		"RUNTIME-HANDOFF", "LOCAL-CONFIG-NOTE", "BARE-REPO-CONFIG", "UNREADABLE-REPO-PLAN", "OUTSIDE-ROOT-CONTENT",
		"FORCED-RUNTIME-STATE", "FORCED-IGNORED-FILE",
	}}
}

func (w sharedScope) toolset(t *testing.T, turn Turn) *Toolset {
	t.Helper()
	s := NewToolset(Env{Root: w.root, RepoRoots: map[string]string{"acme/api": w.root, "acme/secret": w.nested},
		Forge: newFakeForge()}, turn)
	t.Cleanup(s.Close)
	return s
}

// A reply lands in a thread every participant sees, and the turn command
// names none of them, so the file tools serve every turn, the host
// operator's own included, only what every participant could read: the files
// git tracks in a repository the turn command lists as readable (the rule
// #2472 gave the run tools). An operator's untracked or ignored files, their
// .nightgauge runtime state and a nested clone of a repository the turn may
// not read are never shown, named in a listing or matched by a search, and a
// symlink cannot reach them. A file the repository's own rules ignore is not
// served even when someone force-added it. A refusal of a file the turn may
// not see is the refusal a file that does not exist gets, whether the file is
// untracked, behind a symlink or in a repository the turn may not read, so it
// tells the model nothing about what is on the machine, not even where an
// unreadable repository's clone is. Before the fix the operator's turn read
// every one of them.
func TestFileTools_ServeEveryTurnOnlyTrackedFilesOfReadableRepos(t *testing.T) {
	w := newSharedScope(t)
	turns := map[string]Turn{
		"the operator": operatorTurn("read"),
		"a teammate":   teammateTurn("acct-bob", "bob", "read", Repo{"acme", "api"}),
	}
	for who, turn := range turns {
		s := w.toolset(t, turn)
		_, missing := call(t, s, ToolReadFile, map[string]any{"path": "no-such-file.txt"})
		if code(missing) != CodeNotFound {
			t.Fatalf("%s: a missing file: %v, want %s", who, missing, CodeNotFound)
		}

		for _, c := range []struct {
			name, path string
			// want is the refusal: "missing" is exactly the error a path
			// with nothing at it gets.
			want string
		}{
			{"an ignored .env", ".env", CodeSecretPath},
			{"an ignored env file the deny list does not name", "deploy/prod.env", "missing"},
			{"an untracked file", "scratch.txt", "missing"},
			{"an untracked file in a tracked directory", "docs/draft.md", "missing"},
			{"an ignored file", "local/notes.txt", "missing"},
			{".nightgauge runtime state", ".nightgauge/session-handoff.md", "missing"},
			{"the local config", ".nightgauge/config.local.yaml", "missing"},
			{".git/config", ".git/config", CodeSecretPath},
			{"a bare repository's config", "backup.git/config", "missing"},
			{"a tracked symlink into an unreadable repository", "docs/to-vendor", "missing"},
			{"a tracked symlink to an ignored file", "docs/to-local", "missing"},
			{"a tracked symlink out of the root", "docs/to-outside", "missing"},
			{"an untracked symlink into an unreadable repository", "peek", "missing"},
			{"an untracked symlink out of the root", "escape", "missing"},
			{"a tracked symlink into .git", "docs/to-git", "missing"},
			{"a tracked symlink to runtime state", "docs/to-handoff", "missing"},
			{"force-added runtime state", ".nightgauge/run-state.json", "missing"},
			{"a force-added ignored file", "local/forced.txt", "missing"},
			{"a file in an unreadable repository", "vendor/secret/PLAN.md", "missing"},
			{"a missing file in an unreadable repository", "vendor/secret/no-such-file.md", "missing"},
			// No path reaches git: a name is matched exactly, never as an
			// option, a glob or pathspec magic.
			{"a glob", "*.md", "missing"},
			{"pathspec magic", ":(glob)**/README.md", "missing"},
			{"an option", "--output=x", "missing"},
		} {
			out, err := call(t, s, ToolReadFile, map[string]any{"path": c.path})
			switch {
			case c.want == "missing" && (err == nil || *err != *missing):
				t.Errorf("%s: read_file %s (%s): %v %s, want exactly the missing file's %v", who, c.path, c.name, err, out, missing)
			case c.want != "missing" && code(err) != c.want:
				t.Errorf("%s: read_file %s (%s): %v %s, want %s", who, c.path, c.name, err, out, c.want)
			}
			for _, leak := range w.leaks {
				if strings.Contains(out, leak) {
					t.Errorf("%s: read_file %s (%s) showed %s", who, c.path, c.name, leak)
				}
			}
		}

		for _, c := range []struct{ path, want string }{
			{"local", "missing"}, {"deploy", "missing"}, {"backup.git", "missing"},
			{"vendor", "missing"}, {"vendor/secret", "missing"},
		} {
			out, err := call(t, s, ToolListDirectory, map[string]any{"path": c.path})
			if c.want == "missing" && (err == nil || *err != *missing) || c.want != "missing" && code(err) != c.want {
				t.Errorf("%s: list_directory %s: %v %s, want %s", who, c.path, err, out, c.want)
			}
		}
		for dir, hidden := range map[string][]string{
			".":           {"scratch.txt", "local", "deploy", "vendor", "backup.git", "peek", "escape", ".env"},
			"docs":        {"draft.md"},
			".nightgauge": {"session-handoff.md", "config.local.yaml", "run-state.json"},
		} {
			out, err := call(t, s, ToolListDirectory, map[string]any{"path": dir})
			if err != nil {
				t.Fatalf("%s: list_directory %s: %v", who, dir, err)
			}
			for _, name := range hidden {
				if strings.Contains(out, `"`+name+`"`) {
					t.Errorf("%s: list_directory %s names %s: %s", who, dir, name, out)
				}
			}
		}

		out, err := call(t, s, ToolSearchFiles, map[string]any{"pattern": strings.Join(w.leaks, "|")})
		if err != nil {
			t.Fatalf("%s: search: %v", who, err)
		}
		if _, content := envelope(t, out); len(content["matches"].([]any)) != 0 {
			t.Errorf("%s: a search found files the conversation may not read: %v", who, content["matches"])
		}
		if _, err := call(t, s, ToolSearchFiles, map[string]any{"pattern": "PLAN", "path": "vendor/secret"}); err == nil || *err != *missing {
			t.Errorf("%s: a search of an unreadable repository: %v, want exactly the missing file's %v", who, err, missing)
		}
		if _, err := call(t, s, ToolSearchFiles, map[string]any{"pattern": "NOTES", "path": "local"}); err == nil || *err != *missing {
			t.Errorf("%s: a search of an ignored directory: %v, want exactly the missing file's %v", who, err, missing)
		}

		// What every participant could read still reads.
		for path, want := range map[string]string{
			"README.md": "The API service.", "docs/guide.md": "A tracked guide.", "docs/to-readme": "The API service.",
			".nightgauge/config.yaml": "default_repo: api",
		} {
			if out, err := call(t, s, ToolReadFile, map[string]any{"path": path}); err != nil || !strings.Contains(out, want) {
				t.Errorf("%s: read_file %s, a tracked file of a readable repository: %v %s", who, path, err, out)
			}
		}
		out, err = call(t, s, ToolListDirectory, map[string]any{"path": "."})
		for _, name := range []string{"README.md", "src", "docs", ".gitignore", ".nightgauge"} {
			if err != nil || !strings.Contains(out, `"`+name+`"`) {
				t.Errorf("%s: list_directory . lost tracked %s: %v %s", who, name, err, out)
			}
		}
		out, err = call(t, s, ToolSearchFiles, map[string]any{"pattern": "API service|tracked guide"})
		if err != nil || !strings.Contains(out, "README.md:3") || !strings.Contains(out, "docs/guide.md:1") {
			t.Errorf("%s: a search lost the tracked files: %v %s", who, err, out)
		}
	}

	// A turn that may read acme/secret reads its tracked files, through the
	// nested checkout or a tracked symlink into it, and still nothing else.
	both := teammateTurn("acct-bob", "bob", "read", Repo{"acme", "api"}, Repo{"acme", "secret"})
	s := w.toolset(t, both)
	for _, path := range []string{"vendor/secret/PLAN.md", "docs/to-vendor"} {
		if out, err := call(t, s, ToolReadFile, map[string]any{"path": path}); err != nil || !strings.Contains(out, "UNREADABLE-REPO-PLAN") {
			t.Errorf("read_file %s with acme/secret readable: %v %s", path, err, out)
		}
	}
	if _, err := call(t, s, ToolReadFile, map[string]any{"path": "scratch.txt"}); code(err) != CodeNotFound {
		t.Errorf("an untracked file with acme/secret readable: %v", err)
	}
}

// A checkout git cannot list serves nothing: what cannot be shown tracked is
// not served.
func TestFileTools_ACheckoutGitCannotListServesNothing(t *testing.T) {
	root := t.TempDir()
	write(t, root, "notes.txt", "NOT-A-REPOSITORY\n")
	s := NewToolset(Env{Root: root, RepoRoots: map[string]string{"acme/api": root}}, operatorTurn("read"))
	t.Cleanup(s.Close)
	if out, err := call(t, s, ToolReadFile, map[string]any{"path": "notes.txt"}); err == nil || strings.Contains(out, "NOT-A-REPOSITORY") {
		t.Errorf("a file of a checkout git cannot list: %v %s", err, out)
	}
	out, err := call(t, s, ToolListDirectory, map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("the checkout's root: %v", err)
	}
	if strings.Contains(out, "notes.txt") {
		t.Errorf("the listing names a file git cannot show tracked: %s", out)
	}
}

// The tracked files of every readable checkout under the root are listed
// before the first path is judged, and an unreadable checkout's never are
// (security review): git is never run because of what a path names, so how
// long a refusal takes cannot show that a symlink's target exists, and no
// repository the turn may not read is ever probed.
func TestFileTools_ListEveryReadableCheckoutBeforeAnyPath(t *testing.T) {
	w := newSharedScope(t)
	var listed []string
	real := trackedLister
	trackedLister = func(dir string) *trackedFiles {
		listed = append(listed, dir)
		return real(dir)
	}
	t.Cleanup(func() { trackedLister = real })
	canon := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	both := []Repo{{"acme", "api"}, {"acme", "secret"}}
	for _, c := range []struct {
		name, path string
		readable   []Repo
		want       []string
	}{
		{"a missing path, both readable", "no-such-file.txt", both, []string{w.root, w.nested}},
		{"a tracked symlink to a missing file, both readable", "docs/to-nowhere", both, []string{w.root, w.nested}},
		{"a tracked symlink to an existing file, both readable", "docs/to-vendor", both, []string{w.root, w.nested}},
		{"a tracked symlink into acme/secret, only acme/api readable", "docs/to-vendor", []Repo{{"acme", "api"}},
			[]string{w.root}},
		{"a path in acme/secret, only it readable", "vendor/secret/PLAN.md", []Repo{{"acme", "secret"}}, []string{w.nested}},
	} {
		listed = nil
		s := w.toolset(t, teammateTurn("acct-bob", "bob", "read", c.readable...))
		_, _ = call(t, s, ToolReadFile, map[string]any{"path": c.path})
		got := map[string]bool{}
		for _, d := range listed {
			got[canon(d)] = true
		}
		if len(listed) != len(c.want) || len(got) != len(c.want) {
			t.Errorf("%s: listed %v, want each of %v once", c.name, listed, c.want)
			continue
		}
		for _, d := range c.want {
			if !got[canon(d)] {
				t.Errorf("%s: listed %v, want %v", c.name, listed, c.want)
			}
		}
	}
}
