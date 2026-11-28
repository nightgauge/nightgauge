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
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/gateledger"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/internal/runstate"
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
		"empty":         ``,
		"not json":      `nope`,
		"wrong schema":  `{"schema":2,"findings":[]}`,
		"no findings":   `{"schema":1}`,
		"extra key":     `{"schema":1,"findings":[],"x":1}`,
		"trailing":      `{"schema":1,"findings":[]}{}`,
		"finding key":   line(`"line":1,"x":1`),
		"missing line":  line(`"pad":0`)[:0] + `{"schema":1,"findings":[{"pass":"security","file":"a","claim":"c","severity":"major","fix":"f"}]}`,
		"zero line":     line(`"line":0`),
		"unknown pass":  `{"schema":1,"findings":[` + finding("style", "major") + `]}`,
		"unknown sev":   `{"schema":1,"findings":[` + finding("security", "minor") + `]}`,
		"absolute":      strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `"a.go"`, `"/etc/a"`, 1),
		"dotdot":        strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `"a.go"`, `"x/../../a"`, 1),
		"multiline":     strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `correctness major`, `a\nb`, 1),
		"long claim":    strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `correctness major`, strings.Repeat("x", maxClaim+1), 1),
		"long fix":      strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `"change"`, `"`+strings.Repeat("x", maxFix+1)+`"`, 1),
		"credential":    strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `change`, `ghp_abc`, 1),
		"file newline":  strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `"a.go"`, `"a\nb.go"`, 1),
		"file tab":      strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `"a.go"`, `"a\tb.go"`, 1),
		"file NUL":      strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `"a.go"`, `"a\u0000.go"`, 1),
		"file bidi":     strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `"a.go"`, `"a\u202e.go"`, 1),
		"claim control": strings.Replace(`{"schema":1,"findings":[`+ok+`]}`, `correctness major`, `x\u0007y`, 1),
		"over the cap":  `{"schema":1,"findings":[` + many + `]}`,
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
		fix, disc := Route([]Finding{{Pass: tc.pass, Severity: tc.severity}}, nil)
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
	if _, err := Record(context.Background(), s, "t0", writeFindings(t,
		finding("correctness", "blocker"), finding("security", "major")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = "t1"
	out, err := Record(context.Background(), s, "t1", writeFindings(t), time.Time{})
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
	if _, err := Record(context.Background(), s, "t0", writeFindings(t,
		finding("correctness", "blocker"), finding("security", "major")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = "t1"
	out, err := Record(context.Background(), s, "t1", writeFindings(t, elsewhere("security", "major")), time.Time{})
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
	if _, err := Record(context.Background(), s, "t0", writeFindings(t,
		finding("correctness", "blocker"), finding("security", "major")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = "t1"
	if out, err := Record(context.Background(), s, "t2", writeFindings(t), time.Time{}); err != nil || out.Verdict != "catch" {
		t.Fatalf("moved tree: %+v, %v", out, err)
	}
	if out, err := Record(context.Background(), s, "t1", filepath.Join(t.TempDir(), "missing.json"), time.Time{}); err != nil || out.Verdict != "catch" {
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
		if i == config.DefaultReviewMaxRounds-1 {
			other = late
		}
		if _, err := Record(context.Background(), s, tree, writeFindings(t, finding("correctness", "blocker"), other), time.Time{}); err != nil {
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
	if _, err := Record(context.Background(), session(root, "t0"), "t0", writeFindings(t), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := filed(t, root); len(got) != 1 || got[0].SourceStage != Stage {
		t.Fatalf("filed = %+v", got)
	}
}

func TestTwoPassesAtOneLocationStayTwoFindings(t *testing.T) {
	root := repo(t)
	if _, err := Record(context.Background(), session(root, "t0"), "t0", writeFindings(t,
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
	out, err := Record(context.Background(), s, "tree-a", writeFindings(t,
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
	if _, err := Record(context.Background(), s, "tree-a", writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = "tree-b"
	out, err := Record(context.Background(), s, "tree-b", writeFindings(t), time.Time{})
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
			out, err := Record(context.Background(), s, tc.current, tc.path, time.Time{})
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
		if _, err := Record(context.Background(), s, tree, writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
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
	if n, err := gateledger.ReviewCatches(root, s.Issue, s.PipelineRun); err != nil || n != config.DefaultReviewMaxRounds {
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
	if _, err := Record(context.Background(), s, "tree-a", writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
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

// A tracked file matching a local exclude must not read as deleted (#2889).
func TestBundleKeepsTrackedExcludedFiles(t *testing.T) {
	s := bundleRepo(t, true)
	if err := os.WriteFile(filepath.Join(s.Root, ".git", "info", "exclude"), []byte("a.txt\n.gitignore\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Tree = fingerprint(t, s.Root)
	b, err := BuildBundle(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	diff, _ := os.ReadFile(b.DiffPath)
	if strings.Contains(string(diff), "deleted file mode") {
		t.Fatalf("a tracked excluded file reads as deleted: %s", diff)
	}
	if !strings.Contains(string(diff), "+two") {
		t.Fatalf("diff missed uncommitted work: %s", diff)
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
	for i := range config.DefaultReviewMaxRounds {
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
		if _, err := Record(context.Background(), s, tree, writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
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
	if n, err := gateledger.ReviewCatches(root, s.Issue, s.PipelineRun); err != nil || n != config.DefaultReviewMaxRounds {
		t.Fatalf("the cap record must not count as a round: %d, %v", n, err)
	}
}

// TestReuseBelowTheCapAndPassAtTheCap: a stored catch below the cap and a
// stored pass at the cap are both reused, with no cap record.
func TestReuseBelowTheCapAndPassAtTheCap(t *testing.T) {
	root := repo(t)
	s := session(root, "t0")
	if _, err := Record(context.Background(), s, "t0", writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if p, err := Consult(s); err != nil || !p.Reusable || p.CapReached || p.Result.Verdict != "catch" {
		t.Fatalf("below the cap a stored catch reuses: %+v, %v", p, err)
	}
	for _, tree := range []string{"t1", "t2"} {
		s.Tree = tree
		if _, err := Record(context.Background(), s, tree, writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	s.Tree = "t3"
	if _, err := Record(context.Background(), s, "t3", writeFindings(t), time.Time{}); err != nil {
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

func TestClaimCriterionIsDerivedForPlanComplianceOnly(t *testing.T) {
	for _, tc := range []struct {
		pass, claim string
		want        int
	}{
		{"plan-compliance", "Criterion 2: the test is missing", 2},
		{"plan-compliance", "acceptance criterion 10 - not delivered", 10},
		{"plan-compliance", "criterion 3. absent", 3},
		{"plan-compliance", "The change misses criterion 2", 0},
		{"plan-compliance", "Criterion two: absent", 0},
		{"security", "Criterion 2: the token leaks", 0},
		{"correctness", "Criterion 1: wrong", 0},
	} {
		if got := claimCriterion(tc.pass, tc.claim); got != tc.want {
			t.Errorf("%s %q: got %d, want %d", tc.pass, tc.claim, got, tc.want)
		}
	}
}

// TestFindingsSchemaStaysClosedToACriterionKey: the criterion link is derived
// from the claim, so neither a criterion nor a criterion_text key is accepted,
// and the derived value never leaves in JSON (#3186).
func TestFindingsSchemaStaysClosedToACriterionKey(t *testing.T) {
	ok := `{"pass":"plan-compliance","file":"a.go","line":1,"claim":"Criterion 1: x","severity":"major","fix":"f"`
	for _, extra := range []string{`,"criterion":1}`, `,"criterion_text":"x"}`, `,"Criterion":1}`} {
		if _, err := Parse([]byte(`{"schema":1,"findings":[` + ok + extra + `]}`)); err == nil {
			t.Errorf("accepted the key in %s", extra)
		}
	}
	got, err := Parse([]byte(`{"schema":1,"findings":[` + ok + `}]}`))
	if err != nil || len(got) != 1 || got[0].Criterion != 1 {
		t.Fatalf("%+v, %v", got, err)
	}
	out, _ := json.Marshal(got[0])
	var keys map[string]any
	if err := json.Unmarshal(out, &keys); err != nil {
		t.Fatal(err)
	}
	for k := range keys {
		if strings.Contains(strings.ToLower(k), "criterion") {
			t.Fatalf("the derived criterion was serialised: %s", out)
		}
	}
}

func writeIssueContext(t *testing.T, root string, criteria ...string) {
	t.Helper()
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := json.Marshal(map[string]any{
		"title": "t", "requirements": map[string]any{"acceptance_criteria": criteria},
	})
	if err := os.WriteFile(filepath.Join(dir, "issue-7.json"), doc, 0o600); err != nil {
		t.Fatal(err)
	}
}

func planFinding(claim string) string {
	return `{"pass":"plan-compliance","file":"m.json","line":3,"claim":"` + claim +
		`","severity":"major","fix":"f"}`
}

// TestRecordLinksTheNamedCriterionAndTheRun: a plan-compliance major naming
// criterion 2 carries that criterion's text and the checkout's run id; one
// naming no criterion, or one out of range, carries none.
func TestRecordLinksTheNamedCriterionAndTheRun(t *testing.T) {
	root := repo(t)
	writeIssueContext(t, root, "First criterion.", "  `opencode.json` records the\n newest tested version. ")
	checkout, err := layout.CheckoutDir(root)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := runstate.MarkRunning(checkout, runstate.MarkRunningOptions{IssueNumber: 7, Branch: "feat/7-x"})
	if err != nil {
		t.Fatal(err)
	}
	s := session(root, "t0")
	if _, err := Record(context.Background(), s, "t0", writeFindings(t,
		planFinding("Criterion 2: max_tested was not bumped"),
		planFinding("Criterion 9: out of range"),
		planFinding("scope creep in m.json")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	got := map[string]discoveredwork.Record{}
	for _, r := range filed(t, root) {
		got[r.Summary] = r
	}
	if len(got) != 3 {
		t.Fatalf("records: %+v", got)
	}
	for summary, want := range map[string]string{
		"plan-compliance: Criterion 2: max_tested was not bumped": "`opencode.json` records the newest tested version.",
		"plan-compliance: Criterion 9: out of range":              "",
		"plan-compliance: scope creep in m.json":                  "",
	} {
		r := got[summary]
		if r.Criterion != want {
			t.Errorf("%s: criterion %q, want %q", summary, r.Criterion, want)
		}
		if r.RunID == nil || *r.RunID != rs.RunID {
			t.Errorf("%s: run id %v, want %s", summary, r.RunID, rs.RunID)
		}
	}
}

// TestRecordWithoutAnIssueContextLinksNoCriterion files as before.
func TestRecordWithoutAnIssueContextLinksNoCriterion(t *testing.T) {
	root := repo(t)
	s := session(root, "t0")
	if _, err := Record(context.Background(), s, "t0", writeFindings(t, planFinding("Criterion 1: absent")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	recs := filed(t, root)
	if len(recs) != 1 || recs[0].Criterion != "" || recs[0].RunID != nil {
		t.Fatalf("%+v", recs)
	}
}

// threeSectionBody spreads its criteria over three headings, the #3081 shape a
// model extraction truncated to the first section.
const threeSectionBody = `## Summary

Intro.

## Acceptance Criteria

- [ ] First section criterion.

## Release criteria

- [ ] Second section criterion.

## Docs criteria

- [ ] Third section criterion names docs/X.md.
`

func writeReconcile(t *testing.T, root, body string) []string {
	t.Helper()
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	res := preflight.ReconcileACs(root, 7, body)
	if err := preflight.WriteACReconcile(res, filepath.Join(dir, "ac-reconcile-7.json")); err != nil {
		t.Fatal(err)
	}
	texts := make([]string, len(res.AcceptanceCriteria))
	for i, c := range res.AcceptanceCriteria {
		texts[i] = c.Text
	}
	return texts
}

// TestIssueContextCarriesEveryReconciledCriterion (#3354): the bundle's issue
// gets the report's criteria from all sections, in order, not the model's
// one-item list, and keeps the other requirements keys.
func TestIssueContextCarriesEveryReconciledCriterion(t *testing.T) {
	root := repo(t)
	writeIssueContext(t, root, "First section criterion.")
	want := writeReconcile(t, root, threeSectionBody)
	if len(want) != 3 {
		t.Fatalf("parser found %d criteria: %v", len(want), want)
	}
	ic, err := loadIssueContext(root, 7)
	if err != nil {
		t.Fatal(err)
	}
	got := ic.criteria()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("criteria %q, want %q", got, want)
	}
	if !strings.Contains(want[2], "Third section") {
		t.Fatalf("order: %q", want)
	}
}

// TestReconciledCriteriaKeepOtherRequirementsKeys: only the criteria list is
// replaced, and a null requirements value does not panic.
func TestReconciledCriteriaKeepOtherRequirementsKeys(t *testing.T) {
	dir := t.TempDir()
	report := `{"acceptance_criteria":[{"index":1,"text":"A."}]}`
	if err := os.WriteFile(filepath.Join(dir, "ac-reconcile-7.json"), []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	ic := issueContext{Requirements: json.RawMessage(`{"acceptance_criteria":["x"],"constraints":["keep"]}`)}
	got := ic.withReconciledCriteria(dir, 7)
	if string(got.Requirements) != `{"acceptance_criteria":["A."],"constraints":["keep"]}` {
		t.Fatalf("requirements %s", got.Requirements)
	}
	null := issueContext{Requirements: json.RawMessage(`null`)}.withReconciledCriteria(dir, 7)
	if c := null.criteria(); len(c) != 1 || c[0] != "A." {
		t.Fatalf("null requirements: %q", c)
	}
}

// TestRecordResolvesACriterionBeyondTheModelList (#3354): "Criterion 3" names
// the third section's criterion, which the model list did not hold.
func TestRecordResolvesACriterionBeyondTheModelList(t *testing.T) {
	root := repo(t)
	writeIssueContext(t, root, "First section criterion.")
	want := writeReconcile(t, root, threeSectionBody)
	if _, err := Record(context.Background(), session(root, "t0"), "t0",
		writeFindings(t, planFinding("Criterion 3: docs missing")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	recs := filed(t, root)
	if len(recs) != 1 || recs[0].Criterion != want[2] {
		t.Fatalf("records %+v, want criterion %q", recs, want[2])
	}
}

// TestIssueContextWithoutAReportIsUnchanged: no report, the model list stands.
func TestIssueContextWithoutAReportIsUnchanged(t *testing.T) {
	root := repo(t)
	writeIssueContext(t, root, "Only.")
	ic, err := loadIssueContext(root, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got := ic.criteria(); len(got) != 1 || got[0] != "Only." {
		t.Fatalf("%q", got)
	}
}

func summaries(recs []discoveredwork.Record) []string {
	out := []string{}
	for _, r := range recs {
		if r.SourceStage == discoveredProducer {
			out = append(out, r.Summary+"|"+strings.Join(r.Evidence, ";"))
		}
	}
	return out
}

func claimed(pass, severity, claim string) string {
	return strings.Replace(finding(pass, severity), pass+" "+severity, claim, 1)
}

// TestReuseRestoresThatRoundsDiscoveredWork: trees A, B, A leave A's records,
// not B's (#2872).
func TestReuseRestoresThatRoundsDiscoveredWork(t *testing.T) {
	root := repo(t)
	s := session(root, "A")
	if _, err := Record(context.Background(), s, "A", writeFindings(t, claimed("security", "major", "claim X")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	want := summaries(filed(t, root))
	s.Tree = "B"
	if _, err := Record(context.Background(), s, "B", writeFindings(t, claimed("security", "major", "claim Y")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = "A"
	p, err := Consult(s)
	if err != nil || !p.Reusable {
		t.Fatalf("%+v, %v", p, err)
	}
	got := summaries(filed(t, root))
	if strings.Join(got, "\n") != strings.Join(want, "\n") || len(got) != 1 ||
		!strings.Contains(got[0], "claim X") {
		t.Fatalf("capture after reuse = %v, want %v", got, want)
	}
	if len(p.Result.Discovered) != 1 {
		t.Fatalf("reused outcome must report its records: %+v", p.Result)
	}
}

// TestReusedPassWithNoDiscoveredClearsAnotherRounds: a stored round with none
// stores [] and restores an empty set.
func TestReusedPassWithNoDiscoveredClearsAnotherRounds(t *testing.T) {
	root := repo(t)
	s := session(root, "A")
	if _, err := Record(context.Background(), s, "A", writeFindings(t), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = "B"
	if _, err := Record(context.Background(), s, "B", writeFindings(t, claimed("security", "major", "claim Y")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = "A"
	if p, err := Consult(s); err != nil || !p.Reusable || p.Result.Verdict != "pass" {
		t.Fatalf("%+v, %v", p, err)
	}
	if got := summaries(filed(t, root)); len(got) != 0 {
		t.Fatalf("B's records survived A's reuse: %v", got)
	}
}

// TestReusedCapRestoresTheLastCatchsDiscoveredWork: the cap carries the last
// catch's records, as a fresh round on that tree leaves them.
func TestReusedCapRestoresTheLastCatchsDiscoveredWork(t *testing.T) {
	root := repo(t)
	s := session(root, "")
	for i, tree := range []string{"t0", "t1", "t2"} {
		s.Tree = tree
		claim := fmt.Sprintf("claim %d", i)
		if _, err := Record(context.Background(), s, tree, writeFindings(t, finding("correctness", "blocker"),
			claimed("security", "major", claim)), time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	want := summaries(filed(t, root))
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := discoveredwork.ReplaceProducer(dir, 7, discoveredProducer, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	p, err := Consult(s)
	if err != nil || !p.CapReached {
		t.Fatalf("%+v, %v", p, err)
	}
	if got := summaries(filed(t, root)); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("cap capture = %v, want %v", got, want)
	}
	rec, err := LatestCap(root, s.Issue)
	if err != nil || rec == nil || len(rec.Outcome.Discovered) != 1 {
		t.Fatalf("the cap record must store its records: %+v, %v", rec, err)
	}
}

// TestReuseOfAnEventWithoutDiscoveredLeavesTheCapture: an event written before
// the field existed restores nothing, and an agent's record survives.
func TestReuseOfAnEventWithoutDiscoveredLeavesTheCapture(t *testing.T) {
	root := repo(t)
	s := session(root, "B")
	if _, err := Record(context.Background(), s, "B", writeFindings(t, claimed("security", "major", "claim Y")), time.Time{}); err != nil {
		t.Fatal(err)
	}
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
	if err := gateledger.Append(root, gateledger.Event{
		RunID: "old", Event: gateledger.EventFinish, Kind: gateledger.KindReview, Tree: "A",
		Verdict: gateledger.VerdictPass, Issue: 7, Stage: Stage,
		Review: []byte(`{"verdict":"pass","counts":{},"fix":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	s.Tree = "A"
	if p, err := Consult(s); err != nil || !p.Reusable {
		t.Fatalf("%+v, %v", p, err)
	}
	got := filed(t, root)
	if len(got) != 2 {
		t.Fatalf("capture changed: %+v", got)
	}
}

// writeTree writes files into root's index and returns the tree git records.
func writeTree(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if _, err := git(ctx, root, "add", "-A", "--", "."); err != nil {
		t.Fatal(err)
	}
	out, err := git(ctx, root, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func lines(n int, changed map[int]string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if v, ok := changed[i]; ok {
			b.WriteString(v + "\n")
			continue
		}
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func at(pass, severity, file string, line int, claim string) string {
	return fmt.Sprintf(`{"pass":%q,"file":%q,"line":%d,"claim":%q,"severity":%q,"fix":"change"}`,
		pass, file, line, claim, severity)
}

// TestCapFollowsMaxRounds: the cap is review.max_rounds, and the summary and the
// cap record name it.
func TestCapFollowsMaxRounds(t *testing.T) {
	for _, rounds := range []int{2, 4} {
		t.Run(fmt.Sprint(rounds), func(t *testing.T) {
			root := repo(t)
			s := session(root, "")
			s.MaxRounds = rounds
			for i := range rounds {
				s.Tree = fmt.Sprintf("t%d", i)
				if p, err := Consult(s); err != nil || p.CapReached || p.Round != i+1 {
					t.Fatalf("round %d: %+v, %v", i+1, p, err)
				}
				if _, err := Record(context.Background(), s, s.Tree, writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
					t.Fatal(err)
				}
			}
			s.Tree = "next"
			p, err := Consult(s)
			if err != nil || !p.CapReached {
				t.Fatalf("round %d must be capped: %+v, %v", rounds+1, p, err)
			}
			if want := fmt.Sprintf("cap of %d reached", rounds); !strings.Contains(p.Result.Summary, want) || p.Result.Rounds != rounds {
				t.Fatalf("summary %q rounds %d, want %q", p.Result.Summary, p.Result.Rounds, want)
			}
			rec, err := LatestCap(root, s.Issue)
			if err != nil || rec == nil || rec.Outcome.Rounds != rounds {
				t.Fatalf("the cap record must name %d rounds: %+v, %v", rounds, rec, err)
			}
		})
	}
}

// TestLastRoundRoutesByChangedLines: in the last allowed round, a correctness
// major outside the lines changed since the previous round's tree is discovered
// work; inside them, and any blocker, it is to fix.
func TestLastRoundRoutesByChangedLines(t *testing.T) {
	root := repo(t)
	s := session(root, "")
	s.MaxRounds = 2
	s.Tree = writeTree(t, root, map[string]string{"a.go": lines(20, nil), "b.go": lines(5, nil)})
	if _, err := Record(context.Background(), s, s.Tree, writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = writeTree(t, root, map[string]string{"a.go": lines(20, map[int]string{10: "fixed"})})
	out, err := Record(context.Background(), s, s.Tree, writeFindings(t,
		at("correctness", "major", "a.go", 10, "inside"),
		at("correctness", "major", "a.go", 3, "outside"),
		at("correctness", "major", "b.go", 1, "untouched file"),
		at("security", "blocker", "b.go", 2, "blocker outside")), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var fix, disc []string
	for _, f := range out.Fix {
		fix = append(fix, f.Claim)
	}
	for _, f := range out.Discovered {
		disc = append(disc, f.Claim)
	}
	if strings.Join(fix, ",") != "inside,blocker outside" {
		t.Fatalf("fix = %v", fix)
	}
	if strings.Join(disc, ",") != "outside,untouched file" {
		t.Fatalf("discovered = %v", disc)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("warnings = %v", out.Warnings)
	}
}

// TestLastRoundIgnoresDiffNoprefix: a user's diff.noprefix=true must not drop
// the scope, which would turn every in-diff correctness major into discovered work.
func TestLastRoundIgnoresDiffNoprefix(t *testing.T) {
	root := repo(t)
	gittest.Run(t, root, "config", "diff.noprefix", "true")
	s := session(root, "")
	s.MaxRounds = 2
	s.Tree = writeTree(t, root, map[string]string{"a.go": lines(20, nil)})
	if _, err := Record(context.Background(), s, s.Tree, writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = writeTree(t, root, map[string]string{"a.go": lines(20, map[int]string{10: "fixed"})})
	out, err := Record(context.Background(), s, s.Tree, writeFindings(t,
		at("correctness", "major", "a.go", 10, "inside")), time.Time{})
	if err != nil || len(out.Fix) != 1 || len(out.Discovered) != 0 {
		t.Fatalf("in-diff major must stay to fix under diff.noprefix: %+v, %v", out, err)
	}
}

// TestEarlierRoundFixesAnOutsideMajor: before the last round, a correctness
// major outside the changed lines is still to fix.
func TestEarlierRoundFixesAnOutsideMajor(t *testing.T) {
	root := repo(t)
	s := session(root, "")
	s.MaxRounds = 3
	s.Tree = writeTree(t, root, map[string]string{"a.go": lines(20, nil)})
	if _, err := Record(context.Background(), s, s.Tree, writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = writeTree(t, root, map[string]string{"a.go": lines(20, map[int]string{10: "fixed"})})
	out, err := Record(context.Background(), s, s.Tree, writeFindings(t,
		at("correctness", "major", "a.go", 3, "outside")), time.Time{})
	if err != nil || len(out.Fix) != 1 || len(out.Discovered) != 0 {
		t.Fatalf("round 2 of 3 routes as before: %+v, %v", out, err)
	}
}

// TestLastRoundWithUnknownScopeFixes: a previous tree git cannot read leaves the
// scope unknown, so the major is to fix and a warning names why.
func TestLastRoundWithUnknownScopeFixes(t *testing.T) {
	root := repo(t)
	s := session(root, "")
	s.MaxRounds = 2
	s.Tree = "0000000000000000000000000000000000000001"
	if _, err := Record(context.Background(), s, s.Tree, writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Tree = writeTree(t, root, map[string]string{"a.go": lines(5, nil)})
	out, err := Record(context.Background(), s, s.Tree, writeFindings(t,
		at("correctness", "major", "a.go", 3, "outside")), time.Time{})
	if err != nil || len(out.Fix) != 1 || len(out.Warnings) != 1 ||
		!strings.Contains(out.Warnings[0], "last-round scope unknown") {
		t.Fatalf("an unknown scope is must-fix with a warning: %+v, %v", out, err)
	}
}

// TestLastRoundOnAnUnchangedTreeFixes: the previous round reviewed this same
// tree, so nothing changed and the scope is unknown, not empty.
func TestLastRoundOnAnUnchangedTreeFixes(t *testing.T) {
	root := repo(t)
	s := session(root, "")
	s.MaxRounds = 2
	s.Tree = writeTree(t, root, map[string]string{"a.go": lines(5, nil)})
	if _, err := Record(context.Background(), s, s.Tree, writeFindings(t, finding("security", "blocker")), time.Time{}); err != nil {
		t.Fatal(err)
	}
	out, err := Record(context.Background(), s, s.Tree, writeFindings(t,
		at("correctness", "major", "a.go", 3, "c")), time.Time{})
	if err != nil || len(out.Fix) != 1 || len(out.Discovered) != 0 {
		t.Fatalf("an unchanged tree routes as before: %+v, %v", out, err)
	}
}

// TestMaxRoundsOneHasNoScope: with one round there is no previous tree, so the
// rule cannot apply.
func TestMaxRoundsOneHasNoScope(t *testing.T) {
	root := repo(t)
	s := session(root, "")
	s.MaxRounds = 1
	s.Tree = writeTree(t, root, map[string]string{"a.go": lines(5, nil)})
	out, err := Record(context.Background(), s, s.Tree, writeFindings(t,
		at("correctness", "major", "a.go", 3, "c")), time.Time{})
	if err != nil || len(out.Fix) != 1 || len(out.Warnings) != 0 {
		t.Fatalf("max_rounds 1 routes as before: %+v, %v", out, err)
	}
}

func TestRouteWithScope(t *testing.T) {
	one, nine := 1, 9
	scope := ChangedLines{"a.go": {{5, 10}}}
	fix, disc := Route([]Finding{
		{Pass: "correctness", Severity: "major", File: "a.go", Line: &nine, Claim: "in range"},
		{Pass: "correctness", Severity: "major", File: "a.go", Line: &one, Claim: "out of range"},
		{Pass: "correctness", Severity: "major", File: "a.go", Claim: "no line, changed file"},
		{Pass: "correctness", Severity: "major", File: "c.go", Claim: "unchanged file"},
		{Pass: "security", Severity: "blocker", File: "c.go", Claim: "blocker"},
		{Pass: "security", Severity: "major", File: "a.go", Line: &nine, Claim: "security major"},
	}, &scope)
	claims := func(fs []Finding) string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Claim)
		}
		return strings.Join(out, ",")
	}
	if got := claims(fix); got != "in range,no line, changed file,blocker" {
		t.Errorf("fix = %s", got)
	}
	if got := claims(disc); got != "out of range,unchanged file,security major" {
		t.Errorf("discovered = %s", got)
	}
}

func TestParseChangedLines(t *testing.T) {
	diff := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -3 +3 @@
-old
+new
@@ -10,2 +10,3 @@ func x() {
@@ -20,4 +22,0 @@
diff --git a/gone.go b/gone.go
--- a/gone.go
+++ /dev/null
@@ -1,3 +0,0 @@
diff --git a/new.go b/new.go
--- /dev/null
+++ b/new.go
@@ -0,0 +1,4 @@
`
	got := parseChangedLines([]byte(diff))
	want := ChangedLines{"a.go": {{3, 3}, {10, 12}, {22, 22}}, "new.go": {{1, 4}}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestRowNamesTheModel: every review row names the reviewer's model.
func TestRowNamesTheModel(t *testing.T) {
	root := repo(t)
	s := session(root, "t0")
	s.Model = "opus"
	if _, err := Record(context.Background(), s, "t0", writeFindings(t), time.Time{}); err != nil {
		t.Fatal(err)
	}
	s.Model, s.Tree = "", "t1"
	if _, err := Record(context.Background(), s, "t1", writeFindings(t), time.Time{}); err != nil {
		t.Fatal(err)
	}
	path, err := layout.CheckoutPath(root, state.GateMetricsName)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var models []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		models = append(models, row.Model)
	}
	if strings.Join(models, ",") != "opus,adapter-default" {
		t.Fatalf("models = %v", models)
	}
}
