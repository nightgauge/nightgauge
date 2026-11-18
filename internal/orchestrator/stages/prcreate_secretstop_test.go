package stages

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/scan"
)

// secretStopRepo builds a repo with main and a feature branch one commit
// ahead, plus the pipeline state directory, and returns (repo, stateDir).
func secretStopRepo(t *testing.T, content string) (string, string) {
	t.Helper()
	repo := gittest.InitRepo(t, t.TempDir(), "-q", "-b", "main")
	gittest.Run(t, repo, "config", "user.email", "fixture@example.invalid")
	gittest.Run(t, repo, "config", "user.name", "Fixture")
	gittest.InitialCommit(t, repo)
	gittest.Run(t, repo, "checkout", "-q", "-b", "feat/42-pr-create")
	commitConf(t, repo, content, "feature")
	dir := filepath.Dir(pipelineContextPath(repo, "x"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return repo, dir
}

func commitConf(t *testing.T, repo, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "conf.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "add", "conf.txt")
	gittest.Run(t, repo, "commit", "-q", "-m", msg)
	return strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
}

// rewrittenRepo commits the secret, then rewrites it out of history: the
// returned SHA still exists as an object but is no longer an ancestor of
// HEAD, whose conf.txt holds final.
func rewrittenRepo(t *testing.T, final string) (repo, stateDir, oldSHA string) {
	t.Helper()
	repo, stateDir = secretStopRepo(t, criticalLine)
	oldSHA = strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	gittest.Run(t, repo, "reset", "-q", "--hard", "HEAD~1")
	commitConf(t, repo, final, "feature, rewritten")
	return repo, stateDir, oldSHA
}

func writeStop(t *testing.T, stateDir, scanner, head string) string {
	t.Helper()
	p := filepath.Join(stateDir, gateStopName(42))
	data, _ := json.Marshal(prCreateGateStop{Gate: gateSecurityScan, Scanner: scanner, Reason: "old", HeadSHA: head})
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readBytes(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("record must survive: %v", err)
	}
	return b
}

// fakeGitleaks records each base it is asked to scan and returns n, err.
type fakeGitleaks struct {
	n     int
	err   error
	bases []string
}

func (f *fakeGitleaks) scan(_ context.Context, _, base string) (int, error) {
	f.bases = append(f.bases, base)
	return f.n, f.err
}

// stopRunner is newTestRunner with a fake gitleaks, so no test depends on a
// gitleaks install.
func stopRunner(snap PRCreateSnapshot, prc *fakePRClient, git gitClient, gl *fakeGitleaks) *DeterministicPRCreateRunner {
	r := newTestRunner(snap, prc, git)
	r.gitleaksScan = gl.scan
	return r
}

// A connection string with inline credentials: a critical pattern. Built at
// runtime so this source file does not itself carry the pattern.
var criticalLine = "DB=" + "postgresql" + "://admin:hunter2@db.internal/app\n"

const absentSHA = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

func injectedStop() func(string, int) (*prCreateGateStop, error) {
	return func(string, int) (*prCreateGateStop, error) {
		return &prCreateGateStop{Gate: gateSecurityScan, Scanner: scannerDiffSecrets, HeadSHA: absentSHA}, nil
	}
}

func notAncestor(context.Context, string, string) (bool, error) { return false, nil }

func TestSecretStop_CriticalFindingBlocksAndRefreshesRecord(t *testing.T) {
	repo, stateDir, old := rewrittenRepo(t, criticalLine)
	recPath := writeStop(t, stateDir, scannerDiffSecrets, old)
	head := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))

	prc := &fakePRClient{createdPR: &CreatedPR{Number: 9}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, &fakeGitleaks{})
	res, err := r.Run(context.Background(), 42, "owner/repo", repo)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != CreatePathBlocked || res.Reason != ReasonSecurityScanFailed {
		t.Fatalf("got path=%q reason=%q, want blocked/%s", res.Path, res.Reason, ReasonSecurityScanFailed)
	}
	if prc.createCalls != 0 {
		t.Errorf("CreatePR called %d times on a blocked rescan", prc.createCalls)
	}
	var rec prCreateGateStop
	_ = json.Unmarshal(readBytes(t, recPath), &rec)
	if rec.Gate != gateSecurityScan || rec.HeadSHA != head || rec.Scanner != scannerDiffSecrets {
		t.Errorf("record = %+v, want gate %s scanner %s at head %s", rec, gateSecurityScan, scannerDiffSecrets, head)
	}
	if rec.Reason != "1 critical secret finding(s) in the changed files" || rec.CreatedAt == "" {
		t.Errorf("record reason/created_at not refreshed: %+v", rec)
	}
}

