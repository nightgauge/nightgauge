package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetProtection_HappyPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawProtectedBranch{
			Name:           "main",
			AllowForcePush: false,
			PushAccessLevels: []AccessLevel{
				{AccessLevel: 40, Description: "Maintainers"},
			},
			MergeAccessLevels: []AccessLevel{
				{AccessLevel: 40, Description: "Maintainers"},
			},
		})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/approvals", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawApprovals{ApprovalsBeforeMerge: 2})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	rs := NewRulesetService(c)
	got, err := rs.GetProtection(context.Background(), "o", "r", "main")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Branch != "main" || got.Pattern != "main" {
		t.Errorf("branch/pattern = %q/%q", got.Branch, got.Pattern)
	}
	if got.AllowForcePush {
		t.Error("AllowForcePush = true, want false")
	}
	if got.ApprovalsRequired != 2 {
		t.Errorf("ApprovalsRequired = %d, want 2", got.ApprovalsRequired)
	}
	if len(got.PushAccessLevels) != 1 || got.PushAccessLevels[0].AccessLevel != 40 {
		t.Errorf("PushAccessLevels: %+v", got.PushAccessLevels)
	}
}

func TestGetProtection_AllowForcePushTrue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/protected_branches/dev", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawProtectedBranch{Name: "dev", AllowForcePush: true})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/approvals", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawApprovals{ApprovalsBeforeMerge: 0})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	rs := NewRulesetService(c)
	got, err := rs.GetProtection(context.Background(), "o", "r", "dev")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got.AllowForcePush {
		t.Error("AllowForcePush = false, want true")
	}
}

func TestGetProtection_404ReturnsZeroValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"not found"}`, 404)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	rs := NewRulesetService(c)
	got, err := rs.GetProtection(context.Background(), "o", "r", "main")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Branch != "main" {
		t.Errorf("Branch = %q, want main", got.Branch)
	}
	if got.Pattern != "" || got.ApprovalsRequired != 0 || got.AllowForcePush {
		t.Errorf("expected zero-value protection, got %+v", got)
	}
}

func TestCheckRulesets_NoBlockers(t *testing.T) {
	mux := http.NewServeMux()
	// PRService.GetPR fetches the MR.
	mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests/7", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "iid": 7, "state": "opened", "target_branch": "main", "source_branch": "feat",
		})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawProtectedBranch{Name: "main"})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/approvals", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawApprovals{ApprovalsBeforeMerge: 0})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	rs := NewRulesetService(c)
	got, err := rs.CheckRulesets(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got.AllowedToMerge {
		t.Errorf("AllowedToMerge = false, want true (got blockers=%v)", got.Blockers)
	}
	if got.BaseRef != "main" {
		t.Errorf("BaseRef = %q, want main", got.BaseRef)
	}
}

func TestCheckRulesets_RequiredApprovalsMissingBlocks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests/8", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 2, "iid": 8, "state": "opened", "target_branch": "main", "source_branch": "feat",
		})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawProtectedBranch{Name: "main"})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/approvals", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawApprovals{ApprovalsBeforeMerge: 2})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests/8/approvals", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawMRApprovals{ApprovalsRequired: 2, ApprovalsLeft: 2})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	rs := NewRulesetService(c)
	got, err := rs.CheckRulesets(context.Background(), "o", "r", 8)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.AllowedToMerge {
		t.Error("AllowedToMerge = true, want false (missing approvals)")
	}
	if len(got.Blockers) != 1 || got.Blockers[0] != "required_pull_request_reviews" {
		t.Errorf("Blockers = %v, want [required_pull_request_reviews]", got.Blockers)
	}
}

func TestCheckRulesets_RequiredApprovalsSatisfied(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests/9", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 3, "iid": 9, "state": "opened", "target_branch": "main", "source_branch": "feat",
		})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawProtectedBranch{Name: "main"})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/approvals", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawApprovals{ApprovalsBeforeMerge: 2})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests/9/approvals", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawMRApprovals{ApprovalsRequired: 2, ApprovalsLeft: 0})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	rs := NewRulesetService(c)
	got, err := rs.CheckRulesets(context.Background(), "o", "r", 9)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got.AllowedToMerge {
		t.Errorf("AllowedToMerge = false, want true (got blockers=%v)", got.Blockers)
	}
}

func TestCheckRulesets_403InformationalMessage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests/10", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 4, "iid": 10, "state": "opened", "target_branch": "main", "source_branch": "feat",
		})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, 403)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	rs := NewRulesetService(c)
	got, err := rs.CheckRulesets(context.Background(), "o", "r", 10)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got.AllowedToMerge {
		t.Error("AllowedToMerge = false, want true on 403 — should degrade gracefully")
	}
}

func TestSatisfyRulesets_RequiredReviewIsInformational(t *testing.T) {
	c := NewClient("http://example.invalid", "tok")
	rs := NewRulesetService(c)
	resolved, err := rs.SatisfyRulesets(context.Background(), "o", "r", 1, []string{"required_pull_request_reviews"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resolved) != 0 {
		t.Errorf("resolved = %v, want empty (human action required)", resolved)
	}
}

func TestSatisfyRulesets_NoBlockersReturnsNil(t *testing.T) {
	c := NewClient("http://example.invalid", "tok")
	rs := NewRulesetService(c)
	resolved, err := rs.SatisfyRulesets(context.Background(), "o", "r", 1, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resolved != nil {
		t.Errorf("resolved = %v, want nil", resolved)
	}
}

// TestProjectPathEscape ensures the test paths above match the actual
// projectPath escaping convention. If the convention drifts, this test
// flags it before the integration tests above start mysteriously failing.
func TestProjectPathEscape(t *testing.T) {
	got := projectPath("o", "r")
	if !strings.EqualFold(got, "o%2Fr") {
		t.Errorf("projectPath(o, r) = %q, want o%%2Fr", got)
	}
}
