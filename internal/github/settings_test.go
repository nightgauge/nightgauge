package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newSettingsTestServer creates a test HTTP server for settings REST API tests.
// handler receives all requests; use r.Method to distinguish GET vs PATCH.
func newSettingsTestServer(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	client := NewClientWithURL("test-token", srv.URL)
	return client, srv.Close
}

func TestNewSettingsService(t *testing.T) {
	client := NewClientWithToken("test-token")
	svc := NewSettingsService(client)
	if svc == nil {
		t.Fatal("NewSettingsService returned nil")
	}
	if svc.client != client {
		t.Error("SettingsService.client is not the provided client")
	}
}

func TestGetRepositorySettings_AutoMergeEnabled(t *testing.T) {
	client, cleanup := newSettingsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "expected GET", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/repos/nightgauge/myrepo" {
			http.Error(w, fmt.Sprintf("unexpected path: %s", r.URL.Path), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"full_name":        "nightgauge/myrepo",
			"allow_auto_merge": true,
		})
	})
	defer cleanup()

	svc := NewSettingsService(client)
	settings, err := svc.GetRepositorySettings(context.Background(), "nightgauge", "myrepo")
	if err != nil {
		t.Fatalf("GetRepositorySettings returned error: %v", err)
	}
	if !settings.AllowAutoMerge {
		t.Error("expected AllowAutoMerge = true, got false")
	}
	if settings.RepoFullName != "nightgauge/myrepo" {
		t.Errorf("expected RepoFullName = %q, got %q", "nightgauge/myrepo", settings.RepoFullName)
	}
	if settings.Owner != "nightgauge" {
		t.Errorf("expected Owner = %q, got %q", "nightgauge", settings.Owner)
	}
	if settings.Repo != "myrepo" {
		t.Errorf("expected Repo = %q, got %q", "myrepo", settings.Repo)
	}
}

func TestGetRepositorySettings_DeleteBranchOnMerge(t *testing.T) {
	client, cleanup := newSettingsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"full_name":              "nightgauge/myrepo",
			"allow_auto_merge":       false,
			"delete_branch_on_merge": true,
		})
	})
	defer cleanup()

	svc := NewSettingsService(client)
	settings, err := svc.GetRepositorySettings(context.Background(), "nightgauge", "myrepo")
	if err != nil {
		t.Fatalf("GetRepositorySettings returned error: %v", err)
	}
	if !settings.DeleteBranchOnMerge {
		t.Error("expected DeleteBranchOnMerge = true, got false")
	}
}

func TestEnableDeleteBranchOnMerge_Success(t *testing.T) {
	var gotBody map[string]interface{}
	client, cleanup := newSettingsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, "expected PATCH", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"delete_branch_on_merge": true})
	})
	defer cleanup()

	svc := NewSettingsService(client)
	if err := svc.EnableDeleteBranchOnMerge(context.Background(), "nightgauge", "myrepo"); err != nil {
		t.Fatalf("EnableDeleteBranchOnMerge returned error: %v", err)
	}
	val, ok := gotBody["delete_branch_on_merge"]
	if !ok {
		t.Error("request body missing delete_branch_on_merge field")
	} else if val != true {
		t.Errorf("expected delete_branch_on_merge=true in body, got %v", val)
	}
}

func TestGetRepositorySettings_AutoMergeDisabled(t *testing.T) {
	client, cleanup := newSettingsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"full_name":        "nightgauge/otherrepo",
			"allow_auto_merge": false,
		})
	})
	defer cleanup()

	svc := NewSettingsService(client)
	settings, err := svc.GetRepositorySettings(context.Background(), "nightgauge", "otherrepo")
	if err != nil {
		t.Fatalf("GetRepositorySettings returned error: %v", err)
	}
	if settings.AllowAutoMerge {
		t.Error("expected AllowAutoMerge = false, got true")
	}
}

func TestGetRepositorySettings_APIError(t *testing.T) {
	client, cleanup := newSettingsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	defer cleanup()

	svc := NewSettingsService(client)
	_, err := svc.GetRepositorySettings(context.Background(), "nightgauge", "nonexistent")
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
}

func TestGetRepositorySettings_UnauthorizedError(t *testing.T) {
	client, cleanup := newSettingsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	})
	defer cleanup()

	svc := NewSettingsService(client)
	_, err := svc.GetRepositorySettings(context.Background(), "nightgauge", "myrepo")
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
}

func TestDisableAutoMerge_Success(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotBody map[string]interface{}

	client, cleanup := newSettingsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"full_name":        "nightgauge/myrepo",
			"allow_auto_merge": false,
		})
	})
	defer cleanup()

	svc := NewSettingsService(client)
	err := svc.DisableAutoMerge(context.Background(), "nightgauge", "myrepo")
	if err != nil {
		t.Fatalf("DisableAutoMerge returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("expected PATCH request, got %s", gotMethod)
	}
	if gotPath != "/repos/nightgauge/myrepo" {
		t.Errorf("expected path /repos/nightgauge/myrepo, got %s", gotPath)
	}
	val, ok := gotBody["allow_auto_merge"]
	if !ok {
		t.Error("request body missing allow_auto_merge field")
	} else if val != false {
		t.Errorf("expected allow_auto_merge=false in body, got %v", val)
	}
}

func TestDisableAutoMerge_APIError(t *testing.T) {
	client, cleanup := newSettingsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Forbidden"}`, http.StatusForbidden)
	})
	defer cleanup()

	svc := NewSettingsService(client)
	err := svc.DisableAutoMerge(context.Background(), "nightgauge", "myrepo")
	if err == nil {
		t.Fatal("expected error for 403 response, got nil")
	}
}
