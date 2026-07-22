package graduation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

const autoFixtureDecisions = `# Decisions: #4242

## ADR-001: Always validate input at API boundaries

**Status**: Accepted
**Context**: Every public handler that accepts untrusted bytes must validate.
**Decision**: Always validate inputs at API boundaries. No service trusts upstream services for shape, length, or charset. Every handler that accepts untrusted bytes MUST validate before logging or persisting.
**Consequences**: All handlers share one validation pattern, attack surface drops, and reviewers can grep for the helper across services to audit coverage.

## ADR-002: Specific cache at packages/foo/bar.ts
<!-- graduated-to: docs/X.md -->

**Status**: Accepted
**Context**: this PR needs a local cache in packages/foo/bar.ts.
**Decision**: Add an LRU cache at packages/foo/bar.ts.
**Consequences**: [Expected impact, trade-offs, and follow-up actions]
`

// writeFixture builds a temp workspace with the decisions fixture and a few
// docs/*.md files so suggestedDest can score them.
func writeFixture(t *testing.T, decisionsContent string, docsFiles map[string]string) string {
	t.Helper()
	root := t.TempDir()
	kbDir := filepath.Join(root, ".nightgauge", "knowledge", "features", "4242-auto-test")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "decisions.md"), []byte(decisionsContent), 0o644); err != nil {
		t.Fatal(err)
	}
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range docsFiles {
		if err := os.WriteFile(filepath.Join(docs, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// --- fakes -----------------------------------------------------------------

type fakeGit struct {
	current       string
	createdBranch string
	createdFrom   string
	pushedBranch  string
	commits       []string
	checkouts     []string
	createErr     error
	pushErr       error
	commitErr     error
	branchExists  map[string]bool
	branchDeleted []string
}

func (f *fakeGit) CurrentBranch() (string, error) { return f.current, nil }
func (f *fakeGit) LocalBranchExists(n string) (bool, error) {
	if f.branchExists == nil {
		return false, nil
	}
	return f.branchExists[n], nil
}
func (f *fakeGit) BranchCreateFrom(n, base string) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.createdBranch = n
	f.createdFrom = base
	return nil
}
func (f *fakeGit) BranchDelete(n string) error {
	f.branchDeleted = append(f.branchDeleted, n)
	return nil
}
func (f *fakeGit) Checkout(b string) error { f.checkouts = append(f.checkouts, b); return nil }
func (f *fakeGit) Commit(m string) (string, error) {
	if f.commitErr != nil {
		return "", f.commitErr
	}
	f.commits = append(f.commits, m)
	return "deadbeef", nil
}
func (f *fakeGit) PushBranch(n string) error {
	if f.pushErr != nil {
		return f.pushErr
	}
	f.pushedBranch = n
	return nil
}

type fakeForge struct {
	repoID         string
	createPRReturn *forgetypes.PullRequest
	openPRs        []forgetypes.PullRequest
	createErr      error
	updateErr      error
	updateLabels   []string
	addItemReturn  string
	addItemErr     error
	statusErr      error
	statusField    string
	statusOption   string
	itemAdded      string
	createCalls    []createCall
}

type createCall struct {
	repoID, title, body, head, base string
}

func (f *fakeForge) GetRepoID(_ context.Context, _, _ string) (string, error) {
	if f.repoID == "" {
		return "R_1", nil
	}
	return f.repoID, nil
}
func (f *fakeForge) ListOpenPRsForBranch(_ context.Context, _, _, _ string) ([]forgetypes.PullRequest, error) {
	return f.openPRs, nil
}
func (f *fakeForge) CreatePR(_ context.Context, repoID, title, body, head, base string) (*forgetypes.PullRequest, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.createCalls = append(f.createCalls, createCall{repoID, title, body, head, base})
	if f.createPRReturn != nil {
		return f.createPRReturn, nil
	}
	return &forgetypes.PullRequest{Number: 9001, URL: "https://example/pr/9001", NodeID: "PR_9001"}, nil
}
func (f *fakeForge) UpdatePR(_ context.Context, _ string, opts forge.UpdatePROptions) (*forgetypes.PullRequest, error) {
	if opts.Labels != nil {
		f.updateLabels = append([]string{}, *opts.Labels...)
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return &forgetypes.PullRequest{}, nil
}
func (f *fakeForge) AddProjectItem(_ context.Context, contentNodeID string) (string, error) {
	if f.addItemErr != nil {
		return "", f.addItemErr
	}
	f.itemAdded = contentNodeID
	if f.addItemReturn == "" {
		return "PVTI_1", nil
	}
	return f.addItemReturn, nil
}
func (f *fakeForge) SetProjectStatus(_ context.Context, _, field, option string) error {
	f.statusField = field
	f.statusOption = option
	return f.statusErr
}

// --- tests ----------------------------------------------------------------

func TestKebab(t *testing.T) {
	cases := map[string]string{
		"Always validate input at API boundaries": "always-validate-input-at-api-boundaries",
		"  Hello, World!  ":                       "hello-world",
		"foo___bar---baz":                         "foo-bar-baz",
		"":                                        "",
		"ALL CAPS":                                "all-caps",
	}
	for in, want := range cases {
		if got := kebab(in); got != want {
			t.Errorf("kebab(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveAnchor_Collision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ARCH.md")
	body := "# Architecture\n\n## Always Validate Input At API Boundaries\n\nexisting prose\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := deriveAnchor(path, "Always validate input at API boundaries")
	if err != nil {
		t.Fatalf("deriveAnchor: %v", err)
	}
	if got != "always-validate-input-at-api-boundaries-2" {
		t.Errorf("got %q, want collision suffix", got)
	}
}

func TestDeriveAnchor_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "NEW.md")
	got, err := deriveAnchor(path, "Brand New Section")
	if err != nil {
		t.Fatalf("deriveAnchor: %v", err)
	}
	if got != "brand-new-section" {
		t.Errorf("got %q, want base anchor", got)
	}
}

func TestAppendDestinationSection_LeadingNewlines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DEST.md")
	if err := os.WriteFile(path, []byte("# Header\n\nprior content without trailing newline"), 0o644); err != nil {
		t.Fatal(err)
	}
	rendered := "## New Section\n\nbody\n"
	if err := appendDestinationSection(path, rendered); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "\n\n## New Section") {
		t.Errorf("expected two newlines before appended section, got:\n%s", got)
	}
}

func TestAppendDestinationSection_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "NEW.md")
	rendered := "## hello\nbody\n"
	if err := appendDestinationSection(path, rendered); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != rendered {
		t.Errorf("expected %q, got %q", rendered, got)
	}
}

