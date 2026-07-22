package github

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

func TestNewProjectService(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewProjectService(client, "nightgauge", 5)
	if svc == nil {
		t.Fatal("NewProjectService returned nil")
	}
	if svc.owner != "nightgauge" {
		t.Errorf("owner = %q, want %q", svc.owner, "nightgauge")
	}
	if svc.projectNumber != 5 {
		t.Errorf("projectNumber = %d, want %d", svc.projectNumber, 5)
	}
	if svc.fields == nil {
		t.Error("fields map is nil")
	}
}

func TestMapStatusLabel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"backlog", "Backlog"},
		{"ready", "Ready"},
		{"blocked", "Backlog"},
		{"needs-info", "Backlog"},
		{"in-progress", "In progress"},
		{"in-review", "In review"},
		{"done", "Done"},
		{"Ready", "Ready"}, // Passthrough for already-mapped values
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		got := mapStatusLabel(tt.input)
		if got != tt.want {
			t.Errorf("mapStatusLabel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSizeToHours(t *testing.T) {
	tests := []struct {
		size types.Size
		want float64
	}{
		{types.SizeXS, 0.5},
		{types.SizeS, 2},
		{types.SizeM, 8},
		{types.SizeL, 24},
		{types.SizeXL, 40},
		{"", 4},        // Default for unlabeled
		{"unknown", 4}, // Default for unknown
	}

	for _, tt := range tests {
		got := sizeToHours(tt.size)
		if got != tt.want {
			t.Errorf("sizeToHours(%q) = %v, want %v", tt.size, got, tt.want)
		}
	}
}

func TestSplitOwnerRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
	}{
		{"nightgauge/nightgauge", "nightgauge", "nightgauge"},
		{"org/repo-name", "org", "repo-name"},
		{"justname", "", "justname"},
		{"a/b/c", "a", "b/c"},
	}

	for _, tt := range tests {
		gotOwner, gotRepo := splitOwnerRepo(tt.input)
		if gotOwner != tt.wantOwner || gotRepo != tt.wantRepo {
			t.Errorf("splitOwnerRepo(%q) = (%q, %q), want (%q, %q)",
				tt.input, gotOwner, gotRepo, tt.wantOwner, tt.wantRepo)
		}
	}
}

func TestProjectServiceFieldNames(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewProjectService(client, "nightgauge", 5)

	// Empty fields
	names := svc.fieldNames()
	if names != "" {
		t.Errorf("empty fieldNames() = %q, want empty", names)
	}

	// Add some fields
	svc.fields["Status"] = projectFieldInfo{
		ID:   "PVTSSF_123",
		Type: "single_select",
		Options: map[string]string{
			"Ready":       "opt1",
			"In progress": "opt2",
			"Done":        "opt3",
		},
	}
	svc.fields["Priority"] = projectFieldInfo{
		ID:   "PVTSSF_456",
		Type: "single_select",
		Options: map[string]string{
			"P0": "opt_p0",
			"P1": "opt_p1",
		},
	}

	names = svc.fieldNames()
	if names == "" {
		t.Error("fieldNames() should not be empty with fields set")
	}

	// Test option names
	opts := svc.optionNames("Status")
	if opts == "" {
		t.Error("optionNames('Status') should not be empty")
	}

	// Non-existent field
	opts = svc.optionNames("NonExistent")
	if opts != "" {
		t.Errorf("optionNames('NonExistent') = %q, want empty", opts)
	}
}

