package forgecmd

import (
	"encoding/json"
	"strings"
	"testing"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	pkgtypes "github.com/nightgauge/nightgauge/pkg/types"
)

// TestIssueJSON_GhAlignedFields documents the JSON tag set as a stable
// contract. Adding new fields is fine; renaming or removing existing
// ones is a breaking change and should fail this assertion until the
// snapshot fixtures and skill consumers are updated in lock-step.
func TestIssueJSON_GhAlignedFields(t *testing.T) {
	want := []string{
		`"v"`, `"number"`, `"title"`, `"body"`, `"state"`, `"labels"`,
		`"assignees"`, `"url"`, `"author"`, `"milestone"`,
		`"createdAt"`, `"updatedAt"`, `"closedAt"`, `"comments"`,
	}
	data, err := json.Marshal(IssueJSON{V: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, w := range want {
		if !strings.Contains(string(data), w) {
			t.Errorf("IssueJSON missing field %s in %s", w, data)
		}
	}
}

func TestPRJSON_GhAlignedFields(t *testing.T) {
	want := []string{
		`"v"`, `"number"`, `"title"`, `"body"`, `"state"`, `"isDraft"`,
		`"headRefName"`, `"baseRefName"`, `"url"`, `"mergeable"`,
		`"author"`, `"labels"`, `"createdAt"`, `"updatedAt"`,
	}
	data, _ := json.Marshal(PRJSON{V: 1})
	for _, w := range want {
		if !strings.Contains(string(data), w) {
			t.Errorf("PRJSON missing field %s in %s", w, data)
		}
	}
}

func TestCheckRollupJSON_FieldsAndVersion(t *testing.T) {
	want := []string{
		`"v"`, `"number"`, `"state"`, `"total"`, `"successful"`,
		`"failed"`, `"pending"`, `"isTerminal"`, `"requiredCheckNames"`, `"checks"`,
	}
	data, _ := json.Marshal(CheckRollupJSON{V: 1})
	for _, w := range want {
		if !strings.Contains(string(data), w) {
			t.Errorf("CheckRollupJSON missing field %s in %s", w, data)
		}
	}
}

func TestIssueFromForge_NilSafe(t *testing.T) {
	got := IssueFromForge(nil)
	if got.V != 1 {
		t.Errorf("v = %d", got.V)
	}
	if got.Labels == nil || got.Assignees == nil || got.Comments == nil {
		t.Errorf("nil-safe slices not initialized: %+v", got)
	}
}

func TestPRFromForge_PreservesFields(t *testing.T) {
	pr := &forgetypes.PullRequest{
		Number: 42, Title: "title", State: "OPEN", IsDraft: true,
		HeadRef: "feat/x", BaseRef: "main", URL: "u",
		Labels: []string{"L1"}, Additions: 10, Deletions: 5,
	}
	got := PRFromForge(pr)
	if got.Number != 42 {
		t.Errorf("number = %d", got.Number)
	}
	if got.HeadRefName != "feat/x" {
		t.Errorf("headRefName = %q", got.HeadRefName)
	}
	if got.BaseRefName != "main" {
		t.Errorf("baseRefName = %q", got.BaseRefName)
	}
	if !got.IsDraft {
		t.Errorf("isDraft should be true")
	}
	if len(got.Labels) != 1 || got.Labels[0].Name != "L1" {
		t.Errorf("labels = %+v", got.Labels)
	}
}

func TestCheckRollupFromForge_NilSafe(t *testing.T) {
	got := CheckRollupFromForge(nil)
	if got.V != 1 {
		t.Errorf("v = %d", got.V)
	}
	if got.Checks == nil {
		t.Errorf("checks should be non-nil slice")
	}
	if got.RequiredCheckNames == nil {
		t.Errorf("requiredCheckNames should be non-nil slice")
	}
}

func TestBoardItemFromForge_PreservesPriorityAndSize(t *testing.T) {
	bi := &forgetypes.BoardItem{
		Number: 7, Title: "t", Priority: pkgtypes.PriorityP1, Size: pkgtypes.SizeM,
	}
	got := BoardItemFromForge(bi)
	if got.Priority != "P1" || got.Size != "M" {
		t.Errorf("priority/size = %q/%q", got.Priority, got.Size)
	}
}
