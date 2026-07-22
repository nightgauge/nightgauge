package forgecmd

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/spf13/cobra"
)

// fakeForge is a minimal ForgeClient that returns the per-service
// fakes set on it. Unset fields panic only if exercised.
type fakeForge struct {
	issues   *fakeIssueService
	prs      *fakePRService
	project  *fakeProjectService
	board    *fakeBoardService
	ci       *fakeCIService
	labels   *fakeLabelService
	rulesets forge.RulesetService
	auth     *fakeAuthService
	repo     *fakeRepoService
	graphql  *fakeGraphQLService
}

func (f *fakeForge) Issues() forge.IssueService     { return f.issues }
func (f *fakeForge) PRs() forge.PRService           { return f.prs }
func (f *fakeForge) Project() forge.ProjectService  { return f.project }
func (f *fakeForge) Board() forge.BoardService      { return f.board }
func (f *fakeForge) CI() forge.CIService            { return f.ci }
func (f *fakeForge) Labels() forge.LabelService     { return f.labels }
func (f *fakeForge) Rulesets() forge.RulesetService { return f.rulesets }
func (f *fakeForge) Auth() forge.AuthService        { return f.auth }
func (f *fakeForge) Repo() forge.RepoService        { return f.repo }

// ExecuteGraphQL satisfies forge.GraphQLService when the test sets a
// fakeGraphQLService on the fake forge. The graphql subcommand
// type-asserts forge.ForgeClient to forge.GraphQLService — fakeForge
// satisfies that assertion when graphql is non-nil; callers that omit
// the field exercise the ErrUnsupported path.
func (f *fakeForge) ExecuteGraphQL(ctx context.Context, query string, vars map[string]interface{}) ([]byte, error) {
	if f.graphql == nil {
		return nil, forge.ErrUnsupported
	}
	return f.graphql.ExecuteGraphQL(ctx, query, vars)
}

// withFakeForge replaces forgeFromContext for the test's lifetime so
// the subcommand resolver returns the supplied fake.
func withFakeForge(t *testing.T, fake forge.ForgeClient) {
	t.Helper()
	orig := forgeFromContext
	forgeFromContext = func(_ *cobra.Command) (forge.ForgeClient, error) {
		return fake, nil
	}
	t.Cleanup(func() { forgeFromContext = orig })
}

// --- IssueService fake (minimal — only the methods we exercise) ---

type fakeIssueService struct {
	getIssueErr  error
	getIssueResp *forgetypes.Issue
	listResp     []forgetypes.Issue
	createResp   *forgetypes.Issue
	editResp     *forgetypes.Issue
	addLabelsErr error
	calls        []string
}

func (f *fakeIssueService) GetIssue(_ context.Context, _, _ string, n int) (*forgetypes.Issue, error) {
	f.calls = append(f.calls, "GetIssue")
	if f.getIssueErr != nil {
		return nil, f.getIssueErr
	}
	if f.getIssueResp != nil {
		return f.getIssueResp, nil
	}
	return &forgetypes.Issue{Number: n}, nil
}

func (f *fakeIssueService) GetIssuesByNumbers(_ context.Context, _, _ string, _ []int) (map[int]*forgetypes.Issue, error) {
	return nil, nil
}

func (f *fakeIssueService) ListIssues(_ context.Context, _, _ string, _ []string) ([]forgetypes.Issue, error) {
	f.calls = append(f.calls, "ListIssues")
	return f.listResp, nil
}

func (f *fakeIssueService) IterateIssues(_ context.Context, _, _ string, _ []string) forge.Iterator[forgetypes.Issue] {
	return nil
}

func (f *fakeIssueService) SearchIssues(_ context.Context, _, _, _ string, _ int) ([]forgetypes.Issue, error) {
	return nil, nil
}