func TestAutoGraduate_NoCandidates(t *testing.T) {
	root := writeFixture(t, "# Decisions: #4242\n\n(no ADRs yet)\n", map[string]string{
		"ARCHITECTURE.md": "# x",
	})
	res, err := AutoGraduate(context.Background(), AutoGraduateInput{
		WorkspaceRoot: root,
		IssueNumber:   4242,
		DryRun:        true,
		Git:           &fakeGit{current: "main"},
	})
	if err != nil {
		t.Fatalf("AutoGraduate: %v", err)
	}
	if res.Status != AutoStatusNoCandidates {
		t.Errorf("status = %q, want no_candidates", res.Status)
	}
}

func TestAutoGraduate_DryRun(t *testing.T) {
	root := writeFixture(t, autoFixtureDecisions, map[string]string{
		"ARCHITECTURE.md":   "# Architecture\n",
		"CODE_STANDARDS.md": "# Code standards\n",
		"KNOWLEDGE_BASE.md": "# KB\n",
	})
	res, err := AutoGraduate(context.Background(), AutoGraduateInput{
		WorkspaceRoot: root,
		IssueNumber:   4242,
		DryRun:        true,
		Git:           &fakeGit{current: "main"},
	})
	if err != nil {
		t.Fatalf("AutoGraduate: %v", err)
	}
	if res.Status != AutoStatusDryRun {
		t.Fatalf("status = %q, want dry_run", res.Status)
	}
	if len(res.PerCandidate) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(res.PerCandidate))
	}
	o := res.PerCandidate[0]
	if o.ADRIndex != 1 {
		t.Errorf("ADRIndex = %d, want 1", o.ADRIndex)
	}
	if o.Branch != "docs/graduate-4242-adr-001" {
		t.Errorf("Branch = %q, want docs/graduate-4242-adr-001", o.Branch)
	}
	if o.Status != AutoStatusDryRun {
		t.Errorf("per-candidate status = %q, want dry_run", o.Status)
	}
	if !strings.Contains(o.PlannedAppend, "Always validate input at API boundaries") {
		t.Errorf("PlannedAppend missing title, got: %s", o.PlannedAppend)
	}
	if !strings.Contains(o.PlannedAppend, "<!-- graduated-from:") {
		t.Errorf("PlannedAppend missing graduated-from marker")
	}
	if !strings.Contains(o.PlannedAppend, "**Decision**: Always validate inputs at API boundaries.") {
		t.Errorf("PlannedAppend missing verbatim Decision block")
	}
	// Ensure no filesystem mutation happened in dry-run mode.
	src := filepath.Join(root, ".nightgauge", "knowledge", "features", "4242-auto-test", "decisions.md")
	raw, _ := os.ReadFile(src)
	if strings.Contains(string(raw), "graduated-to: docs/CODE_STANDARDS.md") {
		t.Errorf("dry-run mutated source decisions.md")
	}
}

