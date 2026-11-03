package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/gateledger"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/scaffold"
	"github.com/nightgauge/nightgauge/internal/state"
)

func repo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gittest.Run(t, root, "init", "-q")
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeFindings(t *testing.T, findings ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "findings.json")
	body := `{"schema":1,"findings":[` + strings.Join(findings, ",") + `]}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func finding(pass, severity string) string {
	return `{"pass":"` + pass + `","file":"a.go","line":1,"claim":"` + pass + ` ` + severity +
		`","severity":"` + severity + `","fix":"change"}`
}

// elsewhere is finding at another line.
func elsewhere(pass, severity string) string {
	return strings.Replace(finding(pass, severity), `"line":1`, `"line":2`, 1)
}

func rows(t *testing.T, root string) []state.GateResult {
	t.Helper()
	got, err := state.ReadGateMetricsForIssue(root, 7)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func session(root, tree string) Session {
	return Session{Root: root, Issue: 7, Tree: tree, PipelineRun: "run", Now: time.Unix(1000, 0)}
}

func TestParseRejects(t *testing.T) {
	ok := finding("correctness", "major")
	line := func(l string) string {
		return `{"schema":1,"findings":[` + strings.Replace(ok, `"line":1`, l, 1) + `]}`
	}
	many := strings.TrimSuffix(strings.Repeat(ok+",", maxFindings+1), ",")
	for name, in := range map[string]string{
		"empty":        ``,
		"not json":     `nope`,
		"wrong schema": `{"schema":2,"findings":[]}`,
		"no findings":  `{"schema":1}`,
		"extra key":    `{"schema":1,"findings":[],"x":1}`,
		"trailing":     `{"schema":1,"findings":[]}{}`,
		"finding key":  line(`"line":1,"x":1`),
		"missing line": line(`"pad":0`)[:0] + `{"schema":1,"findings":[{"pass":"security","file":"a","claim":"c","severity":"major","fix":"f"}]}`,
		"zero line":    line(`"line":0`),
		"unknown pass": `{"schema":1,"findings":[` + finding("style", "major") + `]}`,
		"unknown sev":  `{"schema":1,"findings":[` + finding("security", "minor") + `]}`,
		"absolute":     strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `"a.go"`, `"/etc/a"`, 1),
		"dotdot":       strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `"a.go"`, `"x/../../a"`, 1),
		"multiline":    strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `correctness major`, `a\nb`, 1),
		"long claim":   strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `correctness major`, strings.Repeat("x", maxClaim+1), 1),
		"long fix":     strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `"change"`, `"`+strings.Repeat("x", maxFix+1)+`"`, 1),
		"credential":   strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `change`, `ghp_abc`, 1),
		"over the cap": `{"schema":1,"findings":[` + many + `]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(in)); err == nil {
				t.Fatalf("accepted %q", in)
			}
		})
	}
	nullLine := `{"schema":1,"findings":[` + strings.Replace(ok, `"line":1`, `"line":null`, 1) + `]}`
	if _, err := Parse([]byte(nullLine)); err != nil {
		t.Fatalf("a null line is valid: %v", err)
	}
}

func TestRoute(t *testing.T) {
	for _, tc := range []struct {
		pass, severity string
		fix, disc      int
	}{
		{"security", "blocker", 1, 0},
		{"plan-compliance", "blocker", 1, 0},
		{"correctness", "major", 1, 0},
		{"security", "major", 0, 1},
		{"plan-compliance", "major", 0, 1},
		{"correctness", "nitpick", 0, 0},
	} {
		fix, disc := Route([]Finding{{Pass: tc.pass, Severity: tc.severity}})
		if len(fix) != tc.fix || len(disc) != tc.disc {
			t.Errorf("%s %s: fix=%d discovered=%d", tc.pass, tc.severity, len(fix), len(disc))
		}
	}
}

func filed(t *testing.T, root string) []discoveredwork.Record {
	t.Helper()
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := discoveredwork.Load(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	if capture == nil { // nothing was ever filed
		return nil
	}
	return capture.Records
}

func TestDiscoveredDroppedByALaterRoundIsNotFiled(t *testing.T) {
	root := repo(t)
	s := session(root, "t0")
	if _, err := Record(s, "t0", writeFindings(t,
		finding("correctness", "blocker"), finding("security", "major")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = "t1"
	out, err := Record(s, "t1", writeFindings(t), time.Time{})
	if err != nil || out.Verdict != "pass" {
		t.Fatalf("%+v, %v", out, err)
	}
	if got := filed(t, root); len(got) != 0 {
		t.Fatalf("fixed work was filed: %+v", got)
	}
}

func TestDiscoveredALaterRoundStillReportsIsFiledOnce(t *testing.T) {
	root := repo(t)
	s := session(root, "t0")
	if _, err := Record(s, "t0", writeFindings(t,
		finding("correctness", "blocker"), finding("security", "major")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = "t1"
	out, err := Record(s, "t1", writeFindings(t, elsewhere("security", "major")), time.Time{})
	if err != nil || len(out.Discovered) != 1 {
		t.Fatalf("%+v, %v", out, err)
	}
	if got := filed(t, root); len(got) != 1 {
		t.Fatalf("filed = %+v", got)
	}
}

func TestUnfinishedRoundLeavesTheLastFinishedRoundsFindings(t *testing.T) {
	root := repo(t)
	s := session(root, "t0")
	if _, err := Record(s, "t0", writeFindings(t,
		finding("correctness", "blocker"), finding("security", "major")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = "t1"
	if out, err := Record(s, "t2", writeFindings(t), time.Time{}); err != nil || out.Verdict != "catch" {
		t.Fatalf("moved tree: %+v, %v", out, err)
	}
	if out, err := Record(s, "t1", filepath.Join(t.TempDir(), "missing.json"), time.Time{}); err != nil || out.Verdict != "catch" {
		t.Fatalf("missing findings: %+v, %v", out, err)
	}
	if got := filed(t, root); len(got) != 1 {
		t.Fatalf("filed = %+v", got)
	}
}

func TestCatchesToTheCapLeaveOnlyTheLastRoundsFindings(t *testing.T) {
	root := repo(t)
	s := session(root, "")
	early := strings.Replace(finding("security", "major"), "security major", "early claim", 1)
	late := strings.Replace(early, "early claim", "late claim", 1)
	for i, tree := range []string{"t0", "t1", "t2"} {
		s.Tree = tree
		other := early
		if i == MaxRounds-1 {
			other = late
		}
		if _, err := Record(s, tree, writeFindings(t, finding("correctness", "blocker"), other), time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	got := filed(t, root)
	if len(got) != 1 || !strings.Contains(got[0].Summary, "late claim") {
		t.Fatalf("filed = %+v", got)
	}
}

func TestAnAgentsRecordSurvivesEveryRound(t *testing.T) {
	root := repo(t)
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := discoveredwork.Append(dir, discoveredwork.Record{
		Summary: "agent note", Evidence: []string{"a.go:1"}, SourceStage: Stage, IssueNumber: 7,
		SuggestedType: "bug", SuggestedPriority: "low", SuggestedSize: "S",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(session(root, "t0"), "t0", writeFindings(t), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := filed(t, root); len(got) != 1 || got[0].SourceStage != Stage {
		t.Fatalf("filed = %+v", got)
	}
}

func TestTwoPassesAtOneLocationStayTwoFindings(t *testing.T) {
	root := repo(t)
	if _, err := Record(session(root, "t0"), "t0", writeFindings(t,
		finding("security", "major"), finding("plan-compliance", "major")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := filed(t, root); len(got) != 2 {
		t.Fatalf("filed = %+v", got)
	}
}

func TestRecordCatchThenSameTreeIsReusedAndDurationIsWritten(t *testing.T) {
	root := repo(t)
	s := session(root, "tree-a")
	started := s.Now.Add(-90 * time.Second)
	out, err := Record(s, "tree-a", writeFindings(t,
		finding("correctness", "blocker"), finding("security", "major"), finding("security", "nitpick")), started)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != "catch" || len(out.Fix) != 1 || len(out.Discovered) != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	got := rows(t, root)
	if len(got) != 1 || got[0].DurationMs != 90000 || got[0].Findings == nil ||
		*got[0].Findings != (state.FindingCounts{Blocker: 1, Major: 1, Nitpick: 1}) {
		t.Fatalf("row = %+v", got)
	}
	if got := filed(t, root); len(got) != 1 {
		t.Fatalf("discovered = %+v", got)
	}

	p, err := Consult(s)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Reusable || p.Result.Verdict != "catch" || len(p.Result.Fix) != 1 {
		t.Fatalf("second prepare must reuse the stored catch: %+v", p)
	}
	if got := rows(t, root); len(got) != 2 || got[1].Result != "catch" {
		t.Fatalf("reuse must restore the row for a fresh worktree: %+v", got)
	}
}

func TestPassOnFixedTreeSupersedesCatch(t *testing.T) {
	root := repo(t)
	s := session(root, "tree-a")
	if _, err := Record(s, "tree-a", writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = "tree-b"
	out, err := Record(s, "tree-b", writeFindings(t), time.Time{})
	if err != nil || out.Verdict != "pass" {
		t.Fatalf("%+v, %v", out, err)
	}
	got := rows(t, root)
	if got[len(got)-1].Result != "pass" {
		t.Fatalf("rows = %+v", got)
	}
}

func TestUnfinishedReviewsCatchAndLeaveNoLedgerEvent(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := writeFindings(t)
	for name, tc := range map[string]struct{ current, path string }{
		"no file":    {"tree-a", filepath.Join(t.TempDir(), "absent.json")},
		"rejected":   {"tree-a", bad},
		"tree moved": {"tree-b", empty},
	} {
		t.Run(name, func(t *testing.T) {
			root := repo(t)
			s := session(root, "tree-a")
			out, err := Record(s, tc.current, tc.path, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			if out.Verdict != "catch" || !strings.Contains(out.Summary, "did not complete") {
				t.Fatalf("outcome = %+v", out)
			}
			if ev, _ := gateledger.LatestReview(root, 7, "tree-a"); ev != nil {
				t.Fatalf("an unfinished review left a ledger event: %+v", ev)
			}
			p, err := Consult(s)
			if err != nil || p.Reusable {
				t.Fatalf("an unfinished review must run again: %+v, %v", p, err)
			}
		})
	}
}

func TestCapIsPerPipelineRunAndCountsCatchesOnly(t *testing.T) {
	root := repo(t)
	s := session(root, "")
	for _, tree := range []string{"t0", "t1", "t2"} {
		s.Tree = tree
		if p, err := Consult(s); err != nil || p.CapReached {
			t.Fatalf("%s: capped early: %+v, %v", tree, p, err)
		}
		if _, err := Record(s, tree, writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	s.Tree = "t3"
	p, err := Consult(s)
	if err != nil || !p.CapReached || p.Result.Verdict != "catch" {
		t.Fatalf("round 4 must stop: %+v, %v", p, err)
	}
	if len(p.Result.Fix) != 1 || p.Result.Counts.Blocker != 1 {
		t.Fatalf("the last catch's findings must stand at the cap: %+v", p.Result)
	}
	got := rows(t, root)
	if last := got[len(got)-1]; last.Result != "catch" || !strings.HasPrefix(last.ErrorSummary, CapMarker) {
		t.Fatalf("the cap must leave a marked catch: %+v", last)
	}
	rec, err := LatestCap(root, s.Issue)
	if err != nil || rec == nil {
		t.Fatalf("no cap record: %+v, %v", rec, err)
	}
	if rec.Worktree != root || rec.Tree != "t3" || len(rec.Outcome.Fix) != 1 {
		t.Fatalf("the record must name the worktree and the findings: %+v", rec)
	}
	if n, err := gateledger.ReviewCatches(root, s.Issue, s.PipelineRun); err != nil || n != MaxRounds {
		t.Fatalf("the cap record must not count as a round: %d, %v", n, err)
	}
	s.PipelineRun = "next"
	if p, err := Consult(s); err != nil || p.CapReached || p.Round != 1 {
		t.Fatalf("a new run starts a new count: %+v, %v", p, err)
	}
}

func TestReviewEventsDoNotTouchTheLocalGateAnswer(t *testing.T) {
	root := repo(t)
	if err := gateledger.Append(root, gateledger.Event{
		RunID: "g", Event: gateledger.EventFinish, Kind: gateledger.KindLocalGate,
		Tree: "tree-a", Verdict: gateledger.VerdictPass, Full: true, Issue: 7,
	}); err != nil {
		t.Fatal(err)
	}
	s := session(root, "tree-a")
	if _, err := Record(s, "tree-a", writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	a, err := gateledger.Latest(root, "tree-a", gateledger.KindLocalGate)
	if err != nil || !a.Reusable {
		t.Fatalf("a review catch unproved the gate pass: %+v, %v", a, err)
	}
	if _, err := Consult(s); err != nil { // writes a reused review event
		t.Fatal(err)
	}
	u, err := gateledger.UsageIn(root, 7, Stage, time.Unix(0, 0), time.Now().Add(time.Hour))
	if err != nil || u.Reused != 0 {
		t.Fatalf("a review reuse counted as a gate reuse: %+v, %v", u, err)
	}
}

// bundleRepo is a repository with the real ignore rules, a committed base at
// origin/main, and an issue context. ignored false leaves the rules out.
func bundleRepo(t *testing.T, ignored bool) Session {
	t.Helper()
	root := repo(t)
	if ignored {
		rules := filepath.Join(root, ".nightgauge", ".gitignore")
		if err := os.MkdirAll(filepath.Dir(rules), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rules, []byte(scaffold.GitignoreTemplate), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "base")
	gittest.Run(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, _ := layout.PipelineStateDir(root)
	body, _ := json.Marshal(map[string]any{"title": "T", "base_branch": "main", "requirements": map[string]string{"goal": "G"}})
	if err := os.WriteFile(filepath.Join(dir, "issue-7.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return session(root, fingerprint(t, root))
}

func fingerprint(t *testing.T, root string) string {
	t.Helper()
	tree, err := hooks.TreeFingerprint(context.Background(), &hooks.ExecCmdRunner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestBundleHoldsUncommittedWorkAndNoFindingsFile(t *testing.T) {
	s := bundleRepo(t, true)
	b, err := BuildBundle(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	diff, _ := os.ReadFile(b.DiffPath)
	if !strings.Contains(string(diff), "+two") {
		t.Fatalf("diff missed uncommitted work: %s", diff)
	}
	if _, err := os.Stat(b.FindingsPath); !os.IsNotExist(err) {
		t.Fatal("a pre-created findings file would read as a clean review")
	}
}

// Reuse rests on this: a bundle that moved the tree would make every review the
// review of a tree nobody can ask about again, and record would call it moved.
func TestBundleDoesNotMoveTheTree(t *testing.T) {
	s := bundleRepo(t, true)
	b, err := BuildBundle(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.FindingsPath, []byte(`{"schema":1,"findings":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fingerprint(t, s.Root); got != s.Tree {
		t.Fatalf("the bundle moved the tree: %s to %s", s.Tree, got)
	}
}

