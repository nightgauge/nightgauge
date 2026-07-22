package github

import (
	"context"
	"fmt"
	"strings"
	"sync"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/pkg/types"
	"github.com/shurcooL/graphql"
)

// ProjectService manages GitHub Project V2 board operations:
// adding items, syncing field values, drift detection, and epic estimates.
type ProjectService struct {
	client        *Client
	owner         string
	ownerType     OwnerType
	projectNumber int
	mu            sync.Mutex

	// Cached after first introspection query
	projectID string
	fields    map[string]projectFieldInfo // field name → info
}

// projectFieldInfo holds cached field metadata.
type projectFieldInfo struct {
	ID      string
	Type    string            // "single_select", "number", "text", "iteration"
	Options map[string]string // option name → option ID (for single_select)
}

// NewProjectService creates a project service for the given owner and project number.
// ownerType distinguishes organizations ("org") from user accounts ("user").
func NewProjectService(client *Client, owner string, projectNumber int, ownerType ...OwnerType) *ProjectService {
	ot := OwnerTypeOrg
	if len(ownerType) > 0 {
		ot = ownerType[0]
	}
	return &ProjectService{
		client:        client,
		owner:         owner,
		ownerType:     ot,
		projectNumber: projectNumber,
		fields:        make(map[string]projectFieldInfo),
	}
}

// ensureFields fetches and caches all project field IDs and option mappings.
func (p *ProjectService) ensureFields(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.projectID != "" {
		return nil
	}

	vars := map[string]interface{}{
		"owner":         graphql.String(p.owner),
		"projectNumber": graphql.Int(p.projectNumber),
	}

	result, err := queryProjectFieldsFull(ctx, p.client, p.ownerType, vars)
	if err != nil {
		return fmt.Errorf("fetch project fields: %w", err)
	}

	p.projectID = string(result.ID)

	for _, f := range result.Fields {
		switch f.TypeName {
		case "ProjectV2SingleSelectField":
			info := projectFieldInfo{
				ID:      string(f.SingleSelect.ID),
				Type:    "single_select",
				Options: make(map[string]string),
			}
			for _, opt := range f.SingleSelect.Options {
				info.Options[string(opt.Name)] = string(opt.ID)
			}
			p.fields[string(f.SingleSelect.Name)] = info

		case "ProjectV2IterationField":
			info := projectFieldInfo{
				ID:      string(f.Iteration.ID),
				Type:    "iteration",
				Options: make(map[string]string),
			}
			for _, iter := range f.Iteration.Configuration.Iterations {
				info.Options[string(iter.Title)] = string(iter.ID)
			}
			p.fields[string(f.Iteration.Name)] = info

		case "ProjectV2Field":
			name := string(f.GenericField.Name)
			dataType := string(f.GenericField.DataType)
			info := projectFieldInfo{
				ID:   string(f.GenericField.ID),
				Type: strings.ToLower(dataType),
			}
			p.fields[name] = info
		}
	}

	return nil
}

// invalidateCache clears cached field data so next call re-fetches.
func (p *ProjectService) invalidateCache() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.projectID = ""
	p.fields = make(map[string]projectFieldInfo)
}

// AddItem adds an issue or PR to the project board by its node ID.
// Returns the project item ID.
func (p *ProjectService) AddItem(ctx context.Context, contentNodeID string) (string, error) {
	if err := p.ensureFields(ctx); err != nil {
		return "", err
	}

	var m addProjectItemMutation
	input := map[string]interface{}{
		"input": AddProjectV2ItemByIdInput{
			ProjectID: graphql.ID(p.projectID),
			ContentID: graphql.ID(contentNodeID),
		},
	}

	if err := p.client.mutate(ctx, &m, input); err != nil {
		return "", fmt.Errorf("add item to project: %w", err)
	}

	return string(m.AddProjectV2ItemById.Item.ID), nil
}

// AddIssueByNumber looks up an issue's node ID and adds it to the project board.
// Returns the project item ID.
func (p *ProjectService) AddIssueByNumber(ctx context.Context, owner, repo string, number int) (string, error) {
	issueSvc := NewIssueService(p.client)
	issue, err := issueSvc.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return "", fmt.Errorf("fetch issue #%d: %w", number, err)
	}

	itemID, err := p.AddItem(ctx, issue.NodeID)
	if err != nil {
		return "", err
	}

	// Sync labels to project fields
	if err := p.syncLabelsToFields(ctx, itemID, issue.Labels, owner, repo, number); err != nil {
		return itemID, fmt.Errorf("item added but field sync failed: %w", err)
	}

	return itemID, nil
}