func TestAutoGraduate_Idempotent(t *testing.T) {
	// Pre-marked ADR-001 so the idempotency branch triggers. ADR-001 stays
	// above the MinScore=1 threshold even after the -2 already_graduated
	// penalty (2 general + 2 pattern + 1 filled − 2 graduated = 3), so the
	// candidate selector still picks it up.
	idempotentFixture := strings.Replace(autoFixtureDecisions,
		"## ADR-001: Always validate input at API boundaries\n",
		"## ADR-001: Always validate input at API boundaries\n<!-- graduated-to: docs/CODE_STANDARDS.md#always-validate-input-at-api-boundaries -->\n",
		1,
	)
	root := writeFixture(t, idempotentFixture, map[string]string{
		"ARCHITECTURE.md":   "# Architecture\n",
		"CODE_STANDARDS.md": "# Code standards\n",
		"KNOWLEDGE_BASE.md": "# KB\n",
	})
	res, err := AutoGraduate(context.Background(), AutoGraduateInput{
		WorkspaceRoot: root,
		IssueNumber:   4242,
		ADRIndex:      1,
		BaseBranch:    "main",
		Owner:         "nightgauge",
		Repo:          "nightgauge",
		Git:           &fakeGit{current: "main"},
		Forge:         &fakeForge{openPRs: []forgetypes.PullRequest{{Number: 7777, URL: "https://example/pr/7777", NodeID: "PR_7777"}}},
	})
	if err != nil {
		t.Fatalf("AutoGraduate: %v", err)
	}
	if len(res.PerCandidate) != 1 {
		t.Fatalf("want 1 outcome, got %d", len(res.PerCandidate))
	}
	o := res.PerCandidate[0]
	if o.Status != AutoStatusAlreadyGraduated {
		t.Errorf("status = %q, want already_graduated", o.Status)
	}
	if o.PRNumber != 7777 {
		t.Errorf("PRNumber = %d, want 7777 (looked up via ListOpenPRsForBranch)", o.PRNumber)
	}
}

func TestAutoGraduate_TieUnresolved(t *testing.T) {
	tieFixture := `# Decisions: #4242

## ADR-001: Always validate input at API boundaries

**Status**: Accepted
**Context**: ctx
**Decision**: Always validate inputs at API boundaries. No service trusts upstream services. Every handler MUST validate before logging.
**Consequences**: Attack surface drops. Reviewers can grep for the helper across services. The validator centralizes test fixtures.

## ADR-002: Never store secrets in env files

**Status**: Accepted
**Context**: ctx
**Decision**: Never store secrets in plaintext env files. Every secret MUST be loaded from a secrets manager at runtime.
**Consequences**: Secret rotation is decoupled from deploys. Onboarding now requires a vault token. Local dev seeds use a separate fixture.
`
	root := writeFixture(t, tieFixture, map[string]string{
		"ARCHITECTURE.md":   "# Architecture\n",
		"CODE_STANDARDS.md": "# Code standards\n",
		"SECURITY.md":       "# Security\n",
	})
	res, err := AutoGraduate(context.Background(), AutoGraduateInput{
		WorkspaceRoot: root,
		IssueNumber:   4242,
		DryRun:        true,
		Git:           &fakeGit{current: "main"},
	})
	if err != nil {
		t.Fatalf("AutoGraduate: %v", err)
	}
	if res.Status != AutoStatusTieUnresolved {
		t.Errorf("status = %q, want tie_unresolved", res.Status)
	}
	if len(res.TiedADRIndexes) != 2 {
		t.Errorf("TiedADRIndexes = %v, want 2 entries", res.TiedADRIndexes)
	}
}

