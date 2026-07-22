package stages

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// richSnap returns a minimal snapshot that the decision matrix accepts —
// rich-context, no punt signals. Tests start from this and mutate one field
// at a time to exercise individual matrix rows.
func richSnap() PRCreateSnapshot {
	return PRCreateSnapshot{
		IssueNumber:      42,
		IssueTitle:       "feat: add deterministic pr-create",
		IssueType:        "feature",
		Branch:           "feat/42-pr-create",
		BaseBranch:       "main",
		HasDev:           true,
		FilesCreated:     []string{"a.go", "a_test.go"},
		HasValidate:      true,
		ValidationStatus: "passed",
		BuildPassed:      true,
		UnitTestsPassed:  true,
		TestsPassed:      4,
	}
}

// ── Decision matrix (pure function) ───────────────────────────────────────

func TestDecideCreate_RichContext(t *testing.T) {
	d := DecideCreate(richSnap())
	if !d.ShouldCreate || d.Punt {
		t.Fatalf("rich snapshot should create, got %+v", d)
	}
	if d.Reason != ReasonRichContext {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonRichContext)
	}
}

func TestDecideCreate_MissingDev_Punts(t *testing.T) {
	s := richSnap()
	s.HasDev = false
	d := DecideCreate(s)
	if !d.Punt || d.Reason != ReasonMissingDevContext {
		t.Errorf("missing dev should punt with %q, got %+v", ReasonMissingDevContext, d)
	}
}

func TestDecideCreate_BatchMode_Punts(t *testing.T) {
	s := richSnap()
	s.BatchPresent = true
	d := DecideCreate(s)
	if !d.Punt || d.Reason != ReasonBatchMode {
		t.Errorf("batch mode should punt with %q, got %+v", ReasonBatchMode, d)
	}
}

func TestDecideCreate_Spike_Punts(t *testing.T) {
	s := richSnap()
	s.IssueType = "spike"
	d := DecideCreate(s)
	if !d.Punt || d.Reason != ReasonSpikeIssue {
		t.Errorf("spike should punt with %q, got %+v", ReasonSpikeIssue, d)
	}
}

func TestDecideCreate_BranchIsBase_Punts(t *testing.T) {
	s := richSnap()
	s.Branch = "main"
	d := DecideCreate(s)
	if !d.Punt || d.Reason != ReasonBranchIsBase {
		t.Errorf("branch == base should punt, got %+v", d)
	}
}

func TestDecideCreate_MissingValidate_Punts(t *testing.T) {
	s := richSnap()
	s.HasValidate = false
	d := DecideCreate(s)
	if !d.Punt || d.Reason != ReasonMissingValidateContext {
		t.Errorf("missing validate should punt, got %+v", d)
	}
}

func TestDecideCreate_ValidationFailed_Punts(t *testing.T) {
	s := richSnap()
	s.ValidationStatus = "failed"
	d := DecideCreate(s)
	if !d.Punt || !strings.Contains(d.Reason, ReasonValidationNotPassed) {
		t.Errorf("validation failed should punt, got %+v", d)
	}
}

func TestDecideCreate_ValidateErrorCategory_Punts(t *testing.T) {
	s := richSnap()
	s.ValidateErrorCategory = "build-failed"
	d := DecideCreate(s)
	if !d.Punt || !strings.Contains(d.Reason, "build-failed") {
		t.Errorf("error category should punt, got %+v", d)
	}
}

func TestDecideCreate_DeadCodeError_Punts(t *testing.T) {
	s := richSnap()
	s.DeadCodeWarningError = true
	d := DecideCreate(s)
	if !d.Punt || d.Reason != ReasonDeadCodeBlocked {
		t.Errorf("dead code error should punt, got %+v", d)
	}
}

func TestDecideCreate_SecurityFailed_Punts(t *testing.T) {
	s := richSnap()
	s.SecurityScan = "failed"
	d := DecideCreate(s)
	if !d.Punt || d.Reason != ReasonSecurityScanFailed {
		t.Errorf("security failed should punt, got %+v", d)
	}
}

func TestDecideCreate_ScopeDriftFailed_Punts(t *testing.T) {
	s := richSnap()
	s.ScopeDrift = "failed"
	d := DecideCreate(s)
	if !d.Punt || d.Reason != ReasonScopeDriftFailed {
		t.Errorf("scope drift failed should punt, got %+v", d)
	}
}

func TestDecideCreate_ChecklistOpen_Punts(t *testing.T) {
	s := richSnap()
	s.ManualChecklistOpen = true
	d := DecideCreate(s)
	if !d.Punt || d.Reason != ReasonChecklistUnverified {
		t.Errorf("checklist open should punt, got %+v", d)
	}
}

