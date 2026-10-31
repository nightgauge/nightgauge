package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func skillTreeCopy(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "skills")
	if err := os.CopyFS(dst, os.DirFS("../../skills")); err != nil {
		t.Fatal(err)
	}
	return dst
}

// runExit runs cmd as main would: the exit code, and everything a caller reads.
func runExit(t *testing.T, cmd *cobra.Command, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	var stderr bytes.Buffer
	code := exitCodeFor(&stderr, cmd, err)
	if err != nil {
		out.WriteString(err.Error())
	}
	return code, out.String() + stderr.String()
}

func TestRenderExitsNonZeroWhenSharedIsRemoved(t *testing.T) {
	tree := skillTreeCopy(t)
	if code, _ := runExit(t, skillRenderCmd(), "--stage", "feature-dev", "--skills-root", tree); code != 0 {
		t.Fatalf("a complete tree exited %d", code)
	}
	if err := os.RemoveAll(filepath.Join(tree, "_shared")); err != nil {
		t.Fatal(err)
	}
	code, out := runExit(t, skillRenderCmd(), "--stage", "feature-dev", "--skills-root", tree)
	if code == 0 {
		t.Fatal("render of a tree without _shared exited 0")
	}
	if !strings.Contains(out, "../_shared/PIPELINE_CONTEXT.md") {
		t.Errorf("output does not name the missing file:\n%s", out)
	}
}

func TestPreflightSkillBundleExitCodes(t *testing.T) {
	tree := skillTreeCopy(t)
	if code, out := runExit(t, preflightSkillBundleCmd(), "--skills-root", tree); code != 0 {
		t.Fatalf("a complete tree exited %d:\n%s", code, out)
	}
	if err := os.RemoveAll(filepath.Join(tree, "_shared")); err != nil {
		t.Fatal(err)
	}
	code, out := runExit(t, preflightSkillBundleCmd(), "--skills-root", tree)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "../_shared/PIPELINE_CONTEXT.md") {
		t.Errorf("output does not name the missing file:\n%s", out)
	}
	if code, _ := runExit(t, preflightSkillBundleCmd(), "--skills-root", filepath.Join(tree, "absent")); code != exitCouldNotRun {
		t.Fatalf("absent tree exit = %d, want %d", code, exitCouldNotRun)
	}
	if code, _ := runExit(t, preflightSkillBundleCmd()); code != exitCouldNotRun {
		t.Fatalf("no --skills-root exit = %d, want %d", code, exitCouldNotRun)
	}
}