func TestSecretStop_CleanRescanClearsRecordAndCreates(t *testing.T) {
	repo, stateDir, old := rewrittenRepo(t, "plain text\n")
	recPath := writeStop(t, stateDir, scannerDiffSecrets, old)

	prc := &fakePRClient{createdPR: &CreatedPR{Number: 9, URL: "u"}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, &fakeGitleaks{})
	res, err := r.Run(context.Background(), 42, "owner/repo", repo)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != CreatePathCreated || prc.createCalls != 1 {
		t.Fatalf("got path=%q createCalls=%d, want created/1", res.Path, prc.createCalls)
	}
	if _, err := os.Stat(recPath); !os.IsNotExist(err) {
		t.Errorf("record must be removed after a clean rescan, stat err=%v", err)
	}
}

// AC 4 (Part B): a fix committed on top leaves the secret in history, so a
// clean HEAD does not clear the stop.
func TestSecretStop_RecordedCommitStillAncestorKeepsStop(t *testing.T) {
	repo, stateDir := secretStopRepo(t, criticalLine)
	secretSHA := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	commitConf(t, repo, "plain text\n", "remove the secret")
	recPath := writeStop(t, stateDir, scannerDiffSecrets, secretSHA)
	before := readBytes(t, recPath)

	gl := &fakeGitleaks{}
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 9}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, gl)
	res, err := r.Run(context.Background(), 42, "owner/repo", repo)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != CreatePathBlocked || prc.createCalls != 0 {
		t.Fatalf("got path=%q createCalls=%d, want blocked/0", res.Path, prc.createCalls)
	}
	if !strings.Contains(res.Detail, secretSHA[:12]) || !strings.Contains(res.Detail, "rewrite") {
		t.Errorf("detail must name the commit and the rewrite: %q", res.Detail)
	}
	if string(readBytes(t, recPath)) != string(before) {
		t.Error("record must be untouched while the commit is in history")
	}
	if len(gl.bases) != 0 {
		t.Errorf("gitleaks ran %d times while the stop held", len(gl.bases))
	}
}

// AC 5 / Part C: a gitleaks or unnamed-scanner stop is re-judged once the
// commit is out of history, instead of blocking every retry.
func TestSecretStop_GitleaksAndUnnamedStopsReJudgedCleanClear(t *testing.T) {
	for _, scanner := range []string{scannerGitleaks, ""} {
		repo, stateDir, old := rewrittenRepo(t, "plain text\n")
		recPath := writeStop(t, stateDir, scanner, old)
		gl := &fakeGitleaks{}
		prc := &fakePRClient{createdPR: &CreatedPR{Number: 9}}
		r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, gl)
		res, err := r.Run(context.Background(), 42, "owner/repo", repo)
		if err != nil {
			t.Fatal(err)
		}
		if res.Path != CreatePathCreated || prc.createCalls != 1 {
			t.Errorf("scanner %q: got path=%q createCalls=%d, want created/1", scanner, res.Path, prc.createCalls)
		}
		if len(gl.bases) != 1 || gl.bases[0] != "main" {
			t.Errorf("scanner %q: gitleaks bases = %v, want [main]", scanner, gl.bases)
		}
		if _, err := os.Stat(recPath); !os.IsNotExist(err) {
			t.Errorf("scanner %q: record must be removed, stat err=%v", scanner, err)
		}
	}
}

