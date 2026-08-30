package forgecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

func TestLabelList_JSON(t *testing.T) {
	withFakeForge(t, &fakeForge{
		labels: &fakeLabelService{
			listResp: []*forgetypes.Label{
				{ID: "L_1", Name: "type:bug", Color: "ff0000"},
				{ID: "L_2", Name: "type:feature", Color: "00ff00"},
			},
		},
	})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"label", "list", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got []LabelJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
	if got[0].V != 1 || got[0].Name != "type:bug" {
		t.Errorf("got[0] = %+v", got[0])
	}
}

func TestLabelCreate(t *testing.T) {
	labels := &fakeLabelService{createResp: &forgetypes.Label{ID: "L_new", Name: "ready"}}
	withFakeForge(t, &fakeForge{labels: labels})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"label", "create", "--name", "ready", "--color", "00ff00", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(labels.calls, "Create") {
		t.Errorf("Create not called: %v", labels.calls)
	}
	var got LabelJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "ready" {
		t.Errorf("name = %q", got.Name)
	}
}

// --- Label NAMES, not node IDs (#1214) ---
//
// `--labels` consumed GraphQL node IDs while every caller — including the
// examples printed on these very commands — passed names, so each documented
// invocation failed with `Could not resolve to a node with the global id of
// 'type:bug'`. These tests assert the resolved IDs actually reach the forge:
// the bug is entirely in that argument's value, and it survived precisely
// because nothing looked at it.

func repoLabels() *fakeLabelService {
	return &fakeLabelService{listResp: []*forgetypes.Label{
		{ID: "LA_bug", Name: "type:bug"},
		{ID: "LA_sdk", Name: "component:sdk"},
	}}
}

func runForge(t *testing.T, args ...string) error {
	t.Helper()
	root := Cmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(args)
	return root.ExecuteContext(context.Background())
}

func TestLabelAdd_RoutesViaIssueService(t *testing.T) {
	issues := &fakeIssueService{}
	withFakeForge(t, &fakeForge{issues: issues, labels: repoLabels()})
	if err := runForge(t, "label", "add", "--issue-id", "I_x",
		"--labels", "type:bug,component:sdk", "--json"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(issues.calls, "AddLabels") {
		t.Errorf("AddLabels not called: %v", issues.calls)
	}
	if want := []string{"LA_bug", "LA_sdk"}; !reflect.DeepEqual(issues.addLabelIDs, want) {
		t.Errorf("AddLabels got %v, want %v — a name reaching the mutation is the bug",
			issues.addLabelIDs, want)
	}
}

func TestLabelRemove_ResolvesNamesToIDs(t *testing.T) {
	issues := &fakeIssueService{}
	withFakeForge(t, &fakeForge{issues: issues, labels: repoLabels()})
	if err := runForge(t, "label", "remove", "--issue-id", "I_x",
		"--labels", "component:sdk", "--json"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if want := []string{"LA_sdk"}; !reflect.DeepEqual(issues.removeLabelIDs, want) {
		t.Errorf("RemoveLabels got %v, want %v", issues.removeLabelIDs, want)
	}
}

func TestLabelAdd_UnknownNameChangesNothing(t *testing.T) {
	issues := &fakeIssueService{}
	withFakeForge(t, &fakeForge{issues: issues, labels: repoLabels()})
	err := runForge(t, "label", "add", "--issue-id", "I_x", "--labels", "no:such-label")
	if err == nil {
		t.Fatal("execute returned nil error for an unknown label name")
	}
	if !strings.Contains(err.Error(), `"no:such-label"`) {
		t.Errorf("error %q does not name the missing label", err)
	}
	// The resolution has to precede the mutation, or the caller gets a
	// partially-applied change alongside a failure.
	if contains(issues.calls, "AddLabels") {
		t.Errorf("AddLabels was called despite the unresolvable name: %v", issues.calls)
	}
}

func TestIssueCreate_ResolvesLabelNamesToIDs(t *testing.T) {
	issues := &fakeIssueService{createResp: &forgetypes.Issue{Number: 42, Title: "t"}}
	withFakeForge(t, &fakeForge{issues: issues, labels: repoLabels()})
	if err := runForge(t, "issue", "create", "--repo-id", "R_1", "--title", "t",
		"--labels", "type:bug", "--json"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if want := []string{"LA_bug"}; !reflect.DeepEqual(issues.createLabelIDs, want) {
		t.Errorf("CreateIssue got labelIds %v, want %v", issues.createLabelIDs, want)
	}
}

func TestIssueCreate_UnknownLabelNameCreatesNothing(t *testing.T) {
	issues := &fakeIssueService{createResp: &forgetypes.Issue{Number: 42}}
	withFakeForge(t, &fakeForge{issues: issues, labels: repoLabels()})
	if err := runForge(t, "issue", "create", "--repo-id", "R_1", "--title", "t",
		"--labels", "no:such-label"); err == nil {
		t.Fatal("execute returned nil error for an unknown label name")
	}
	if contains(issues.calls, "CreateIssue") {
		t.Errorf("the issue was created despite the unresolvable label: %v", issues.calls)
	}
}
