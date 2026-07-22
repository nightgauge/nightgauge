package gitlab

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// BoardStatusStrategy enumerates the supported Status mapping modes for
// GitLab issue boards. See forge.Config.BoardStatusStrategy for the
// caller-facing rationale.
type BoardStatusStrategy string

const (
	StrategyLabelStatus BoardStatusStrategy = "label-status"
	StrategyStateOnly   BoardStatusStrategy = "state-only"
)

// ProjectService implements forge.ProjectService for GitLab. The
// implementation maps every project-board operation onto a GitLab REST call
// (or a small chain of them), with a per-field dispatch table that keeps
// the surface explicit.
//
// A single ProjectService instance is bound to a single GitLab project
// (via Owner/Repo) and a Status mapping strategy. Field caches (label
// existence, iteration list, milestone list) are populated lazily.
type ProjectService struct {
	client   *Client
	owner    string
	repo     string
	strategy BoardStatusStrategy
	boardID  int

	mu              sync.Mutex
	knownLabels     map[string]struct{} // <name> → exists; populated lazily
	iterationsCache []rawIteration
	milestonesCache []rawMilestone
}

// NewProjectService constructs a ProjectService bound to a client. The owner,
// repo, and strategy are configured by the ForgeAdapter from forge.Config.
// Empty strategy defaults to StrategyLabelStatus.
func NewProjectService(client *Client) *ProjectService {
	return NewProjectServiceFor(client, "", "", "", 0)
}

// NewProjectServiceFor builds a ProjectService bound to a specific
// owner/repo/strategy/board. boardID is the GitLab board number (zero means
// "no board bound").
func NewProjectServiceFor(client *Client, owner, repo string, strategy BoardStatusStrategy, boardID int) *ProjectService {
	if strategy == "" {
		strategy = StrategyLabelStatus
	}
	return &ProjectService{
		client:      client,
		owner:       owner,
		repo:        repo,
		strategy:    strategy,
		boardID:     boardID,
		knownLabels: make(map[string]struct{}),
	}
}

// resolveProject returns the (owner, repo) the service is bound to, or an
// error when neither was configured.
func (p *ProjectService) resolveProject(owner, repo string) (string, string, string, error) {
	o := owner
	r := repo
	if o == "" {
		o = p.owner
	}
	if r == "" {
		r = p.repo
	}
	if o == "" || r == "" {
		return "", "", "", fmt.Errorf("gitlab project: owner/repo not configured")
	}
	return o, r, projectPath(o, r), nil
}

// putIssue is the workhorse for every issue mutation (label updates,
// weight, health, iteration, milestone, state changes). The payload is
// any map[string]any per the GitLab REST contract.
func (p *ProjectService) putIssue(ctx context.Context, owner, repo string, iid int, payload map[string]any, op string) (*rawIssueList, error) {
	full := p.client.buildURL(
		fmt.Sprintf("/projects/%s/issues/%d", projectPath(owner, repo), iid), nil,
	)
	var raw rawIssueList
	if _, err := p.client.do(ctx, "PUT", full, payload, &raw, op); err != nil {
		return nil, err
	}
	return &raw, nil
}

// fetchIssue retrieves the current label set for an issue without issuing a
// second BoardService call. Used by every Status / scoped-label write so the
// existing labels can be filtered before re-applying.
func (p *ProjectService) fetchIssue(ctx context.Context, owner, repo string, iid int) (*rawIssueList, error) {
	full := p.client.buildURL(
		fmt.Sprintf("/projects/%s/issues/%d", projectPath(owner, repo), iid), nil,
	)
	var raw rawIssueList
	if _, err := p.client.do(ctx, "GET", full, nil, &raw, fmt.Sprintf("get issue #%d", iid)); err != nil {
		return nil, err
	}
	return &raw, nil
}

// stripScopedLabels removes any "<prefix>::*" labels from labels, returning
// a new slice. Used so a Status / Priority / Size write replaces existing
// scoped labels rather than accumulating them.
func stripScopedLabels(labels []string, prefix string) []string {
	want := prefix + "::"
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if !strings.HasPrefix(l, want) {
			out = append(out, l)
		}
	}
	return out
}