func TestDecideCreate_NoChanges_Punts(t *testing.T) {
	s := richSnap()
	s.FilesCreated = nil
	s.FilesModified = nil
	s.FilesDeleted = nil
	d := DecideCreate(s)
	if !d.Punt || d.Reason != ReasonNoChanges {
		t.Errorf("no changes should punt, got %+v", d)
	}
}

// ── Title rendering ───────────────────────────────────────────────────────

func TestRenderTitle_StripsTypePrefix(t *testing.T) {
	s := richSnap()
	s.IssueTitle = "feat: add deterministic pr-create"
	got := RenderTitle(s)
	want := "feat(#42): add deterministic pr-create"
	if got != want {
		t.Errorf("RenderTitle = %q, want %q", got, want)
	}
}

func TestRenderTitle_StripsPrefixWithScope(t *testing.T) {
	s := richSnap()
	s.IssueTitle = "fix(api): handle null response"
	s.IssueType = "fix"
	got := RenderTitle(s)
	want := "fix(#42): handle null response"
	if got != want {
		t.Errorf("RenderTitle = %q, want %q", got, want)
	}
}

func TestRenderTitle_LeavesUnknownPrefixIntact(t *testing.T) {
	s := richSnap()
	s.IssueTitle = "WIP: experimental thing"
	got := RenderTitle(s)
	if !strings.HasSuffix(got, "WIP: experimental thing") {
		t.Errorf("unknown prefix should be preserved verbatim, got %q", got)
	}
}

func TestRenderTitle_UnknownTypeFallsBackToChore(t *testing.T) {
	s := richSnap()
	s.IssueType = "unknown-type"
	s.IssueTitle = "do the thing"
	got := RenderTitle(s)
	if !strings.HasPrefix(got, "chore(#42):") {
		t.Errorf("unknown type should prefix with chore, got %q", got)
	}
}

func TestRenderTitle_Deterministic(t *testing.T) {
	s := richSnap()
	first := RenderTitle(s)
	for i := 0; i < 100; i++ {
		got := RenderTitle(s)
		if got != first {
			t.Fatalf("RenderTitle drifted on iteration %d: first=%q got=%q", i, first, got)
		}
	}
}

// ── Body rendering ────────────────────────────────────────────────────────

func TestRenderBody_Deterministic(t *testing.T) {
	s := richSnap()
	s.FilesCreated = []string{"z.go", "a.go", "m.go"}
	s.FilesModified = []string{"y.go", "b.go"}
	first := RenderBody(s)
	for i := 0; i < 100; i++ {
		got := RenderBody(s)
		if got != first {
			t.Fatalf("RenderBody drifted on iteration %d", i)
		}
	}
}

func TestRenderBody_SortsFileLists(t *testing.T) {
	s := richSnap()
	s.FilesCreated = []string{"z.go", "a.go", "m.go"}
	got := RenderBody(s)
	// Created list should appear in sorted order.
	idxA := strings.Index(got, "- a.go")
	idxM := strings.Index(got, "- m.go")
	idxZ := strings.Index(got, "- z.go")
	if idxA < 0 || idxM < 0 || idxZ < 0 || !(idxA < idxM && idxM < idxZ) {
		t.Errorf("expected sorted file order a → m → z; body = %s", got)
	}
}

func TestRenderBody_ClosesIssue(t *testing.T) {
	body := RenderBody(richSnap())
	if !strings.Contains(body, "Closes #42") {
		t.Errorf("body must include Closes #42, got %q", body)
	}
}

func TestRenderBody_PartOfWhenParent(t *testing.T) {
	s := richSnap()
	s.NativeParent = 100
	body := RenderBody(s)
	if !strings.Contains(body, "Part of #100") {
		t.Errorf("body must include Part of #100 when NativeParent is set, got %q", body)
	}
	if !strings.Contains(body, "Closes #42") {
		t.Errorf("body must still include Closes #42 alongside Part of, got %q", body)
	}
}

func TestRenderBody_OmitsPartOfWhenStandalone(t *testing.T) {
	body := RenderBody(richSnap())
	if strings.Contains(body, "Part of #") {
		t.Errorf("standalone issue body should NOT include Part of, got %q", body)
	}
}

func TestRenderBody_KnowledgeSectionOnlyWhenSet(t *testing.T) {
	s := richSnap()
	without := RenderBody(s)
	if strings.Contains(without, "## Knowledge") {
		t.Errorf("body without knowledge should omit ## Knowledge section")
	}

	s.KnowledgeSection = "## Knowledge\n\n- [PRD](path/PRD.md)\n"
	with := RenderBody(s)
	if !strings.Contains(with, "## Knowledge") || !strings.Contains(with, "[PRD]") {
		t.Errorf("body with knowledge should include rendered section, got %q", with)
	}
}

