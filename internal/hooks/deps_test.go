package hooks

import (
	"context"
	"fmt"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// mockFetcher implements IssueFetcher for testing.
type mockFetcher struct {
	issues map[string]*types.Issue
}

func (m *mockFetcher) GetIssue(_ context.Context, owner, repo string, number int) (*types.Issue, error) {
	key := fmt.Sprintf("%s/%s#%d", owner, repo, number)
	if issue, ok := m.issues[key]; ok {
		return issue, nil
	}
	return nil, fmt.Errorf("issue not found: %s", key)
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
