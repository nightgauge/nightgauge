package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// jsonl is a JSONL body of one line per timestamp, each naming where it was
// written, so a merge can be checked line by line.
func jsonl(where string, stamps ...string) string {
	var b strings.Builder
	for _, s := range stamps {
		fmt.Fprintf(&b, `{"timestamp":"2026-09-%sT00:00:00Z","from":%q}`+"\n", s, where)
	}
	return b.String()
}

// TestLayoutMigrationAppendOnlyAtBothLocations (#2307): an append-only log
// (github-api.jsonl) present at more than one place the migration takes it
// from merges as the union of its lines, never a conflict. The real case was
// a main checkout and a linked pipeline worktree under .nightgauge/worktrees,
// each with its own legacy logs/github-api.jsonl: both rows were planned as
// plain moves into the one CLONE/logs, and the second found the first's file
// there at apply time and reported a conflict after moving everything else.
func TestLayoutMigrationAppendOnlyAtBothLocations(t *testing.T) {
	type fixture struct {
		r            layoutRepo
		m            *layoutMigrator
		mainLog      string
		linkedLog    string
		cloneLog     string
		linkedChkout string
	}
	setup := func(t *testing.T) fixture {
		r := newLayoutRepo(t)
		nd := filepath.Join(r.root, ".nightgauge")
		wt := filepath.Join(nd, "worktrees", "repo-issue-8")
		gittest.Run(t, r.root, "worktree", "add", "-q", "-b", "issue-8", wt)
		m := r.migrator("")
		m.checkoutEntries = allCheckoutEntries
		return fixture{
			r: r, m: m, linkedChkout: wt,
			mainLog:   filepath.Join(nd, "logs", "github-api.jsonl"),
			linkedLog: filepath.Join(wt, ".nightgauge", "logs", "github-api.jsonl"),
			cloneLog:  filepath.Join(r.newRoot, "logs", "github-api.jsonl"),
		}
	}
	// fix runs doctor --fix and asserts it completed with no conflict and no
	// BLOCKED outcome after a successful apply.
	fix := func(t *testing.T, f fixture) {
		t.Helper()
		rep := f.r.fixer(f.m).Run(context.Background(), FixOptions{})
		for _, res := range rep.Results {
			if res.Outcome != OutcomeFixed {
				t.Errorf("%s %s: outcome %s (%s), want fixed", res.Finding.Code, res.Finding.Title, res.Outcome, res.Detail)
			}
		}
		if rep.ExitCode != 0 || rep.Counts.Conflict != 0 || rep.Counts.Blocked != 0 {
			t.Fatalf("fix: exit %d, counts %+v; want exit 0, no conflict and nothing blocked", rep.ExitCode, rep.Counts)
		}
	}
	secondRunIsNoOp := func(t *testing.T, f fixture) {
		t.Helper()
		before := treeDigest(t, f.r.newRoot)
		again := f.m.Migrate(context.Background())
		if again.Changed() || len(again.Conflicts) > 0 || again.Version != LayoutVersion {
			t.Errorf("second run: %s; want a no-op at layout v%d", again.Summary(), LayoutVersion)
		}
		if treeDigest(t, f.r.newRoot) != before {
			t.Error("the second run changed the new root")
		}
		if found, _ := layoutFindings(f.m); len(found) != 0 {
			t.Errorf("findings after the migration:\n%s", findingsText(found))
		}
	}

	t.Run("the main checkout and a linked pipeline worktree merge", func(t *testing.T) {
		f := setup(t)
		writeLayoutFile(t, f.mainLog, jsonl("main", "01", "03"), 0o644)
		writeLayoutFile(t, f.linkedLog, jsonl("linked", "02", "04"), 0o644)
		// Other data beside the logs, so the pass has several findings.
		plan := filepath.Join(f.r.root, ".nightgauge", "plans", "issue-5.md")
		retro := filepath.Join(f.linkedChkout, ".nightgauge", "retros", "issue-8.md")
		writeLayoutFile(t, plan, "plan\n", 0o644)
		writeLayoutFile(t, retro, "retro\n", 0o644)

		found, _ := layoutFindings(f.m)
		if c := findingsWith(found, codeLayoutConflict); len(c) != 0 {
			t.Fatalf("scan reported conflicts:\n%s", findingsText(c))
		}
		fix(t, f)

		want := jsonl("main", "01") + jsonl("linked", "02") + jsonl("main", "03") + jsonl("linked", "04")
		if got := readLayoutFile(t, f.cloneLog); got != want {
			t.Errorf("merged github-api.jsonl =\n%s\nwant the union ordered by timestamp:\n%s", got, want)
		}
		assertGone(t, f.mainLog)
		assertGone(t, plan)
		if got := readLayoutFile(t, filepath.Join(f.r.newRoot, "plans", "issue-5.md")); got != "plan\n" {
			t.Errorf("plan = %q", got)
		}
		if got := readLayoutFile(t, filepath.Join(f.r.newRoot, "retros", "issue-8.md")); got != "retro\n" {
			t.Errorf("retro = %q", got)
		}
		secondRunIsNoOp(t, f)
	})

	t.Run("a clone target an earlier run populated merges from both checkouts", func(t *testing.T) {
		f := setup(t)
		writeLayoutFile(t, f.cloneLog, jsonl("clone", "01", "05"), 0o600)
		writeLayoutFile(t, f.mainLog, jsonl("main", "02"), 0o644)
		writeLayoutFile(t, f.linkedLog, jsonl("linked", "03"), 0o644)
		fix(t, f)
		want := jsonl("clone", "01") + jsonl("main", "02") + jsonl("linked", "03") + jsonl("clone", "05")
		if got := readLayoutFile(t, f.cloneLog); got != want {
			t.Errorf("merged github-api.jsonl =\n%s\nwant:\n%s", got, want)
		}
		assertGone(t, f.mainLog)
		secondRunIsNoOp(t, f)
	})

	t.Run("pipeline history from both checkouts merges", func(t *testing.T) {
		f := setup(t)
		mainHist := filepath.Join(f.r.root, ".nightgauge", "pipeline", "history", "runs.jsonl")
		linkedHist := filepath.Join(f.linkedChkout, ".nightgauge", "pipeline", "history", "runs.jsonl")
		writeLayoutFile(t, mainHist, jsonl("main", "01"), 0o644)
		writeLayoutFile(t, linkedHist, jsonl("linked", "02"), 0o644)
		fix(t, f)
		if got, want := readLayoutFile(t, filepath.Join(f.r.newRoot, "pipeline", "history", "runs.jsonl")),
			jsonl("main", "01")+jsonl("linked", "02"); got != want {
			t.Errorf("merged history =\n%s\nwant:\n%s", got, want)
		}
		secondRunIsNoOp(t, f)
	})

	t.Run("a non-log file from both checkouts still conflicts and nothing moves", func(t *testing.T) {
		f := setup(t)
		mainPlan := filepath.Join(f.r.root, ".nightgauge", "plans", "issue-5.md")
		linkedPlan := filepath.Join(f.linkedChkout, ".nightgauge", "plans", "issue-5.md")
		writeLayoutFile(t, mainPlan, "main plan\n", 0o644)
		writeLayoutFile(t, linkedPlan, "linked plan\n", 0o644)
		writeLayoutFile(t, f.mainLog, jsonl("main", "01"), 0o644)

		found, _ := layoutFindings(f.m)
		if c := findingsWith(found, codeLayoutConflict); len(c) != 1 {
			t.Errorf("conflict findings = %s, want one for the plan both checkouts hold", findingsText(c))
		}
		rep := f.r.fixer(f.m).Run(context.Background(), FixOptions{})
		if rep.ExitCode != 3 {
			t.Errorf("fix exit = %d, want 3 (conflict)", rep.ExitCode)
		}
		for _, res := range rep.Results {
			if res.Outcome == OutcomeBlocked {
				t.Errorf("%s: blocked (%s); a conflict is reported as a conflict", res.Finding.Title, res.Detail)
			}
		}
		if readLayoutFile(t, mainPlan) != "main plan\n" || readLayoutFile(t, linkedPlan) != "linked plan\n" {
			t.Error("a conflicting plan was changed")
		}
		assertGone(t, filepath.Join(f.r.newRoot, "plans", "issue-5.md"))
		// The migration stops for the whole clone on a conflict.
		if readLayoutFile(t, f.mainLog) != jsonl("main", "01") {
			t.Error("a file moved while a conflict stood")
		}
		mrep := f.m.Migrate(context.Background())
		if s := mrep.Summary(); !strings.Contains(s, "nothing moved") || mrep.Changed() {
			t.Errorf("summary %q; want a conflict that moved nothing", s)
		}
	})
}

