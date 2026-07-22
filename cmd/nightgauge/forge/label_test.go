package forgecmd

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestLabelAdd_RoutesViaIssueService(t *testing.T) {
	issues := &fakeIssueService{}
	withFakeForge(t, &fakeForge{issues: issues, labels: &fakeLabelService{}})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"label", "add", "--issue-id", "I_x", "--labels", "L_1,L_2", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(issues.calls, "AddLabels") {
		t.Errorf("AddLabels not called: %v", issues.calls)
	}
}