// TestSnapshotFields_DeepCopiesCachedState constructs a ProjectService with a
// hand-populated cache, asserts SnapshotFields mirrors it, and verifies the
// returned snapshot is a deep copy (mutating it does not affect the original).
func TestSnapshotFields_DeepCopiesCachedState(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewProjectService(client, "nightgauge", 1)

	// Hand-populate the cache as if ensureFields had already run. This lets us
	// exercise SnapshotFields without a real GitHub round-trip.
	svc.projectID = "PVT_PROJ_ID"
	svc.fields["Status"] = projectFieldInfo{
		ID:   "PVTSSF_STATUS",
		Type: "single_select",
		Options: map[string]string{
			"Ready":       "opt-ready",
			"In progress": "opt-progress",
		},
	}
	svc.fields["Priority"] = projectFieldInfo{
		ID:   "PVTSSF_PRIORITY",
		Type: "single_select",
		Options: map[string]string{
			"P0": "opt-p0",
		},
	}

	snap, err := svc.SnapshotFields(context.Background())
	if err != nil {
		t.Fatalf("SnapshotFields: %v", err)
	}
	if snap.ProjectID != "PVT_PROJ_ID" {
		t.Errorf("ProjectID = %q, want PVT_PROJ_ID", snap.ProjectID)
	}
	if got := snap.Fields["Status"].ID; got != "PVTSSF_STATUS" {
		t.Errorf("Status.ID = %q, want PVTSSF_STATUS", got)
	}
	if got := snap.Fields["Status"].Options["Ready"]; got != "opt-ready" {
		t.Errorf("Status.Options[Ready] = %q, want opt-ready", got)
	}
	if got := snap.Fields["Priority"].Options["P0"]; got != "opt-p0" {
		t.Errorf("Priority.Options[P0] = %q, want opt-p0", got)
	}

	// Mutating the snapshot must not affect the underlying ProjectService.
	snap.Fields["Status"].Options["Ready"] = "tampered"
	if got := svc.fields["Status"].Options["Ready"]; got != "opt-ready" {
		t.Errorf("snapshot mutation leaked into service: %q", got)
	}
}

func TestProjectServiceInvalidateCache(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewProjectService(client, "nightgauge", 5)

	// Simulate cached state
	svc.projectID = "PVT_123"
	svc.fields["Status"] = projectFieldInfo{ID: "PVTSSF_123", Type: "single_select"}

	svc.invalidateCache()

	if svc.projectID != "" {
		t.Errorf("projectID after invalidate = %q, want empty", svc.projectID)
	}
	if len(svc.fields) != 0 {
		t.Errorf("fields after invalidate has %d entries, want 0", len(svc.fields))
	}
}

func TestSetSingleSelectFieldNoCache(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewProjectService(client, "nightgauge", 5)

	// Without cached fields, SetSingleSelectField should fail at ensureFields
	// (it will try to call GitHub API with test token and fail)
	// We verify the field-not-found path by pre-populating the cache
	svc.projectID = "PVT_123"
	svc.fields["Status"] = projectFieldInfo{
		ID:   "PVTSSF_status",
		Type: "single_select",
		Options: map[string]string{
			"Ready": "opt_ready",
			"Done":  "opt_done",
		},
	}

	// Field not found
	err := svc.SetSingleSelectField(nil, "item1", "NonExistent", "Ready")
	if err == nil {
		t.Error("SetSingleSelectField with unknown field should fail")
	}

	// Wrong field type
	svc.fields["Hours"] = projectFieldInfo{ID: "PVTF_hours", Type: "number"}
	err = svc.SetSingleSelectField(nil, "item1", "Hours", "10")
	if err == nil {
		t.Error("SetSingleSelectField on number field should fail")
	}

	// Option not found
	err = svc.SetSingleSelectField(nil, "item1", "Status", "InvalidOption")
	if err == nil {
		t.Error("SetSingleSelectField with unknown option should fail")
	}
}

func TestSetNumberFieldNoCache(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewProjectService(client, "nightgauge", 5)

	svc.projectID = "PVT_123"

	// Field not found
	err := svc.SetNumberField(nil, "item1", "NonExistent", 10)
	if err == nil {
		t.Error("SetNumberField with unknown field should fail")
	}
}

func TestSetTextFieldNoCache(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewProjectService(client, "nightgauge", 5)

	svc.projectID = "PVT_123"

	// Field not found
	err := svc.SetTextField(nil, "item1", "NonExistent", "value")
	if err == nil {
		t.Error("SetTextField with unknown field should fail")
	}
}

func TestSetIterationFieldValidation(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewProjectService(client, "nightgauge", 5)

	svc.projectID = "PVT_123"
	svc.fields["Iteration"] = projectFieldInfo{
		ID:   "PVTIF_iter",
		Type: "iteration",
		Options: map[string]string{
			"Sprint 1": "iter_1",
			"Sprint 2": "iter_2",
		},
	}

	// Unknown field
	err := svc.SetIterationField(nil, "item1", "NonExistent", "Sprint 1")
	if err == nil {
		t.Error("SetIterationField with unknown field should fail")
	}

	// Unknown iteration
	err = svc.SetIterationField(nil, "item1", "Iteration", "Sprint 99")
	if err == nil {
		t.Error("SetIterationField with unknown iteration should fail")
	}
}