func (f *fakeIssueService) HasLabel(_ context.Context, _, _ string, _ int, _ string) (bool, error) {
	return false, nil
}
func (f *fakeIssueService) GetRepoLabels(_ context.Context, _, _ string) (map[string]string, error) {
	return nil, nil
}
func (f *fakeIssueService) CreateIssue(_ context.Context, _, _, _ string, _ []string) (*forgetypes.Issue, error) {
	f.calls = append(f.calls, "CreateIssue")
	return f.createResp, nil
}
func (f *fakeIssueService) CloseIssue(_ context.Context, _ string) error {
	f.calls = append(f.calls, "CloseIssue")
	return nil
}
func (f *fakeIssueService) ReopenIssue(_ context.Context, _ string) error {
	f.calls = append(f.calls, "ReopenIssue")
	return nil
}
func (f *fakeIssueService) EditIssue(_ context.Context, _, _ string) (*forgetypes.Issue, error) {
	f.calls = append(f.calls, "EditIssue")
	return f.editResp, nil
}
func (f *fakeIssueService) UpdateIssue(_ context.Context, _ string, _ forge.UpdateIssueOptions) (*forgetypes.Issue, error) {
	return nil, nil
}
func (f *fakeIssueService) AddComment(_ context.Context, _, _ string) error {
	f.calls = append(f.calls, "AddComment")
	return nil
}
func (f *fakeIssueService) AddSubIssue(_ context.Context, _, _ string) error    { return nil }
func (f *fakeIssueService) RemoveSubIssue(_ context.Context, _, _ string) error { return nil }
func (f *fakeIssueService) LinkSubIssue(_ context.Context, _, _ string, _, _ int) error {
	return nil
}
func (f *fakeIssueService) AddBlockedBy(_ context.Context, _, _ string) error    { return nil }
func (f *fakeIssueService) RemoveBlockedBy(_ context.Context, _, _ string) error { return nil }
func (f *fakeIssueService) AddLabels(_ context.Context, _ string, _ []string) error {
	f.calls = append(f.calls, "AddLabels")
	return f.addLabelsErr
}
func (f *fakeIssueService) RemoveLabels(_ context.Context, _ string, _ []string) error {
	f.calls = append(f.calls, "RemoveLabels")
	return nil
}
func (f *fakeIssueService) SyncStatusLabel(_ context.Context, _, _ string, _ int, _ string) error {
	return nil
}
func (f *fakeIssueService) MarkRefined(_ context.Context, _, _ string, _ int) error { return nil }
func (f *fakeIssueService) GetEpicProgress(_ context.Context, _ string) (*forgetypes.EpicProgress, error) {
	return nil, nil
}
func (f *fakeIssueService) GetEpicProgressByNumber(_ context.Context, _, _ string, _ int) (*forgetypes.EpicProgress, error) {
	return nil, nil
}

// --- PRService fake ---

type fakePRService struct {
	getResp    *forgetypes.PullRequest
	listResp   []forgetypes.PullRequest
	updateResp *forgetypes.PullRequest
	mergeErr   error
	mergeStrat string
	calls      []string
}

func (f *fakePRService) GetPR(_ context.Context, _, _ string, n int) (*forgetypes.PullRequest, error) {
	f.calls = append(f.calls, "GetPR")
	if f.getResp != nil {
		return f.getResp, nil
	}
	return &forgetypes.PullRequest{Number: n}, nil
}
func (f *fakePRService) ListPRs(_ context.Context, _, _, _, _ string) ([]forgetypes.PullRequest, error) {
	f.calls = append(f.calls, "ListPRs")
	return f.listResp, nil
}
func (f *fakePRService) IteratePRs(_ context.Context, _, _, _, _ string) forge.Iterator[forgetypes.PullRequest] {
	return nil
}
func (f *fakePRService) CreatePR(_ context.Context, _, _, _, _, _ string) (*forgetypes.PullRequest, error) {
	f.calls = append(f.calls, "CreatePR")
	return &forgetypes.PullRequest{Number: 1, Title: "new"}, nil
}
func (f *fakePRService) UpdatePR(_ context.Context, _ string, _ forge.UpdatePROptions) (*forgetypes.PullRequest, error) {
	f.calls = append(f.calls, "UpdatePR")
	return f.updateResp, nil
}
func (f *fakePRService) ClosePR(_ context.Context, _ string) error {
	f.calls = append(f.calls, "ClosePR")
	return nil
}
func (f *fakePRService) MergePR(_ context.Context, _ string) error {
	f.calls = append(f.calls, "MergePR")
	return f.mergeErr
}
func (f *fakePRService) MergePRWithStrategy(_ context.Context, _ string, strategy string) (string, error) {
	f.calls = append(f.calls, "MergePRWithStrategy")
	f.mergeStrat = strategy
	return "deadbeef", nil
}
func (f *fakePRService) DeleteBranch(_ context.Context, _, _, _ string) error { return nil }
func (f *fakePRService) CreateEpicPR(_ context.Context, _, _ string, _ int, _, _, _ string) (*forgetypes.EpicPRResult, error) {
	return nil, nil
}
func (f *fakePRService) MergeEpicPR(_ context.Context, _, _ string, _, _ string) error {
	return nil
}