// TestLayoutReportSummaryIsConsistent (#2307): the summary never says
// "nothing moved" beside a count of moved files.
func TestLayoutReportSummaryIsConsistent(t *testing.T) {
	c := []layoutConflict{{Class: "plans", Legacy: "/a", Target: "/b"}}
	if s := (LayoutReport{Conflicts: c}).Summary(); !strings.Contains(s, "1 conflict(s), nothing moved") {
		t.Errorf("conflict alone: %q, want it to say nothing moved", s)
	}
	for _, rep := range []LayoutReport{
		{Conflicts: c, Moved: 2345},
		{Conflicts: c, WorktreesMoved: 1},
		{Conflicts: c, CachesDeleted: 1},
		{Conflicts: c, Merged: 1},
	} {
		s := rep.Summary()
		if strings.Contains(s, "nothing moved") {
			t.Errorf("%+v: summary %q says nothing moved", rep, s)
		}
		if !strings.Contains(s, "1 conflict(s) left in place") {
			t.Errorf("%+v: summary %q does not report the conflict left in place", rep, s)
		}
	}
}

// TestLayoutPlaceFileClassifiesAtMoveTime (#2307): a target that appeared
// after the scan (a newer build writing the new location) is classified again
// when the file is moved: an append-only log merges, the same bytes remove the
// source, and anything else is a conflict that overwrites nothing.
func TestLayoutPlaceFileClassifiesAtMoveTime(t *testing.T) {
	dir := t.TempDir()
	logs := LayoutEntry{Class: "logs", Kind: LayoutFiles,
		AppendOnly: func(rel string) bool { return strings.HasPrefix(rel, "github-api") && strings.HasSuffix(rel, ".jsonl") }}
	place := func(rel, src, dst string) LayoutReport {
		t.Helper()
		var rep LayoutReport
		f := layoutFile{Rel: rel, Src: filepath.Join(dir, "old", rel), Dst: filepath.Join(dir, "new", rel)}
		writeLayoutFile(t, f.Src, src, 0o644)
		writeLayoutFile(t, f.Dst, dst, 0o600)
		if err := placeFile(layoutItem{Entry: logs}, f, &rep); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		return rep
	}
	if rep := place("github-api.jsonl", jsonl("old", "01"), jsonl("new", "02")); rep.Merged != 1 || len(rep.Conflicts) != 0 {
		t.Errorf("append-only: %s; want one merge", rep.Summary())
	}
	if got, want := readLayoutFile(t, filepath.Join(dir, "new", "github-api.jsonl")), jsonl("old", "01")+jsonl("new", "02"); got != want {
		t.Errorf("merged = %q, want %q", got, want)
	}
	assertGone(t, filepath.Join(dir, "old", "github-api.jsonl"))
	if rep := place("same.log", "x\n", "x\n"); rep.Removed != 1 {
		t.Errorf("same bytes: %s; want the source removed", rep.Summary())
	}
	rep := place("session.log", "old\n", "new\n")
	if len(rep.Conflicts) != 1 || rep.Changed() {
		t.Errorf("differing non-log: %s; want one conflict and no change", rep.Summary())
	}
	if readLayoutFile(t, filepath.Join(dir, "new", "session.log")) != "new\n" || readLayoutFile(t, filepath.Join(dir, "old", "session.log")) != "old\n" {
		t.Error("a conflicting file was changed")
	}
}
