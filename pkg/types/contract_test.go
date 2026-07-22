package types_test

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/pkg/types"
)

const fixturesDir = "../../tests/fixtures/contracts"

// structJSONKeys extracts all JSON field names from a struct type via reflection.
// It returns sorted keys so comparisons are deterministic.
func structJSONKeys(v interface{}) []string {
	t := reflect.TypeOf(v)
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		keys = append(keys, strings.Split(tag, ",")[0])
	}
	sort.Strings(keys)
	return keys
}

// fixtureKeys loads a JSON fixture file and returns its sorted top-level keys.
func fixtureKeys(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read fixture %s: %v", path, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to parse fixture %s: %v", path, err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// loadFixture reads a JSON fixture and unmarshals it into a generic map.
func loadFixture(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read fixture %s: %v", path, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to parse fixture %s: %v", path, err)
	}
	return m
}

// --- BoardItem contract tests ---

func TestBoardItemContractKeys(t *testing.T) {
	structKeys := structJSONKeys(types.BoardItem{})
	fixtKeys := fixtureKeys(t, fixturesDir+"/board-item.json")

	if !reflect.DeepEqual(structKeys, fixtKeys) {
		missing := diff(structKeys, fixtKeys)
		extra := diff(fixtKeys, structKeys)
		if len(missing) > 0 {
			t.Errorf("BoardItem struct has keys not in fixture: %v — update the fixture", missing)
		}
		if len(extra) > 0 {
			t.Errorf("fixture has keys not in BoardItem struct: %v — update the struct or fixture", extra)
		}
	}
}

func TestBoardItemContractValues(t *testing.T) {
	item := types.BoardItem{
		ID:            "PVI_item123",
		NodeID:        "I_node456",
		Number:        42,
		Title:         "Add photo upload feature",
		State:         "OPEN",
		Status:        "Ready",
		Priority:      types.PriorityP1,
		Size:          types.SizeM,
		PipelineStage: "feature-dev",
		Labels:        []string{"type:feature", "priority:high"},
		Repo:          "nightgauge/nightgauge",
		URL:           "https://github.com/nightgauge/nightgauge/issues/42",
		CreatedAt:     time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2025, 1, 16, 14, 45, 0, 0, time.UTC),
		IsPR:          false,
		IsEpic:        false,
		ParentNumber:  10,
		ParentTitle:   "Epic: Photo Management",
		SubIssues: []types.SubIssueRef{
			{NodeID: "I_sub789", Number: 43, Title: "Implement upload API", State: "OPEN", Repo: "nightgauge/nightgauge", Labels: []string{"size:M"}},
		},
		BlockedBy: []types.BlockingRef{
			{NodeID: "I_block101", Number: 40, Title: "Setup storage backend", State: "CLOSED", Repo: "nightgauge/nightgauge"},
		},
		Blocking: []types.BlockingRef{
			{NodeID: "I_block202", Number: 44, Title: "Add photo gallery view", State: "OPEN", Repo: "nightgauge/nightgauge"},
		},
	}

	got, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("failed to marshal BoardItem: %v", err)
	}

	want, err := os.ReadFile(fixturesDir + "/board-item.json")
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	var gotMap, wantMap map[string]interface{}
	if err := json.Unmarshal(got, &gotMap); err != nil {
		t.Fatalf("failed to parse marshaled JSON: %v", err)
	}
	if err := json.Unmarshal(want, &wantMap); err != nil {
		t.Fatalf("failed to parse fixture JSON: %v", err)
	}

	if !reflect.DeepEqual(gotMap, wantMap) {
		t.Errorf("BoardItem serialization does not match fixture\nGot:  %s\nWant: %s", got, want)
	}
}

// --- Issue contract tests ---

func TestIssueContractKeys(t *testing.T) {
	structKeys := structJSONKeys(types.Issue{})
	fixtKeys := fixtureKeys(t, fixturesDir+"/issue.json")

	if !reflect.DeepEqual(structKeys, fixtKeys) {
		missing := diff(structKeys, fixtKeys)
		extra := diff(fixtKeys, structKeys)
		if len(missing) > 0 {
			t.Errorf("Issue struct has keys not in fixture: %v — update the fixture", missing)
		}
		if len(extra) > 0 {
			t.Errorf("fixture has keys not in Issue struct: %v — update the struct or fixture", extra)
		}
	}
}