func TestSecretStop_GitleaksMissingKeepsStop(t *testing.T) {
	repo, stateDir, old := rewrittenRepo(t, "plain text\n")
	recPath := writeStop(t, stateDir, scannerGitleaks, old)
	before := readBytes(t, recPath)
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 9}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, &fakeGitleaks{err: errGitleaksUnavailable})
	r.diffScan = func(context.Context, scan.DiffSecretsOptions) (*scan.DiffSecretsResult, error) {
		t.Fatal("diffScan must not run when gitleaks cannot")
		return nil, nil
	}
	res, _ := r.Run(context.Background(), 42, "owner/repo", repo)
	if res.Path != CreatePathBlocked || prc.createCalls != 0 || !strings.Contains(res.Detail, "not installed") {
		t.Fatalf("got %+v createCalls=%d, want blocked naming the missing tool", res, prc.createCalls)
	}
	if string(readBytes(t, recPath)) != string(before) {
		t.Error("record must be untouched when gitleaks is missing")
	}
}

func TestSecretStop_GitleaksFailureKeepsStop(t *testing.T) {
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 1}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, &fakeGitleaks{err: errors.New("exit 2")})
	r.readGateStop = injectedStop()
	r.isAncestor = notAncestor
	r.writeGateStop = func(string, int, prCreateGateStop) error {
		t.Fatal("a failed gitleaks run must not rewrite the record")
		return nil
	}
	r.diffScan = func(context.Context, scan.DiffSecretsOptions) (*scan.DiffSecretsResult, error) {
		t.Fatal("diffScan must not run when gitleaks failed")
		return nil, nil
	}
	res, _ := r.Run(context.Background(), 42, "owner/repo", "/wd")
	if res.Path != CreatePathBlocked || prc.createCalls != 0 || !strings.Contains(res.Detail, "could not run") {
		t.Fatalf("got %+v createCalls=%d, want blocked naming the failed run", res, prc.createCalls)
	}
}

func TestSecretStop_GitleaksFindingsRefreshRecord(t *testing.T) {
	repo, stateDir, old := rewrittenRepo(t, "plain text\n")
	recPath := writeStop(t, stateDir, scannerDiffSecrets, old)
	head := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 9}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, &fakeGitleaks{n: 2})
	res, _ := r.Run(context.Background(), 42, "owner/repo", repo)
	if res.Path != CreatePathBlocked || prc.createCalls != 0 {
		t.Fatalf("got path=%q createCalls=%d, want blocked/0", res.Path, prc.createCalls)
	}
	var rec prCreateGateStop
	_ = json.Unmarshal(readBytes(t, recPath), &rec)
	if rec.Scanner != scannerGitleaks || rec.HeadSHA != head || rec.Reason != "2 gitleaks finding(s) in main..HEAD" {
		t.Errorf("record = %+v, want scanner gitleaks at %s with the gitleaks count", rec, head)
	}
}

func TestSecretStop_EmptyHeadSHAKeepsStop(t *testing.T) {
	repo, stateDir := secretStopRepo(t, "plain text\n")
	recPath := writeStop(t, stateDir, scannerDiffSecrets, "")
	before := readBytes(t, recPath)
	gl := &fakeGitleaks{}
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 9}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, gl)
	r.diffScan = func(context.Context, scan.DiffSecretsOptions) (*scan.DiffSecretsResult, error) {
		t.Fatal("diffScan must not run without a recorded commit")
		return nil, nil
	}
	res, _ := r.Run(context.Background(), 42, "owner/repo", repo)
	if res.Path != CreatePathBlocked || prc.createCalls != 0 || len(gl.bases) != 0 {
		t.Fatalf("got path=%q createCalls=%d gitleaks=%d, want blocked/0/0", res.Path, prc.createCalls, len(gl.bases))
	}
	if string(readBytes(t, recPath)) != string(before) {
		t.Error("record must be untouched")
	}
}