func TestBundleIsRefusedWhereGitWouldSeeIt(t *testing.T) {
	s := bundleRepo(t, false)
	if _, err := BuildBundle(context.Background(), s); err == nil {
		t.Fatal("a bundle was built in a location git does not ignore")
	}
	if _, err := os.Stat(filepath.Join(s.Root, ".nightgauge", "review")); !os.IsNotExist(err) {
		t.Fatalf("a refused bundle left files behind: %v", err)
	}
}

func TestBundleLivesInTheWorkspace(t *testing.T) {
	s := bundleRepo(t, true)
	b, err := BuildBundle(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{b.DiffPath, b.IssuePath, b.PassesPath, b.PromptPath, b.FindingsPath} {
		if !strings.HasPrefix(p, filepath.Join(s.Root, ".nightgauge", "review", "7-")) {
			t.Errorf("%s is outside the workspace's bundle directory", p)
		}
	}
}

func TestNewBundleSweepsEarlierOnesAndTheLogOutlivesThem(t *testing.T) {
	s := bundleRepo(t, true)
	ctx := context.Background()
	first, err := BuildBundle(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	neighbour := filepath.Join(filepath.Dir(first.Dir), "77-other-issue")
	if err := os.Mkdir(neighbour, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first.LogPath, []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Now = s.Now.Add(time.Minute)
	second, err := BuildBundle(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Dir); !os.IsNotExist(err) {
		t.Errorf("the earlier bundle survived: %v", err)
	}
	if _, err := os.Stat(second.Dir); err != nil {
		t.Errorf("the new bundle is gone: %v", err)
	}
	if _, err := os.Stat(neighbour); err != nil {
		t.Errorf("another issue's bundle was swept: %v", err)
	}
	if data, err := os.ReadFile(first.LogPath); err != nil || string(data) != "log" {
		t.Errorf("the earlier log did not survive: %q, %v", data, err)
	}
	logs, _ := layout.CloneLogsDir(s.Root)
	if filepath.Dir(second.LogPath) != logs || second.LogPath == first.LogPath {
		t.Errorf("log paths: %s then %s, want separate files in %s", first.LogPath, second.LogPath, logs)
	}
}

func TestRulesComeFromTheBundleNotTheTree(t *testing.T) {
	s := bundleRepo(t, true)
	b, err := BuildBundle(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := os.ReadFile(b.PromptPath)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := os.ReadFile(b.PassesPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rules, passesDoc) {
		t.Fatal("the bundle's rules are not the embedded rules")
	}
	if !strings.Contains(string(prompt), b.PassesPath) {
		t.Fatalf("the prompt does not point at the bundle's rules: %s", prompt)
	}
	for _, banned := range []string{"docs/", "PR_REVIEW_PASSES"} {
		if strings.Contains(string(prompt), banned) {
			t.Fatalf("the prompt sends the reviewer to the tree for its rules: %q", banned)
		}
	}
}

func TestPassesMatchTheDoc(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "PR_REVIEW_PASSES.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	start, end := strings.Index(doc, "## Passes\n"), strings.Index(doc, "## Turn limit\n")
	if start < 0 || end < start {
		t.Fatal("docs/PR_REVIEW_PASSES.md lost its Passes or Turn limit heading")
	}
	// The copy ends where its last section does; the doc has a blank line there.
	if want := strings.TrimRight(doc[start:end], "\n") + "\n"; want != string(passesDoc) {
		t.Fatalf("internal/review/passes.md differs from the Passes through Caps sections of "+
			"docs/PR_REVIEW_PASSES.md. Copy them across.\nwant:\n%s", want)
	}
}

func TestCapWithNoReadableCatchStillReports(t *testing.T) {
	root := repo(t)
	s := session(root, "t3")
	for i := range MaxRounds {
		if err := gateledger.Append(root, gateledger.Event{
			RunID: fmt.Sprintf("c%d", i), Event: gateledger.EventFinish, Kind: gateledger.KindReview,
			Tree: fmt.Sprintf("t%d", i), Verdict: gateledger.VerdictFail, Issue: s.Issue,
			PipelineRun: s.PipelineRun,
		}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Consult(s)
	if err != nil || !p.CapReached {
		t.Fatalf("the cap must stop: %+v, %v", p, err)
	}
	if p.Result.Fix == nil || len(p.Result.Fix) != 0 {
		t.Fatalf("an unreadable catch leaves an empty fix list, never nil: %+v", p.Result)
	}
}

// TestThirdCatchThenSameTreeReachesTheCap: a third catch that leaves the tree
// unchanged must still write the cap record, not reuse the catch (#2876).
func TestThirdCatchThenSameTreeReachesTheCap(t *testing.T) {
	root := repo(t)
	s := session(root, "")
	for _, tree := range []string{"t0", "t1", "t2"} {
		s.Tree = tree
		if p, err := Consult(s); err != nil || p.CapReached {
			t.Fatalf("%s: capped early: %+v, %v", tree, p, err)
		}
		if _, err := Record(s, tree, writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	s.Tree = "t2"
	p, err := Consult(s)
	if err != nil || !p.CapReached || p.Reusable || p.Result.Verdict != "catch" {
		t.Fatalf("the unchanged tree must stop at the cap: %+v, %v", p, err)
	}
	if len(p.Result.Fix) != 1 {
		t.Fatalf("the third catch's findings must stand: %+v", p.Result)
	}
	rec, err := LatestCap(root, s.Issue)
	if err != nil || rec == nil {
		t.Fatalf("no cap record: %+v, %v", rec, err)
	}
	if rec.Tree != "t2" || rec.Worktree != root || len(rec.Outcome.Fix) != 1 || rec.RunID == "" {
		t.Fatalf("the record must name the tree, worktree, findings and run: %+v", rec)
	}
	if n, err := gateledger.ReviewCatches(root, s.Issue, s.PipelineRun); err != nil || n != MaxRounds {
		t.Fatalf("the cap record must not count as a round: %d, %v", n, err)
	}
}

// TestReuseBelowTheCapAndPassAtTheCap: a stored catch below the cap and a
// stored pass at the cap are both reused, with no cap record.
func TestReuseBelowTheCapAndPassAtTheCap(t *testing.T) {
	root := repo(t)
	s := session(root, "t0")
	if _, err := Record(s, "t0", writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if p, err := Consult(s); err != nil || !p.Reusable || p.CapReached || p.Result.Verdict != "catch" {
		t.Fatalf("below the cap a stored catch reuses: %+v, %v", p, err)
	}
	for _, tree := range []string{"t1", "t2"} {
		s.Tree = tree
		if _, err := Record(s, tree, writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	s.Tree = "t3"
	if _, err := Record(s, "t3", writeFindings(t), time.Time{}); err != nil {
		t.Fatal(err)
	}
	p, err := Consult(s)
	if err != nil || !p.Reusable || p.CapReached || p.Result.Verdict != "pass" {
		t.Fatalf("a stored pass at the cap reuses: %+v, %v", p, err)
	}
	if rec, err := LatestCap(root, s.Issue); err != nil || rec != nil {
		t.Fatalf("no cap record expected: %+v, %v", rec, err)
	}
}
