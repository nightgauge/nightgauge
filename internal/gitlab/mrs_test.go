package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

const sampleMRJSON = `{
  "id": 4001,
  "iid": 7,
  "project_id": 5,
  "title": "Add new feature",
  "description": "MR body",
  "state": "opened",
  "source_branch": "feat/x",
  "target_branch": "main",
  "web_url": "https://gitlab.example.com/o/r/-/merge_requests/7",
  "labels": ["enhancement"],
  "draft": true,
  "work_in_progress": false,
  "merge_status": "can_be_merged",
  "squash": true,
  "allow_force_push": false,
  "approvals_before_merge": 2,
  "assignees": [{"id":3,"username":"bob"}]
}`

func TestGetPR_HappyPath(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/merge_requests/7", 200, sampleMRJSON)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewPRService(c)

	got, err := svc.GetPR(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if got.Number != 7 {
		t.Errorf("Number = %d", got.Number)
	}
	if got.NodeID != "4001" {
		t.Errorf("NodeID = %q", got.NodeID)
	}
	if got.HeadRef != "feat/x" || got.BaseRef != "main" {
		t.Errorf("Head/Base = %q/%q", got.HeadRef, got.BaseRef)
	}
	if got.State != "OPEN" {
		t.Errorf("State = %q, want OPEN", got.State)
	}
	if !got.IsDraft {
		t.Error("IsDraft = false, want true")
	}
	if got.Mergeable != "MERGEABLE" {
		t.Errorf("Mergeable = %q", got.Mergeable)
	}
}

func TestTranslateMergeStatus(t *testing.T) {
	cases := map[string]string{
		"can_be_merged":    "MERGEABLE",
		"cannot_be_merged": "CONFLICTING",
		"unchecked":        "UNKNOWN",
		"checking":         "UNKNOWN",
		"":                 "UNKNOWN",
	}
	for in, want := range cases {
		if got := translateMergeStatus(in); got != want {
			t.Errorf("translateMergeStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalisePRState(t *testing.T) {
	cases := map[string]string{
		"opened": "OPEN",
		"closed": "CLOSED",
		"merged": "MERGED",
		"locked": "LOCKED",
	}
	for in, want := range cases {
		if got := normalisePRState(in); got != want {
			t.Errorf("normalisePRState(%q) = %q", in, got)
		}
	}
}

func TestCreatePR_PostsExpectedBody(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("POST", "/api/v4/projects/o%2Fr/merge_requests", 201, sampleMRJSON)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewPRService(c)

	got, err := svc.CreatePR(context.Background(), "o/r", "Add new feature", "MR body", "feat/x", "main")
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if got.Number != 7 {
		t.Errorf("Number = %d", got.Number)
	}

	var body map[string]any
	if err := json.Unmarshal(srv.lastBody, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["title"] != "Add new feature" {
		t.Errorf("title = %v", body["title"])
	}
	if body["source_branch"] != "feat/x" {
		t.Errorf("source_branch = %v", body["source_branch"])
	}
	if body["target_branch"] != "main" {
		t.Errorf("target_branch = %v", body["target_branch"])
	}
}

func TestCreatePR_RejectsBadRepoID(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewPRService(c)
	_, err := svc.CreatePR(context.Background(), "no-slash", "t", "b", "h", "m")
	if err == nil || !strings.Contains(err.Error(), "owner/repo") {
		t.Errorf("expected error about owner/repo, got %v", err)
	}
}

func TestUpdatePR_SendsAllSpecifiedFields(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("PUT", "/api/v4/projects/o%2Fr/merge_requests/7", 200, sampleMRJSON)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewPRService(c)

	title := "New title"
	draft := false
	target := "develop"
	squash := true
	afp := true
	approvals := 3
	labels := []string{"a", "b"}

	got, err := svc.UpdatePR(context.Background(), "o/r!7", forge.UpdatePROptions{
		Title:                &title,
		Draft:                &draft,
		TargetBranch:         &target,
		Squash:               &squash,
		AllowForcePush:       &afp,
		ApprovalsBeforeMerge: &approvals,
		Labels:               &labels,
	})
	if err != nil {
		t.Fatalf("UpdatePR: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}

	var body map[string]any
	if err := json.Unmarshal(srv.lastBody, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for k, want := range map[string]any{
		"title":                  "New title",
		"draft":                  false,
		"target_branch":          "develop",
		"squash":                 true,
		"allow_force_push":       true,
		"approvals_before_merge": float64(3),
		"labels":                 "a,b",
	} {
		if body[k] != want {
			t.Errorf("posted %s = %v, want %v", k, body[k], want)
		}
	}
}

func TestUpdatePR_ApprovalsCEReturnsEditionError(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("PUT", "/api/v4/projects/o%2Fr/merge_requests/7", 400, `{"message":"approvals_before_merge is unavailable on Community Edition"}`)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewPRService(c)

	approvals := 1
	_, err := svc.UpdatePR(context.Background(), "o/r!7", forge.UpdatePROptions{ApprovalsBeforeMerge: &approvals})
	if !errors.Is(err, forge.ErrUnsupportedOnEdition) {
		t.Errorf("expected ErrUnsupportedOnEdition, got %v", err)
	}
}

func TestClosePR_SendsCloseEvent(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("PUT", "/api/v4/projects/o%2Fr/merge_requests/7", 200, sampleMRJSON)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewPRService(c)

	if err := svc.ClosePR(context.Background(), "o/r!7"); err != nil {
		t.Fatalf("ClosePR: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(srv.lastBody, &body)
	if body["state_event"] != "close" {
		t.Errorf("state_event = %v", body["state_event"])
	}
}

func TestMergePR_DefaultStrategy(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("PUT", "/api/v4/projects/o%2Fr/merge_requests/7/merge", 200,
		`{"merge_commit_sha":"abc123"}`)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewPRService(c)

	if err := svc.MergePR(context.Background(), "o/r!7"); err != nil {
		t.Fatalf("MergePR: %v", err)
	}

	// No squash flag should be sent on default merge.
	var body map[string]any
	_ = json.Unmarshal(srv.lastBody, &body)
	if _, ok := body["squash"]; ok {
		t.Errorf("squash flag should not be sent on default merge: %v", body)
	}
}

func TestMergePRWithStrategy_SquashSetsFlag(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("PUT", "/api/v4/projects/o%2Fr/merge_requests/7/merge", 200,
		`{"squash_commit_sha":"sq1"}`)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewPRService(c)

	sha, err := svc.MergePRWithStrategy(context.Background(), "o/r!7", "SQUASH")
	if err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if sha != "sq1" {
		t.Errorf("sha = %q, want sq1", sha)
	}
	var body map[string]any
	_ = json.Unmarshal(srv.lastBody, &body)
	if body["squash"] != true {
		t.Errorf("squash = %v, want true", body["squash"])
	}
}

func TestListPRs_FiltersStateAndHeadRef(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/merge_requests", 200, "["+sampleMRJSON+"]")
	c := NewClient(srv.srv.URL, "tok")
	svc := NewPRService(c)

	got, err := svc.ListPRs(context.Background(), "o", "r", "OPEN", "feat/x")
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	q := srv.lastReq.URL.RawQuery
	if !strings.Contains(q, "state=open") || !strings.Contains(q, "source_branch=feat") {
		t.Errorf("unexpected query: %q", q)
	}
}

func TestDeleteBranch(t *testing.T) {
	srv := newStubServer(t)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/repository/branches/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(204)
	})
	c := NewClient(srv.srv.URL, "tok")
	svc := NewPRService(c)
	if err := svc.DeleteBranch(context.Background(), "o", "r", "feat/x"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
}

func TestParseMRRef(t *testing.T) {
	cases := []struct {
		ref         string
		owner, repo string
		iid         int
		ok          bool
	}{
		{"o/r!7", "o", "r", 7, true},
		{"o/r#42", "o", "r", 42, true},
		{"bad", "", "", 0, false},
		{"o/r!abc", "", "", 0, false},
	}
	for _, c := range cases {
		ow, rp, iid, err := parseMRRef(c.ref)
		ok := err == nil
		if ok != c.ok {
			t.Errorf("parseMRRef(%q): ok = %v, want %v (err=%v)", c.ref, ok, c.ok, err)
		}
		if ok && (ow != c.owner || rp != c.repo || iid != c.iid) {
			t.Errorf("parseMRRef(%q) = (%q,%q,%d), want (%q,%q,%d)",
				c.ref, ow, rp, iid, c.owner, c.repo, c.iid)
		}
	}
}

func TestEpicHelpers_ReturnUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewPRService(c)
	ctx := context.Background()
	if _, err := svc.CreateEpicPR(ctx, "o", "r", 1, "t", "b", "main"); !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("CreateEpicPR: want ErrUnsupported, got %v", err)
	}
	if err := svc.MergeEpicPR(ctx, "o", "r", "node", "b"); !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("MergeEpicPR: want ErrUnsupported, got %v", err)
	}
}
