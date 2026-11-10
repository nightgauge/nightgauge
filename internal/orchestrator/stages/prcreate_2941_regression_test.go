package stages

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gateledger"
	"github.com/nightgauge/nightgauge/internal/preflight"
)

// Regression replays for #2941. Each one feeds pr-create the inputs a real
// run gave it and that it misread, so a complete delivery merged as `Refs`.

// The #2934 criteria, verbatim.
var issue2934ACs = []string{
	"`TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched` waits until `BackgroundInFlight()` is 0, after `WaitStopped` and before the first snapshot (for example with `stopAndJoinLoop`). It still fails if the loop comes up again or writes after that point",
	"No production code under `internal/orchestrator` changes. The test still pins the #2891 guarantee that a resumed loop stopped with `Stop` and `WaitStopped` is joined",
	"`go test ./internal/ipc -run 'TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched|TestAutonomous' -race -count=50` passes on the branch, and the command and its result are in the PR body",
}

const stopJoinTestBase = `package ipc

import "testing"

func TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched(t *testing.T) {
	// snapshot taken right after WaitStopped
}
`

const stopJoinTestFixed = `package ipc

import "testing"

func TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched(t *testing.T) {
	stopAndJoinLoop(t) // waits until BackgroundInFlight() is 0
}
`

// The lines of /tmp/ng-gate-2934.log the criteria turn on.
const gateLog2934 = `=== RUN   TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched
--- PASS: TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched (0.35s)
=== RUN   TestAutonomousStopJoin
--- PASS: TestAutonomousStopJoin (0.01s)
ok  	github.com/nightgauge/nightgauge/internal/ipc	12.004s
`