func TestIssueContractValues(t *testing.T) {
	issue := types.Issue{
		NodeID:            "I_node456",
		Number:            42,
		Title:             "Add photo upload feature",
		Body:              "## Summary\nImplement photo upload with drag-and-drop support.",
		State:             "OPEN",
		StateReason:       "REOPENED",
		Labels:            []string{"type:feature", "priority:high"},
		Repo:              "nightgauge/nightgauge",
		URL:               "https://github.com/nightgauge/nightgauge/issues/42",
		Assignees:         []string{"octocat"},
		IsEpic:            true,
		Milestone:         "Sprint 1",
		ParentIssueID:     "I_parent100",
		ParentIssueNumber: 40,
		SubIssues: []types.SubIssueRef{
			{NodeID: "I_sub789", Number: 43, Title: "Implement upload API", State: "OPEN", Repo: "nightgauge/nightgauge", Labels: []string{"size:M"}},
		},
		BlockedBy: []types.BlockingRef{
			{NodeID: "I_block101", Number: 40, Title: "Setup storage backend", State: "CLOSED", Repo: "nightgauge/nightgauge"},
		},
		Blocking: []types.BlockingRef{
			{NodeID: "I_block202", Number: 44, Title: "Add photo gallery view", State: "OPEN", Repo: "nightgauge/nightgauge"},
		},
	}

	got, err := json.Marshal(issue)
	if err != nil {
		t.Fatalf("failed to marshal Issue: %v", err)
	}

	want, err := os.ReadFile(fixturesDir + "/issue.json")
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	var gotMap, wantMap map[string]interface{}
	if err := json.Unmarshal(got, &gotMap); err != nil {
		t.Fatalf("failed to parse marshaled JSON: %v", err)
	}
	if err := json.Unmarshal(want, &wantMap); err != nil {
		t.Fatalf("failed to parse fixture JSON: %v", err)
	}

	if !reflect.DeepEqual(gotMap, wantMap) {
		t.Errorf("Issue serialization does not match fixture\nGot:  %s\nWant: %s", got, want)
	}
}

// --- SubIssueRef contract test ---

func TestSubIssueRefContractKeys(t *testing.T) {
	structKeys := structJSONKeys(types.SubIssueRef{})
	// SubIssueRef is validated via the nested objects in board-item.json and issue.json.
	// Load a SubIssueRef from the board-item fixture.
	fixture := loadFixture(t, fixturesDir+"/board-item.json")
	subIssues, ok := fixture["subIssues"].([]interface{})
	if !ok || len(subIssues) == 0 {
		t.Fatal("fixture board-item.json must have at least one subIssues entry")
	}
	sub, ok := subIssues[0].(map[string]interface{})
	if !ok {
		t.Fatal("subIssues[0] is not an object")
	}

	var subKeys []string
	for k := range sub {
		subKeys = append(subKeys, k)
	}
	sort.Strings(subKeys)

	if !reflect.DeepEqual(structKeys, subKeys) {
		t.Errorf("SubIssueRef struct keys do not match fixture subIssues[0] keys\nStruct: %v\nFixture: %v", structKeys, subKeys)
	}
}

// --- BlockingRef contract test ---

func TestBlockingRefContractKeys(t *testing.T) {
	structKeys := structJSONKeys(types.BlockingRef{})
	fixture := loadFixture(t, fixturesDir+"/board-item.json")
	blockedBy, ok := fixture["blockedBy"].([]interface{})
	if !ok || len(blockedBy) == 0 {
		t.Fatal("fixture board-item.json must have at least one blockedBy entry")
	}
	block, ok := blockedBy[0].(map[string]interface{})
	if !ok {
		t.Fatal("blockedBy[0] is not an object")
	}

	var blockKeys []string
	for k := range block {
		blockKeys = append(blockKeys, k)
	}
	sort.Strings(blockKeys)

	if !reflect.DeepEqual(structKeys, blockKeys) {
		t.Errorf("BlockingRef struct keys do not match fixture blockedBy[0] keys\nStruct: %v\nFixture: %v", structKeys, blockKeys)
	}
}

// diff returns elements in a that are not in b.
func diff(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, s := range b {
		set[s] = true
	}
	var result []string
	for _, s := range a {
		if !set[s] {
			result = append(result, s)
		}
	}
	return result
}

// --- StatusCounts contract tests ---

func TestStatusCountsContractKeys(t *testing.T) {
	structKeys := structJSONKeys(types.StatusCounts{})
	fixtKeys := fixtureKeys(t, fixturesDir+"/status-counts.json")

	if !reflect.DeepEqual(structKeys, fixtKeys) {
		missing := diff(structKeys, fixtKeys)
		extra := diff(fixtKeys, structKeys)
		if len(missing) > 0 {
			t.Errorf("StatusCounts struct has keys not in fixture: %v — update the fixture", missing)
		}
		if len(extra) > 0 {
			t.Errorf("fixture has keys not in StatusCounts struct: %v — update the struct or fixture", extra)
		}
	}
}