func TestAutoGraduate_CreatedHappyPath(t *testing.T) {
	root := writeFixture(t, autoFixtureDecisions, map[string]string{
		"ARCHITECTURE.md":   "# Architecture\n",
		"CODE_STANDARDS.md": "# Code standards\n",
		"KNOWLEDGE_BASE.md": "# KB\n",
	})
	git := &fakeGit{current: "feat/4242-test"}
	fg := &fakeForge{}
	res, err := AutoGraduate(context.Background(), AutoGraduateInput{
		WorkspaceRoot: root,
		IssueNumber:   4242,
		BaseBranch:    "main",
		Owner:         "nightgauge",
		Repo:          "nightgauge",
		Git:           git,
		Forge:         fg,
	})
	if err != nil {
		t.Fatalf("AutoGraduate: %v", err)
	}
	if res.Status != AutoStatusCreated {
		t.Fatalf("status = %q, want created. PerCandidate=%+v", res.Status, res.PerCandidate)
	}
	if len(res.PerCandidate) != 1 {
		t.Fatalf("want 1 outcome, got %d", len(res.PerCandidate))
	}
	o := res.PerCandidate[0]
	if o.PRNumber != 9001 {
		t.Errorf("PRNumber = %d, want 9001", o.PRNumber)
	}
	if o.Branch != "docs/graduate-4242-adr-001" {
		t.Errorf("Branch = %q, want docs/graduate-4242-adr-001", o.Branch)
	}
	if git.createdBranch != "docs/graduate-4242-adr-001" || git.createdFrom != "main" {
		t.Errorf("BranchCreateFrom(%q, %q), want (docs/graduate-4242-adr-001, main)", git.createdBranch, git.createdFrom)
	}
	if git.pushedBranch != "docs/graduate-4242-adr-001" {
		t.Errorf("PushBranch = %q, want docs/graduate-4242-adr-001", git.pushedBranch)
	}
	if len(git.commits) != 1 || !strings.HasPrefix(git.commits[0], "docs(#4242): graduate Always validate input at API boundaries") {
		t.Errorf("commits = %v", git.commits)
	}
	if len(fg.updateLabels) != 3 ||
		fg.updateLabels[0] != "type:docs" ||
		fg.updateLabels[1] != "priority:medium" ||
		fg.updateLabels[2] != "size:S" {
		t.Errorf("labels = %v, want [type:docs priority:medium size:S]", fg.updateLabels)
	}
	if fg.statusField != "Status" || fg.statusOption != "Ready" {
		t.Errorf("project status = %s/%s, want Status/Ready", fg.statusField, fg.statusOption)
	}
	if !o.BoardSynced {
		t.Errorf("BoardSynced = false, want true")
	}
	// Source decisions.md should now contain the graduated-to marker.
	src := filepath.Join(root, ".nightgauge", "knowledge", "features", "4242-auto-test", "decisions.md")
	raw, _ := os.ReadFile(src)
	if !strings.Contains(string(raw), "<!-- graduated-to:") {
		t.Errorf("source decisions.md missing graduated-to marker")
	}
	// Destination doc should contain the graduated-from marker and verbatim block.
	dest, _ := os.ReadFile(filepath.Join(root, o.DestinationDoc))
	if !strings.Contains(string(dest), "<!-- graduated-from:") {
		t.Errorf("destination doc missing graduated-from marker")
	}
	if !strings.Contains(string(dest), "**Decision**: Always validate inputs at API boundaries.") {
		t.Errorf("destination doc missing verbatim Decision body")
	}
	// Original branch restored.
	if len(git.checkouts) == 0 || git.checkouts[len(git.checkouts)-1] != "feat/4242-test" {
		t.Errorf("expected restore to feat/4242-test, got checkouts=%v", git.checkouts)
	}
}

func TestAutoGraduate_LabelUnsupportedIsNonFatal(t *testing.T) {
	root := writeFixture(t, autoFixtureDecisions, map[string]string{
		"CODE_STANDARDS.md": "# Code standards\n",
	})
	fg := &fakeForge{updateErr: forge.ErrUnsupported}
	res, err := AutoGraduate(context.Background(), AutoGraduateInput{
		WorkspaceRoot: root,
		IssueNumber:   4242,
		Owner:         "nightgauge",
		Repo:          "nightgauge",
		Git:           &fakeGit{current: "main"},
		Forge:         fg,
	})
	if err != nil {
		t.Fatalf("AutoGraduate: %v", err)
	}
	if res.Status != AutoStatusCreated {
		t.Fatalf("status = %q, want created (label ErrUnsupported is non-fatal)", res.Status)
	}
}

func TestAutoGraduate_PushFailureIsError(t *testing.T) {
	root := writeFixture(t, autoFixtureDecisions, map[string]string{
		"CODE_STANDARDS.md": "# Code standards\n",
	})
	git := &fakeGit{current: "main", pushErr: errors.New("network")}
	res, err := AutoGraduate(context.Background(), AutoGraduateInput{
		WorkspaceRoot: root,
		IssueNumber:   4242,
		Owner:         "nightgauge",
		Repo:          "nightgauge",
		Git:           git,
		Forge:         &fakeForge{},
	})
	if err != nil {
		t.Fatalf("AutoGraduate: %v", err)
	}
	if res.Status != AutoStatusError {
		t.Errorf("status = %q, want error", res.Status)
	}
	if len(res.PerCandidate) != 1 || res.PerCandidate[0].Status != AutoStatusError {
		t.Errorf("expected per-candidate error, got %+v", res.PerCandidate)
	}
}
