package forgecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

func TestRepoView_JSONShapeMatchesGH(t *testing.T) {
	withFakeForge(t, &fakeForge{
		repo: &fakeRepoService{
			resp: &forgetypes.Repo{
				NameWithOwner: "nightgauge/nightgauge",
				Owner:         "nightgauge",
				Name:          "nightgauge",
			},
		},
	})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"repo", "view", "--repo", "nightgauge/nightgauge", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got RepoJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if got.NameWithOwner != "nightgauge/nightgauge" {
		t.Errorf("nameWithOwner = %q", got.NameWithOwner)
	}
	if got.Owner != "nightgauge" {
		t.Errorf("owner = %q", got.Owner)
	}
	if got.Name != "nightgauge" {
		t.Errorf("name = %q", got.Name)
	}
	if got.V != 1 {
		t.Errorf("v = %d, want 1", got.V)
	}
}

func TestRepoView_RequiresRepoFlag(t *testing.T) {
	withFakeForge(t, &fakeForge{repo: &fakeRepoService{}})
	root := Cmd()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs([]string{"repo", "view", "--json"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatalf("expected error when --repo is missing")
	}
	if !strings.Contains(stderr.String(), "--repo is required") {
		t.Errorf("expected --repo required error, got: %s", stderr.String())
	}
}

func TestRepoView_PropagatesServiceError(t *testing.T) {
	withFakeForge(t, &fakeForge{
		repo: &fakeRepoService{err: errors.New("boom")},
	})
	root := Cmd()
	stderr := &bytes.Buffer{}
	root.SetOut(&bytes.Buffer{})
	root.SetErr(stderr)
	root.SetArgs([]string{"repo", "view", "--repo", "o/r", "--json"})
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatalf("expected error to surface")
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("expected service error in stderr: %s", stderr.String())
	}
}
