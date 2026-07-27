package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"
)

// `last_bash_command` answers "what was the last thing it typed", which is not
// the same question as "what did it actually do". A validate stage that exited
// with `true` as its last command is equally consistent with a benign trailing
// `|| true` and with a stage that ran no verification at all — and because
// stage subprocesses run with `--no-session-persistence`, there is no
// transcript to settle it afterwards. `recent_bash` carries the surrounding
// commands so the record answers the question it is the only evidence for.
// (#156)

func TestBoundRecentBash_KeepsNewestWhenOverCap(t *testing.T) {
	// A stage can run hundreds of Bash calls. The tail is what a post-mortem
	// reads, so an over-long slice must lose its head, not its tail.
	var entries []RecentBashEntry
	for i := 0; i < RecentBashMaxEntries*3; i++ {
		entries = append(entries, RecentBashEntry{Cmd: string(rune('a' + i%26))})
	}
	entries[len(entries)-1].Cmd = "the-last-thing-it-ran"

	got := BoundRecentBash(entries)
	if len(got) != RecentBashMaxEntries {
		t.Fatalf("len = %d, want %d", len(got), RecentBashMaxEntries)
	}
	if got[len(got)-1].Cmd != "the-last-thing-it-ran" {
		t.Errorf("newest entry dropped: tail = %q", got[len(got)-1].Cmd)
	}
}

func TestBoundRecentBash_TruncatesEachCommand(t *testing.T) {
	// One pathological command must not bloat the daily JSONL. The TS side
	// already truncates; this is the enforcement at the point of persistence.
	long := "echo " + strings.Repeat("x", RecentBashCommandMaxRunes*2)
	got := BoundRecentBash([]RecentBashEntry{{Cmd: long}})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	runes := []rune(got[0].Cmd)
	if len(runes) != RecentBashCommandMaxRunes+1 { // + the ellipsis
		t.Errorf("len(runes) = %d, want %d", len(runes), RecentBashCommandMaxRunes+1)
	}
	if !strings.HasSuffix(got[0].Cmd, "…") {
		t.Errorf("truncation not marked: %q", got[0].Cmd[len(got[0].Cmd)-8:])
	}
	if !strings.HasPrefix(got[0].Cmd, "echo xxx") {
		t.Errorf("truncated from the wrong end: %q", got[0].Cmd[:16])
	}
}

func TestBoundRecentBash_LeavesAConformingSliceAlone(t *testing.T) {
	exit := 0
	in := []RecentBashEntry{{Cmd: "go test ./...", Exit: &exit}, {Cmd: "true"}}
	got := BoundRecentBash(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Cmd != "go test ./..." || got[1].Cmd != "true" {
		t.Errorf("commands mutated: %+v", got)
	}
	if got[0].Exit == nil || *got[0].Exit != 0 {
		t.Errorf("Exit pointer not carried: %v", got[0].Exit)
	}
	if got[1].Exit != nil {
		t.Errorf("Exit invented for an unresolved command: %v", *got[1].Exit)
	}
}

func TestBoundRecentBash_EmptyIsNilSoOmitemptyDropsIt(t *testing.T) {
	// A healthy stage that ran no Bash should not gain a `"recent_bash": []`
	// on every line.
	if got := BoundRecentBash(nil); got != nil {
		t.Errorf("nil in, %v out", got)
	}
	if got := BoundRecentBash([]RecentBashEntry{}); got != nil {
		t.Errorf("empty in, %v out", got)
	}
}

// The absent-vs-zero distinction is the whole reason Exit is a pointer: a
// command that succeeded must stay distinguishable from one whose result never
// landed before the stage exited. A plain int renders both as 0.
func TestRecentBashEntry_ZeroExitSerialisesDistinctlyFromAbsent(t *testing.T) {
	zero := 0
	line, err := json.Marshal([]RecentBashEntry{
		{Cmd: "go build ./...", Exit: &zero},
		{Cmd: "go test ./...", Exit: nil},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(line), `"cmd":"go build ./...","exit":0`) {
		t.Errorf("a real exit 0 was dropped by omitempty: %s", line)
	}
	if strings.Contains(string(line), `"cmd":"go test ./...","exit"`) {
		t.Errorf("an unobserved exit was booked as a value: %s", line)
	}

	var back []RecentBashEntry
	if err := json.Unmarshal(line, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back[0].Exit == nil || *back[0].Exit != 0 {
		t.Errorf("exit 0 did not round-trip: %v", back[0].Exit)
	}
	if back[1].Exit != nil {
		t.Errorf("absent exit round-tripped as %v", *back[1].Exit)
	}
}

// recent_bash is additive. Existing readers and retro tooling key on
// last_bash_command / last_bash_exit, so both must still be written, with the
// last ring entry naming the same command.
func TestStageExitRecord_RecentBashDoesNotDisplaceLastBash(t *testing.T) {
	exit := 1
	rec := StageExitRecord{
		Repo:            "nightgauge/nightgauge",
		Issue:           156,
		Stage:           "feature-validate",
		LastBashCommand: "true",
		LastBashExit:    &exit,
		RecentBash: []RecentBashEntry{
			{Cmd: "npm test", Exit: &exit},
			{Cmd: "true", Exit: &exit},
		},
	}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back StageExitRecord
	if err := json.Unmarshal(line, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.LastBashCommand != "true" {
		t.Errorf("LastBashCommand = %q, want %q", back.LastBashCommand, "true")
	}
	if back.LastBashExit == nil || *back.LastBashExit != 1 {
		t.Errorf("LastBashExit = %v", back.LastBashExit)
	}
	if len(back.RecentBash) != 2 {
		t.Fatalf("len(RecentBash) = %d, want 2", len(back.RecentBash))
	}
	if back.RecentBash[len(back.RecentBash)-1].Cmd != back.LastBashCommand {
		t.Errorf("tail %q disagrees with LastBashCommand %q",
			back.RecentBash[len(back.RecentBash)-1].Cmd, back.LastBashCommand)
	}
	// The point of the issue: the record now shows a test suite ran before the
	// no-op tail, which `last_bash_command` alone could not distinguish from a
	// stage that verified nothing.
	if back.RecentBash[0].Cmd != "npm test" {
		t.Errorf("preceding command lost: %+v", back.RecentBash)
	}
	if !strings.Contains(string(line), `"recent_bash"`) {
		t.Errorf("recent_bash JSON tag missing: %s", line)
	}
}

func TestStageExitRecord_RecentBashOmittedWhenEmpty(t *testing.T) {
	line, err := json.Marshal(StageExitRecord{Repo: "o/r", Issue: 1, Stage: "pr-merge"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(line), "recent_bash") {
		t.Errorf("healthy terse record gained a recent_bash key: %s", line)
	}
}