// syncLabelsToFields maps issue labels to project field values.
// owner and repo are required to support the Estimate field lookup (no-overwrite check).
func (p *ProjectService) syncLabelsToFields(ctx context.Context, itemID string, labels []string, owner, repo string, issueNumber int) error {
	for _, label := range labels {
		parts := strings.SplitN(label, ":", 2)
		if len(parts) != 2 {
			continue
		}

		prefix, value := parts[0], parts[1]
		switch prefix {
		case "priority":
			if err := p.syncPriorityLabel(ctx, itemID, value); err != nil {
				return err
			}
		case "size":
			if err := p.SetSingleSelectField(ctx, itemID, "Size", value); err != nil {
				return err
			}
		case "status":
			mapped := mapStatusLabel(value)
			if err := p.SetSingleSelectField(ctx, itemID, "Status", mapped); err != nil {
				return err
			}
		}
	}

	// Set Estimate from size label (no-op if already set or no size label)
	if err := p.SetEstimateFromLabels(ctx, owner, repo, issueNumber, labels, nil); err != nil {
		// Non-fatal: Estimate field may not exist on all project boards
		_ = err
	}
	return nil
}

// syncPriorityLabel maps priority label values to project field option names.
func (p *ProjectService) syncPriorityLabel(ctx context.Context, itemID, value string) error {
	priorityMap := map[string]string{
		"critical": "P0",
		"high":     "P1",
		"medium":   "P2",
		"low":      "P3",
	}
	mapped, ok := priorityMap[value]
	if !ok {
		return nil
	}
	return p.SetSingleSelectField(ctx, itemID, "Priority", mapped)
}

// mapStatusLabel maps status label values to project board status names.
func mapStatusLabel(value string) string {
	statusMap := map[string]string{
		"backlog":     "Backlog",
		"ready":       "Ready",
		"blocked":     "Backlog",
		"needs-info":  "Backlog",
		"in-progress": "In progress",
		"in-review":   "In review",
		"done":        "Done",
	}
	if mapped, ok := statusMap[value]; ok {
		return mapped
	}
	return value
}

// SetSingleSelectField sets a single-select field value on a project item.
func (p *ProjectService) SetSingleSelectField(ctx context.Context, itemID, fieldName, optionName string) error {
	if err := p.ensureFields(ctx); err != nil {
		return err
	}

	field, ok := p.fields[fieldName]
	if !ok {
		return fmt.Errorf("field %q not found on project (available: %s)", fieldName, p.fieldNames())
	}
	if field.Type != "single_select" {
		return fmt.Errorf("field %q is type %s, not single_select", fieldName, field.Type)
	}

	optionID, ok := field.Options[optionName]
	if !ok {
		return fmt.Errorf("option %q not found for field %q (available: %s)", optionName, fieldName, p.optionNames(fieldName))
	}

	return p.updateSingleSelect(ctx, itemID, field.ID, optionID)
}

// SetNumberField sets a number field value on a project item.
func (p *ProjectService) SetNumberField(ctx context.Context, itemID, fieldName string, value float64) error {
	if err := p.ensureFields(ctx); err != nil {
		return err
	}

	field, ok := p.fields[fieldName]
	if !ok {
		return fmt.Errorf("field %q not found on project (available: %s)", fieldName, p.fieldNames())
	}

	return p.updateNumberField(ctx, itemID, field.ID, value)
}

// SetTextField sets a text field value on a project item.
func (p *ProjectService) SetTextField(ctx context.Context, itemID, fieldName, value string) error {
	if err := p.ensureFields(ctx); err != nil {
		return err
	}

	field, ok := p.fields[fieldName]
	if !ok {
		return fmt.Errorf("field %q not found on project (available: %s)", fieldName, p.fieldNames())
	}

	return p.updateTextField(ctx, itemID, field.ID, value)
}

// SetTextFieldOptional sets a text field value on a project item.
// Returns nil (without error) when the field does not exist on the board.
// Use this for optional fields such as "Pipeline Stage" that may not be
// configured on all project boards.
func (p *ProjectService) SetTextFieldOptional(ctx context.Context, itemID, fieldName, value string) error {
	if err := p.ensureFields(ctx); err != nil {
		return err
	}
	field, ok := p.fields[fieldName]
	if !ok {
		return nil // field not present — graceful degradation
	}
	return p.updateTextField(ctx, itemID, field.ID, value)
}

// SetDateField sets a date field value on a project item.
// dateValue must be ISO 8601 format (YYYY-MM-DD).
func (p *ProjectService) SetDateField(ctx context.Context, itemID, fieldName, dateValue string) error {
	if err := p.ensureFields(ctx); err != nil {
		return err
	}
	field, ok := p.fields[fieldName]
	if !ok {
		return fmt.Errorf("field %q not found on project (available: %s)", fieldName, p.fieldNames())
	}
	return p.updateDateField(ctx, itemID, field.ID, dateValue)
}