func TestSecretStop_PrunedRecordedCommitIsNotAncestor(t *testing.T) {
	repo, stateDir := secretStopRepo(t, "plain text\n")
	writeStop(t, stateDir, scannerDiffSecrets, absentSHA)
	gl := &fakeGitleaks{}
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 9}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, gl)
	res, _ := r.Run(context.Background(), 42, "owner/repo", repo)
	if res.Path != CreatePathCreated || len(gl.bases) != 1 {
		t.Fatalf("got path=%q gitleaks=%d (%s), want created after both scans", res.Path, len(gl.bases), res.Detail)
	}
}

func TestSecretStop_AncestorCheckErrorFailsClosed(t *testing.T) {
	gl := &fakeGitleaks{}
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 1}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, gl)
	r.readGateStop = injectedStop()
	r.isAncestor = func(context.Context, string, string) (bool, error) { return false, errors.New("corrupt pack") }
	res, _ := r.Run(context.Background(), 42, "owner/repo", "/wd")
	if res.Path != CreatePathBlocked || prc.createCalls != 0 || len(gl.bases) != 0 {
		t.Fatalf("got path=%q createCalls=%d gitleaks=%d, want blocked/0/0", res.Path, prc.createCalls, len(gl.bases))
	}
}

func TestDefaultIsAncestor(t *testing.T) {
	repo, _ := secretStopRepo(t, criticalLine)
	first := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	ctx := context.Background()
	if ok, err := defaultIsAncestor(ctx, repo, first); err != nil || !ok {
		t.Errorf("HEAD itself: ok=%v err=%v, want true", ok, err)
	}
	gittest.Run(t, repo, "reset", "-q", "--hard", "HEAD~1")
	commitConf(t, repo, "x\n", "other")
	if ok, err := defaultIsAncestor(ctx, repo, first); err != nil || ok {
		t.Errorf("rewritten-out commit: ok=%v err=%v, want false", ok, err)
	}
	if ok, err := defaultIsAncestor(ctx, repo, absentSHA); err != nil || ok {
		t.Errorf("absent commit: ok=%v err=%v, want false", ok, err)
	}
	for _, bad := range []string{"main", "--all", "stale000"} {
		if _, err := defaultIsAncestor(ctx, repo, bad); err == nil {
			t.Errorf("%q: want an error for a non-commit id", bad)
		}
	}
	if _, err := defaultIsAncestor(ctx, t.TempDir(), first); err == nil {
		t.Error("outside a repo: want an error")
	}
}

func fakeGitleaksBin(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gitleaks"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return argsFile
}

func TestDefaultGitleaksScan(t *testing.T) {
	ctx := context.Background()
	wd := t.TempDir()

	argsFile := fakeGitleaksBin(t, "echo 'RuleID: a'; echo 'RuleID: b'; echo 'leaks found: 2'; exit 1")
	n, err := defaultGitleaksScan(ctx, wd, "main")
	if err != nil || n != 2 {
		t.Errorf("findings: n=%d err=%v, want 2", n, err)
	}
	args, _ := os.ReadFile(argsFile)
	if want := "detect\n--source\n.\n--log-opts\nmain..HEAD\n--no-banner\n"; string(args) != want {
		t.Errorf("args = %q, want %q", args, want)
	}

	fakeGitleaksBin(t, "echo 'leaks found: 1'; exit 1")
	if n, err := defaultGitleaksScan(ctx, wd, "main"); err != nil || n != 1 {
		t.Errorf("leaks found, no RuleID: n=%d err=%v, want 1", n, err)
	}

	fakeGitleaksBin(t, "echo 'no leaks found'; exit 0")
	if n, err := defaultGitleaksScan(ctx, wd, "main"); err != nil || n != 0 {
		t.Errorf("clean: n=%d err=%v, want 0", n, err)
	}

	fakeGitleaksBin(t, "echo 'fatal: bad revision'; exit 2")
	if _, err := defaultGitleaksScan(ctx, wd, "main"); err == nil || errors.Is(err, errGitleaksUnavailable) {
		t.Errorf("failed run: err=%v, want a run error", err)
	}

	t.Setenv("PATH", t.TempDir())
	if _, err := defaultGitleaksScan(ctx, wd, "main"); !errors.Is(err, errGitleaksUnavailable) {
		t.Errorf("missing: err=%v, want errGitleaksUnavailable", err)
	}
}

