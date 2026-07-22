package forgecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

func TestPRView_JSON(t *testing.T) {
	withFakeForge(t, &fakeForge{
		prs: &fakePRService{
			getResp: &forgetypes.PullRequest{Number: 99, Title: "pr-title", State: "OPEN"},
		},
	})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"pr", "view", "99", "--repo", "x/y", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got PRJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Number != 99 {
		t.Errorf("number = %d", got.Number)
	}
	if got.V != 1 {
		t.Errorf("schema v = %d", got.V)
	}
}

func TestMRAlias_RoutesToPR(t *testing.T) {
	withFakeForge(t, &fakeForge{
		prs: &fakePRService{getResp: &forgetypes.PullRequest{Number: 7}},
	})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"mr", "view", "7", "--repo", "x/y", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute via mr alias: %v", err)
	}
	var got PRJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Number != 7 {
		t.Errorf("alias did not route to pr — got number %d", got.Number)
	}
}

func TestPRChecks_SchemaVersioned(t *testing.T) {
	withFakeForge(t, &fakeForge{
		ci: &fakeCIService{
			resp: &forgetypes.CheckStatus{
				PRNumber: 3362, State: "success", Total: 5, Successful: 5,
				Checks: []forgetypes.CheckDetail{{Name: "build", Status: "completed", Conclusion: "success", Required: true}},
			},
		},
	})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"pr", "checks", "3362", "--repo", "x/y", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got CheckRollupJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.V != 1 {
		t.Errorf("v = %d, want 1", got.V)
	}
	if got.Number != 3362 {
		t.Errorf("number = %d, want 3362", got.Number)
	}
	if len(got.Checks) != 1 || got.Checks[0].Name != "build" {
		t.Errorf("checks = %+v", got.Checks)
	}
}

func TestPRMerge_DefaultStrategy(t *testing.T) {
	prs := &fakePRService{}
	withFakeForge(t, &fakeForge{prs: prs})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"pr", "merge", "--node-id", "PR_x", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(prs.calls, "MergePR") {
		t.Errorf("expected MergePR call, got %v", prs.calls)
	}
}

func TestPRMerge_WithStrategy(t *testing.T) {
	prs := &fakePRService{}
	withFakeForge(t, &fakeForge{prs: prs})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"pr", "merge", "--node-id", "PR_x", "--strategy", "squash", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(prs.calls, "MergePRWithStrategy") {
		t.Errorf("expected MergePRWithStrategy, got %v", prs.calls)
	}
	if prs.mergeStrat != "squash" {
		t.Errorf("strategy = %q, want squash", prs.mergeStrat)
	}
}
