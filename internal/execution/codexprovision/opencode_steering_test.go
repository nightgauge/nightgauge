package codexprovision

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// resolvedTempDir is a temporary directory with its symbolic links resolved
// (macOS puts them under the /var link), the form every entry is written in.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// relEntries returns the entries relative to root, slash-separated.
func relEntries(t *testing.T, root string, entries []string) []string {
	t.Helper()
	out := make([]string, len(entries))
	for i, e := range entries {
		rel, err := filepath.Rel(root, e)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = filepath.ToSlash(rel)
	}
	return out
}

// instructionsIn is the instructions entries of root and every warning
// reading them gave.
func instructionsIn(root string) ([]string, []string) {
	files := newWorktreeReader(root)
	entries, warnings := openCodeInstructions(root, files.read)
	return entries, append(files.warnings, warnings...)
}

// TestOpenCodeImports: an `@path` import in the repository's steering file
// resolves, relative to the importing file, to an instructions entry of its
// own inside the worktree. An absolute path, a path in the home directory, a
// `..` that leaves the worktree, a symbolic link out of it, a URL and a name
// OpenCode would read as a pattern are each left out with a warning. A chain
// is followed three imports deep and the fourth is left out with a warning,
// and a cycle ends. An @ that names no file, an @ in code and an address are
// not imports.
func TestOpenCodeImports(t *testing.T) {
	root := resolvedTempDir(t)
	outside := filepath.Join(resolvedTempDir(t), "outside.md")
	writeFile(t, outside, "OUTSIDE\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), strings.Join([]string{
		"# Contract",
		"",
		"Read @docs/x.md and @docs/chain1.md then @docs/cycle-a.md",
		"Not these: @/etc/passwd @~/.ssh/config @../../x @docs/link.md @https://example.test/rules.md @docs/pat[1].md",
		"Nor @someone-who-reviews, mail@example.test, `@docs/in-span.md` or:",
		"",
		"```text",
		"@docs/in-fence.md",
		"```",
		"",
		"Escaped: @docs/with\\ space.md",
	}, "\n"))
	writeFile(t, filepath.Join(root, "docs", "x.md"), "X\n")
	writeFile(t, filepath.Join(root, "docs", "chain1.md"), "one @chain2.md\n")
	writeFile(t, filepath.Join(root, "docs", "chain2.md"), "two @chain3.md\n")
	writeFile(t, filepath.Join(root, "docs", "chain3.md"), "three @chain4.md\n")
	writeFile(t, filepath.Join(root, "docs", "chain4.md"), "four\n")
	writeFile(t, filepath.Join(root, "docs", "cycle-a.md"), "A @cycle-b.md\n")
	writeFile(t, filepath.Join(root, "docs", "cycle-b.md"), "B @cycle-a.md @../AGENTS.md\n")
	writeFile(t, filepath.Join(root, "docs", "pat[1].md"), "PATTERN\n")
	writeFile(t, filepath.Join(root, "docs", "in-span.md"), "SPAN\n")
	writeFile(t, filepath.Join(root, "docs", "in-fence.md"), "FENCE\n")
	writeFile(t, filepath.Join(root, "docs", "with space.md"), "SPACE\n")
	if err := os.Symlink(outside, filepath.Join(root, "docs", "link.md")); err != nil {
		t.Fatal(err)
	}

	entries, warnings := instructionsIn(root)
	want := []string{"AGENTS.md", "docs/x.md", "docs/chain1.md", "docs/chain2.md", "docs/chain3.md", "docs/cycle-a.md", "docs/cycle-b.md"}
	if got := relEntries(t, root, entries); !slices.Equal(got, want) {
		t.Errorf("instructions = %v\nwant           %v", got, want)
	}
	w := strings.Join(warnings, "\n")
	for _, want := range []string{
		`"@/etc/passwd", an absolute path`,
		`"@~/.ssh/config", a path in the home directory`,
		`"@../../x", which leaves the worktree`,
		`"@docs/link.md", a symbolic link to a file outside the worktree`,
		`"@https://example.test/rules.md", a URL`,
		`"@docs/pat[1].md", which is not given to OpenCode`,
		`"@chain4.md", more than 3 imports deep`,
		`"@docs/with space.md", which is not given to OpenCode`,
	} {
		if !strings.Contains(w, want) {
			t.Errorf("no warning says %s:\n%s", want, w)
		}
	}
	for _, quiet := range []string{"someone-who-reviews", "example.test,", "in-span", "in-fence", "cycle-a.md\", more"} {
		if strings.Contains(w, quiet) {
			t.Errorf("a warning names %q, which is not an import to leave out:\n%s", quiet, w)
		}
	}
	if len(warnings) != 8 {
		t.Errorf("%d warnings, want 8:\n%s", len(warnings), w)
	}
}