// SetDateFieldOptional sets a date field value, returning nil when the field
// does not exist on the board (graceful degradation for optional fields).
func (p *ProjectService) SetDateFieldOptional(ctx context.Context, itemID, fieldName, dateValue string) error {
	if err := p.ensureFields(ctx); err != nil {
		return err
	}
	field, ok := p.fields[fieldName]
	if !ok {
		return nil // field not present — skip silently
	}
	return p.updateDateField(ctx, itemID, field.ID, dateValue)
}

// SetIterationField sets an iteration field value on a project item.
func (p *ProjectService) SetIterationField(ctx context.Context, itemID, fieldName, iterationTitle string) error {
	if err := p.ensureFields(ctx); err != nil {
		return err
	}

	field, ok := p.fields[fieldName]
	if !ok {
		return fmt.Errorf("field %q not found on project (available: %s)", fieldName, p.fieldNames())
	}

	iterID, ok := field.Options[iterationTitle]
	if !ok {
		return fmt.Errorf("iteration %q not found for field %q (available: %s)", iterationTitle, fieldName, p.optionNames(fieldName))
	}

	return p.updateIterationField(ctx, itemID, field.ID, iterID)
}

// SyncStatus updates the Status field for an issue identified by number.
func (p *ProjectService) SyncStatus(ctx context.Context, owner, repo string, issueNumber int, status string) error {
	itemID, err := p.findItemID(ctx, owner, repo, issueNumber)
	if err != nil {
		return err
	}

	mapped := mapStatusLabel(status)
	return p.SetSingleSelectField(ctx, itemID, "Status", mapped)
}

// SyncIteration updates the Iteration field for an issue identified by number.
func (p *ProjectService) SyncIteration(ctx context.Context, owner, repo string, issueNumber int, iteration string) error {
	itemID, err := p.findItemID(ctx, owner, repo, issueNumber)
	if err != nil {
		return err
	}

	// Find iteration field — try "Iteration" and "Sprint" as common names
	for _, fieldName := range []string{"Iteration", "Sprint"} {
		if _, ok := p.fields[fieldName]; ok {
			return p.SetIterationField(ctx, itemID, fieldName, iteration)
		}
	}

	return fmt.Errorf("no Iteration or Sprint field found on project (available: %s)", p.fieldNames())
}

// SetHours sets the hours estimate field for an issue on the project board.
func (p *ProjectService) SetHours(ctx context.Context, owner, repo string, issueNumber int, hours float64) error {
	itemID, err := p.findItemID(ctx, owner, repo, issueNumber)
	if err != nil {
		return err
	}

	// Try common field names for hours
	for _, fieldName := range []string{"Hours", "Epic Hours", "Estimate"} {
		if _, ok := p.fields[fieldName]; ok {
			return p.SetNumberField(ctx, itemID, fieldName, hours)
		}
	}

	return fmt.Errorf("no Hours/Epic Hours/Estimate field found on project (available: %s)", p.fieldNames())
}

// SetDateFieldByNumber sets a date field on the project item for the given issue number.
// It resolves the item ID internally — use this from the CLI where only the issue number is known.
func (p *ProjectService) SetDateFieldByNumber(ctx context.Context, owner, repo string, issueNumber int, fieldName, dateValue string) error {
	itemID, err := p.findItemID(ctx, owner, repo, issueNumber)
	if err != nil {
		return err
	}
	return p.SetDateField(ctx, itemID, fieldName, dateValue)
}

// BulkAddResult is an alias for the forge-agnostic bulk-add summary.
type BulkAddResult = forgetypes.BulkAddResult

// BulkAddIssues adds all provided issues to the project board sequentially.
// Errors are accumulated — all issues are attempted even if some fail.
func (p *ProjectService) BulkAddIssues(ctx context.Context, owner, repo string, issues []types.Issue) BulkAddResult {
	result := BulkAddResult{Total: len(issues), Mode: "bulk"}
	for _, issue := range issues {
		if _, err := p.AddIssueByNumber(ctx, owner, repo, issue.Number); err != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("#%d: %s", issue.Number, err))
		} else {
			result.Added++
		}
	}
	return result
}

// UpdateEpicEstimates rolls up sub-issue size estimates for an epic.
// Returns the total estimated hours.
//
// Labels are now carried in SubIssueRef, so no additional network calls are
// needed beyond the single GetEpicProgressByNumber fetch.
func (p *ProjectService) UpdateEpicEstimates(ctx context.Context, owner, repo string, epicNumber int) (float64, error) {
	issueSvc := NewIssueService(p.client)
	epic, err := issueSvc.GetEpicProgressByNumber(ctx, owner, repo, epicNumber)
	if err != nil {
		return 0, fmt.Errorf("fetch epic: %w", err)
	}

	var totalHours float64
	for _, si := range epic.SubIssues {
		size := sizeFromLabels(si.Labels)
		totalHours += sizeToHours(size)
	}

	if err := p.SetHours(ctx, owner, repo, epicNumber, totalHours); err != nil {
		return totalHours, fmt.Errorf("set hours on epic: %w", err)
	}

	return totalHours, nil
}

