package gateledger

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

func finish(kind, tree, verdict string, full bool) Event {
	return Event{RunID: "r", Event: EventFinish, Kind: kind, Tree: tree, Verdict: verdict, Full: full}
}

func TestLatest(t *testing.T) {
	const tree = "abc"
	pass := finish(KindLocalGate, tree, VerdictPass, true)
	parity := finish(KindParity, tree, VerdictPass, true)
	moved := pass
	moved.TreeMoved = true
	cases := []struct {
		name   string
		events []Event
		kind   string
		want   bool
	}{
		{"empty ledger", nil, KindLocalGate, false},
		{"a local-gate pass answers local-gate", []Event{pass}, KindLocalGate, true},
		{"a local-gate pass answers parity", []Event{pass}, KindParity, true},
		{"a parity pass answers parity", []Event{parity}, KindParity, true},
		{"a parity pass never answers local-gate", []Event{parity}, KindLocalGate, false},
		{"another tree proves nothing", []Event{finish(KindLocalGate, "other", VerdictPass, true)}, KindLocalGate, false},
		{"a later failure voids a pass", []Event{pass, finish(KindLocalGate, tree, VerdictFail, true)}, KindLocalGate, false},
		{"a later parity failure voids a local-gate pass", []Event{pass, finish(KindParity, tree, VerdictFail, true)}, KindLocalGate, false},
		{"a later infra finish voids a pass", []Event{pass, finish(KindLocalGate, tree, VerdictInfra, true)}, KindLocalGate, false},
		{"a later pass clears an earlier failure", []Event{finish(KindLocalGate, tree, VerdictFail, true), pass}, KindLocalGate, true},
		{"a partial pass is not reusable", []Event{finish(KindLocalGate, tree, VerdictPass, false)}, KindLocalGate, false},
		{"a pass whose tree moved is not reusable", []Event{moved}, KindLocalGate, false},
		{"a newer parity pass does not hide a local-gate pass", []Event{pass, parity}, KindLocalGate, true},
		{"a start with no finish proves nothing", []Event{{RunID: "r", Event: EventStart, Kind: KindLocalGate, Tree: tree, Full: true}}, KindLocalGate, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := repo(t)
			for _, ev := range tc.events {
				if err := Append(root, ev); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Latest(root, tree, tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if got.Reusable != tc.want {
				t.Errorf("Reusable = %v, want %v (%s)", got.Reusable, tc.want, got.Reason)
			}
			if got.Reason == "" {
				t.Error("Reason is empty; the caller has nothing to print")
			}
		})
	}
}

func TestLatest_EmptyTreeFailsClosed(t *testing.T) {
	root := repo(t)
	if err := Append(root, finish(KindLocalGate, "", VerdictPass, true)); err != nil {
		t.Fatal(err)
	}
	if got, _ := Latest(root, "", KindLocalGate); got.Reusable {
		t.Error("an unfingerprinted tree was reusable")
	}
}

func TestLatest_UnreadableLineStopsTheWalk(t *testing.T) {
	root := repo(t)
	if err := Append(root, finish(KindLocalGate, "abc", VerdictPass, true)); err != nil {
		t.Fatal(err)
	}
	path, err := Path(root)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{torn\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got, _ := Latest(root, "abc", KindLocalGate); got.Reusable {
		t.Error("a pass behind an unreadable line was reusable")
	}
	if err := Append(root, finish(KindLocalGate, "abc", VerdictPass, true)); err != nil {
		t.Fatal(err)
	}
	if got, _ := Latest(root, "abc", KindLocalGate); !got.Reusable {
		t.Errorf("a pass newer than the torn line was refused: %s", got.Reason)
	}
}

func TestPath_UsesThePipelineStateDir(t *testing.T) {
	path, err := Path(repo(t))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != FileName {
		t.Errorf("Path = %s", path)
	}
}

func TestUsageIn(t *testing.T) {
	root := repo(t)
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	at := func(m int) string { return base.Add(time.Duration(m) * time.Minute).Format(time.RFC3339Nano) }
	mine := func(ev Event, m int) Event {
		ev.Issue, ev.Stage, ev.TS = 7, "feature-validate", at(m)
		return ev
	}
	done := Event{RunID: "done", Event: EventFinish, Kind: KindLocalGate, Verdict: VerdictFail, Full: true, Seconds: 900}
	partial := Event{RunID: "partial", Event: EventFinish, Kind: KindLocalGate, Verdict: VerdictPass, Seconds: 60}
	killed := Event{RunID: "killed", Event: EventStart, Kind: KindLocalGate, Full: true}
	startDone := Event{RunID: "done", Event: EventStart, Kind: KindLocalGate, Full: true}
	reused := Event{RunID: "again", Event: EventReused, Kind: KindLocalGate}
	otherStage := done
	otherStage.RunID = "other"
	events := []Event{
		mine(startDone, 1), mine(done, 16), mine(partial, 17), mine(killed, 18), mine(reused, 19),
		mine(done, 90), // outside the window
	}
	stageless := done
	stageless.RunID, stageless.Issue, stageless.TS, stageless.Seconds = "bare", 7, at(20), 100
	wrongStage := mine(otherStage, 21)
	wrongStage.Stage = "pr-merge"
	wrongIssue := mine(otherStage, 22)
	wrongIssue.Issue = 8
	events = append(events, stageless, wrongStage, wrongIssue)
	for _, ev := range events {
		if err := Append(root, ev); err != nil {
			t.Fatal(err)
		}
	}
	got, err := UsageIn(root, 7, "feature-validate", base, base.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{Runs: 2, Seconds: 1000, Reused: 1, Unfinished: 1}
	if got != want {
		t.Errorf("UsageIn = %+v, want %+v", got, want)
	}
}