func TestSecretStop_ScanUsesBaseAndRunsOnce(t *testing.T) {
	var calls []scan.DiffSecretsOptions
	snap := richSnap()
	snap.BaseBranch = "release/1.x"
	gl := &fakeGitleaks{}
	r := stopRunner(snap, &fakePRClient{createdPR: &CreatedPR{Number: 1}}, &fakeGit{branch: "feat/42-pr-create"}, gl)
	r.readGateStop = injectedStop()
	r.isAncestor = notAncestor
	r.removeGateStop = func(string, int) error { return nil }
	r.diffScan = func(_ context.Context, o scan.DiffSecretsOptions) (*scan.DiffSecretsResult, error) {
		calls = append(calls, o)
		return &scan.DiffSecretsResult{HeadSHA: "h"}, nil
	}
	res, err := r.Run(context.Background(), 42, "owner/repo", "/wd")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Base != "release/1.x" || calls[0].Workdir != "/wd" {
		t.Fatalf("diffScan calls = %+v, want one with base release/1.x", calls)
	}
	if len(gl.bases) != 1 || gl.bases[0] != "release/1.x" {
		t.Fatalf("gitleaks bases = %v, want [release/1.x]", gl.bases)
	}
	if res.Path != CreatePathCreated {
		t.Errorf("a cleared security_scan stop must create, not punt: %+v", res)
	}
}

func TestSecretStop_NoRecordOrOtherGateIsNoop(t *testing.T) {
	for _, rec := range []*prCreateGateStop{nil, {Gate: "scope_drift"}} {
		gl := &fakeGitleaks{}
		r := stopRunner(richSnap(), &fakePRClient{createdPR: &CreatedPR{Number: 1}}, &fakeGit{branch: "feat/42-pr-create"}, gl)
		r.readGateStop = func(string, int) (*prCreateGateStop, error) { return rec, nil }
		r.diffScan = func(context.Context, scan.DiffSecretsOptions) (*scan.DiffSecretsResult, error) {
			t.Fatal("diffScan must not run without a security_scan record")
			return nil, nil
		}
		res, _ := r.Run(context.Background(), 42, "owner/repo", "/wd")
		if res.Path != CreatePathCreated || len(gl.bases) != 0 {
			t.Errorf("record %+v: path = %q gitleaks=%d, want created/0", rec, res.Path, len(gl.bases))
		}
	}
}

func TestSecretStop_ScanErrorFailsClosed(t *testing.T) {
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 1}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, &fakeGitleaks{})
	r.readGateStop = injectedStop()
	r.isAncestor = notAncestor
	r.writeGateStop = func(string, int, prCreateGateStop) error {
		t.Fatal("a failed scan must not rewrite the record")
		return nil
	}
	r.removeGateStop = func(string, int) error {
		t.Fatal("a failed scan must not clear the record")
		return nil
	}
	r.diffScan = func(context.Context, scan.DiffSecretsOptions) (*scan.DiffSecretsResult, error) {
		return nil, errors.New("base ref does not resolve")
	}
	res, _ := r.Run(context.Background(), 42, "owner/repo", "/wd")
	if res.Path != CreatePathBlocked || prc.createCalls != 0 {
		t.Fatalf("got path=%q createCalls=%d, want blocked/0", res.Path, prc.createCalls)
	}
}

