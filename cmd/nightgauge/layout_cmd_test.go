package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

func runLayoutCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := layoutCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// TestLayoutCmdJSON: `nightgauge layout` prints the resolved per-clone layout,
// the same for the main checkout and a linked worktree (ADR-024 § 7).
func TestLayoutCmdJSON(t *testing.T) {
	root := layouttest.Repo(t)
	gittest.Run(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, root, "worktree", "add", "-q", wt)
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(real, ".git", "nightgauge")

	for _, from := range []string{root, wt} {
		out, err := runLayoutCmd(t, "", "--workdir", from)
		if err != nil {
			t.Fatalf("layout --workdir %s: %v", from, err)
		}
		var rep layoutReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, out)
		}
		want := layoutReport{
			SchemaVersion: 1, Root: from, GitCommonDir: filepath.Join(real, ".git"), Clone: clone,
			Pipeline: filepath.Join(clone, "pipeline"), Plans: filepath.Join(clone, "plans"),
			Retros: filepath.Join(clone, "retros"), Logs: filepath.Join(clone, "logs"),
			State: rep.State, Cache: rep.Cache, Runtime: rep.Runtime,
		}
		if rep != want {
			t.Errorf("layout from %s =\n%+v\nwant\n%+v", from, rep, want)
		}
	}
}

func TestLayoutCmdWriteAndPath(t *testing.T) {
	root := layouttest.Repo(t)
	out, err := runLayoutCmd(t, `{"n":1}`, "write", "pipeline", "issue-7.json", "--workdir", root)
	if err != nil {
		t.Fatal(err)
	}
	written := strings.TrimSpace(out)
	if want := filepath.Join(layouttest.PipelineDir(t, root), "issue-7.json"); written != want {
		t.Fatalf("write printed %q, want %q", written, want)
	}
	if got, _ := os.ReadFile(written); string(got) != `{"n":1}` {
		t.Fatalf("written content = %q", got)
	}
	out, err = runLayoutCmd(t, "", "path", "pipeline", "issue-7.json", "--workdir", root)
	if err != nil || strings.TrimSpace(out) != written {
		t.Fatalf("path = %q, %v; want %q", out, err, written)
	}
	if _, err := runLayoutCmd(t, "x\n", "append", "retros", "7.md", "--workdir", root); err != nil {
		t.Fatal(err)
	}
	if _, err := runLayoutCmd(t, "x", "write", "plans", "../escape.md", "--workdir", root); err == nil {
		t.Error("write ../escape.md succeeded")
	}
	if out := gittest.Run(t, root, "status", "--porcelain", "--ignored"); out != "" {
		t.Errorf("git status after layout writes = %q, want clean", out)
	}
}

func TestLayoutCmdOutsideGit(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	dir := t.TempDir()
	_, err := runLayoutCmd(t, "", "--workdir", dir)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("layout outside git: err = %v, want \"not a git repository\"", err)
	}
}