// TestOpenCodeInstructionsNeverHoldAURL: however the steering imports a URL,
// no instructions entry is one: none matches ^[a-z]+://, and every entry is
// an absolute path.
func TestOpenCodeInstructionsNeverHoldAURL(t *testing.T) {
	root := resolvedTempDir(t)
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "@https://example.test/a.md\n@http://127.0.0.1:9/b.md @file:///etc/passwd @HTTPS://example.test/c.md @docs/ok.md\n")
	writeFile(t, filepath.Join(root, "docs", "ok.md"), "ok @ftp://example.test/d.md\n")
	entries, warnings := instructionsIn(root)
	url := regexp.MustCompile(`^[a-z]+://`)
	for _, e := range entries {
		if url.MatchString(strings.ToLower(e)) || !filepath.IsAbs(e) {
			t.Errorf("instructions entry %q is a URL or not an absolute path", e)
		}
	}
	if got := relEntries(t, root, entries); !slices.Equal(got, []string{"CLAUDE.md", "docs/ok.md"}) {
		t.Errorf("instructions = %v", got)
	}
	if n := strings.Count(strings.Join(warnings, "\n"), "a URL"); n != 5 {
		t.Errorf("%d URL warnings, want 5: %v", n, warnings)
	}
	for _, bad := range []string{"https://example.test/x.md", "relative/x.md", "/tmp/{file:x}.md", "/tmp/a*.md", "/tmp/./x.md"} {
		if OpenCodeInstructionPathError(bad) == nil {
			t.Errorf("OpenCodeInstructionPathError(%q) = nil, want a refusal", bad)
		}
	}
}

// TestOpenCodeSteeringFileFollowsTheSharedRule: the repository's steering file
// is AGENTS.md when it has content of its own, and then CLAUDE.md is not
// given, nor followed; otherwise CLAUDE.md, whose leading @AGENTS.md import
// is skipped. An AGENTS.md that is a symbolic link out of the worktree is left
// out with a warning, and the rule falls through to CLAUDE.md.
func TestOpenCodeSteeringFileFollowsTheSharedRule(t *testing.T) {
	t.Run("AGENTS.md with content", func(t *testing.T) {
		root := resolvedTempDir(t)
		writeFile(t, filepath.Join(root, "AGENTS.md"), "# Contract\nRules.\n")
		writeFile(t, filepath.Join(root, "CLAUDE.md"), "@AGENTS.md\n\n@docs/claude-only.md\n")
		writeFile(t, filepath.Join(root, "docs", "claude-only.md"), "CLAUDE ONLY\n")
		entries, _ := instructionsIn(root)
		if got := relEntries(t, root, entries); !slices.Equal(got, []string{"AGENTS.md"}) {
			t.Errorf("instructions = %v, want AGENTS.md alone", got)
		}
	})
	t.Run("CLAUDE.md only", func(t *testing.T) {
		root := resolvedTempDir(t)
		writeFile(t, filepath.Join(root, "CLAUDE.md"), "@AGENTS.md\n\n# Rules\nSENTINEL-7Q\n@docs/more.md\n")
		writeFile(t, filepath.Join(root, "docs", "more.md"), "MORE\n")
		entries, warnings := instructionsIn(root)
		if got := relEntries(t, root, entries); !slices.Equal(got, []string{"CLAUDE.md", "docs/more.md"}) {
			t.Errorf("instructions = %v, want CLAUDE.md and its import", got)
		}
		if len(warnings) != 0 {
			t.Errorf("warnings: %v", warnings)
		}
	})
	t.Run("AGENTS.md holding only generated steering", func(t *testing.T) {
		root := resolvedTempDir(t)
		writeFile(t, filepath.Join(root, "AGENTS.md"), steeringManagedBegin+"\ngenerated\n"+steeringManagedEnd+"\n")
		writeFile(t, filepath.Join(root, "CLAUDE.md"), "# Rules\nSee @AGENTS.md for the rest.\n")
		entries, _ := instructionsIn(root)
		if got := relEntries(t, root, entries); !slices.Equal(got, []string{"CLAUDE.md"}) {
			t.Errorf("instructions = %v, want CLAUDE.md alone: AGENTS.md has nothing of the repository's own", got)
		}
	})
	t.Run("a steering file linked out of the worktree", func(t *testing.T) {
		root := resolvedTempDir(t)
		outside := filepath.Join(resolvedTempDir(t), "rules.md")
		writeFile(t, outside, "OUTSIDE\n")
		if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, "CLAUDE.md"), "# Rules\nCLAUDE RULES\n")
		entries, warnings := instructionsIn(root)
		if got := relEntries(t, root, entries); !slices.Equal(got, []string{"CLAUDE.md"}) {
			t.Errorf("instructions = %v, want CLAUDE.md: an AGENTS.md linked out of the worktree holds nothing of the repository's own", got)
		}
		if w := strings.Join(warnings, "\n"); !strings.Contains(w, "AGENTS.md is a symbolic link to a file outside the worktree") {
			t.Errorf("no warning names the linked AGENTS.md: %v", warnings)
		}
	})
	t.Run("no steering", func(t *testing.T) {
		if entries, warnings := instructionsIn(resolvedTempDir(t)); len(entries) != 0 || len(warnings) != 0 {
			t.Errorf("an empty repository gave %v, %v", entries, warnings)
		}
	})
}