func writePipelineJSON(t *testing.T, dir, name string, v any) {
	t.Helper()
	p := pipelineContextPath(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeACReconcile(t *testing.T, dir string, issue int, criteria []string) {
	t.Helper()
	rec := preflight.ACReconcileResult{IssueNumber: issue}
	for i, c := range criteria {
		rec.AcceptanceCriteria = append(rec.AcceptanceCriteria, preflight.ReconciledCriterion{Index: i, Text: c})
	}
	writePipelineJSON(t, dir, "ac-reconcile-"+itoa(issue)+".json", rec)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// replay2938 builds PR #2938's inputs: the test file exists on main and the
// branch modifies it; feature-validate ran the full gate in the background,
// so its validate context carries no unit result, and a gate before #2941 had
// already written the "added but never executed" finding into it.
func replay2938(t *testing.T, withLog, withLedger bool, unitBlock ...map[string]any) PRCreateSnapshot {
	t.Helper()
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q", "-b", "main")
	gitOut(t, dir, "config", "user.email", "test@example.com")
	gitOut(t, dir, "config", "user.name", "Test")
	gitOut(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, dir, "internal/ipc/autonomous_stop_join_test.go", stopJoinTestBase)
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "base")
	gitOut(t, dir, "checkout", "-q", "-b", "feat/2934-stop-join")
	writeFile(t, dir, "internal/ipc/autonomous_stop_join_test.go", stopJoinTestFixed)
	gitOut(t, dir, "commit", "-qam", "fix(#2934): drain background spawns")

	writePipelineJSON(t, dir, "issue-2934.json", map[string]any{
		"title": "TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched races", "type": "bug",
		"branch": "feat/2934-stop-join", "base_branch": "main",
	})
	writePipelineJSON(t, dir, "dev-2934.json", map[string]any{
		"files_changed": map[string]any{"modified": []string{"internal/ipc/autonomous_stop_join_test.go"}},
		"tests_status":  map[string]any{"passed": 1, "failed": 0},
	})
	unit := map[string]any{"ran": false, "passed": false}
	if len(unitBlock) > 0 {
		unit = unitBlock[0]
	}
	if withLog {
		logPath := pipelineContextPath(dir, "gate-2934.log")
		if err := os.WriteFile(logPath, []byte(gateLog2934), 0o644); err != nil {
			t.Fatal(err)
		}
		unit["log"] = logPath
	}
	writePipelineJSON(t, dir, "validate-2934.json", map[string]any{
		"validation_status": "passed_unverified",
		"build":             map[string]any{"ran": true, "passed": true},
		"unit_tests":        unit,
		"unverified_deliverable": map[string]any{
			"detected": true, "tiers": []string{"unit"},
			"files":             []string{"internal/ipc/autonomous_stop_join_test.go"},
			"superseded_status": "passed",
			"summary":           "1 test file added but never executed (unit tier did not run)",
		},
		"acceptance_criteria_verdicts": []map[string]any{
			{"index": 1, "text": issue2934ACs[1], "status": "met",
				"evidence": "diff touches only internal/ipc/autonomous_stop_join_test.go"},
			{"index": 2, "text": issue2934ACs[2], "status": "not_verifiable",
				"evidence": "go test ./internal/ipc -run 'TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched|TestAutonomous' -race -count=50: ok github.com/nightgauge/nightgauge/internal/ipc 119.119s; PR body content is written by pr-create"},
		},
	})
	writeACReconcile(t, dir, 2934, issue2934ACs)
	if withLedger {
		tree, err := gateledger.Fingerprint(context.Background(), gateledger.ExecRunner, dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := gateledger.Append(dir, gateledger.Event{
			RunID: "c7ac460aee8de26c", Event: gateledger.EventFinish, Kind: gateledger.KindLocalGate,
			Tree: tree, Verdict: gateledger.VerdictPass, Full: true, Issue: 2934, Stage: "feature-validate",
		}); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := defaultReadCreateContext(context.Background(), dir, 2934)
	if err != nil {
		t.Fatal(err)
	}
	snap.IssueNumber = 2934
	return snap
}

// AC 1-4 against #2938: the modified test is not "added", the background gate's
// ledger pass confirms the unit run, and the gate log's `--- PASS` line answers
// both test-named criteria, so the PR closes #2934.
func TestRegression2941_Replay2938(t *testing.T) {
	snap := replay2938(t, true, true)
	body := RenderBody(snap)

	if strings.Contains(body, "Unverified deliverable") || strings.Contains(body, "added but never executed") {
		t.Errorf("a modified test file was reported as added:\n%s", body)
	}
	if strings.Contains(body, "not confirmed by feature-validate") || !strings.Contains(body, "- Unit tests: passed (1 passed, 0 failed; confirmed by a full local gate pass on tree ") {
		t.Errorf("Validation must report the ledger-confirmed unit run:\n%s", body)
	}
	for _, i := range []int{0, 2} {
		if v := snap.ACVerdicts[i]; v.Status != ACMet {
			t.Errorf("criterion %d = %s (%s: %s), want met", i+1, v.Status, v.Source, v.Evidence)
		}
	}
	if !strings.Contains(snap.ACVerdicts[0].Evidence, "--- PASS: TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched") {
		t.Errorf("criterion 1 should cite the gate log's PASS line: %s", snap.ACVerdicts[0].Evidence)
	}
	if !strings.Contains(body, "-race -count=50: ok github.com/nightgauge/nightgauge/internal/ipc 119.119s") {
		t.Errorf("the recorded command and result must be in the PR body:\n%s", body)
	}
	if !strings.Contains(body, "Closes #2934") {
		t.Errorf("every criterion is met, so the PR must close #2934:\n%s", body)
	}
}

// Without the log or the ledger nothing proves the test ran, and the
// criterion stays unmet: the fix reads evidence, it does not assume it.
func TestRegression2941_Replay2938WithoutEvidenceStaysUnmet(t *testing.T) {
	snap := replay2938(t, false, false)
	if v := snap.ACVerdicts[0]; v.Status != ACUnmet {
		t.Errorf("criterion 1 = %s (%s), want unmet with no recorded run", v.Status, v.Evidence)
	}
	if body := RenderBody(snap); !strings.Contains(body, "Refs #2934") {
		t.Errorf("an unproven run must not close the issue:\n%s", body)
	}
}

// A FAIL line outranks every other source of a pass.
func TestRegression2941_GateLogFailLineIsUnmet(t *testing.T) {
	snap := replay2938(t, true, true)
	snap.TestOutcomes.Failed = map[string]bool{"TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched": true}
	delete(snap.TestOutcomes.Passed, "TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched")
	r, ok := VerifyACAgainstDiff(issue2934ACs[0], nil, collectFor(t, snap), acTestState(snap))
	if !ok || r.Status != ACUnmet || !strings.Contains(r.Evidence, "--- FAIL: TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched") {
		t.Errorf("got %+v, want unmet naming the FAIL line", r)
	}
}

func collectFor(t *testing.T, snap PRCreateSnapshot) *ACDiff {
	t.Helper()
	dir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(snap.TestLog))))
	d, err := CollectACDiff(context.Background(), dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func fixtureDiff(t *testing.T, name string) *ACDiff {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "2941", name))
	if err != nil {
		t.Fatal(err)
	}
	return memDiff(string(data), nil)
}

// AC 6: #2947 for #2945. `1.26.0` is a bound, not text to add; the diff adds
// 1.27.0 to plugin.json, which is above it.
func TestRegression2941_Replay2947VersionBound(t *testing.T) {
	const ac = "`claude-plugins/nightgauge/.claude-plugin/plugin.json` carries a version above `1.26.0`, chosen by `docs/GIT_WORKFLOW.md § Changelog`, and every other place the repository records the plugin version matches it"
	d := fixtureDiff(t, "pr2947.diff")
	r, ok := VerifyACAgainstDiff(ac, nil, d, ACTestState{})
	if !ok || r.Status != ACMet {
		t.Fatalf("got %+v (ok=%v), want met", r, ok)
	}
	if !strings.Contains(r.Evidence, "adds `1.27.0`") || !strings.Contains(r.Evidence, "above `1.26.0`") {
		t.Errorf("evidence should state the comparison: %s", r.Evidence)
	}

	// The same criterion against a diff that leaves the version at the bound.
	same := memDiff(`diff --git a/claude-plugins/nightgauge/.claude-plugin/plugin.json b/claude-plugins/nightgauge/.claude-plugin/plugin.json
--- a/claude-plugins/nightgauge/.claude-plugin/plugin.json
+++ b/claude-plugins/nightgauge/.claude-plugin/plugin.json
@@ -3 +3 @@
-  "version": "1.25.0",
+  "version": "1.26.0",
`, nil)
	if r, ok := VerifyACAgainstDiff(ac, nil, same, ACTestState{}); !ok || r.Status != ACUnmet || !strings.Contains(r.Evidence, "is above `1.26.0`") {
		t.Errorf("a version equal to the bound must be unmet: %+v", r)
	}
}

// AC 7: #2951 for #2948. The criterion lists the places the rule applies;
// `skillrender.BundleExcludes` is a symbol the diff renames in its package and
// `.vscodeignore` is named in the scripts' added lines.
func TestRegression2941_Replay2951NamedPlaces(t *testing.T) {
	const ac = "The skill bundle check on a packaged VSIX passes for every skill under `skills/`: the files a skill ships are the same in `dist/skills`, in the packaged VSIX, and in what `preflight skill-bundle --source-root` expects. One rule decides what test material stays behind, and `bundle-marketplace.sh`, `skillrender.BundleExcludes`, `scripts/lib/install_skill_tree.py` and `.vscodeignore` all apply it"
	r, ok := VerifyACAgainstDiff(ac, nil, fixtureDiff(t, "pr2951.diff"), ACTestState{})
	if !ok || r.Status != ACMet {
		t.Fatalf("got %+v (ok=%v), want met", r, ok)
	}
	if !strings.Contains(r.Evidence, "changes `skillrender.BundleExcludes` in `internal/skillrender/bundle_files.go`") {
		t.Errorf("evidence should find the symbol in its package: %s", r.Evidence)
	}
}

// AC 7: PR #2956 for #2953. `scripts/ci-local.sh` is what the hook acts on; the
// hook and its tests, which carry the tokens, are in internal/hooks.
func TestRegression2941_Replay2956ActedOnFile(t *testing.T) {
	const ac = "1. A PreToolUse:Bash check in the Claude Code plugin denies a Bash call that starts the full gate (`nightgauge ci gate-run`, or the repository's declared gate command such as `scripts/ci-local.sh`) unless `run_in_background` is `true` and `timeout` is at least the gate's limit (`hooks.pre_push.timeout_seconds`, else its default). The deny reason tells the agent how to start the gate instead. It is unit-tested for: foreground call denied, background call with a short timeout denied, background call with an adequate timeout allowed, an unrelated command allowed, and `&` inside a foreground call denied."
	r, ok := VerifyACAgainstDiff(ac, nil, fixtureDiff(t, "pr2956.diff"), ACTestState{})
	if !ok || r.Status != ACMet {
		t.Fatalf("got %+v (ok=%v), want met", r, ok)
	}
	if !strings.Contains(r.Evidence, "internal/hooks/gate_background") {
		t.Errorf("evidence should cite the hook's code: %s", r.Evidence)
	}
}

// AC 5: #2942 for #2934. The branch's work is on main and the branch has only
// merged main since, so it has commits ahead and no diff. pr-create must not
// open the empty PR; it reports the criteria instead.
func TestRegression2941_Replay2942EmptyDiff(t *testing.T) {
	dir := initRepo(t) // on fix/1179-x
	writeFile(t, dir, "internal/ipc/x_test.go", "package ipc\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "work")
	gitOut(t, dir, "checkout", "-q", "main")
	gitOut(t, dir, "merge", "-q", "--no-ff", "-m", "merge the PR", "fix/1179-x")
	writeFile(t, dir, "OTHER.md", "main moved on\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "main")
	gitOut(t, dir, "checkout", "-q", "fix/1179-x")
	gitOut(t, dir, "merge", "-q", "--no-ff", "-m", "Merge main", "main")
	if ahead := gitOut(t, dir, "rev-list", "--count", "main..HEAD"); ahead == "0" {
		t.Fatal("setup: the branch must have commits ahead of main")
	}

	writePipelineJSON(t, dir, "issue-2934.json", map[string]any{"type": "bug", "branch": "fix/1179-x", "base_branch": "main"})
	writePipelineJSON(t, dir, "dev-2934.json", map[string]any{"files_changed": map[string]any{"modified": []string{"internal/ipc/x_test.go"}}})
	writePipelineJSON(t, dir, "validate-2934.json", map[string]any{
		"validation_status": "passed", "unit_tests": map[string]any{"ran": true, "passed": true},
		"acceptance_criteria_verdicts": []map[string]any{{"index": 0, "text": "Ship it", "status": "met", "evidence": "on main at abc123"}},
	})
	writeACReconcile(t, dir, 2934, []string{"Ship it", "Document it"})

	pr := &fakePRClient{}
	g := &fakeGit{}
	r := NewDeterministicPRCreateRunner().WithPRCreateClient(pr).WithGitClient(g)
	res, err := r.Run(context.Background(), 2934, "nightgauge/nightgauge", dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != CreatePathPunt || res.Reason != ReasonNoDiffAgainstBase {
		t.Fatalf("got %s/%s, want a %s punt", res.Path, res.Reason, ReasonNoDiffAgainstBase)
	}
	if pr.createCalls != 0 || g.pushCalls != 0 {
		t.Errorf("an empty branch must be neither pushed nor opened: push=%d create=%d", g.pushCalls, pr.createCalls)
	}
	closeIt, comment := NoDiffOutcome(2934, res.Branch, res.BaseBranch, res.ACVerdicts)
	if closeIt {
		t.Error("one criterion is unverified, so the issue must stay open")
	}
	if !strings.Contains(comment, "- Document it") || !strings.Contains(comment, "Ship it (feature-validate: on main at abc123)") {
		t.Errorf("the comment must carry the evidence and name the unmet criterion:\n%s", comment)
	}
}

func TestDecideCreate_NoDiffAgainstBase_Punts(t *testing.T) {
	s := richSnap()
	s.TreeDiffEmpty = true
	if d := DecideCreate(s); !d.Punt || d.Reason != ReasonNoDiffAgainstBase {
		t.Errorf("got %+v, want a %s punt", d, ReasonNoDiffAgainstBase)
	}
}

func TestNoDiffOutcome_AllMetCloses(t *testing.T) {
	closeIt, comment := NoDiffOutcome(7, "feat/7", "main", []ACVerdict{
		{Text: "Ship it", Status: ACMet, Source: ACSourceValidate, Evidence: "on main"},
	})
	if !closeIt || !strings.Contains(comment, "Ship it (feature-validate: on main)") || !strings.Contains(comment, "the issue is closed") {
		t.Errorf("close=%v comment:\n%s", closeIt, comment)
	}
	if closeIt, _ := NoDiffOutcome(7, "feat/7", "main", nil); closeIt {
		t.Error("no criteria is not every criterion met")
	}
	if d := NoDiffTerminalDetail(7, "feat/7", "main", false, 2); !strings.HasPrefix(d, "[not-pipeline-actionable]") {
		t.Errorf("an open issue must park: %s", d)
	}
	if d := NoDiffTerminalDetail(7, "feat/7", "main", true, 0); !strings.HasPrefix(d, "issue_closed:") {
		t.Errorf("a closed issue must read as issue_closed: %s", d)
	}
}

// AC 4: a criterion asking for its evidence in the PR body is met only when
// feature-validate recorded a command and result to carry there.
func TestResolveACVerdicts_PRBodyEvidence(t *testing.T) {
	const ac = "`bash scripts/check-plugin-version.sh --tag v0.8.1-rc.2` passes on the branch, and the command and its output are in the PR body"
	other := ACVerdict{Index: 1, Text: "Something else", Status: "met", Evidence: "x"}

	got := ResolveACVerdicts([]string{ac, "Something else"}, []ACVerdict{other}, nil, nil)
	if got[0].Status != ACUnmet || got[0].Evidence != ACNoPRNote {
		t.Errorf("no recorded evidence: got %+v", got[0])
	}

	rec := ACVerdict{Index: 0, Text: ac, Status: "not_verifiable",
		Evidence: "scripts/check-plugin-version.sh --tag v0.8.1-rc.2 exited 0 on HEAD 25e471e5 (OK plugin 1.27.0); PR body content is written by pr-create"}
	got = ResolveACVerdicts([]string{ac, "Something else"}, []ACVerdict{rec, other}, nil, nil)
	if got[0].Status != ACMet || got[0].Source != ACSourceValidate || !strings.Contains(got[0].Evidence, "exited 0") {
		t.Errorf("recorded command and result: got %+v", got[0])
	}
	body := RenderBody(PRCreateSnapshot{IssueNumber: 1, ACVerdicts: got})
	if !strings.Contains(body, "exited 0 on HEAD 25e471e5") {
		t.Errorf("the body must carry the recorded result:\n%s", body)
	}

	noResult := ACVerdict{Index: 0, Text: ac, Status: "not_verifiable", Evidence: "PR body content is written by pr-create"}
	got = ResolveACVerdicts([]string{ac}, []ACVerdict{noResult}, nil, nil)
	if got[0].Status != ACUnmet || got[0].Evidence != ACNoPRNote {
		t.Errorf("evidence with no result: got %+v", got[0])
	}
}

func TestUnitTestsLabel_LedgerSource(t *testing.T) {
	s := PRCreateSnapshot{HasValidate: true, UnitTestsPassed: true, UnitTestsSource: ledgerSource("e367b3b7c92bbc8dae3a"), TestsPassed: 1}
	if got := unitTestsLabel(s); got != "passed (1 passed, 0 failed; confirmed by a full local gate pass on tree e367b3b7c92b)" {
		t.Errorf("label = %q", got)
	}
	s.TestsPassed = 0
	if got := unitTestsLabel(s); got != "passed (confirmed by a full local gate pass on tree e367b3b7c92b)" {
		t.Errorf("label = %q", got)
	}
}

func TestVerifyACAgainstDiff_Bounds(t *testing.T) {
	d := memDiff(`diff --git a/cfg.yaml b/cfg.yaml
--- a/cfg.yaml
+++ b/cfg.yaml
@@ -1 +1 @@
-retries: 2
+retries: 5
`, nil)
	cases := []struct{ text, want string }{
		{"`cfg.yaml` sets retries to at least `3`", ACMet},
		{"`cfg.yaml` sets retries to at most `4`", ACUnmet},
		{"`cfg.yaml` sets retries below `6`", ACMet},
		{"`cfg.yaml` sets retries greater than `5`", ACUnmet},
		// Not a bound: the value is text the diff must add.
		{"`cfg.yaml` sets retries to `5`", ACMet},
	}
	for _, tc := range cases {
		if r, ok := VerifyACAgainstDiff(tc.text, nil, d, ACTestState{}); !ok || r.Status != tc.want {
			t.Errorf("%q: got %+v (ok=%v), want %s", tc.text, r, ok, tc.want)
		}
	}
}

// A bare file name names the one diffed file with that base name, and a
// token the named file lacks is found in the rest of the diff.
func TestVerifyACAgainstDiff_NamedPlaceElsewhere(t *testing.T) {
	d := memDiff(`diff --git a/scripts/run.sh b/scripts/run.sh
--- a/scripts/run.sh
+++ b/scripts/run.sh
@@ -1,0 +2 @@
+check_gate
diff --git a/internal/hooks/gate.go b/internal/hooks/gate.go
--- a/internal/hooks/gate.go
+++ b/internal/hooks/gate.go
@@ -1,0 +2 @@
+const flag = "run_in_background"
`, map[string]string{
		// Read content: an unreadable file leaves a missing token undecided (#3037).
		"scripts/run.sh":         "#!/bin/sh\ncheck_gate\n",
		"internal/hooks/gate.go": "package hooks\nconst flag = \"run_in_background\"\n",
	})
	r, ok := VerifyACAgainstDiff("A hook denies `run.sh` unless `run_in_background` is set", nil, d, ACTestState{})
	if !ok || r.Status != ACMet || !strings.Contains(r.Evidence, "internal/hooks/gate.go:2") {
		t.Errorf("got %+v (ok=%v)", r, ok)
	}
	r, ok = VerifyACAgainstDiff("`run.sh` adds `never_written`", nil, d, ACTestState{})
	if !ok || r.Status != ACUnmet || !strings.Contains(r.Evidence, "every other changed file") {
		t.Errorf("a token the whole diff lacks is unmet: %+v (ok=%v)", r, ok)
	}
}

// An unrelated number on an added line is not the bounded value: only a value
// the change replaces counts, and with no replacement the bound is undecided.
func TestVerifyACAgainstDiff_BoundIgnoresUnrelatedNumbers(t *testing.T) {
	d := memDiff(`diff --git a/cfg.yaml b/cfg.yaml
--- a/cfg.yaml
+++ b/cfg.yaml
@@ -1 +1,2 @@
-retries: 2
+retries: 2
+timeout: 30
`, nil)
	r, ok := VerifyACAgainstDiff("`cfg.yaml` sets retries to at least `3`", nil, d, ACTestState{})
	if ok && r.Status == ACMet {
		t.Fatalf("timeout 30 satisfied a retries bound: %+v", r)
	}
	added := memDiff(`diff --git a/cfg.yaml b/cfg.yaml
--- a/cfg.yaml
+++ b/cfg.yaml
@@ -1,0 +2 @@
+timeout: 30
`, nil)
	if r, ok := VerifyACAgainstDiff("`cfg.yaml` sets retries to at least `3`", nil, added, ACTestState{}); ok && r.Status == ACMet {
		t.Fatalf("an added unrelated number met the bound: %+v", r)
	}
}

// A unit block that says the suite ran and failed keeps that verdict: a ledger
// pass never overwrites a recorded failure.
func TestRegression2941_LedgerNeverOverwritesRecordedFailure(t *testing.T) {
	snap := replay2938(t, false, true, map[string]any{"ran": true, "passed": false})
	if snap.UnitTestsPassed {
		t.Fatalf("a recorded unit failure was replaced by a ledger pass (source %q)", snap.UnitTestsSource)
	}
}

// A gate log with no ledger pass on this tree answers no named test.
func TestRegression2941_LogWithoutLedgerPassIsIgnored(t *testing.T) {
	snap := replay2938(t, true, false)
	if len(snap.TestOutcomes.Passed) != 0 {
		t.Fatalf("an untied gate log answered named tests: %v", snap.TestOutcomes.Passed)
	}
}

// Evidence that records a failure is never promoted, even when it carries the
// words "pass" or "ok".
func TestResolveACVerdicts_PRBodyNegativeEvidenceStaysUnmet(t *testing.T) {
	const ac = "`go test ./...` passes, and the command and its output are in the PR body"
	for _, ev := range []string{
		"ran go test ./...: tests did not pass, ok to retry",
		"go test ./... exited 1; --- FAIL: TestX",
		"go test ./... pass rate was low",
	} {
		v := ACVerdict{Index: 0, Text: ac, Status: "not_verifiable", Evidence: ev}
		if got := ResolveACVerdicts([]string{ac}, []ACVerdict{v}, nil, nil); got[0].Status == ACMet {
			t.Errorf("evidence %q promoted to met: %+v", ev, got[0])
		}
	}
}
