package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHandoffCmd_ExitContract pins #1481's exit codes: findings are a verdict
// (1), a path that cannot be read is "could not run" (2), clean is 0.
func TestHandoffCmd_ExitContract(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale.md")
	if err := os.WriteFile(stale, []byte("<!-- nightgauge:handoff\nrepo: r\nupdated: 2000-01-01\nneeds: []\n-->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "README.md"), []byte("# no header\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, int) {
		cmd := handoffCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append(args, "--workspace-root", dir))
		err := cmd.Execute()
		var stderr bytes.Buffer
		return out.String(), exitCodeFor(&stderr, cmd, err)
	}

	out, code := run(stale)
	if code != 1 || !strings.Contains(out, "handoff-stale") {
		t.Fatalf("stale: code=%d out=%s", code, out)
	}
	out, code = run(empty)
	if code != 0 || !strings.Contains(out, "No handoff headers found.") {
		t.Fatalf("empty: code=%d out=%s", code, out)
	}
	if _, code = run(filepath.Join(dir, "missing.md")); code != 2 {
		t.Fatalf("missing: code=%d", code)
	}
}
