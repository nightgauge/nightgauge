package boardcache

import (
	"context"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// WrapClient returns a forge.ForgeClient whose Board() reads through the cache
// and whose Project() invalidates that board on every mutation it issues.
//
// Both halves are required for the cache to be correct rather than merely
// cheap. A read-through cache alone would let this process write a status and
// then read its own stale snapshot back — the board would appear not to have
// changed, which for a surface like the Action Center means showing an operator
// a card they just resolved.
func WrapClient(c *Cache, inner forge.ForgeClient, owner string, project int) forge.ForgeClient {
	if c == nil || inner == nil {
		return inner
	}
	return &cachedClient{
		ForgeClient: inner,
		board:       c.Wrap(inner.Board(), owner, project),
		project:     wrapProject(c, inner.Project(), owner, project),
	}
}

// cachedClient embeds the inner client so every service it does not override is
// passed through untouched.
type cachedClient struct {
	forge.ForgeClient
	board   forge.BoardService
	project forge.ProjectService
}

func (c *cachedClient) Board() forge.BoardService     { return c.board }
func (c *cachedClient) Project() forge.ProjectService { return c.project }

// wrapProject returns nil for a nil inner service so the caller's own nil check
// (`Forge.Project() == nil`) keeps working — several producers branch on it.
func wrapProject(c *Cache, inner forge.ProjectService, owner string, project int) forge.ProjectService {
	if inner == nil {
		return nil
	}
	return &invalidatingProject{ProjectService: inner, cache: c, owner: owner, project: project}
}

// invalidatingProject drops the board's snapshots after any call that can
// change what a board read would return.
//
// It embeds ProjectService rather than listing all ~25 methods, so read-only
// and schema-inspection calls pass straight through. The risk that buys — a
// mutating method added to the interface later would silently bypass
// invalidation — is not left to vigilance: TestEveryMutatingProjectMethodIsIntercepted
// enumerates the interface by reflection and fails when a method is neither
// overridden here nor named in the read-only allowlist.
type invalidatingProject struct {
	forge.ProjectService
	cache   *Cache
	owner   string
	project int
}

// invalidate is called after the inner call returns, on success OR failure. A
// failed mutation is exactly the case where the board's true state is least
// certain — the write may have landed before the error — so keeping a snapshot
// across it is the wrong bet.
func (p *invalidatingProject) invalidate() { p.cache.Invalidate(p.owner, p.project) }

func (p *invalidatingProject) AddItem(ctx context.Context, contentNodeID string) (string, error) {
	defer p.invalidate()
	return p.ProjectService.AddItem(ctx, contentNodeID)
}

func (p *invalidatingProject) AddIssueByNumber(ctx context.Context, owner, repo string, number int) (string, error) {
	defer p.invalidate()
	return p.ProjectService.AddIssueByNumber(ctx, owner, repo, number)
}

func (p *invalidatingProject) BulkAddIssues(ctx context.Context, owner, repo string, issues []forgetypes.Issue) forgetypes.BulkAddResult {
	defer p.invalidate()
	return p.ProjectService.BulkAddIssues(ctx, owner, repo, issues)
}

func (p *invalidatingProject) SyncStatus(ctx context.Context, owner, repo string, issueNumber int, status string) error {
	defer p.invalidate()
	return p.ProjectService.SyncStatus(ctx, owner, repo, issueNumber, status)
}

func (p *invalidatingProject) MoveStatus(ctx context.Context, owner, repo string, issueNumber int, newStatus string) error {
	defer p.invalidate()
	return p.ProjectService.MoveStatus(ctx, owner, repo, issueNumber, newStatus)
}

func (p *invalidatingProject) SyncIteration(ctx context.Context, owner, repo string, issueNumber int, iteration string) error {
	defer p.invalidate()
	return p.ProjectService.SyncIteration(ctx, owner, repo, issueNumber, iteration)
}

func (p *invalidatingProject) SetSingleSelectField(ctx context.Context, itemID, fieldName, optionName string) error {
	defer p.invalidate()
	return p.ProjectService.SetSingleSelectField(ctx, itemID, fieldName, optionName)
}

func (p *invalidatingProject) SetNumberField(ctx context.Context, itemID, fieldName string, value float64) error {
	defer p.invalidate()
	return p.ProjectService.SetNumberField(ctx, itemID, fieldName, value)
}

func (p *invalidatingProject) SetTextField(ctx context.Context, itemID, fieldName, value string) error {
	defer p.invalidate()
	return p.ProjectService.SetTextField(ctx, itemID, fieldName, value)
}

func (p *invalidatingProject) SetTextFieldOptional(ctx context.Context, itemID, fieldName, value string) error {
	defer p.invalidate()
	return p.ProjectService.SetTextFieldOptional(ctx, itemID, fieldName, value)
}

func (p *invalidatingProject) SetDateField(ctx context.Context, itemID, fieldName, dateValue string) error {
	defer p.invalidate()
	return p.ProjectService.SetDateField(ctx, itemID, fieldName, dateValue)
}

func (p *invalidatingProject) SetDateFieldOptional(ctx context.Context, itemID, fieldName, dateValue string) error {
	defer p.invalidate()
	return p.ProjectService.SetDateFieldOptional(ctx, itemID, fieldName, dateValue)
}

func (p *invalidatingProject) SetIterationField(ctx context.Context, itemID, fieldName, iterationTitle string) error {
	defer p.invalidate()
	return p.ProjectService.SetIterationField(ctx, itemID, fieldName, iterationTitle)
}

func (p *invalidatingProject) SetFields(ctx context.Context, owner, repo string, issueNumber int, fields map[string]string) error {
	defer p.invalidate()
	return p.ProjectService.SetFields(ctx, owner, repo, issueNumber, fields)
}

func (p *invalidatingProject) SetHours(ctx context.Context, owner, repo string, issueNumber int, hours float64) error {
	defer p.invalidate()
	return p.ProjectService.SetHours(ctx, owner, repo, issueNumber, hours)
}

func (p *invalidatingProject) SetDateFieldByNumber(ctx context.Context, owner, repo string, issueNumber int, fieldName, dateValue string) error {
	defer p.invalidate()
	return p.ProjectService.SetDateFieldByNumber(ctx, owner, repo, issueNumber, fieldName, dateValue)
}

func (p *invalidatingProject) SetEstimateFromLabels(ctx context.Context, owner, repo string, issueNumber int, labels []string, mapping map[string]float64) error {
	defer p.invalidate()
	return p.ProjectService.SetEstimateFromLabels(ctx, owner, repo, issueNumber, labels, mapping)
}

func (p *invalidatingProject) AddBlockedByNumber(ctx context.Context, owner, repo string, blockedNumber, blockerNumber int) error {
	defer p.invalidate()
	return p.ProjectService.AddBlockedByNumber(ctx, owner, repo, blockedNumber, blockerNumber)
}

func (p *invalidatingProject) RemoveBlockedByNumber(ctx context.Context, owner, repo string, blockedNumber, blockerNumber int) error {
	defer p.invalidate()
	return p.ProjectService.RemoveBlockedByNumber(ctx, owner, repo, blockedNumber, blockerNumber)
}

func (p *invalidatingProject) UpdateEpicEstimates(ctx context.Context, owner, repo string, epicNumber int) (float64, error) {
	defer p.invalidate()
	return p.ProjectService.UpdateEpicEstimates(ctx, owner, repo, epicNumber)
}

func (p *invalidatingProject) EnsureFields(ctx context.Context, schema forgetypes.FieldSchema) (*forgetypes.EnsureFieldsResult, error) {
	defer p.invalidate()
	return p.ProjectService.EnsureFields(ctx, schema)
}

func (p *invalidatingProject) DriftFix(ctx context.Context) ([]forgetypes.FieldDrift, error) {
	defer p.invalidate()
	return p.ProjectService.DriftFix(ctx)
}
