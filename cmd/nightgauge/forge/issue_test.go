package forgecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

func TestIssueView_JSON(t *testing.T) {
	withFakeForge(t, &fakeForge{
		issues: &fakeIssueService{
			getIssueResp: &forgetypes.Issue{
				Number: 3362, Title: "forge subcommand", State: "open",
				Labels: []string{"type:feature"}, URL: "https://example.invalid/3362",
			},
		},
	})

	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"issue", "view", "3362", "--repo", "nightgauge/nightgauge", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var got IssueJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v\noutput: %s", err, stdout.String())
	}
	if got.Number != 3362 {
		t.Errorf("number = %d, want 3362", got.Number)
	}
	if got.Title != "forge subcommand" {
		t.Errorf("title = %q", got.Title)
	}
	if got.V != 1 {
		t.Errorf("schema version v = %d, want 1", got.V)
	}
	if len(got.Labels) != 1 || got.Labels[0].Name != "type:feature" {
		t.Errorf("labels = %+v", got.Labels)
	}
}

func TestIssueView_RequiresRepo(t *testing.T) {
	withFakeForge(t, &fakeForge{issues: &fakeIssueService{}})
	root := Cmd()
	stderr := &bytes.Buffer{}
	root.SetOut(&bytes.Buffer{})
	root.SetErr(stderr)
	root.SetArgs([]string{"issue", "view", "1"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error when --repo missing")
	}
	if !strings.Contains(err.Error(), "--repo is required") {
		t.Errorf("err = %q", err.Error())
	}
}

func TestIssueList_JSON(t *testing.T) {
	withFakeForge(t, &fakeForge{
		issues: &fakeIssueService{
			listResp: []forgetypes.Issue{
				{Number: 1, Title: "a"},
				{Number: 2, Title: "b"},
			},
		},
	})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"issue", "list", "--repo", "x/y", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got []IssueJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestIssueClose_EmitsClosedEvent(t *testing.T) {
	issues := &fakeIssueService{}
	withFakeForge(t, &fakeForge{issues: issues})

	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"issue", "close", "--node-id", "I_xxx", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["closed"] != true {
		t.Errorf("expected closed:true, got %+v", got)
	}
	called := false
	for _, c := range issues.calls {
		if c == "CloseIssue" {
			called = true
		}
	}
	if !called {
		t.Errorf("CloseIssue not called: %v", issues.calls)
	}
}

func TestIssueComment(t *testing.T) {
	issues := &fakeIssueService{}
	withFakeForge(t, &fakeForge{issues: issues})

	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"issue", "comment", "--subject-id", "I_x", "--body", "hi", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(issues.calls, "AddComment") {
		t.Errorf("AddComment not called: %v", issues.calls)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
