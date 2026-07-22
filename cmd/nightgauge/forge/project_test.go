package forgecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

func TestProjectFieldList_JSON(t *testing.T) {
	withFakeForge(t, &fakeForge{
		project: &fakeProjectService{
			snapshotResp: &forgetypes.FieldsSnapshot{
				ProjectID: "P_1",
				Fields: map[string]forgetypes.FieldInfo{
					"Status":   {ID: "F_status", Type: "single-select", Options: map[string]string{"Ready": "opt_1"}},
					"Priority": {ID: "F_priority", Type: "single-select"},
				},
			},
		},
	})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"project", "field-list", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got []ProjectFieldJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
	for _, f := range got {
		if f.V != 1 {
			t.Errorf("v = %d", f.V)
		}
	}
}

func TestProjectItemList_JSON(t *testing.T) {
	withFakeForge(t, &fakeForge{
		board: &fakeBoardService{
			listResp: []forgetypes.BoardItem{
				{Number: 10, Title: "first", Status: "Ready", Priority: "P1", Repo: "x/y"},
			},
		},
	})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"project", "item-list", "--json", "--status", "Ready"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got []BoardItemJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Number != 10 {
		t.Errorf("got %+v", got)
	}
}

func TestProjectItemRemove_ReturnsUnsupported(t *testing.T) {
	withFakeForge(t, &fakeForge{project: &fakeProjectService{}})
	root := Cmd()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs([]string{"project", "item-remove"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("expected wrapped ErrUnsupported, got %v", err)
	}
}

func TestProjectItemAdd(t *testing.T) {
	project := &fakeProjectService{addItemResp: "PVTI_1"}
	withFakeForge(t, &fakeForge{project: project})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"project", "item-add", "--number", "42", "--repo", "x/y", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["itemId"] != "PVTI_1" {
		t.Errorf("itemId = %v", got["itemId"])
	}
	if !contains(project.calls, "AddIssueByNumber") {
		t.Errorf("AddIssueByNumber not called: %v", project.calls)
	}
}

func TestProjectFieldSet_RoutesByType(t *testing.T) {
	tests := []struct {
		fieldType string
		callName  string
		value     string
	}{
		{"single-select", "SetSingleSelectField", "Ready"},
		{"text", "SetTextField", "hello"},
		{"number", "SetNumberField", "5"},
		{"date", "SetDateField", "2026-05-10"},
	}
	for _, tt := range tests {
		t.Run(tt.fieldType, func(t *testing.T) {
			project := &fakeProjectService{}
			withFakeForge(t, &fakeForge{project: project})
			root := Cmd()
			stdout := &bytes.Buffer{}
			root.SetOut(stdout)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs([]string{
				"project", "field-set",
				"--item-id", "PVTI_1", "--field", "F", "--value", tt.value,
				"--type", tt.fieldType, "--json",
			})
			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("execute %s: %v", tt.fieldType, err)
			}
			if !contains(project.calls, tt.callName) {
				t.Errorf("%s not called for type=%q: %v", tt.callName, tt.fieldType, project.calls)
			}
		})
	}
}