// --- ProjectService fake ---

type fakeProjectService struct {
	snapshotResp *forgetypes.FieldsSnapshot
	addItemResp  string
	calls        []string
}

func (f *fakeProjectService) AddItem(_ context.Context, _ string) (string, error) { return "", nil }
func (f *fakeProjectService) AddIssueByNumber(_ context.Context, _, _ string, _ int) (string, error) {
	f.calls = append(f.calls, "AddIssueByNumber")
	return f.addItemResp, nil
}
func (f *fakeProjectService) BulkAddIssues(_ context.Context, _, _ string, _ []forgetypes.Issue) forgetypes.BulkAddResult {
	return forgetypes.BulkAddResult{}
}
func (f *fakeProjectService) SyncStatus(_ context.Context, _, _ string, _ int, _ string) error {
	return nil
}
func (f *fakeProjectService) MoveStatus(_ context.Context, _, _ string, _ int, _ string) error {
	return nil
}
func (f *fakeProjectService) SyncIteration(_ context.Context, _, _ string, _ int, _ string) error {
	return nil
}
func (f *fakeProjectService) SetSingleSelectField(_ context.Context, _, _, _ string) error {
	f.calls = append(f.calls, "SetSingleSelectField")
	return nil
}
func (f *fakeProjectService) SetNumberField(_ context.Context, _, _ string, _ float64) error {
	f.calls = append(f.calls, "SetNumberField")
	return nil
}
func (f *fakeProjectService) SetTextField(_ context.Context, _, _, _ string) error {
	f.calls = append(f.calls, "SetTextField")
	return nil
}
func (f *fakeProjectService) SetTextFieldOptional(_ context.Context, _, _, _ string) error {
	return nil
}
func (f *fakeProjectService) SetDateField(_ context.Context, _, _, _ string) error {
	f.calls = append(f.calls, "SetDateField")
	return nil
}
func (f *fakeProjectService) SetDateFieldOptional(_ context.Context, _, _, _ string) error {
	return nil
}
func (f *fakeProjectService) SetIterationField(_ context.Context, _, _, _ string) error {
	return nil
}
func (f *fakeProjectService) SetFields(_ context.Context, _, _ string, _ int, _ map[string]string) error {
	return nil
}
func (f *fakeProjectService) SetHours(_ context.Context, _, _ string, _ int, _ float64) error {
	return nil
}
func (f *fakeProjectService) SetDateFieldByNumber(_ context.Context, _, _ string, _ int, _, _ string) error {
	return nil
}
func (f *fakeProjectService) SetEstimateFromLabels(_ context.Context, _, _ string, _ int, _ []string, _ map[string]float64) error {
	return nil
}
func (f *fakeProjectService) AddBlockedByNumber(_ context.Context, _, _ string, _, _ int) error {
	return nil
}
func (f *fakeProjectService) RemoveBlockedByNumber(_ context.Context, _, _ string, _, _ int) error {
	return nil
}
func (f *fakeProjectService) UpdateEpicEstimates(_ context.Context, _, _ string, _ int) (float64, error) {
	return 0, nil
}
func (f *fakeProjectService) EnsureFields(_ context.Context, _ forgetypes.FieldSchema) (*forgetypes.EnsureFieldsResult, error) {
	return nil, nil
}
func (f *fakeProjectService) DriftCheck(_ context.Context) ([]forgetypes.FieldDrift, error) {
	return nil, nil
}
func (f *fakeProjectService) DriftFix(_ context.Context) ([]forgetypes.FieldDrift, error) {
	return nil, nil
}
func (f *fakeProjectService) SnapshotFields(_ context.Context) (*forgetypes.FieldsSnapshot, error) {
	f.calls = append(f.calls, "SnapshotFields")
	if f.snapshotResp != nil {
		return f.snapshotResp, nil
	}
	return &forgetypes.FieldsSnapshot{ProjectID: "P_1", Fields: map[string]forgetypes.FieldInfo{}}, nil
}