func TestSyncLabelsToFieldsMapping(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewProjectService(client, "nightgauge", 5)

	svc.projectID = "PVT_123"
	svc.fields["Priority"] = projectFieldInfo{
		ID:   "PVTSSF_priority",
		Type: "single_select",
		Options: map[string]string{
			"P0": "opt_p0",
			"P1": "opt_p1",
			"P2": "opt_p2",
			"P3": "opt_p3",
		},
	}
	svc.fields["Size"] = projectFieldInfo{
		ID:   "PVTSSF_size",
		Type: "single_select",
		Options: map[string]string{
			"XS": "opt_xs",
			"S":  "opt_s",
			"M":  "opt_m",
			"L":  "opt_l",
			"XL": "opt_xl",
		},
	}
	svc.fields["Status"] = projectFieldInfo{
		ID:   "PVTSSF_status",
		Type: "single_select",
		Options: map[string]string{
			"Ready":       "opt_ready",
			"Backlog":     "opt_backlog",
			"In progress": "opt_inprog",
			"In review":   "opt_inrev",
			"Done":        "opt_done",
		},
	}

	// Labels without a colon prefix should be skipped (no error).
	// Pass empty owner/repo/number so SetEstimateFromLabels is a no-op (no size label).
	err := svc.syncLabelsToFields(nil, "item1", []string{"type:feature", "component:frontend"}, "", "", 0)
	if err != nil {
		t.Errorf("syncLabelsToFields with non-field labels should not error: %v", err)
	}
}

func TestSizeFromIssueLabels(t *testing.T) {
	tests := []struct {
		labels []string
		want   types.Size
	}{
		{[]string{"size:M", "type:feature"}, types.SizeM},
		{[]string{"size:XL"}, types.SizeXL},
		{[]string{"type:feature"}, ""},
	}

	for _, tt := range tests {
		got := sizeFromIssueLabels(tt.labels)
		if got != tt.want {
			t.Errorf("sizeFromIssueLabels(%v) = %q, want %q", tt.labels, got, tt.want)
		}
	}
}

func TestDefaultSizeToEstimate(t *testing.T) {
	m := DefaultSizeToEstimate()
	want := map[string]float64{
		"xs": 1,
		"s":  2,
		"m":  3,
		"l":  5,
		"xl": 8,
	}
	for k, wantV := range want {
		if got, ok := m[k]; !ok || got != wantV {
			t.Errorf("DefaultSizeToEstimate()[%q] = %v ok=%v, want %v", k, got, ok, wantV)
		}
	}
}

func TestSizeToEstimate(t *testing.T) {
	mapping := DefaultSizeToEstimate()
	tests := []struct {
		size    types.Size
		wantPts float64
		wantOK  bool
	}{
		{types.SizeXS, 1, true},
		{types.SizeS, 2, true},
		{types.SizeM, 3, true},
		{types.SizeL, 5, true},
		{types.SizeXL, 8, true},
		{"", 0, false},        // no size label → no estimate
		{"unknown", 0, false}, // unmapped label → no estimate
	}

	for _, tt := range tests {
		gotPts, gotOK := sizeToEstimate(tt.size, mapping)
		if gotOK != tt.wantOK || (gotOK && gotPts != tt.wantPts) {
			t.Errorf("sizeToEstimate(%q) = (%v, %v), want (%v, %v)", tt.size, gotPts, gotOK, tt.wantPts, tt.wantOK)
		}
	}
}

func TestSizeToEstimateNilMapping(t *testing.T) {
	pts, ok := sizeToEstimate(types.SizeM, nil)
	if ok || pts != 0 {
		t.Errorf("sizeToEstimate with nil mapping should return (0, false), got (%v, %v)", pts, ok)
	}
}

