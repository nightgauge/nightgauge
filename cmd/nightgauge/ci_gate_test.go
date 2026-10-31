package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gateledger"
	"github.com/nightgauge/nightgauge/internal/hooks"
)

func gateRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func fingerprint(t *testing.T, root string) string {
	t.Helper()
	tree, err := hooks.TreeFingerprint(context.Background(), &hooks.ExecCmdRunner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func record(t *testing.T, root string, ev gateledger.Event) gateledger.Event {
	t.Helper()
	if err := completeGateEvent(context.Background(), root, &ev); err != nil {
		t.Fatal(err)
	}
	if err := gateledger.Append(root, ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestGateStatus_ReusesAnIdenticalTreeOnly(t *testing.T) {
	t.Setenv("NIGHTGAUGE_STAGE", "feature-validate")
	t.Setenv("NIGHTGAUGE_ISSUE_NUMBER", "7")
	root := gateRepo(t)
	ctx := context.Background()
	if st := resolveGateStatus(ctx, root, gateledger.KindLocalGate); st.Reusable {
		t.Fatalf("empty ledger was reusable: %+v", st)
	}
	tree := fingerprint(t, root)
	record(t, root, gateledger.Event{
		Event: gateledger.EventFinish, RunID: "r", Kind: gateledger.KindLocalGate,
		Tree: tree, Verdict: gateledger.VerdictPass, Full: true, Seconds: 900,
	})
	st := resolveGateStatus(ctx, root, gateledger.KindLocalGate)
	if !st.Reusable || st.PassedStage != "feature-validate" || st.PassedSeconds != 900 {
		t.Fatalf("pass on this tree was not reported: %+v", st)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := resolveGateStatus(ctx, root, gateledger.KindLocalGate); st.Reusable {
		t.Fatalf("an edited tree reused the pass: %+v", st)
	}
}

func TestGateRecord_ATreeThatMovedIsNotReusable(t *testing.T) {
	root := gateRepo(t)
	before := fingerprint(t, root)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("mid-run edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := record(t, root, gateledger.Event{
		Event: gateledger.EventFinish, RunID: "r", Kind: gateledger.KindLocalGate,
		Tree: before, Verdict: gateledger.VerdictPass, Full: true,
	})
	if !ev.TreeMoved {
		t.Fatal("TreeMoved = false for an edit during the run")
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := resolveGateStatus(context.Background(), root, gateledger.KindLocalGate); st.Reusable {
		t.Fatalf("a pass over a moving tree was reused: %+v", st)
	}
}

func TestGateStatus_NamesTheRepositorysGate(t *testing.T) {
	root := gateRepo(t)
	if st := resolveGateStatus(context.Background(), root, gateledger.KindParity); st.GateCommand != "" {
		t.Fatalf("GateCommand = %q for a repository with no gate", st.GateCommand)
	}
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "ci-local.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := resolveGateStatus(context.Background(), root, gateledger.KindParity)
	if st.GateCommand != "bash scripts/ci-local.sh" || !st.GateRecordsItself {
		t.Fatalf("default gate: %+v", st)
	}
	cfg := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "config.yaml"),
		[]byte("local_gate:\n  steps:\n    - [go, vet, ./...]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st = resolveGateStatus(context.Background(), root, gateledger.KindParity)
	if st.GateCommand != "go vet ./..." || st.GateRecordsItself {
		t.Fatalf("declared gate: %+v", st)
	}
}

func TestCompleteGateEvent_Refuses(t *testing.T) {
	root := gateRepo(t)
	for name, ev := range map[string]gateledger.Event{
		"unknown event":      {Event: "nope"},
		"finish, no verdict": {Event: gateledger.EventFinish, RunID: "r"},
		"finish, no run id":  {Event: gateledger.EventFinish, Verdict: gateledger.VerdictPass},
	} {
		if err := completeGateEvent(context.Background(), root, &ev); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestIssueFromBranch(t *testing.T) {
	for branch, want := range map[string]int{
		"feat/2784-a-gate": 2784, "fix/12-x": 12, "main": 0, "feat/no-number": 0, "": 0,
	} {
		if got := issueFromBranch(branch); got != want {
			t.Errorf("issueFromBranch(%q) = %d, want %d", branch, got, want)
		}
	}
}