func TestRenderBody_BuildAndTestSummary(t *testing.T) {
	s := richSnap()
	body := RenderBody(s)
	if !strings.Contains(body, "Build: passed") {
		t.Errorf("expected 'Build: passed' in body, got %q", body)
	}
	if !strings.Contains(body, "Unit tests: passed (4 passed, 0 failed)") {
		t.Errorf("expected unit test summary, got %q", body)
	}
}

// ── Runner end-to-end ─────────────────────────────────────────────────────

// fakePRClient is an in-memory prCreateClient stub.
type fakePRClient struct {
	repoID         string
	createdPR      *CreatedPR
	createErr      error
	getRepoErr     error
	listErr        error
	listResults    []CreatedPR
	createCalls    int
	listCalls      int
	getRepoIDCalls int
}

func (f *fakePRClient) GetRepoID(_ context.Context, _, _ string) (string, error) {
	f.getRepoIDCalls++
	if f.getRepoErr != nil {
		return "", f.getRepoErr
	}
	if f.repoID == "" {
		return "REPO_ID", nil
	}
	return f.repoID, nil
}

func (f *fakePRClient) CreatePR(_ context.Context, _, _, _, _, _ string) (*CreatedPR, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.createdPR, nil
}

func (f *fakePRClient) ListOpenPRsForBranch(_ context.Context, _, _, _ string) ([]CreatedPR, error) {
	f.listCalls++
	return f.listResults, f.listErr
}

// fakeGit is a deterministic git client stub.
type fakeGit struct {
	pushErr      error
	pushCalls    int
	remoteExists bool
	remoteErr    error
	remoteCalls  int
}

func (f *fakeGit) PushBranch(_ context.Context, _, _ string) error {
	f.pushCalls++
	return f.pushErr
}

func (f *fakeGit) RemoteBranchExists(_ context.Context, _, _ string) (bool, error) {
	f.remoteCalls++
	return f.remoteExists, f.remoteErr
}

// newTestRunner builds a runner with stubbed read/write hooks. Tests drive
// behavior by populating the returned snapshot and the fake clients.
func newTestRunner(snap PRCreateSnapshot, prc prCreateClient, git gitClient) *DeterministicPRCreateRunner {
	r := NewDeterministicPRCreateRunner()
	r.prClient = prc
	r.git = git
	r.readContext = func(_ string, _ int) (PRCreateSnapshot, error) { return snap, nil }
	r.writeContext = func(_ string, _ prContextPayload) error { return nil }
	return r
}

func TestRunner_RichContext_CreatesPR(t *testing.T) {
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 99, URL: "https://github.com/owner/repo/pull/99", NodeID: "PR_99"}}
	git := &fakeGit{}
	r := newTestRunner(richSnap(), prc, git)

	res, err := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != CreatePathCreated {
		t.Errorf("Path = %q, want %q", res.Path, CreatePathCreated)
	}
	if res.PRNumber != 99 || res.PRURL == "" {
		t.Errorf("missing PR fields: %+v", res)
	}
	if res.Title == "" || res.Body == "" {
		t.Errorf("title/body should be populated: %+v", res)
	}
	if prc.createCalls != 1 {
		t.Errorf("CreatePR call count = %d, want 1", prc.createCalls)
	}
	if git.pushCalls != 1 {
		t.Errorf("push call count = %d, want 1", git.pushCalls)
	}
}

func TestRunner_AlreadyExists_SkipsCreate(t *testing.T) {
	prc := &fakePRClient{listResults: []CreatedPR{{Number: 50, URL: "https://github.com/owner/repo/pull/50"}}}
	git := &fakeGit{}
	r := newTestRunner(richSnap(), prc, git)

	res, _ := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if res.Path != CreatePathCreated || res.PRNumber != 50 {
		t.Errorf("expected created path with existing PR #50, got %+v", res)
	}
	if prc.createCalls != 0 {
		t.Errorf("CreatePR should not be called when an open PR exists, got %d", prc.createCalls)
	}
	if git.pushCalls != 0 {
		t.Errorf("push should not run when PR already exists, got %d", git.pushCalls)
	}
	if res.Reason != ReasonAlreadyExists {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonAlreadyExists)
	}
}

func TestRunner_PushFails_Punts(t *testing.T) {
	prc := &fakePRClient{}
	// Push fails AND the branch is absent from origin → genuinely cannot proceed.
	git := &fakeGit{pushErr: errors.New("auth failed"), remoteExists: false}
	r := newTestRunner(richSnap(), prc, git)

	res, _ := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if res.Path != CreatePathPunt {
		t.Errorf("Path = %q, want punt", res.Path)
	}
	if !strings.Contains(res.Reason, ReasonPushFailed) {
		t.Errorf("Reason = %q, want push-failed", res.Reason)
	}
	if prc.createCalls != 0 {
		t.Errorf("CreatePR should not run after push failure, got %d", prc.createCalls)
	}
}

