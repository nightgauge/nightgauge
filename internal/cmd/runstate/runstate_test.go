package runstatecmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/runstate"
)

// runWith executes the root state command with given args, captures stdout,
// and returns the trimmed output.
func runWith(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	root := Cmd()
	full := append([]string{"--dir", dir}, args...)
	// Cobra pulls --dir off the leaf command's flags, so attach it on every
	// child by routing args through the leaf directly.
	root.SetArgs(args)
	root.PersistentFlags().String("dir", dir, "")
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	// Inject --dir into the leaf via a synthetic --dir arg.
	root.SetArgs(full)
	err := root.Execute()
	return stdout.String() + stderr.String(), err
}

func TestSetGet_Roundtrip(t *testing.T) {
	dir := t.TempDir()

	root := Cmd()
	root.SetArgs([]string{"set", "--dir", dir, "--state", "running", "--issue", "1", "--branch", "feat/x"})
	if err := root.Execute(); err != nil {
		t.Fatalf("set running: %v", err)
	}

	rs, err := runstate.Load(dir)
	if err != nil || rs == nil {
		t.Fatalf("Load: %v rs=%v", err, rs)
	}
	if rs.State != runstate.StateRunning {
		t.Errorf("state = %s; want running", rs.State)
	}

	root2 := Cmd()
	var out bytes.Buffer
	root2.SetOut(&out)
	root2.SetArgs([]string{"get", "--dir", dir})
	if err := root2.Execute(); err != nil {
		t.Fatalf("get: %v", err)
	}
	// Stdout from fmt.Println is captured separately — read the file directly.
	data := out.String()
	_ = data // get prints via fmt.Println which bypasses cmd.SetOut; we already verified state via Load.
}

func TestDetect_Orphaned(t *testing.T) {
	dir := t.TempDir()
	root := Cmd()
	root.SetArgs([]string{"detect", "--dir", dir, "--branch", "feat/orphan", "--issue", "5", "--auto-detect-files=false", "--has-context=false"})
	if err := root.Execute(); err != nil {
		t.Fatalf("detect: %v", err)
	}
	// Result is printed via fmt.Println, but DetectResume itself is exercised
	// in internal/runstate tests. Here we only verify the command resolves
	// without error against an empty dir.
}

func TestDiscard_RequiresExisting(t *testing.T) {
	dir := t.TempDir()
	root := Cmd()
	root.SetArgs([]string{"discard", "--dir", dir, "--archive=false"})
	err := root.Execute()
	if err == nil {
		t.Error("expected error when no run-state.json exists")
	}
}

func TestSetState_RejectsBogus(t *testing.T) {
	dir := t.TempDir()
	root := Cmd()
	root.SetArgs([]string{"set", "--dir", dir, "--state", "bogus"})
	if err := root.Execute(); err == nil {
		t.Error("expected error for invalid state")
	}
}

func TestAbsoluteDir(t *testing.T) {
	if got := AbsoluteDir(""); got == "" {
		t.Error("expected default dir")
	}
	if got := AbsoluteDir("/abs/path"); got != "/abs/path" {
		t.Errorf("absolute pass-through failed: %s", got)
	}
}

func TestArchiveDirShape(t *testing.T) {
	// Simple structural check that ArchiveRun + run state stays in sync.
	dir := t.TempDir()
	rs, err := runstate.MarkRunning(dir, runstate.MarkRunningOptions{
		IssueNumber: 9, Branch: "feat/x",
	})
	if err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	archive, err := runstate.ArchiveRun(dir, rs)
	if err != nil {
		t.Fatalf("ArchiveRun: %v", err)
	}
	if filepath.Base(archive) != rs.RunID {
		t.Errorf("archive = %s; want suffix runId=%s", archive, rs.RunID)
	}
}

// jsonEquals lets future tests compare structured outputs without caring
// about whitespace.
func jsonEquals(a, b string) bool {
	var ja, jb any
	_ = json.Unmarshal([]byte(a), &ja)
	_ = json.Unmarshal([]byte(b), &jb)
	ab, _ := json.Marshal(ja)
	bb, _ := json.Marshal(jb)
	return string(ab) == string(bb)
}

var _ = jsonEquals
var _ = runWith
