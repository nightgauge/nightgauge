package hooks

import (
	"context"
	"errors"
	"fmt"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// mockFetcher implements IssueFetcher for testing. It serves its fixtures the
// way github.IssueService.GetIssueWithRelations does: a relationship list the
// read does not name comes back empty, and IsEpic still reports sub-issues.
type mockFetcher struct {
	issues map[string]*types.Issue
	// truncated names, per issue, the relationship connections whose later
	// pages cannot be read: a read naming any of them fails with
	// ErrConnectionTruncated, and a read naming none is unaffected.
	truncated map[string]gh.IssueRelations
	// reads records the relations each read named, keyed by issue.
	reads map[string][]gh.IssueRelations
}

func (m *mockFetcher) GetIssueWithRelations(_ context.Context, owner, repo string, number int, rels gh.IssueRelations) (*types.Issue, error) {
	key := fmt.Sprintf("%s/%s#%d", owner, repo, number)
	if m.reads == nil {
		m.reads = map[string][]gh.IssueRelations{}
	}
	m.reads[key] = append(m.reads[key], rels)
	if m.truncated[key]&rels != 0 {
		return nil, fmt.Errorf("fetch issue #%d: relationship of %s: %w: read page 2: status 502",
			number, key, gh.ErrConnectionTruncated)
	}
	issue, ok := m.issues[key]
	if !ok {
		return nil, fmt.Errorf("issue not found: %s", key)
	}
	out := *issue
	out.IsEpic = issue.IsEpic || len(issue.SubIssues) > 0
	if rels&gh.RelationSubIssues == 0 {
		out.SubIssues = nil
	}
	if rels&gh.RelationBlockedBy == 0 {
		out.BlockedBy = nil
	}
	if rels&gh.RelationBlocking == 0 {
		out.Blocking = nil
	}
	return &out, nil
}

func TestEvaluateIssueDeps_NoBlockers(t *testing.T) {
	mock := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#42": {
			Number:    42,
			BlockedBy: nil,
		},
	}}

	result, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.HasOpenDependencies {
		t.Error("expected HasOpenDependencies=false")
	}
	if result.OpenCount != 0 {
		t.Errorf("expected OpenCount=0, got %d", result.OpenCount)
	}
	if result.ShouldBlock {
		t.Error("expected ShouldBlock=false")
	}
	if result.IssueNumber != 42 {
		t.Errorf("expected IssueNumber=42, got %d", result.IssueNumber)
	}
}

func TestEvaluateIssueDeps_WithOpenBlockers(t *testing.T) {
	mock := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#1459": {
			Number: 1459,
			BlockedBy: []types.BlockingRef{
				{Number: 1457, Title: "PlatformApiClient", State: "OPEN", Repo: "nightgauge/nightgauge"},
			},
		},
	}}

	result, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 1459)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasOpenDependencies {
		t.Error("expected HasOpenDependencies=true")
	}
	if result.OpenCount != 1 {
		t.Errorf("expected OpenCount=1, got %d", result.OpenCount)
	}
	if !result.ShouldBlock {
		t.Error("expected ShouldBlock=true")
	}
	if result.OpenDependencies[0].Number != 1457 {
		t.Errorf("expected blocker #1457, got #%d", result.OpenDependencies[0].Number)
	}
}

func TestEvaluateIssueDeps_ClosedBlockersIgnored(t *testing.T) {
	mock := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#100": {
			Number: 100,
			BlockedBy: []types.BlockingRef{
				{Number: 99, Title: "Done task", State: "CLOSED", Repo: "nightgauge/nightgauge"},
				{Number: 98, Title: "Also done", State: "CLOSED", Repo: "nightgauge/nightgauge"},
			},
		},
	}}

	result, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.HasOpenDependencies {
		t.Error("expected HasOpenDependencies=false for all-closed blockers")
	}
	if result.OpenCount != 0 {
		t.Errorf("expected OpenCount=0, got %d", result.OpenCount)
	}
}

func TestEvaluateIssueDeps_MixedBlockers(t *testing.T) {
	mock := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#200": {
			Number: 200,
			BlockedBy: []types.BlockingRef{
				{Number: 199, Title: "Closed one", State: "CLOSED", Repo: "nightgauge/nightgauge"},
				{Number: 198, Title: "Still open", State: "OPEN", Repo: "nightgauge/nightgauge"},
				{Number: 197, Title: "Another open", State: "OPEN", Repo: "nightgauge/nightgauge"},
			},
		},
	}}

	result, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasOpenDependencies {
		t.Error("expected HasOpenDependencies=true")
	}
	if result.OpenCount != 2 {
		t.Errorf("expected OpenCount=2, got %d", result.OpenCount)
	}
}

func TestEvaluateIssueDeps_NotFound(t *testing.T) {
	mock := &mockFetcher{issues: map[string]*types.Issue{}}

	_, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 999)
	if err == nil {
		t.Error("expected error for non-existent issue")
	}
}

func TestExtractVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"git version 2.39.1", "2.39.1"},
		{"v18.17.0", "18.17.0"},
		{"node v20.10.0", "20.10.0"},
		{"npm 10.2.3", "10.2.3"},
		{"gh version 2.40.0 (2024-01-15)", "2.40.0"},
		{"no version here", ""},
	}

	for _, tt := range tests {
		got := extractVersion(tt.input)
		if got != tt.want {
			t.Errorf("extractVersion(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMeetsMinVersion(t *testing.T) {
	tests := []struct {
		version    string
		minVersion string
		want       bool
	}{
		{"2.39.1", "2.0", true},
		{"3.0.0", "2.0", true},
		{"1.9.0", "2.0", false},
		{"2.0.0", "2.0", true},
		{"18.17.0", "18.0", true},
		{"16.0.0", "18.0", false},
	}

	for _, tt := range tests {
		got := meetsMinVersion(tt.version, tt.minVersion)
		if got != tt.want {
			t.Errorf("meetsMinVersion(%q, %q) = %v, want %v", tt.version, tt.minVersion, got, tt.want)
		}
	}
}

func TestParseVersionParts(t *testing.T) {
	tests := []struct {
		input string
		want  []int
	}{
		{"2.39.1", []int{2, 39, 1}},
		{"18.17", []int{18, 17}},
		{"3", []int{3}},
		{"abc", nil},
	}

	for _, tt := range tests {
		got := parseVersionParts(tt.input)
		if tt.want == nil {
			if len(got) != 0 {
				t.Errorf("parseVersionParts(%q) = %v, want empty", tt.input, got)
			}
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("parseVersionParts(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Errorf("parseVersionParts(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestEvaluateVersionCheck(t *testing.T) {
	tests := []struct {
		plugin string
		skill  string
		wantOK bool
	}{
		{"1.0.0", "1.0.0", true},
		{"1.0.0", "1.0.1", false},
		{"", "1.0.0", true},
		{"1.0.0", "", true},
		{"", "", true},
	}

	for _, tt := range tests {
		result := EvaluateVersionCheck(tt.plugin, tt.skill)
		if result.OK != tt.wantOK {
			t.Errorf("EvaluateVersionCheck(%q, %q).OK = %v, want %v", tt.plugin, tt.skill, result.OK, tt.wantOK)
		}
	}
}

func TestEvaluateDeps(t *testing.T) {
	result := EvaluateDeps()
	// git should be available in any dev environment
	if len(result.Required) == 0 {
		t.Error("expected at least one required dependency")
	}

	// Find git in required
	found := false
	for _, dep := range result.Required {
		if dep.Name == "git" {
			found = true
			if !dep.Available {
				t.Error("git should be available")
			}
			if dep.Version == "" {
				t.Error("git version should not be empty")
			}
		}
	}
	if !found {
		t.Error("git not found in required deps")
	}
}

// --- Body-declared dependencies at pickup (#1492) -----------------------------

// TestEvaluateIssueDeps_BodyDeclaredSameRepo is the pickup half of #1492. The
// gate read only GitHub's native blockedBy relation, so an issue whose body
// said "Depends on: #1187" had dependencies.blockedBy written as [] in
// issue-<N>.json — and feature-planning was the first stage to notice the
// prerequisite, mid-plan, by reading the body itself.
func TestEvaluateIssueDeps_BodyDeclaredSameRepo(t *testing.T) {
	mock := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#1188": {
			Number: 1188,
			Body:   "## Goal\n\nRewrite the client.\n\nDepends on: #1187\n",
		},
		"nightgauge/nightgauge#1187": {
			Number: 1187,
			Title:  "Prerequisite",
			State:  "OPEN",
		},
	}}

	result, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 1188)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.ShouldBlock || result.OpenCount != 1 {
		t.Fatalf("expected the body-declared dependency to block, got %+v", result)
	}
	dep := result.OpenDependencies[0]
	if dep.Number != 1187 || dep.Repo != "nightgauge/nightgauge" {
		t.Errorf("dependency = %+v, want nightgauge/nightgauge#1187", dep)
	}
	if dep.Source != "body" || dep.SourceLine != "Depends on: #1187" {
		t.Errorf("dependency provenance = %q / %q — an operator must be told which "+
			"declaration to edit to clear the hold", dep.Source, dep.SourceLine)
	}
}

// TestEvaluateIssueDeps_BodyDeclaredClosedDoesNotBlock — a declaration that has
// already shipped is satisfied, exactly like a closed native blocker.
func TestEvaluateIssueDeps_BodyDeclaredClosedDoesNotBlock(t *testing.T) {
	mock := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#1188": {Number: 1188, Body: "Blocked by #1187"},
		"nightgauge/nightgauge#1187": {Number: 1187, State: "CLOSED"},
	}}

	result, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 1188)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ShouldBlock {
		t.Fatalf("a closed prerequisite must not hold pickup, got %+v", result)
	}
}