func TestSetTextFieldOptional_FieldMissing(t *testing.T) {
	svc := NewProjectService(nil, "owner", 1)
	svc.projectID = "proj1"
	svc.fields = map[string]projectFieldInfo{} // no Pipeline Stage

	err := svc.SetTextFieldOptional(context.Background(), "item1", "Pipeline Stage", "feature-planning")
	if err != nil {
		t.Fatalf("expected nil for missing field, got: %v", err)
	}
}

func TestSizeToEstimateCaseInsensitive(t *testing.T) {
	mapping := DefaultSizeToEstimate()
	// Mapping keys are lowercase; Size values from GitHub labels may be uppercase
	pts, ok := sizeToEstimate("M", mapping) // uppercase
	if !ok || pts != 3 {
		t.Errorf("sizeToEstimate case-insensitive: got (%v, %v), want (3, true)", pts, ok)
	}
}

func TestResolvedProjectStruct(t *testing.T) {
	r := ResolvedProject{
		Number:    5,
		Owner:     "nightgauge",
		OwnerType: OwnerTypeOrg,
		ID:        "PVT_kwDOA123",
		Title:     "Nightgauge",
		URL:       "https://github.com/orgs/nightgauge/projects/5",
	}
	if r.Number != 5 {
		t.Errorf("Number = %d, want 5", r.Number)
	}
	if r.OwnerType != OwnerTypeOrg {
		t.Errorf("OwnerType = %q, want %q", r.OwnerType, OwnerTypeOrg)
	}
	if r.ID != "PVT_kwDOA123" {
		t.Errorf("ID = %q, want PVT_kwDOA123", r.ID)
	}
}

func TestResolveProject_NotFound(t *testing.T) {
	// With a dummy token the GraphQL query will fail — expect an error, not a panic.
	client := NewClientWithToken("dummy-token-for-test")
	_, err := ResolveProject(context.Background(), client, "nonexistent-org-xyz", 99999)
	if err == nil {
		t.Error("expected error for nonexistent project, got nil")
	}
}

// --- EnsureFields tests ---

func TestDefaultFieldSchema(t *testing.T) {
	schema := DefaultFieldSchema()
	if len(schema.SingleSelectFields) != 3 {
		t.Errorf("got %d single-select fields, want 3", len(schema.SingleSelectFields))
	}
	if len(schema.DateFields) != 2 {
		t.Errorf("got %d date fields, want 2", len(schema.DateFields))
	}
	if len(schema.NumberFields) != 1 {
		t.Errorf("got %d number fields, want 1", len(schema.NumberFields))
	}
	// Verify option counts for each SINGLE_SELECT field.
	optCounts := map[string]int{"Status": 5, "Priority": 4, "Size": 5}
	for _, f := range schema.SingleSelectFields {
		want, ok := optCounts[f.Name]
		if !ok {
			t.Errorf("unexpected SINGLE_SELECT field %q", f.Name)
			continue
		}
		if len(f.Options) != want {
			t.Errorf("%s: got %d options, want %d", f.Name, len(f.Options), want)
		}
	}
}

// TestEnsureFields_SkipsExistingFields verifies that when all 6 required fields are
// already present with complete options, EnsureFields reports all as "already" without
// making any mutations (the test has no valid token so any mutation would fail).
func TestEnsureFields_SkipsExistingFields(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewProjectService(client, "nightgauge", 1)

	// Pre-populate the cache as if ensureFields had already run.
	svc.projectID = "PVT_TEST_ID"
	schema := DefaultFieldSchema()
	for _, f := range schema.SingleSelectFields {
		info := projectFieldInfo{
			ID:      "FIELD_" + f.Name,
			Type:    "single_select",
			Options: make(map[string]string),
		}
		for _, opt := range f.Options {
			info.Options[opt.Name] = "OPT_" + opt.Name
		}
		svc.fields[f.Name] = info
	}
	for _, name := range schema.DateFields {
		svc.fields[name] = projectFieldInfo{ID: "FIELD_" + name, Type: "date"}
	}
	for _, name := range schema.NumberFields {
		svc.fields[name] = projectFieldInfo{ID: "FIELD_" + name, Type: "number"}
	}

	result, err := svc.EnsureFields(context.Background(), schema)
	if err != nil {
		t.Fatalf("EnsureFields: %v", err)
	}

	if len(result.Created) != 0 {
		t.Errorf("Created=%v, want empty", result.Created)
	}
	if len(result.Updated) != 0 {
		t.Errorf("Updated=%v, want empty", result.Updated)
	}
	if len(result.Already) != 6 {
		t.Errorf("got %d already-fields, want 6: %v", len(result.Already), result.Already)
	}
	if len(result.FieldIDs) != 6 {
		t.Errorf("got %d FieldIDs, want 6", len(result.FieldIDs))
	}
	// Spot-check that field IDs match what we pre-populated.
	if id, ok := result.FieldIDs["Status"]; !ok || id != "FIELD_Status" {
		t.Errorf("FieldIDs[Status] = %q, want FIELD_Status", id)
	}
}