// #3828: when the local push is rejected (e.g. diverged worktree) but feature-dev
// already pushed the branch to origin, pr-create must open the PR from the remote
// branch instead of punting to the LLM path (which force-pushes → blocked →
// AskUserQuestion dead-end in headless mode).
func TestRunner_PushRejected_RemoteBranchExists_CreatesPR(t *testing.T) {
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 77, URL: "https://github.com/owner/repo/pull/77", NodeID: "PR_77"}}
	git := &fakeGit{pushErr: errors.New("! [rejected] (non-fast-forward)"), remoteExists: true}
	r := newTestRunner(richSnap(), prc, git)

	res, err := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != CreatePathCreated || res.PRNumber != 77 {
		t.Errorf("expected created PR #77 from existing remote branch, got %+v", res)
	}
	if prc.createCalls != 1 {
		t.Errorf("CreatePR call count = %d, want 1", prc.createCalls)
	}
	if git.remoteCalls != 1 {
		t.Errorf("RemoteBranchExists should be consulted once after push failure, got %d", git.remoteCalls)
	}
	if res.Reason != ReasonPushedRemoteExists {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonPushedRemoteExists)
	}
}

// If the push fails and existence can't be determined, punt rather than guess.
func TestRunner_PushFails_RemoteCheckErrors_Punts(t *testing.T) {
	prc := &fakePRClient{}
	git := &fakeGit{pushErr: errors.New("network"), remoteErr: errors.New("ls-remote timeout")}
	r := newTestRunner(richSnap(), prc, git)

	res, _ := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if res.Path != CreatePathPunt {
		t.Errorf("Path = %q, want punt", res.Path)
	}
	if prc.createCalls != 0 {
		t.Errorf("CreatePR should not run when existence is unknown, got %d", prc.createCalls)
	}
}

func TestRunner_CreatePRFails_Punts(t *testing.T) {
	prc := &fakePRClient{createErr: errors.New("graphql validation: title too long")}
	git := &fakeGit{}
	r := newTestRunner(richSnap(), prc, git)

	res, _ := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if res.Path != CreatePathPunt {
		t.Errorf("Path = %q, want punt on create failure", res.Path)
	}
	if !strings.Contains(res.Reason, ReasonCreateFailed) {
		t.Errorf("Reason = %q, want create-failed", res.Reason)
	}
}

func TestRunner_NilClients_Punts(t *testing.T) {
	r := NewDeterministicPRCreateRunner()
	r.readContext = func(_ string, _ int) (PRCreateSnapshot, error) { return richSnap(), nil }

	res, _ := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if res.Path != CreatePathPunt {
		t.Errorf("nil clients should punt, got %+v", res)
	}
	if res.Reason != ReasonClientUnavailable {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonClientUnavailable)
	}
}

func TestRunner_DecisionPunt_DoesNotPushOrCreate(t *testing.T) {
	prc := &fakePRClient{}
	git := &fakeGit{}
	s := richSnap()
	s.HasDev = false // forces missing-dev punt
	r := newTestRunner(s, prc, git)

	res, _ := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if res.Path != CreatePathPunt {
		t.Errorf("Path = %q, want punt", res.Path)
	}
	if prc.createCalls != 0 || git.pushCalls != 0 {
		t.Errorf("create/push should not be called on punt: createCalls=%d pushCalls=%d", prc.createCalls, git.pushCalls)
	}
}

func TestRunner_RichContext_FastAndCheap(t *testing.T) {
	// AC #7 cost arm — synthetic rich context must complete quickly. Fakes
	// resolve instantly so DurationMs reflects only Go logic, but recording it
	// asserts the runner does record duration (so a regression to "always 0"
	// is caught).
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 7, URL: "u"}}
	git := &fakeGit{}
	r := newTestRunner(richSnap(), prc, git)
	calls := 0
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time {
		calls++
		return base.Add(time.Duration(calls-1) * 50 * time.Millisecond)
	}

	res, _ := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if res.Path != CreatePathCreated {
		t.Fatalf("expected created, got %+v", res)
	}
	if res.DurationMs <= 0 {
		t.Errorf("DurationMs = %d, want > 0", res.DurationMs)
	}
	if res.DurationMs >= 10_000 {
		t.Errorf("DurationMs = %d, want < 10000 (AC #7)", res.DurationMs)
	}
}