// SetFields sets one or more single-select fields on a project item identified by issue number.
// fields is a map of field name → option name (e.g. {"Priority": "P0", "Size": "M", "Status": "Ready"}).
func (p *ProjectService) SetFields(ctx context.Context, owner, repo string, issueNumber int, fields map[string]string) error {
	itemID, err := p.findItemID(ctx, owner, repo, issueNumber)
	if err != nil {
		return err
	}

	for fieldName, optionName := range fields {
		if fieldName == "Status" {
			optionName = mapStatusLabel(optionName)
		}
		if err := p.SetSingleSelectField(ctx, itemID, fieldName, optionName); err != nil {
			return fmt.Errorf("set %s=%s on #%d: %w", fieldName, optionName, issueNumber, err)
		}
	}
	return nil
}

// AddBlockedByNumber adds a blocking relationship between two issues identified by number.
// blockedNumber is blocked by blockerNumber.
// Returns an error if the blocker is the parent epic of the blocked issue — this
// creates an unresolvable circular dependency and is always a bug.
func (p *ProjectService) AddBlockedByNumber(ctx context.Context, owner, repo string, blockedNumber, blockerNumber int) error {
	issueSvc := NewIssueService(p.client)

	blocked, err := issueSvc.GetIssue(ctx, owner, repo, blockedNumber)
	if err != nil {
		return fmt.Errorf("fetch blocked issue #%d: %w", blockedNumber, err)
	}
	blocker, err := issueSvc.GetIssue(ctx, owner, repo, blockerNumber)
	if err != nil {
		return fmt.Errorf("fetch blocker issue #%d: %w", blockerNumber, err)
	}

	// Guard: reject if the blocker is the parent epic of the blocked issue.
	// A sub-issue blocked by its own epic creates an unresolvable circular dependency.
	if blocked.ParentIssueNumber != 0 && blocker.Number == blocked.ParentIssueNumber {
		return fmt.Errorf(
			"circular dependency: cannot block #%d by its parent epic #%d",
			blockedNumber, blockerNumber,
		)
	}

	return issueSvc.AddBlockedBy(ctx, blocked.NodeID, blocker.NodeID)
}

// RemoveBlockedByNumber removes a blocking relationship between two issues identified by number.
func (p *ProjectService) RemoveBlockedByNumber(ctx context.Context, owner, repo string, blockedNumber, blockerNumber int) error {
	issueSvc := NewIssueService(p.client)

	blocked, err := issueSvc.GetIssue(ctx, owner, repo, blockedNumber)
	if err != nil {
		return fmt.Errorf("fetch blocked issue #%d: %w", blockedNumber, err)
	}
	blocker, err := issueSvc.GetIssue(ctx, owner, repo, blockerNumber)
	if err != nil {
		return fmt.Errorf("fetch blocker issue #%d: %w", blockerNumber, err)
	}

	return issueSvc.RemoveBlockedBy(ctx, blocked.NodeID, blocker.NodeID)
}

// MoveStatus transitions an issue's status with optional validation.
func (p *ProjectService) MoveStatus(ctx context.Context, owner, repo string, issueNumber int, newStatus string) error {
	mapped := mapStatusLabel(newStatus)
	return p.SyncStatus(ctx, owner, repo, issueNumber, mapped)
}

// FieldDrift is an alias for the forge-agnostic field-drift shape.
type FieldDrift = forgetypes.FieldDrift

// DriftCheck audits all project items for field value drift between labels and board fields.
func (p *ProjectService) DriftCheck(ctx context.Context) ([]FieldDrift, error) {
	if err := p.ensureFields(ctx); err != nil {
		return nil, err
	}

	boardSvc := NewBoardService(p.client, p.owner, p.projectNumber, p.ownerType)
	items, err := boardSvc.ListItems(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list board items: %w", err)
	}

	var drifts []FieldDrift

	for _, item := range items {
		expectedPriority := priorityFromLabels(item.Labels)
		expectedSize := sizeFromLabels(item.Labels)

		if expectedPriority != "" && item.Priority != expectedPriority {
			drifts = append(drifts, FieldDrift{
				IssueNumber: item.Number,
				Repo:        item.Repo,
				Title:       item.Title,
				FieldName:   "Priority",
				Expected:    string(expectedPriority),
				Actual:      string(item.Priority),
			})
		}

		if expectedSize != "" && item.Size != expectedSize {
			drifts = append(drifts, FieldDrift{
				IssueNumber: item.Number,
				Repo:        item.Repo,
				Title:       item.Title,
				FieldName:   "Size",
				Expected:    string(expectedSize),
				Actual:      string(item.Size),
			})
		}
	}

	return drifts, nil
}