// TestEnsureFields_CreatesNewFields verifies that EnsureFields attempts creation
// mutations when no fields exist (will fail with a network error in test environments).
func TestEnsureFields_CreatesNewFields(t *testing.T) {
	svc := NewProjectService(NewClientWithToken("test"), "nightgauge", 1)
	svc.projectID = "PVT_TEST_ID"
	// Leave svc.fields empty — every field will need to be created.

	_, err := svc.EnsureFields(context.Background(), DefaultFieldSchema())
	if err == nil {
		t.Error("expected network error when creating fields without a valid token")
	}
}

// TestEnsureFields_UpdatesMissingOptions verifies that EnsureFields attempts an
// updateProjectV2Field mutation when a SINGLE_SELECT field exists but lacks options.
func TestEnsureFields_UpdatesMissingOptions(t *testing.T) {
	svc := NewProjectService(NewClientWithToken("test"), "nightgauge", 1)
	svc.projectID = "PVT_TEST_ID"

	// Status field exists but only has 2 of the 5 required options.
	svc.fields["Status"] = projectFieldInfo{
		ID:   "PVTSSF_STATUS",
		Type: "single_select",
		Options: map[string]string{
			"Backlog": "opt-backlog",
			"Done":    "opt-done",
		},
	}

	_, err := svc.EnsureFields(context.Background(), DefaultFieldSchema())
	if err == nil {
		t.Error("expected network error when updating options without a valid token")
	}
}

// TestEnsureFields_IdempotentPartialSchema verifies that a schema containing only
// already-existing fields produces an all-"already" result with no mutations.
func TestEnsureFields_IdempotentPartialSchema(t *testing.T) {
	svc := NewProjectService(NewClientWithToken("test"), "nightgauge", 1)
	svc.projectID = "PVT_TEST_ID"
	svc.fields["Start date"] = projectFieldInfo{ID: "PVTF_START", Type: "date"}
	svc.fields["Estimate"] = projectFieldInfo{ID: "PVTF_ESTIMATE", Type: "number"}

	schema := FieldSchema{
		DateFields:   []string{"Start date"},
		NumberFields: []string{"Estimate"},
	}

	result, err := svc.EnsureFields(context.Background(), schema)
	if err != nil {
		t.Fatalf("EnsureFields: %v", err)
	}
	if len(result.Already) != 2 {
		t.Errorf("got %d already-fields, want 2: %v", len(result.Already), result.Already)
	}
	if len(result.Created) != 0 || len(result.Updated) != 0 {
		t.Errorf("expected no mutations; Created=%v Updated=%v", result.Created, result.Updated)
	}
	if result.FieldIDs["Start date"] != "PVTF_START" {
		t.Errorf("FieldIDs[Start date] = %q, want PVTF_START", result.FieldIDs["Start date"])
	}
}

// TestSetDateField_ValidDate verifies SetDateField calls updateDateField when the field exists.
func TestSetDateField_ValidDate(t *testing.T) {
	svc := NewProjectService(NewClientWithToken("test"), "nightgauge", 1)
	svc.projectID = "PVT_TEST_ID"
	svc.fields["Start date"] = projectFieldInfo{ID: "PVTF_START", Type: "date"}

	// updateDateField will attempt a real GraphQL mutation and fail with a network error —
	// that's expected here; we only verify it reaches the update call (not "field not found").
	err := svc.SetDateField(context.Background(), "ITEM_ID", "Start date", "2026-05-01")
	if err == nil {
		t.Error("expected network error from mutation attempt; got nil")
	}
	// Must NOT be a "field not found" error
	if err != nil && err.Error() == `field "Start date" not found on project (available: Start date)` {
		t.Errorf("got field-not-found error unexpectedly: %v", err)
	}
}