// TestOpenCodeBaselineSteeringReadsNothingOutsideTheWorktree: the baseline
// steering an OpenCode stage is given reads its sources only inside the
// worktree, as its instructions entries do, because it reaches the model's
// system prompt too. A stage can leave a symbolic link at any source's name:
// AGENTS.md, standards/code-standards.md and docs/GIT_WORKFLOW.md here, each
// linked to a file outside the worktree. None of their content reaches the
// steering, a warning names each once, and the rule falls through to the
// next source: CLAUDE.md for the steering file and the summary of it,
// docs/CODE_STANDARDS.md for the standards.
func TestOpenCodeBaselineSteeringReadsNothingOutsideTheWorktree(t *testing.T) {
	wt := openCodeRepo(t, map[string]string{
		"CLAUDE.md":              "# Rules\n\nCLAUDE-SENTINEL-4M\n",
		"docs/CODE_STANDARDS.md": "# Standards\n\nIN-TREE-STANDARDS-8P\n",
	})
	outside := resolvedTempDir(t)
	for name, sentinel := range map[string]string{
		"AGENTS.md":                   "OUTSIDE-AGENTS-2V",
		"standards/code-standards.md": "OUTSIDE-STANDARDS-5J",
		"docs/GIT_WORKFLOW.md":        "OUTSIDE-WORKFLOW-9R",
	} {
		target := filepath.Join(outside, strings.ReplaceAll(name, "/", "-"))
		writeFile(t, target, "# Outside\n\n"+sentinel+"\n")
		link := filepath.Join(wt, name)
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}

	p := provisionOpenCode(t, wt)
	for _, sentinel := range []string{"OUTSIDE-AGENTS-2V", "OUTSIDE-STANDARDS-5J", "OUTSIDE-WORKFLOW-9R"} {
		if strings.Contains(p.Steering, sentinel) {
			t.Errorf("the steering holds %s, from a file outside the worktree:\n%s", sentinel, p.Steering)
		}
	}
	for _, want := range []string{"CLAUDE-SENTINEL-4M", "IN-TREE-STANDARDS-8P"} {
		if !strings.Contains(p.Steering, want) {
			t.Errorf("the steering does not fall through to %s:\n%s", want, p.Steering)
		}
	}
	root, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Instructions, []string{filepath.Join(root, "CLAUDE.md")}) {
		t.Errorf("Instructions = %v, want the worktree's CLAUDE.md", p.Instructions)
	}
	w := strings.Join(p.Warnings, "\n")
	for _, want := range []string{
		"AGENTS.md is a symbolic link to a file outside the worktree",
		"standards/code-standards.md is a symbolic link to a file outside the worktree",
		"docs/GIT_WORKFLOW.md is a symbolic link to a file outside the worktree",
		"OpenCode's own search still loads it",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("no warning says %q:\n%s", want, w)
		}
	}
	if n := strings.Count(w, "AGENTS.md is a symbolic link"); n != 1 {
		t.Errorf("AGENTS.md is warned about %d times, want once:\n%s", n, w)
	}
}