// DriftFix detects and fixes all field drift. Returns the list of drifts that were corrected.
func (p *ProjectService) DriftFix(ctx context.Context) ([]FieldDrift, error) {
	drifts, err := p.DriftCheck(ctx)
	if err != nil {
		return nil, err
	}

	var fixed []FieldDrift
	for _, drift := range drifts {
		owner, repo := splitOwnerRepo(drift.Repo)
		itemID, findErr := p.findItemID(ctx, owner, repo, drift.IssueNumber)
		if findErr != nil {
			continue
		}

		setErr := p.SetSingleSelectField(ctx, itemID, drift.FieldName, drift.Expected)
		if setErr != nil {
			continue
		}
		fixed = append(fixed, drift)
	}

	return fixed, nil
}

// GetFields returns the cached field metadata (for debugging/introspection).
func (p *ProjectService) GetFields(ctx context.Context) (map[string]projectFieldInfo, error) {
	if err := p.ensureFields(ctx); err != nil {
		return nil, err
	}
	return p.fields, nil
}

// FieldInfo is an alias for the forge-agnostic project-field metadata.
type FieldInfo = forgetypes.FieldInfo

// FieldsSnapshot is an alias for the forge-agnostic deep-copy snapshot.
type FieldsSnapshot = forgetypes.FieldsSnapshot

// SnapshotFields returns a deep-copy of the cached project ID and field
// metadata, fetching it via ensureFields when the cache is cold. The returned
// snapshot is safe to mutate without affecting the underlying ProjectService.
func (p *ProjectService) SnapshotFields(ctx context.Context) (*FieldsSnapshot, error) {
	if err := p.ensureFields(ctx); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	snap := &FieldsSnapshot{
		ProjectID: p.projectID,
		Fields:    make(map[string]FieldInfo, len(p.fields)),
	}
	for name, info := range p.fields {
		opts := make(map[string]string, len(info.Options))
		for k, v := range info.Options {
			opts[k] = v
		}
		snap.Fields[name] = FieldInfo{
			ID:      info.ID,
			Type:    info.Type,
			Options: opts,
		}
	}
	return snap, nil
}

// findItemID finds the project item ID for a given issue number.
// Uses the issue's projectItems connection for O(1) lookup instead of
// paginating through all project items.
func (p *ProjectService) findItemID(ctx context.Context, owner, repo string, issueNumber int) (string, error) {
	if err := p.ensureFields(ctx); err != nil {
		return "", err
	}

	// Fast path: query the issue's projectItems connection directly.
	// This is a single API call regardless of how many items are on the board.
	var q issueProjectItemsQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": graphql.Int(issueNumber),
	}

	if err := p.client.query(ctx, &q, vars); err != nil {
		return "", fmt.Errorf("find project item for issue #%d: %w", issueNumber, err)
	}

	for _, item := range q.Repository.Issue.ProjectItems.Nodes {
		if int(item.Project.Number) == p.projectNumber {
			return string(item.ID), nil
		}
	}

	return "", fmt.Errorf("issue #%d (%s/%s) not found on project board %s/%d", issueNumber, owner, repo, p.owner, p.projectNumber)
}

// --- GraphQL mutation helpers ---

func (p *ProjectService) updateSingleSelect(ctx context.Context, itemID, fieldID, optionID string) error {
	optionStr := graphql.String(optionID)
	return p.updateField(ctx, itemID, fieldID, ProjectV2FieldValue{
		SingleSelectOptionID: &optionStr,
	})
}

func (p *ProjectService) updateNumberField(ctx context.Context, itemID, fieldID string, value float64) error {
	num := graphql.Float(value)
	return p.updateField(ctx, itemID, fieldID, ProjectV2FieldValue{
		Number: &num,
	})
}

func (p *ProjectService) updateTextField(ctx context.Context, itemID, fieldID, value string) error {
	text := graphql.String(value)
	return p.updateField(ctx, itemID, fieldID, ProjectV2FieldValue{
		Text: &text,
	})
}

func (p *ProjectService) updateIterationField(ctx context.Context, itemID, fieldID, iterationID string) error {
	iter := graphql.String(iterationID)
	return p.updateField(ctx, itemID, fieldID, ProjectV2FieldValue{
		IterationID: &iter,
	})
}

func (p *ProjectService) updateDateField(ctx context.Context, itemID, fieldID, dateValue string) error {
	d := graphql.String(dateValue)
	return p.updateField(ctx, itemID, fieldID, ProjectV2FieldValue{
		Date: &d,
	})
}

func (p *ProjectService) updateField(ctx context.Context, itemID, fieldID string, value ProjectV2FieldValue) error {
	var m updateProjectFieldMutation
	input := map[string]interface{}{
		"input": UpdateProjectV2ItemFieldValueInput{
			ProjectID: graphql.ID(p.projectID),
			ItemID:    graphql.ID(itemID),
			FieldID:   graphql.ID(fieldID),
			Value:     value,
		},
	}
	return p.client.mutate(ctx, &m, input)
}