// --- BoardService fake ---

type fakeBoardService struct {
	listResp []forgetypes.BoardItem
	getResp  *forgetypes.BoardItem
}

func (f *fakeBoardService) ListItems(_ context.Context, _ string) ([]forgetypes.BoardItem, error) {
	return f.listResp, nil
}
func (f *fakeBoardService) ListOpenItems(_ context.Context) ([]forgetypes.BoardItem, int, error) {
	return nil, 0, nil
}
func (f *fakeBoardService) CountsByStatus(_ context.Context) (*forgetypes.StatusCounts, error) {
	return nil, nil
}
func (f *fakeBoardService) GetItem(_ context.Context, _, _ string, _ int) (*forgetypes.BoardItem, error) {
	if f.getResp != nil {
		return f.getResp, nil
	}
	return &forgetypes.BoardItem{}, nil
}

// --- CIService fake ---

type fakeCIService struct {
	resp *forgetypes.CheckStatus
}

func (f *fakeCIService) GetCheckStatus(_ context.Context, _, _ string, _ int) (*forgetypes.CheckStatus, error) {
	if f.resp != nil {
		return f.resp, nil
	}
	return &forgetypes.CheckStatus{}, nil
}
func (f *fakeCIService) GetRequiredCheckNames(_ context.Context, _, _, _ string) ([]string, error) {
	return nil, nil
}
func (f *fakeCIService) GetIndividualCheckRuns(_ context.Context, _, _, _ string) ([]forgetypes.CheckDetail, error) {
	return nil, nil
}
func (f *fakeCIService) WaitForChecks(_ context.Context, _, _ string, _ int, _ forgetypes.WaitConfig) (*forgetypes.CheckStatus, error) {
	return nil, nil
}
func (f *fakeCIService) GetRunLogs(_ context.Context, _, _ string, _ int64) (*forgetypes.CIRunLog, error) {
	return nil, nil
}

// --- LabelService fake ---

type fakeLabelService struct {
	listResp   []*forgetypes.Label
	createResp *forgetypes.Label
	calls      []string
}

func (f *fakeLabelService) List(_ context.Context) ([]*forgetypes.Label, error) {
	f.calls = append(f.calls, "List")
	return f.listResp, nil
}
func (f *fakeLabelService) Create(_ context.Context, _, _, _ string) (*forgetypes.Label, error) {
	f.calls = append(f.calls, "Create")
	return f.createResp, nil
}
func (f *fakeLabelService) Delete(_ context.Context, _ string) error {
	f.calls = append(f.calls, "Delete")
	return nil
}

// --- AuthService fake ---

type fakeAuthService struct {
	resp *forgetypes.TokenScopeInfo
	err  error
}

func (f *fakeAuthService) CheckTokenScopes(_ context.Context) (*forgetypes.TokenScopeInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.resp != nil {
		return f.resp, nil
	}
	return &forgetypes.TokenScopeInfo{Login: "test", Valid: true}, nil
}

func (f *fakeAuthService) Whoami(_ context.Context) (*forgetypes.Actor, error) {
	if f.err != nil {
		return nil, f.err
	}
	login := "test"
	if f.resp != nil && f.resp.Login != "" {
		login = f.resp.Login
	}
	return &forgetypes.Actor{Login: login}, nil
}

// --- RepoService fake ---

type fakeRepoService struct {
	resp *forgetypes.Repo
	err  error
}

func (f *fakeRepoService) RepoMetadata(_ context.Context, owner, name string) (*forgetypes.Repo, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.resp != nil {
		return f.resp, nil
	}
	return &forgetypes.Repo{NameWithOwner: owner + "/" + name, Owner: owner, Name: name}, nil
}

// --- GraphQLService fake ---

type fakeGraphQLService struct {
	resp     []byte
	err      error
	lastQ    string
	lastVars map[string]interface{}
}

func (f *fakeGraphQLService) ExecuteGraphQL(_ context.Context, query string, vars map[string]interface{}) ([]byte, error) {
	f.lastQ = query
	f.lastVars = vars
	if f.err != nil {
		return nil, f.err
	}
	if f.resp != nil {
		return f.resp, nil
	}
	return []byte(`{"data":{}}`), nil
}