func TestStatusCountsContractValues(t *testing.T) {
	sc := types.StatusCounts{
		Ready:      5,
		InProgress: 2,
		InReview:   1,
		Done:       12,
		Backlog:    8,
	}

	got, err := json.Marshal(sc)
	if err != nil {
		t.Fatalf("failed to marshal StatusCounts: %v", err)
	}

	want, err := os.ReadFile(fixturesDir + "/status-counts.json")
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	var gotMap, wantMap map[string]interface{}
	if err := json.Unmarshal(got, &gotMap); err != nil {
		t.Fatalf("failed to parse marshaled JSON: %v", err)
	}
	if err := json.Unmarshal(want, &wantMap); err != nil {
		t.Fatalf("failed to parse fixture JSON: %v", err)
	}

	if !reflect.DeepEqual(gotMap, wantMap) {
		t.Errorf("StatusCounts serialization does not match fixture\nGot:  %s\nWant: %s", got, want)
	}
}

// --- EpicProgress contract tests ---

func TestEpicProgressContractKeys(t *testing.T) {
	structKeys := structJSONKeys(types.EpicProgress{})
	fixtKeys := fixtureKeys(t, fixturesDir+"/epic-progress.json")

	// Remove nested "subIssues" from fixture keys — it's an array, not top-level key mismatch
	if !reflect.DeepEqual(structKeys, fixtKeys) {
		missing := diff(structKeys, fixtKeys)
		extra := diff(fixtKeys, structKeys)
		if len(missing) > 0 {
			t.Errorf("EpicProgress struct has keys not in fixture: %v — update the fixture", missing)
		}
		if len(extra) > 0 {
			t.Errorf("fixture has keys not in EpicProgress struct: %v — update the struct or fixture", extra)
		}
	}
}

func TestEpicProgressContractValues(t *testing.T) {
	ep := types.EpicProgress{
		EpicNodeID: "I_epic001",
		Number:     100,
		Title:      "Epic: User Authentication",
		Repo:       "nightgauge/nightgauge",
		SubIssues: []types.SubIssueRef{
			{NodeID: "I_sub001", Number: 101, Title: "Login page", State: "CLOSED", Repo: "nightgauge/nightgauge"},
			{NodeID: "I_sub002", Number: 102, Title: "Registration page", State: "OPEN", Repo: "nightgauge/nightgauge"},
		},
		Total:           2,
		Closed:          1,
		Open:            1,
		PercentComplete: 50,
	}

	got, err := json.Marshal(ep)
	if err != nil {
		t.Fatalf("failed to marshal EpicProgress: %v", err)
	}

	want, err := os.ReadFile(fixturesDir + "/epic-progress.json")
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	var gotMap, wantMap map[string]interface{}
	if err := json.Unmarshal(got, &gotMap); err != nil {
		t.Fatalf("failed to parse marshaled JSON: %v", err)
	}
	if err := json.Unmarshal(want, &wantMap); err != nil {
		t.Fatalf("failed to parse fixture JSON: %v", err)
	}

	if !reflect.DeepEqual(gotMap, wantMap) {
		t.Errorf("EpicProgress serialization does not match fixture\nGot:  %s\nWant: %s", got, want)
	}
}

// --- PullRequest contract tests ---

func TestPullRequestContractKeys(t *testing.T) {
	structKeys := structJSONKeys(types.PullRequest{})
	fixtKeys := fixtureKeys(t, fixturesDir+"/pull-request.json")

	if !reflect.DeepEqual(structKeys, fixtKeys) {
		missing := diff(structKeys, fixtKeys)
		extra := diff(fixtKeys, structKeys)
		if len(missing) > 0 {
			t.Errorf("PullRequest struct has keys not in fixture: %v — update the fixture", missing)
		}
		if len(extra) > 0 {
			t.Errorf("fixture has keys not in PullRequest struct: %v — update the struct or fixture", extra)
		}
	}
}

func TestPullRequestContractValues(t *testing.T) {
	pr := types.PullRequest{
		NodeID:           "PR_node123",
		Number:           99,
		Title:            "feat: add user auth",
		Body:             "## Summary\n\nImplements user authentication.",
		State:            "OPEN",
		HeadRef:          "feat/99-user-auth",
		BaseRef:          "main",
		Repo:             "nightgauge/nightgauge",
		URL:              "https://github.com/nightgauge/nightgauge/pull/99",
		Mergeable:        "MERGEABLE",
		MergeStateStatus: "CLEAN",
		ReviewStatus:     "APPROVED",
		CheckStatus:      "SUCCESS",
		Labels:           []string{"type:feature"},
		IsDraft:          false,
	}

	got, err := json.Marshal(pr)
	if err != nil {
		t.Fatalf("failed to marshal PullRequest: %v", err)
	}

	want, err := os.ReadFile(fixturesDir + "/pull-request.json")
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	var gotMap, wantMap map[string]interface{}
	if err := json.Unmarshal(got, &gotMap); err != nil {
		t.Fatalf("failed to parse marshaled JSON: %v", err)
	}
	if err := json.Unmarshal(want, &wantMap); err != nil {
		t.Fatalf("failed to parse fixture JSON: %v", err)
	}

	if !reflect.DeepEqual(gotMap, wantMap) {
		t.Errorf("PullRequest serialization does not match fixture\nGot:  %s\nWant: %s", got, want)
	}
}