// TestEvaluateIssueDeps_BodyDeclaredNotDoubleCountedWithNative — the same
// dependency declared both ways is one dependency.
func TestEvaluateIssueDeps_BodyDeclaredNotDoubleCountedWithNative(t *testing.T) {
	mock := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#1188": {
			Number: 1188,
			Body:   "Depends on: #1187",
			BlockedBy: []types.BlockingRef{
				{Number: 1187, Title: "Prerequisite", State: "OPEN", Repo: "nightgauge/nightgauge"},
			},
		},
		"nightgauge/nightgauge#1187": {Number: 1187, State: "OPEN"},
	}}

	result, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 1188)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.OpenCount != 1 {
		t.Fatalf("expected 1 dependency, got %d: %+v", result.OpenCount, result.OpenDependencies)
	}
	if result.OpenDependencies[0].Source != "blockedBy" {
		t.Errorf("the native relation must win the dedup, got source %q", result.OpenDependencies[0].Source)
	}
}

// TestEvaluateIssueDeps_DependencyListsCannotFailTheGate — #10 depends in its
// body on OPEN #20, and none of #20's own relationship lists can be read to
// its end. The gate judges #20 by its state alone, so it must not read those
// lists: reading them failed the evaluation, and the skills treat a failed
// check-deps as "no open dependencies", so #10 was picked up while #20 was
// still open.
func TestEvaluateIssueDeps_DependencyListsCannotFailTheGate(t *testing.T) {
	mock := &mockFetcher{
		issues: map[string]*types.Issue{
			"nightgauge/nightgauge#10": {Number: 10, Body: "Depends on: #20"},
			"nightgauge/nightgauge#20": {Number: 20, Title: "Prerequisite", State: "OPEN"},
		},
		truncated: map[string]gh.IssueRelations{"nightgauge/nightgauge#20": gh.AllRelations},
	}

	result, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v — the dependency's own lists are not the gate's to read", err)
	}
	if !result.ShouldBlock || result.OpenCount != 1 || result.OpenDependencies[0].Number != 20 {
		t.Fatalf("result = %+v, want #10 held by its open body-declared dependency #20", result)
	}
	if got := mock.reads["nightgauge/nightgauge#20"]; len(got) != 1 || got[0] != gh.NoRelations {
		t.Errorf("reads of #20 named %v, want one read naming no relationships", got)
	}
}

// TestEvaluateIssueDeps_ReadsOnlyTheIssuesBlockedBy — the issue's sub-issues
// and the issues it blocks are not its dependencies, so a list of either that
// cannot be read whole must not fail the gate. Its blockedBy list is the one
// the gate evaluates and still reads whole.
func TestEvaluateIssueDeps_ReadsOnlyTheIssuesBlockedBy(t *testing.T) {
	mock := &mockFetcher{
		issues: map[string]*types.Issue{
			"nightgauge/nightgauge#10": {
				Number:    10,
				BlockedBy: []types.BlockingRef{{Number: 9, State: "OPEN", Repo: "nightgauge/nightgauge"}},
			},
		},
		truncated: map[string]gh.IssueRelations{
			"nightgauge/nightgauge#10": gh.RelationSubIssues | gh.RelationBlocking,
		},
	}

	result, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v — only the blockedBy list is the gate's to read", err)
	}
	if !result.ShouldBlock || result.OpenCount != 1 || result.OpenDependencies[0].Number != 9 {
		t.Fatalf("result = %+v, want #10 held by its open blocker #9", result)
	}
}

// TestEvaluateIssueDeps_TruncatedBlockedByIsAnError — a blockedBy list that
// cannot be read to its end is the gate's own input, and a short one could
// read as unblocked. The evaluation fails instead.
func TestEvaluateIssueDeps_TruncatedBlockedByIsAnError(t *testing.T) {
	mock := &mockFetcher{
		issues:    map[string]*types.Issue{"nightgauge/nightgauge#10": {Number: 10}},
		truncated: map[string]gh.IssueRelations{"nightgauge/nightgauge#10": gh.RelationBlockedBy},
	}

	result, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 10)
	if !errors.Is(err, gh.ErrConnectionTruncated) {
		t.Fatalf("err = %v (result %+v), want ErrConnectionTruncated", err, result)
	}
}

// TestEvaluateIssueDeps_UnresolvableBodyRefIsSkipped — prose can name a
// repository that does not exist. A permanent un-clearable hold on a typo is
// worse than the deferral it would buy, so the reference is skipped.
func TestEvaluateIssueDeps_UnresolvableBodyRefIsSkipped(t *testing.T) {
	mock := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#1188": {Number: 1188, Body: "Blocked by acme/nonexistent#9999"},
	}}

	result, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 1188)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ShouldBlock {
		t.Fatalf("an unfetchable prose reference must not create a permanent hold, got %+v", result)
	}
}

// TestEvaluateIssueDeps_ProseMentionIsNotADependency — the negative that keeps
// the fix from grounding the fleet.
func TestEvaluateIssueDeps_ProseMentionIsNotADependency(t *testing.T) {
	mock := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#1188": {Number: 1188, Body: "Follow-up to #1187, which shipped the parser."},
		"nightgauge/nightgauge#1187": {Number: 1187, State: "OPEN"},
	}}

	result, err := EvaluateIssueDeps(context.Background(), mock, "nightgauge", "nightgauge", 1188)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ShouldBlock {
		t.Fatalf("a prose mention became a blocker: %+v", result)
	}
}