// labelColorFor produces a deterministic hex colour for a generated scoped
// label so repeated runs against the same project produce the same colour.
// GitLab requires a leading "#"; the caller adds it.
func labelColorFor(name string) string {
	sum := sha1.Sum([]byte(name))
	return "#" + hex.EncodeToString(sum[:3])
}

// ensureLabel idempotently creates a project label. 409 conflicts (label
// already exists) are mapped to nil so callers can treat the call as
// idempotent.
func (p *ProjectService) ensureLabel(ctx context.Context, owner, repo, name string) error {
	p.mu.Lock()
	if _, ok := p.knownLabels[name]; ok {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	full := p.client.buildURL(
		fmt.Sprintf("/projects/%s/labels", projectPath(owner, repo)), nil,
	)
	payload := map[string]any{
		"name":  name,
		"color": labelColorFor(name),
	}
	_, err := p.client.do(ctx, "POST", full, payload, nil, "create label")
	// HTTP 409 is reported as a non-sentinel error by mapStatus. Detect it
	// via the message; the alternative is plumbing a dedicated sentinel
	// which is more invasive than this issue's scope.
	if err != nil && !strings.Contains(err.Error(), "HTTP 409") {
		return fmt.Errorf("ensure label %q: %w", name, err)
	}

	p.mu.Lock()
	p.knownLabels[name] = struct{}{}
	p.mu.Unlock()
	return nil
}

// resolveIID translates an opaque project itemID (e.g. "gitlab:o/r#42") back
// into its (owner, repo, iid) parts. Falls back to the bound project when
// the itemID is just an integer (treated as iid) — the GitHub-side caller
// frequently passes the GraphQL node ID in this slot.
func (p *ProjectService) resolveIID(itemID string) (string, string, int, error) {
	if strings.HasPrefix(itemID, "gitlab:") {
		ref := strings.TrimPrefix(itemID, "gitlab:")
		owner, repo, iid, err := parseIssueRef(ref)
		if err != nil {
			return "", "", 0, fmt.Errorf("malformed gitlab itemID %q: %w", itemID, err)
		}
		return owner, repo, iid, nil
	}
	// Bare integer = iid in the bound project.
	if iid, err := strconv.Atoi(itemID); err == nil {
		owner, repo, _, err := p.resolveProject("", "")
		if err != nil {
			return "", "", 0, err
		}
		return owner, repo, iid, nil
	}
	return "", "", 0, fmt.Errorf("itemID %q must be 'gitlab:owner/repo#iid' or an iid", itemID)
}

// --- Item membership ---

// AddItem accepts an opaque content node ID. GitLab boards display every
// project issue automatically, so the only state to materialise is the
// default Status::Backlog scoped label. Returns a synthetic itemID of the
// form "gitlab:owner/repo#iid" so subsequent setters can recover the
// (owner, repo, iid) triple.
func (p *ProjectService) AddItem(ctx context.Context, contentNodeID string) (string, error) {
	owner, repo, iid, err := p.resolveIID(contentNodeID)
	if err != nil {
		return "", err
	}
	if err := p.applyStatusLabel(ctx, owner, repo, iid, "Backlog"); err != nil {
		return "", err
	}
	return fmt.Sprintf("gitlab:%s/%s#%d", owner, repo, iid), nil
}

// AddIssueByNumber resolves the bound project and ensures the issue lands
// in the default Backlog column. Returns the synthetic itemID format.
func (p *ProjectService) AddIssueByNumber(ctx context.Context, owner, repo string, number int) (string, error) {
	o, r, _, err := p.resolveProject(owner, repo)
	if err != nil {
		return "", err
	}
	if err := p.applyStatusLabel(ctx, o, r, number, "Backlog"); err != nil {
		return "", err
	}
	return fmt.Sprintf("gitlab:%s/%s#%d", o, r, number), nil
}

// BulkAddIssues mirrors the GitHub adapter — sequential per-issue calls,
// errors accumulated. GitLab has no batch endpoint here, but the call is
// short-circuited (no roundtrip) when an issue already carries a Status::*
// label.
func (p *ProjectService) BulkAddIssues(ctx context.Context, owner, repo string, issues []forgetypes.Issue) forgetypes.BulkAddResult {
	out := forgetypes.BulkAddResult{Total: len(issues), Mode: "bulk"}
	for _, iss := range issues {
		if _, err := p.AddIssueByNumber(ctx, owner, repo, iss.Number); err != nil {
			out.Failed++
			out.Errors = append(out.Errors, fmt.Sprintf("#%d: %s", iss.Number, err))
		} else {
			out.Added++
		}
	}
	return out
}

// --- Status routing ---

// applyStatusLabel writes a Status::<value> scoped label on an issue,
// stripping any existing Status::* labels first. When the strategy is
// state-only the function maps Done → close, anything else → reopen +
// clear Status::*, and rejects intermediate statuses that the state-only
// strategy cannot represent.
func (p *ProjectService) applyStatusLabel(ctx context.Context, owner, repo string, iid int, status string) error {
	switch p.strategy {
	case StrategyStateOnly:
		return p.applyStatusStateOnly(ctx, owner, repo, iid, status)
	case StrategyLabelStatus, "":
		return p.applyStatusScopedLabel(ctx, owner, repo, iid, status)
	default:
		return fmt.Errorf("unknown BoardStatusStrategy %q", p.strategy)
	}
}

// applyStatusScopedLabel is the default Status writer: replace Status::* with
// Status::<value>. Auto-creates the scoped label so first-time writes succeed
// without manual board setup.
func (p *ProjectService) applyStatusScopedLabel(ctx context.Context, owner, repo string, iid int, status string) error {
	if status == "" {
		return fmt.Errorf("applyStatusScopedLabel: empty status")
	}
	target := "Status::" + status
	if err := p.ensureLabel(ctx, owner, repo, target); err != nil {
		return err
	}

	cur, err := p.fetchIssue(ctx, owner, repo, iid)
	if err != nil {
		return err
	}
	labels := stripScopedLabels(cur.Labels, "Status")
	labels = append(labels, target)

	_, err = p.putIssue(ctx, owner, repo, iid, map[string]any{
		"labels": strings.Join(labels, ","),
	}, "set status")
	return err
}

// applyStatusStateOnly maps Done → close, anything else → reopen + clear
// Status::* labels. In-between states are rejected because the state-only
// strategy cannot represent them.
func (p *ProjectService) applyStatusStateOnly(ctx context.Context, owner, repo string, iid int, status string) error {
	cur, err := p.fetchIssue(ctx, owner, repo, iid)
	if err != nil {
		return err
	}
	labels := stripScopedLabels(cur.Labels, "Status")

	switch status {
	case "Done":
		_, err = p.putIssue(ctx, owner, repo, iid, map[string]any{
			"labels":      strings.Join(labels, ","),
			"state_event": "close",
		}, "close issue")
		return err
	case "Backlog", "Ready":
		_, err = p.putIssue(ctx, owner, repo, iid, map[string]any{
			"labels":      strings.Join(labels, ","),
			"state_event": "reopen",
		}, "reopen issue")
		return err
	}
	return fmt.Errorf("state-only strategy cannot represent status %q (use label-status)", status)
}

// SyncStatus updates the Status field for an issue identified by number.
func (p *ProjectService) SyncStatus(ctx context.Context, owner, repo string, issueNumber int, status string) error {
	o, r, _, err := p.resolveProject(owner, repo)
	if err != nil {
		return err
	}
	return p.applyStatusLabel(ctx, o, r, issueNumber, status)
}

// MoveStatus is an alias for SyncStatus on GitLab. The split exists on the
// GitHub adapter because Projects V2 reports Status as a board-column move;
// GitLab has no equivalent transition step.
func (p *ProjectService) MoveStatus(ctx context.Context, owner, repo string, issueNumber int, newStatus string) error {
	return p.SyncStatus(ctx, owner, repo, issueNumber, newStatus)
}

// SyncIteration sets the iteration for an issue. EE uses the native
// /iterations endpoint; CE falls back to project milestones.
func (p *ProjectService) SyncIteration(ctx context.Context, owner, repo string, issueNumber int, iteration string) error {
	o, r, _, err := p.resolveProject(owner, repo)
	if err != nil {
		return err
	}
	return p.applyIteration(ctx, o, r, issueNumber, iteration)
}

// applyIteration encapsulates the EE/CE branch. EE: resolve title to
// iteration_id; CE: resolve title to milestone_id (auto-creating the
// milestone when missing).
func (p *ProjectService) applyIteration(ctx context.Context, owner, repo string, iid int, title string) error {
	switch p.client.Edition(ctx) {
	case EditionEE:
		iterID, err := p.resolveIterationID(ctx, owner, title)
		if err != nil {
			return err
		}
		_, err = p.putIssue(ctx, owner, repo, iid, map[string]any{
			"iteration_id": iterID,
		}, "set iteration")
		return err
	case EditionCE, EditionUnknown:
		milID, err := p.resolveMilestoneID(ctx, owner, repo, title, true)
		if err != nil {
			return err
		}
		_, err = p.putIssue(ctx, owner, repo, iid, map[string]any{
			"milestone_id": milID,
		}, "set iteration (milestone fallback)")
		return err
	}
	return fmt.Errorf("unreachable: edition")
}

// resolveIterationID lists EE iterations at the group level and matches by
// title. Result list is cached for the lifetime of the service.
func (p *ProjectService) resolveIterationID(ctx context.Context, group, title string) (int64, error) {
	p.mu.Lock()
	cache := p.iterationsCache
	p.mu.Unlock()

	if cache == nil {
		full := p.client.buildURL(
			fmt.Sprintf("/groups/%s/iterations", url.PathEscape(group)), nil,
		)
		var page []rawIteration
		if _, err := p.client.do(ctx, "GET", full, nil, &page, "list iterations"); err != nil {
			return 0, asEditionError("list iterations", "iteration_id", err)
		}
		p.mu.Lock()
		p.iterationsCache = page
		cache = page
		p.mu.Unlock()
	}
	for _, it := range cache {
		if it.Title == title {
			return it.ID, nil
		}
	}
	return 0, fmt.Errorf("iteration %q not found in group %q", title, group)
}

// resolveMilestoneID lists project milestones and matches by title. When
// autoCreate is true and the milestone is missing, a new one is created
// with the given title and no dates.
func (p *ProjectService) resolveMilestoneID(ctx context.Context, owner, repo, title string, autoCreate bool) (int64, error) {
	p.mu.Lock()
	cache := p.milestonesCache
	p.mu.Unlock()

	if cache == nil {
		full := p.client.buildURL(
			fmt.Sprintf("/projects/%s/milestones", projectPath(owner, repo)), nil,
		)
		var page []rawMilestone
		if _, err := p.client.do(ctx, "GET", full, nil, &page, "list milestones"); err != nil {
			return 0, err
		}
		p.mu.Lock()
		p.milestonesCache = page
		cache = page
		p.mu.Unlock()
	}
	for _, m := range cache {
		if m.Title == title {
			return m.ID, nil
		}
	}
	if !autoCreate {
		return 0, fmt.Errorf("milestone %q not found", title)
	}
	full := p.client.buildURL(
		fmt.Sprintf("/projects/%s/milestones", projectPath(owner, repo)), nil,
	)
	var created rawMilestone
	if _, err := p.client.do(ctx, "POST", full, map[string]any{"title": title}, &created, "create milestone"); err != nil {
		return 0, err
	}
	p.mu.Lock()
	p.milestonesCache = append(p.milestonesCache, created)
	p.mu.Unlock()
	return created.ID, nil
}

// --- Single-field setters ---

// SetSingleSelectField dispatches per-field-name onto the right write path:
// Status / Priority / Size / Health are scoped-label fields; "Health" on EE
// also writes the native health_status field; everything else maps to a
// generic "<fieldName>::<optionName>" scoped label with auto-created label.
func (p *ProjectService) SetSingleSelectField(ctx context.Context, itemID, fieldName, optionName string) error {
	owner, repo, iid, err := p.resolveIID(itemID)
	if err != nil {
		return err
	}
	switch fieldName {
	case "Status":
		return p.applyStatusLabel(ctx, owner, repo, iid, optionName)
	case "Health":
		return p.applyHealth(ctx, owner, repo, iid, optionName)
	default:
		return p.applyScopedLabel(ctx, owner, repo, iid, fieldName, optionName)
	}
}

// applyHealth writes the GitLab native health_status when the instance is
// EE; on CE it falls back to a Health::<value> scoped label so callers
// still see a round-trippable value.
func (p *ProjectService) applyHealth(ctx context.Context, owner, repo string, iid int, value string) error {
	// Translate GitHub's UI labels onto GitLab's enum values.
	hs := forgetypes.MapHealthFromGitHub(value)
	if hs == "" {
		// Allow callers to pass the GitLab-native enum directly.
		hs = forgetypes.HealthStatus(strings.ToLower(strings.ReplaceAll(value, " ", "_")))
	}
	if p.client.Edition(ctx) == EditionEE {
		if _, err := p.putIssue(ctx, owner, repo, iid, map[string]any{
			"health_status": string(hs),
		}, "set health"); err != nil {
			if isHealthCEError(err) {
				return asEditionError("set health", "health_status", err)
			}
			return err
		}
		return nil
	}
	// CE: scoped-label fallback so reads round-trip.
	return p.applyScopedLabel(ctx, owner, repo, iid, "Health", value)
}

// applyScopedLabel writes the generic "<fieldName>::<optionName>" scoped
// label, replacing any existing "<fieldName>::*" labels on the issue.
func (p *ProjectService) applyScopedLabel(ctx context.Context, owner, repo string, iid int, fieldName, optionName string) error {
	if fieldName == "" || optionName == "" {
		return fmt.Errorf("applyScopedLabel: empty field or option")
	}
	target := fieldName + "::" + optionName
	if err := p.ensureLabel(ctx, owner, repo, target); err != nil {
		return err
	}
	cur, err := p.fetchIssue(ctx, owner, repo, iid)
	if err != nil {
		return err
	}
	labels := stripScopedLabels(cur.Labels, fieldName)
	labels = append(labels, target)
	_, err = p.putIssue(ctx, owner, repo, iid, map[string]any{
		"labels": strings.Join(labels, ","),
	}, "set scoped label "+fieldName)
	return err
}

// SetNumberField writes the native weight on EE for the "Weight" field;
// other fields map to an "<field>::<value>" scoped-label fallback.
func (p *ProjectService) SetNumberField(ctx context.Context, itemID, fieldName string, value float64) error {
	owner, repo, iid, err := p.resolveIID(itemID)
	if err != nil {
		return err
	}
	if fieldName == "Weight" {
		if p.client.Edition(ctx) != EditionEE {
			return asEditionError("set weight", "weight", nil)
		}
		_, err := p.putIssue(ctx, owner, repo, iid, map[string]any{
			"weight": int(value),
		}, "set weight")
		if err != nil && isWeightCEError(err) {
			return asEditionError("set weight", "weight", err)
		}
		return err
	}
	// Generic numeric → scoped-label encoding (so reads still parse).
	return p.applyScopedLabel(ctx, owner, repo, iid, fieldName, strconv.FormatFloat(value, 'f', -1, 64))
}

// SetTextField encodes the value as a scoped label "<fieldName>::<value>".
// Text fields with embedded "::" sequences are rejected to keep the
// scoped-label encoding round-trippable.
func (p *ProjectService) SetTextField(ctx context.Context, itemID, fieldName, value string) error {
	if strings.Contains(value, "::") {
		return fmt.Errorf("text field %q contains '::' sequence; scoped-label encoding requires a clean value", fieldName)
	}
	owner, repo, iid, err := p.resolveIID(itemID)
	if err != nil {
		return err
	}
	return p.applyScopedLabel(ctx, owner, repo, iid, fieldName, value)
}

// SetTextFieldOptional is the no-op-on-missing variant of SetTextField. On
// GitLab "missing field" is impossible (any scoped label is auto-created),
// so this is identical to SetTextField.
func (p *ProjectService) SetTextFieldOptional(ctx context.Context, itemID, fieldName, value string) error {
	return p.SetTextField(ctx, itemID, fieldName, value)
}

// SetDateField encodes the date as a "<fieldName>::<YYYY-MM-DD>" scoped
// label. GitLab issues do not have arbitrary date fields outside of due_date,
// so the scoped-label encoding is the canonical fallback.
func (p *ProjectService) SetDateField(ctx context.Context, itemID, fieldName, dateValue string) error {
	owner, repo, iid, err := p.resolveIID(itemID)
	if err != nil {
		return err
	}
	if fieldName == "Target date" || fieldName == "Due date" {
		_, err := p.putIssue(ctx, owner, repo, iid, map[string]any{
			"due_date": dateValue,
		}, "set due date")
		return err
	}
	return p.applyScopedLabel(ctx, owner, repo, iid, fieldName, dateValue)
}

// SetDateFieldOptional is identical to SetDateField on GitLab — there is no
// "field is missing" case for label-encoded fields.
func (p *ProjectService) SetDateFieldOptional(ctx context.Context, itemID, fieldName, dateValue string) error {
	return p.SetDateField(ctx, itemID, fieldName, dateValue)
}

// SetIterationField bridges the GitHub-shaped iterationTitle parameter onto
// the EE iteration_id / CE milestone_id fallback chain.
func (p *ProjectService) SetIterationField(ctx context.Context, itemID, fieldName, iterationTitle string) error {
	owner, repo, iid, err := p.resolveIID(itemID)
	if err != nil {
		return err
	}
	return p.applyIteration(ctx, owner, repo, iid, iterationTitle)
}

// --- Number-keyed convenience setters ---

// SetFields applies a batch of single-select-style field writes against an
// issue identified by number.
func (p *ProjectService) SetFields(ctx context.Context, owner, repo string, issueNumber int, fields map[string]string) error {
	o, r, _, err := p.resolveProject(owner, repo)
	if err != nil {
		return err
	}
	itemID := fmt.Sprintf("gitlab:%s/%s#%d", o, r, issueNumber)
	for name, value := range fields {
		if err := p.SetSingleSelectField(ctx, itemID, name, value); err != nil {
			return fmt.Errorf("set %s=%s on #%d: %w", name, value, issueNumber, err)
		}
	}
	return nil
}

// SetHours maps to weight (EE) or an Estimate::<n> scoped label (CE).
func (p *ProjectService) SetHours(ctx context.Context, owner, repo string, issueNumber int, hours float64) error {
	o, r, _, err := p.resolveProject(owner, repo)
	if err != nil {
		return err
	}
	itemID := fmt.Sprintf("gitlab:%s/%s#%d", o, r, issueNumber)
	if p.client.Edition(ctx) == EditionEE {
		return p.SetNumberField(ctx, itemID, "Weight", hours)
	}
	return p.applyScopedLabel(ctx, o, r, issueNumber, "Estimate", strconv.FormatFloat(hours, 'f', -1, 64))
}

// SetDateFieldByNumber resolves the issue (no introspection needed —
// GitLab uses iid as the natural key) and dispatches to SetDateField.
func (p *ProjectService) SetDateFieldByNumber(ctx context.Context, owner, repo string, issueNumber int, fieldName, dateValue string) error {
	o, r, _, err := p.resolveProject(owner, repo)
	if err != nil {
		return err
	}
	itemID := fmt.Sprintf("gitlab:%s/%s#%d", o, r, issueNumber)
	return p.SetDateField(ctx, itemID, fieldName, dateValue)
}

// SetEstimateFromLabels translates a size label into the project's estimate
// field (weight on EE, Estimate scoped label on CE).
func (p *ProjectService) SetEstimateFromLabels(ctx context.Context, owner, repo string, issueNumber int, labels []string, mapping map[string]float64) error {
	if mapping == nil {
		mapping = map[string]float64{
			"size:XS": 1, "size:S": 2, "size:M": 4, "size:L": 8, "size:XL": 16,
		}
	}
	for _, l := range labels {
		if hours, ok := mapping[l]; ok {
			return p.SetHours(ctx, owner, repo, issueNumber, hours)
		}
	}
	return nil
}

// --- Schema management ---

// EnsureFields walks the requested SingleSelectFields and ensures each
// "<field>::<option>" scoped label exists on the project. Date / number
// fields have no GitLab-side schema to maintain.
func (p *ProjectService) EnsureFields(ctx context.Context, schema forgetypes.FieldSchema) (*forgetypes.EnsureFieldsResult, error) {
	owner, repo, _, err := p.resolveProject("", "")
	if err != nil {
		return nil, err
	}
	out := &forgetypes.EnsureFieldsResult{
		FieldIDs: make(map[string]string),
	}
	existing, err := p.listProjectLabels(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	for _, ssf := range schema.SingleSelectFields {
		for _, opt := range ssf.Options {
			name := ssf.Name + "::" + opt.Name
			if _, ok := existing[name]; ok {
				out.Already = append(out.Already, name)
				continue
			}
			if err := p.ensureLabel(ctx, owner, repo, name); err != nil {
				return nil, err
			}
			out.Created = append(out.Created, name)
			existing[name] = struct{}{}
		}
	}
	return out, nil
}

// listProjectLabels fetches the full label set for the bound project. The
// result is cached in p.knownLabels so subsequent ensureLabel calls
// short-circuit.
func (p *ProjectService) listProjectLabels(ctx context.Context, owner, repo string) (map[string]struct{}, error) {
	q := url.Values{}
	q.Set("per_page", "100")
	full := p.client.buildURL(
		fmt.Sprintf("/projects/%s/labels", projectPath(owner, repo)), q,
	)
	type rawLabel struct {
		Name string `json:"name"`
	}
	known := make(map[string]struct{})
	for full != "" {
		var page []rawLabel
		resp, err := p.client.do(ctx, "GET", full, nil, &page, "list labels")
		if err != nil {
			return nil, err
		}
		for _, l := range page {
			known[l.Name] = struct{}{}
		}
		links := parseLinkHeader(resp.Header.Get("Link"))
		if links.Next == nil {
			break
		}
		full = links.Next.String()
	}
	p.mu.Lock()
	for k := range known {
		p.knownLabels[k] = struct{}{}
	}
	p.mu.Unlock()
	return known, nil
}

// DriftCheck audits the project for Priority/Size scoped-label drift between
// the legacy "size:M" / "priority:high" labels and the typed Priority::P0 /
// Size::M scoped labels. Mirrors the GitHub adapter's drift contract.
func (p *ProjectService) DriftCheck(ctx context.Context) ([]forgetypes.FieldDrift, error) {
	owner, repo, _, err := p.resolveProject("", "")
	if err != nil {
		return nil, err
	}
	board := NewBoardServiceFor(p.client, owner, repo)
	items, err := board.ListItems(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list board items: %w", err)
	}

	var drifts []forgetypes.FieldDrift
	for _, item := range items {
		expectedPriority := priorityFromLabels(item.Labels)
		expectedSize := sizeFromLabels(item.Labels)

		if expectedPriority != "" && item.Priority != expectedPriority {
			drifts = append(drifts, forgetypes.FieldDrift{
				IssueNumber: item.Number, Repo: item.Repo, Title: item.Title,
				FieldName: "Priority",
				Expected:  string(expectedPriority),
				Actual:    string(item.Priority),
			})
		}
		if expectedSize != "" && item.Size != expectedSize {
			drifts = append(drifts, forgetypes.FieldDrift{
				IssueNumber: item.Number, Repo: item.Repo, Title: item.Title,
				FieldName: "Size",
				Expected:  string(expectedSize),
				Actual:    string(item.Size),
			})
		}
	}
	return drifts, nil
}

// DriftFix detects drift and re-applies the expected scoped label.
func (p *ProjectService) DriftFix(ctx context.Context) ([]forgetypes.FieldDrift, error) {
	drifts, err := p.DriftCheck(ctx)
	if err != nil {
		return nil, err
	}
	owner, repo, _, err := p.resolveProject("", "")
	if err != nil {
		return nil, err
	}
	var fixed []forgetypes.FieldDrift
	for _, d := range drifts {
		if err := p.applyScopedLabel(ctx, owner, repo, d.IssueNumber, d.FieldName, d.Expected); err != nil {
			continue
		}
		fixed = append(fixed, d)
	}
	return fixed, nil
}

// SnapshotFields returns a virtual FieldsSnapshot built from the project's
// configured FieldSchema. GitLab has no project-level field metadata to
// introspect, so the snapshot reflects the schema the caller requested via
// EnsureFields rather than a server-side description.
func (p *ProjectService) SnapshotFields(ctx context.Context) (*forgetypes.FieldsSnapshot, error) {
	owner, repo, _, err := p.resolveProject("", "")
	if err != nil {
		return nil, err
	}
	known, err := p.listProjectLabels(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	snap := &forgetypes.FieldsSnapshot{
		ProjectID: strconv.Itoa(p.boardID),
		Fields:    make(map[string]forgetypes.FieldInfo),
	}
	// Group "<field>::<option>" labels by their prefix to reconstruct the
	// virtual single-select schema.
	groups := map[string]map[string]string{}
	for name := range known {
		if i := strings.Index(name, "::"); i > 0 {
			prefix := name[:i]
			value := name[i+2:]
			if groups[prefix] == nil {
				groups[prefix] = make(map[string]string)
			}
			groups[prefix][value] = name
		}
	}
	for prefix, options := range groups {
		snap.Fields[prefix] = forgetypes.FieldInfo{
			ID:      prefix,
			Type:    "single_select",
			Options: options,
		}
	}
	return snap, nil
}

// --- Defer-to-3358 stubs (kept here so the package compiles end-to-end) ---

// AddBlockedByNumber is intentionally not implemented in this adapter — the
// blocking-link surface lands in #3358 alongside sub-issue support. Returns
// ErrUnsupported with the tracking issue so callers can branch.
func (p *ProjectService) AddBlockedByNumber(ctx context.Context, owner, repo string, blockedNumber, blockerNumber int) error {
	return unsupported("ProjectService.AddBlockedByNumber", "#3358")
}

// RemoveBlockedByNumber is the inverse stub; lands with #3358.
func (p *ProjectService) RemoveBlockedByNumber(ctx context.Context, owner, repo string, blockedNumber, blockerNumber int) error {
	return unsupported("ProjectService.RemoveBlockedByNumber", "#3358")
}

// UpdateEpicEstimates rolls up sub-issue weights for an epic. Sub-issue
// resolution depends on #3358's epic-link support, so this stays as a stub
// here.
func (p *ProjectService) UpdateEpicEstimates(ctx context.Context, owner, repo string, epicNumber int) (float64, error) {
	return 0, unsupported("ProjectService.UpdateEpicEstimates", "#3358")
}

// --- CE/EE error classifiers ---

// isWeightCEError detects the CE rejection of the weight field. Conservative
// match — GitLab CE returns 400 with a body that names the field.
func isWeightCEError(err error) bool {
	if err == nil {
		return false
	}
	// Even on EE, weight may be rejected when the project disables the
	// feature; treat any 400 mentioning "weight" as a possible CE-equivalent.
	msg := err.Error()
	if errors.Is(err, forge.ErrUnsupportedOnEdition) {
		return true
	}
	return strings.Contains(msg, "HTTP 400") &&
		strings.Contains(strings.ToLower(msg), "weight")
}

// isHealthCEError detects the CE rejection of the health_status field.
func isHealthCEError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, forge.ErrUnsupportedOnEdition) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 400") &&
		strings.Contains(strings.ToLower(msg), "health_status")
}