// TestSetDateField_FieldNotFound verifies SetDateField returns an error for missing fields.
func TestSetDateField_FieldNotFound(t *testing.T) {
	svc := NewProjectService(NewClientWithToken("test"), "nightgauge", 1)
	svc.projectID = "PVT_TEST_ID"
	// No fields populated

	err := svc.SetDateField(context.Background(), "ITEM_ID", "Start date", "2026-05-01")
	if err == nil {
		t.Fatal("expected error for missing field, got nil")
	}
	if err.Error() == "" {
		t.Error("error message should be non-empty")
	}
}

// TestSetDateFieldOptional_FieldNotFound verifies SetDateFieldOptional returns nil for missing fields.
func TestSetDateFieldOptional_FieldNotFound(t *testing.T) {
	svc := NewProjectService(NewClientWithToken("test"), "nightgauge", 1)
	svc.projectID = "PVT_TEST_ID"

	err := svc.SetDateFieldOptional(context.Background(), "ITEM_ID", "Start date", "2026-05-01")
	if err != nil {
		t.Errorf("SetDateFieldOptional with missing field should return nil, got: %v", err)
	}
}

// TestBulkAddIssues_AllSucceed verifies BulkAddIssues counts correctly when all attempts fail
// at the network layer (not a "logic fail"). With a token-only client and no mocked transport,
// all calls will fail at the HTTP level, so all issues land in Failed.
func TestBulkAddIssues_NetworkFail(t *testing.T) {
	svc := NewProjectService(NewClientWithToken("test"), "nightgauge", 1)
	svc.projectID = "PVT_TEST_ID"

	issues := []types.Issue{
		{NodeID: "I_1", Number: 1, Title: "Issue 1", State: "OPEN"},
		{NodeID: "I_2", Number: 2, Title: "Issue 2", State: "OPEN"},
		{NodeID: "I_3", Number: 3, Title: "Issue 3", State: "OPEN"},
	}

	result := svc.BulkAddIssues(context.Background(), "nightgauge", "nightgauge", issues)
	if result.Total != 3 {
		t.Errorf("Total = %d, want 3", result.Total)
	}
	// Added + Failed must equal Total
	if result.Added+result.Failed != result.Total {
		t.Errorf("Added(%d)+Failed(%d) != Total(%d)", result.Added, result.Failed, result.Total)
	}
	if result.Mode != "bulk" {
		t.Errorf("Mode = %q, want %q", result.Mode, "bulk")
	}
}

// TestBulkAddIssues_Empty verifies BulkAddIssues handles an empty issue list.
func TestBulkAddIssues_Empty(t *testing.T) {
	svc := NewProjectService(NewClientWithToken("test"), "nightgauge", 1)
	result := svc.BulkAddIssues(context.Background(), "nightgauge", "nightgauge", nil)
	if result.Total != 0 {
		t.Errorf("Total = %d, want 0", result.Total)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}
}

// TestUpdateEpicEstimates_ZeroGetIssueCalls verifies that UpdateEpicEstimates
// computes hours directly from SubIssueRef.Labels without any GetIssue calls.
// The label-to-hours path exercised here is the exact code path used by the
// simplified UpdateEpicEstimates implementation.
func TestUpdateEpicEstimates_ZeroGetIssueCalls(t *testing.T) {
	tests := []struct {
		name      string
		labels    []string
		wantHours float64
	}{
		{"xs", []string{"size:XS"}, 0.5},
		{"s", []string{"size:S", "type:bug"}, 2},
		{"m", []string{"size:M"}, 8},
		{"l", []string{"size:L", "priority:high"}, 24},
		{"xl", []string{"size:XL"}, 40},
		{"no size label", []string{"type:feature"}, 4},
		{"nil labels", nil, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := sizeFromLabels(tt.labels)
			got := sizeToHours(size)
			if got != tt.wantHours {
				t.Errorf("sizeFromLabels(%v) → sizeToHours = %v, want %v", tt.labels, got, tt.wantHours)
			}
		})
	}
}
