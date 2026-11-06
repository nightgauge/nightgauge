package orchestrator

import "testing"

func TestHaltedRepos(t *testing.T) {
	if got := HaltedRepos(nil); got == nil || len(got) != 0 {
		t.Errorf("nil state: got %#v, want non-nil empty", got)
	}
	if got := HaltedRepos(&AutonomousState{}); got == nil || len(got) != 0 {
		t.Errorf("no halts: got %#v, want non-nil empty", got)
	}
	st := &AutonomousState{PausedRepos: map[string]*RepoPauseRecord{
		"z/b": {Repo: "z/b", Issue: 2},
		"a/a": {Repo: "a/a", Issue: 1},
		"n/n": nil,
	}}
	got := HaltedRepos(st)
	if len(got) != 2 || got[0].Repo != "a/a" || got[1].Repo != "z/b" {
		t.Errorf("got %#v, want a/a then z/b with the nil entry skipped", got)
	}
}

func TestArchitectureApprovalParked(t *testing.T) {
	if got := ArchitectureApprovalParked(nil); got == nil || len(got) != 0 {
		t.Errorf("nil state: got %#v, want non-nil empty", got)
	}
	st := &AutonomousState{Failed: []FailedItem{
		{Repo: "b/r", Number: 5, Kind: TerminalKindArchitectureApprovalRequired},
		{Repo: "a/r", Number: 9, Kind: TerminalKindArchitectureApprovalRequired, ReleasePending: true},
		{Repo: "a/r", Number: 3, Kind: TerminalKindArchitectureApprovalRequired},
		{Repo: "a/r", Number: 1, Reason: "crashed"},
		{Repo: "a/r", Number: 2, RetryCapped: true},
	}}
	got := ArchitectureApprovalParked(st)
	want := []struct {
		repo string
		n    int
	}{{"a/r", 3}, {"a/r", 9}, {"b/r", 5}}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %d architecture holds", got, len(want))
	}
	for i, w := range want {
		if got[i].Repo != w.repo || got[i].Number != w.n {
			t.Errorf("[%d] = %s#%d, want %s#%d", i, got[i].Repo, got[i].Number, w.repo, w.n)
		}
	}
}