func TestSecretStop_UnreadableRecordFailsClosed(t *testing.T) {
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 1}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, &fakeGitleaks{})
	r.readGateStop = func(string, int) (*prCreateGateStop, error) {
		return nil, errors.New("parse gate-stop record: bad json")
	}
	r.diffScan = func(context.Context, scan.DiffSecretsOptions) (*scan.DiffSecretsResult, error) {
		t.Fatal("diffScan must not run when the record is unreadable")
		return nil, nil
	}
	res, err := r.Run(context.Background(), 42, "owner/repo", "/wd")
	if err != nil || res.Path != CreatePathBlocked || res.Reason != ReasonSecurityScanFailed || prc.createCalls != 0 {
		t.Fatalf("got %+v err=%v createCalls=%d, want blocked/security-scan-failed/0", res, err, prc.createCalls)
	}
}

func TestSecretStop_CorruptRecordOnDiskFailsClosed(t *testing.T) {
	repo, stateDir := secretStopRepo(t, "plain text\n")
	if err := os.WriteFile(filepath.Join(stateDir, gateStopName(42)), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 1}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, &fakeGitleaks{})
	res, _ := r.Run(context.Background(), 42, "owner/repo", repo)
	if res.Path != CreatePathBlocked || prc.createCalls != 0 {
		t.Fatalf("got path=%q createCalls=%d, want blocked/0", res.Path, prc.createCalls)
	}
}

func TestSecretStop_RemoveFailureBlocks(t *testing.T) {
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 1}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, &fakeGitleaks{})
	r.readGateStop = injectedStop()
	r.isAncestor = notAncestor
	r.diffScan = func(context.Context, scan.DiffSecretsOptions) (*scan.DiffSecretsResult, error) {
		return &scan.DiffSecretsResult{}, nil
	}
	r.removeGateStop = func(string, int) error { return errors.New("eperm") }
	res, _ := r.Run(context.Background(), 42, "owner/repo", "/wd")
	if res.Path != CreatePathBlocked || prc.createCalls != 0 {
		t.Fatalf("got path=%q createCalls=%d, want blocked/0", res.Path, prc.createCalls)
	}
}

func TestSecretStop_HeadNotFeatureBranchBlocks(t *testing.T) {
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 1}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "other"}, &fakeGitleaks{})
	r.readGateStop = injectedStop()
	r.isAncestor = func(context.Context, string, string) (bool, error) {
		t.Fatal("history must not be judged on a HEAD that is not the feature branch")
		return false, nil
	}
	r.diffScan = func(context.Context, scan.DiffSecretsOptions) (*scan.DiffSecretsResult, error) {
		t.Fatal("diffScan must not judge a HEAD that is not the feature branch")
		return nil, nil
	}
	res, _ := r.Run(context.Background(), 42, "owner/repo", "/wd")
	if res.Path != CreatePathBlocked || prc.createCalls != 0 {
		t.Fatalf("got path=%q createCalls=%d, want blocked/0", res.Path, prc.createCalls)
	}
}

// Both scanners fire: the reason carries both counts and gitleaks is named.
func TestSecretStop_BothScannersFindingsJoinReason(t *testing.T) {
	repo, stateDir, old := rewrittenRepo(t, criticalLine)
	recPath := writeStop(t, stateDir, scannerDiffSecrets, old)
	r := stopRunner(richSnap(), &fakePRClient{createdPR: &CreatedPR{Number: 9}}, &fakeGit{branch: "feat/42-pr-create"}, &fakeGitleaks{n: 3})
	res, _ := r.Run(context.Background(), 42, "owner/repo", repo)
	want := "3 gitleaks finding(s) in main..HEAD; 1 critical secret finding(s) in the changed files"
	if res.Path != CreatePathBlocked || res.Detail != want {
		t.Fatalf("got path=%q detail=%q, want blocked/%q", res.Path, res.Detail, want)
	}
	var rec prCreateGateStop
	_ = json.Unmarshal(readBytes(t, recPath), &rec)
	if rec.Scanner != scannerGitleaks || rec.Reason != want {
		t.Errorf("record = %+v, want scanner gitleaks with the joined reason", rec)
	}
}