// --- helpers ---

func (p *ProjectService) fieldNames() string {
	names := make([]string, 0, len(p.fields))
	for name := range p.fields {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

func (p *ProjectService) optionNames(fieldName string) string {
	field, ok := p.fields[fieldName]
	if !ok {
		return ""
	}
	names := make([]string, 0, len(field.Options))
	for name := range field.Options {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// --- Field Schema and EnsureFields ---

// FieldSchema is an alias for the forge-agnostic schema description.
type FieldSchema = forgetypes.FieldSchema

// SingleSelectFieldDef is an alias for the forge-agnostic single-select
// field definition.
type SingleSelectFieldDef = forgetypes.SingleSelectFieldDef

// SingleSelectOptionDef is an alias for the forge-agnostic option
// definition. Color is a forge-specific enum value (e.g. GitHub's
// ProjectV2SingleSelectFieldOptionColor).
type SingleSelectOptionDef = forgetypes.SingleSelectOptionDef

// EnsureFieldsResult is an alias for the forge-agnostic outcome shape.
type EnsureFieldsResult = forgetypes.EnsureFieldsResult

// DefaultFieldSchema returns the standard 6-field matrix for Nightgauge project boards.
func DefaultFieldSchema() FieldSchema {
	return FieldSchema{
		SingleSelectFields: []SingleSelectFieldDef{
			{Name: "Status", Options: []SingleSelectOptionDef{
				{Name: "Backlog", Color: "GREEN"},
				{Name: "Ready", Color: "BLUE"},
				{Name: "In progress", Color: "YELLOW"},
				{Name: "In review", Color: "PURPLE"},
				{Name: "Done", Color: "ORANGE"},
			}},
			{Name: "Priority", Options: []SingleSelectOptionDef{
				{Name: "P0", Color: "RED"},
				{Name: "P1", Color: "ORANGE"},
				{Name: "P2", Color: "YELLOW"},
				{Name: "P3", Color: "GREEN"},
			}},
			{Name: "Size", Options: []SingleSelectOptionDef{
				{Name: "XS", Color: "GREEN"},
				{Name: "S", Color: "PURPLE"},
				{Name: "M", Color: "RED"},
				{Name: "L", Color: "YELLOW"},
				{Name: "XL", Color: "PINK"},
			}},
		},
		DateFields:   []string{"Start date", "Target date"},
		NumberFields: []string{"Estimate"},
	}
}

// EnsureFields idempotently creates the required project board fields defined by schema.
//
// For each SINGLE_SELECT field:
//   - If absent: creates the field with all options in one mutation.
//   - If present but missing options: replaces the full option set via updateProjectV2Field.
//   - If present with all options: records as "already".
//
// For DATE and NUMBER fields, creates them if absent; records as "already" if present.
//
// After any mutation, invalidates the cache and re-fetches to populate FieldIDs.
// When no mutations are needed, populates FieldIDs from the initial snapshot.
func (p *ProjectService) EnsureFields(ctx context.Context, schema FieldSchema) (*EnsureFieldsResult, error) {
	if err := p.ensureFields(ctx); err != nil {
		return nil, fmt.Errorf("load project fields: %w", err)
	}

	// Snapshot current state under lock so we work from a stable copy.
	p.mu.Lock()
	projectID := p.projectID
	currentFields := make(map[string]projectFieldInfo, len(p.fields))
	for k, v := range p.fields {
		opts := make(map[string]string, len(v.Options))
		for ok, ov := range v.Options {
			opts[ok] = ov
		}
		currentFields[k] = projectFieldInfo{ID: v.ID, Type: v.Type, Options: opts}
	}
	p.mu.Unlock()

	result := &EnsureFieldsResult{
		Created:  []string{},
		Updated:  []string{},
		Already:  []string{},
		FieldIDs: make(map[string]string),
	}

	// Process SINGLE_SELECT fields.
	for _, fieldDef := range schema.SingleSelectFields {
		existing, exists := currentFields[fieldDef.Name]
		if !exists {
			if _, err := p.createField(ctx, projectID, "SINGLE_SELECT", fieldDef.Name, fieldDef.Options); err != nil {
				return nil, fmt.Errorf("create field %q: %w", fieldDef.Name, err)
			}
			result.Created = append(result.Created, fieldDef.Name)
			continue
		}

		// Determine which required options are missing.
		var missing []SingleSelectOptionDef
		for _, opt := range fieldDef.Options {
			if _, has := existing.Options[opt.Name]; !has {
				missing = append(missing, opt)
			}
		}

		if len(missing) == 0 {
			result.Already = append(result.Already, fieldDef.Name)
			continue
		}

		// updateProjectV2Field replaces the full option set — include existing options too.
		// We only know existing option names (not their original colors), so carry them
		// through with a neutral color; GitHub preserves option IDs across this mutation.
		allOpts := make([]SingleSelectOptionDef, 0, len(existing.Options)+len(missing))
		for name := range existing.Options {
			allOpts = append(allOpts, SingleSelectOptionDef{Name: name, Color: "GRAY"})
		}
		allOpts = append(allOpts, missing...)
		if err := p.replaceFieldOptions(ctx, projectID, existing.ID, allOpts); err != nil {
			return nil, fmt.Errorf("update field %q options: %w", fieldDef.Name, err)
		}
		result.Updated = append(result.Updated, fieldDef.Name)
	}

	// Process DATE fields.
	for _, fieldName := range schema.DateFields {
		if _, exists := currentFields[fieldName]; !exists {
			if _, err := p.createField(ctx, projectID, "DATE", fieldName, nil); err != nil {
				return nil, fmt.Errorf("create date field %q: %w", fieldName, err)
			}
			result.Created = append(result.Created, fieldName)
		} else {
			result.Already = append(result.Already, fieldName)
		}
	}

	// Process NUMBER fields.
	for _, fieldName := range schema.NumberFields {
		if _, exists := currentFields[fieldName]; !exists {
			if _, err := p.createField(ctx, projectID, "NUMBER", fieldName, nil); err != nil {
				return nil, fmt.Errorf("create number field %q: %w", fieldName, err)
			}
			result.Created = append(result.Created, fieldName)
		} else {
			result.Already = append(result.Already, fieldName)
		}
	}

	// Populate FieldIDs. When mutations ran, invalidate and re-fetch for accurate IDs.
	if len(result.Created) > 0 || len(result.Updated) > 0 {
		p.invalidateCache()
		if err := p.ensureFields(ctx); err != nil {
			return nil, fmt.Errorf("refresh field cache after mutations: %w", err)
		}
		p.mu.Lock()
		for name, info := range p.fields {
			result.FieldIDs[name] = info.ID
		}
		p.mu.Unlock()
	} else {
		for name, info := range currentFields {
			result.FieldIDs[name] = info.ID
		}
	}

	return result, nil
}

// createField creates a new project board field of the given data type.
// For SINGLE_SELECT fields, options are set inline in the creation mutation.
// Returns the new field's node ID.
func (p *ProjectService) createField(ctx context.Context, projectID, dataType, name string, options []SingleSelectOptionDef) (string, error) {
	input := CreateProjectV2FieldInput{
		ProjectID: graphql.ID(projectID),
		DataType:  graphql.String(dataType),
		Name:      graphql.String(name),
	}
	if len(options) > 0 {
		opts := make([]SingleSelectFieldOption, len(options))
		for i, o := range options {
			opts[i] = SingleSelectFieldOption{
				Name:        graphql.String(o.Name),
				Color:       graphql.String(o.Color),
				Description: graphql.String(""),
			}
		}
		input.SingleSelectOptions = opts
	}
	var m createProjectV2FieldMutation
	if err := p.client.mutate(ctx, &m, map[string]interface{}{"input": input}); err != nil {
		return "", err
	}
	return string(m.CreateProjectV2Field.ProjectV2Field.ID), nil
}

// replaceFieldOptions replaces all options on an existing SINGLE_SELECT field.
// GitHub's updateProjectV2Field replaces the full option set, so all existing
// options plus any new ones must be included together.
func (p *ProjectService) replaceFieldOptions(ctx context.Context, projectID, fieldID string, options []SingleSelectOptionDef) error {
	opts := make([]SingleSelectFieldOption, len(options))
	for i, o := range options {
		opts[i] = SingleSelectFieldOption{
			Name:        graphql.String(o.Name),
			Color:       graphql.String(o.Color),
			Description: graphql.String(""),
		}
	}
	var m updateProjectV2FieldMutation
	input := UpdateProjectV2FieldInput{
		ProjectID:           graphql.ID(projectID),
		FieldID:             graphql.ID(fieldID),
		SingleSelectOptions: opts,
	}
	return p.client.mutate(ctx, &m, map[string]interface{}{"input": input})
}

// ResolvedProject holds the result of a project number resolution.
type ResolvedProject struct {
	Number    int       `json:"number"`
	Owner     string    `json:"owner"`
	OwnerType OwnerType `json:"owner_type"`
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
}

// ResolveProject resolves a GitHub project by number against both org and user
// ownership with fixed precedence: org is tried first, user is the fallback.
// Returns the first match found or an error if the project is not accessible
// under either owner.
func ResolveProject(ctx context.Context, client *Client, owner string, projectNumber int) (*ResolvedProject, error) {
	vars := map[string]interface{}{
		"owner":         graphql.String(owner),
		"projectNumber": graphql.Int(projectNumber),
	}

	// Try org first (preferred — personal projects cannot be linked to org repos)
	var orgQ projectMetaQuery
	if err := client.query(ctx, &orgQ, vars); err == nil && string(orgQ.Organization.ProjectV2.ID) != "" {
		return &ResolvedProject{
			Number:    projectNumber,
			Owner:     owner,
			OwnerType: OwnerTypeOrg,
			ID:        string(orgQ.Organization.ProjectV2.ID),
			Title:     string(orgQ.Organization.ProjectV2.Title),
			URL:       string(orgQ.Organization.ProjectV2.URL),
		}, nil
	}

	// Fallback: try user ownership
	var userQ userProjectMetaQuery
	if err := client.query(ctx, &userQ, vars); err == nil && string(userQ.User.ProjectV2.ID) != "" {
		return &ResolvedProject{
			Number:    projectNumber,
			Owner:     owner,
			OwnerType: OwnerTypeUser,
			ID:        string(userQ.User.ProjectV2.ID),
			Title:     string(userQ.User.ProjectV2.Title),
			URL:       string(userQ.User.ProjectV2.URL),
		}, nil
	}

	return nil, fmt.Errorf("project #%d not found under org or user %q", projectNumber, owner)
}

func splitOwnerRepo(full string) (string, string) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 {
		return "", full
	}
	return parts[0], parts[1]
}

// sizeToHours converts a size label to estimated hours.
func sizeToHours(size types.Size) float64 {
	switch size {
	case types.SizeXS:
		return 0.5
	case types.SizeS:
		return 2
	case types.SizeM:
		return 8
	case types.SizeL:
		return 24
	case types.SizeXL:
		return 40
	default:
		return 4 // Default for unlabeled
	}
}

// sizeFromIssueLabels extracts size from a list of label strings.
func sizeFromIssueLabels(labels []string) types.Size {
	return sizeFromLabels(labels)
}

// DefaultSizeToEstimate returns the default size label → story-point mapping.
// XS=1, S=2, M=3, L=5, XL=8 (Fibonacci-ish).
func DefaultSizeToEstimate() map[string]float64 {
	return map[string]float64{
		"xs": 1,
		"s":  2,
		"m":  3,
		"l":  5,
		"xl": 8,
	}
}

// sizeToEstimate converts a size label to story points using the provided mapping.
// The lookup is case-insensitive. Returns 0 and false if no mapping exists for the size.
func sizeToEstimate(size types.Size, mapping map[string]float64) (float64, bool) {
	if size == "" || mapping == nil {
		return 0, false
	}
	key := strings.ToLower(string(size))
	pts, ok := mapping[key]
	return pts, ok
}

// getItemEstimate reads the current Estimate field value for the given issue.
// Returns 0 if the field is not set or not found.
func (p *ProjectService) getItemEstimate(ctx context.Context, owner, repo string, issueNumber int) (float64, error) {
	var q issueProjectItemWithFieldsQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": graphql.Int(issueNumber),
	}

	if err := p.client.query(ctx, &q, vars); err != nil {
		return 0, fmt.Errorf("read item fields for issue #%d: %w", issueNumber, err)
	}

	for _, item := range q.Repository.Issue.ProjectItems.Nodes {
		if int(item.Project.Number) != p.projectNumber {
			continue
		}
		for _, fv := range item.FieldValues.Nodes {
			if fv.TypeName != "ProjectV2ItemFieldNumberValue" {
				continue
			}
			name := string(fv.ProjectV2ItemFieldNumber.Field.ProjectV2Field.Name)
			if name == "Estimate" {
				return float64(fv.ProjectV2ItemFieldNumber.Number), nil
			}
		}
	}
	return 0, nil
}

// SetEstimateFromLabels sets the Estimate field on the project item for the
// given issue, derived from its Size label. It is a no-op when:
//   - No size: label exists on the issue
//   - The size has no entry in mapping
//   - The Estimate field is already set to a non-zero value
//
// This ensures manual estimates are never overwritten by the pipeline.
func (p *ProjectService) SetEstimateFromLabels(ctx context.Context, owner, repo string, issueNumber int, labels []string, mapping map[string]float64) error {
	if mapping == nil {
		mapping = DefaultSizeToEstimate()
	}

	size := sizeFromLabels(labels)
	pts, ok := sizeToEstimate(size, mapping)
	if !ok {
		return nil // No size label or no mapping entry — skip silently
	}

	// Read current estimate before writing
	current, err := p.getItemEstimate(ctx, owner, repo, issueNumber)
	if err != nil {
		return err
	}
	if current != 0 {
		return nil // Already set — do not overwrite
	}

	itemID, err := p.findItemID(ctx, owner, repo, issueNumber)
	if err != nil {
		return err
	}
	return p.SetNumberField(ctx, itemID, "Estimate", pts)
}