func TestSecretStop_WriteFailureStaysBlocked(t *testing.T) {
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 1}}
	r := stopRunner(richSnap(), prc, &fakeGit{branch: "feat/42-pr-create"}, &fakeGitleaks{n: 1})
	r.readGateStop = injectedStop()
	r.isAncestor = notAncestor
	r.diffScan = func(context.Context, scan.DiffSecretsOptions) (*scan.DiffSecretsResult, error) {
		return &scan.DiffSecretsResult{}, nil
	}
	r.writeGateStop = func(string, int, prCreateGateStop) error { return errors.New("enospc") }
	res, _ := r.Run(context.Background(), 42, "owner/repo", "/wd")
	if res.Path != CreatePathBlocked || prc.createCalls != 0 || !strings.Contains(res.Detail, "record not refreshed") {
		t.Fatalf("got %+v createCalls=%d, want blocked noting the unrefreshed record", res, prc.createCalls)
	}
}

// A cleared stop overrides a validate context that recorded the failed scan,
// so the create is not punted as security-scan-failed.
func TestSecretStop_ClearedStopOverridesFailedSnapshotScan(t *testing.T) {
	snap := richSnap()
	snap.SecurityScan = "failed"
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 1}}
	r := stopRunner(snap, prc, &fakeGit{branch: "feat/42-pr-create"}, &fakeGitleaks{})
	r.readGateStop = injectedStop()
	r.isAncestor = notAncestor
	r.removeGateStop = func(string, int) error { return nil }
	r.diffScan = func(context.Context, scan.DiffSecretsOptions) (*scan.DiffSecretsResult, error) {
		return &scan.DiffSecretsResult{}, nil
	}
	res, _ := r.Run(context.Background(), 42, "owner/repo", "/wd")
	if res.Path != CreatePathCreated || prc.createCalls != 1 {
		t.Fatalf("got %+v createCalls=%d, want created/1", res, prc.createCalls)
	}
}

// Guards that fail closed before any history query or scan runs.
func TestSecretStop_PreScanGuardsFailClosed(t *testing.T) {
	cases := map[string]func(*PRCreateSnapshot, *fakeGit){
		"option-like base":     func(s *PRCreateSnapshot, _ *fakeGit) { s.BaseBranch = "--output=x" },
		"current branch error": func(_ *PRCreateSnapshot, g *fakeGit) { g.branchErr = errors.New("detached") },
	}
	for name, mutate := range cases {
		snap := richSnap()
		g := &fakeGit{branch: "feat/42-pr-create"}
		mutate(&snap, g)
		gl := &fakeGitleaks{}
		prc := &fakePRClient{createdPR: &CreatedPR{Number: 1}}
		r := stopRunner(snap, prc, g, gl)
		r.readGateStop = injectedStop()
		ancestryRan, diffRan := false, false
		r.isAncestor = func(context.Context, string, string) (bool, error) { ancestryRan = true; return false, nil }
		r.diffScan = func(context.Context, scan.DiffSecretsOptions) (*scan.DiffSecretsResult, error) {
			diffRan = true
			return &scan.DiffSecretsResult{}, nil
		}
		res, _ := r.Run(context.Background(), 42, "owner/repo", "/wd")
		if res.Path != CreatePathBlocked || prc.createCalls != 0 || ancestryRan || diffRan || len(gl.bases) != 0 {
			t.Errorf("%s: got path=%q createCalls=%d ancestry=%v diff=%v gitleaks=%d, want blocked with nothing run",
				name, res.Path, prc.createCalls, ancestryRan, diffRan, len(gl.bases))
		}
	}
}
