package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// mockViewServer creates a test server that routes requests by path:
//   - Requests to "/" are treated as GraphQL (consumed in sequence)
//   - Requests to paths starting with "/orgs/" or "/users/" are REST API calls
//
// graphqlResponses are served in order; after exhaustion the last one repeats.
// restHandler handles all REST API calls.
func mockViewServer(t *testing.T, graphqlResponses []string, restHandler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	var callIdx int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "" {
			// GraphQL request
			idx := int(atomic.AddInt32(&callIdx, 1)) - 1
			if idx >= len(graphqlResponses) {
				idx = len(graphqlResponses) - 1
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, graphqlResponses[idx])
			return
		}
		// REST request
		if restHandler != nil {
			restHandler(w, r)
		} else {
			http.Error(w, "unexpected REST call", http.StatusInternalServerError)
		}
	}))
	client := NewClientWithURL("test-token", srv.URL)
	return client, srv.Close
}

func TestNewViewService(t *testing.T) {
	client := NewClientWithToken("test-token")

	svc := NewViewService(client, "nightgauge", 1)
	if svc == nil {
		t.Fatal("NewViewService returned nil")
	}
	if svc.client != client {
		t.Error("ViewService.client is not the provided client")
	}
	if svc.owner != "nightgauge" {
		t.Errorf("ViewService.owner = %q, want %q", svc.owner, "nightgauge")
	}
	if svc.projectNumber != 1 {
		t.Errorf("ViewService.projectNumber = %d, want 1", svc.projectNumber)
	}
	if svc.ownerType != OwnerTypeOrg {
		t.Errorf("ViewService.ownerType = %q, want %q", svc.ownerType, OwnerTypeOrg)
	}
}

func TestNewViewService_UserOwnerType(t *testing.T) {
	client := NewClientWithToken("test-token")
	svc := NewViewService(client, "markm", 5, OwnerTypeUser)
	if svc.ownerType != OwnerTypeUser {
		t.Errorf("ViewService.ownerType = %q, want %q", svc.ownerType, OwnerTypeUser)
	}
	if svc.projectNumber != 5 {
		t.Errorf("ViewService.projectNumber = %d, want 5", svc.projectNumber)
	}
}

func TestViewList(t *testing.T) {
	listResp := `{
		"data": {
			"organization": {
				"projectV2": {
					"views": {
						"nodes": [
							{"id": "PVT_kwHO123", "name": "Board", "layout": "BOARD_LAYOUT"},
							{"id": "PVT_kwHO124", "name": "Backlog", "layout": "TABLE_LAYOUT"}
						]
					}
				}
			}
		}
	}`

	client, cleanup := mockGraphQLServer(t, listResp)
	defer cleanup()

	svc := NewViewService(client, "nightgauge", 1)
	views, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("List() returned %d views, want 2", len(views))
	}
	if views[0].Name != "Board" {
		t.Errorf("views[0].Name = %q, want %q", views[0].Name, "Board")
	}
	if views[0].Layout != "board" {
		t.Errorf("views[0].Layout = %q, want %q", views[0].Layout, "board")
	}
	if views[0].ID != "PVT_kwHO123" {
		t.Errorf("views[0].ID = %q, want %q", views[0].ID, "PVT_kwHO123")
	}
	if views[1].Layout != "table" {
		t.Errorf("views[1].Layout = %q, want %q", views[1].Layout, "table")
	}
}

func TestViewList_UserOwned(t *testing.T) {
	listResp := `{
		"data": {
			"user": {
				"projectV2": {
					"views": {
						"nodes": [
							{"id": "PVT_user1", "name": "My Board", "layout": "BOARD_LAYOUT"}
						]
					}
				}
			}
		}
	}`

	client, cleanup := mockGraphQLServer(t, listResp)
	defer cleanup()

	svc := NewViewService(client, "markm", 2, OwnerTypeUser)
	views, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("List() returned %d views, want 1", len(views))
	}
	if views[0].Name != "My Board" {
		t.Errorf("views[0].Name = %q, want %q", views[0].Name, "My Board")
	}
}

func TestViewList_Empty(t *testing.T) {
	listResp := `{"data": {"organization": {"projectV2": {"views": {"nodes": []}}}}}`

	client, cleanup := mockGraphQLServer(t, listResp)
	defer cleanup()

	svc := NewViewService(client, "nightgauge", 1)
	views, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("List() returned %d views, want 0", len(views))
	}
}

func TestViewCreate_New(t *testing.T) {
	graphqlResp := `{"data": {"organization": {"projectV2": {"views": {"nodes": []}}}}}`
	restResp := `{"id": 42, "node_id": "PVT_kwHO456", "name": "Ready Items", "layout": "BOARD_LAYOUT"}`

	client, cleanup := mockViewServer(t, []string{graphqlResp}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, restResp)
	})
	defer cleanup()

	svc := NewViewService(client, "nightgauge", 1)
	filter := "status:Ready"
	view, err := svc.Create(context.Background(), "Ready Items", "board", &filter)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if view.ID != "PVT_kwHO456" {
		t.Errorf("Create() ID = %q, want %q", view.ID, "PVT_kwHO456")
	}
	if view.Name != "Ready Items" {
		t.Errorf("Create() Name = %q, want %q", view.Name, "Ready Items")
	}
	if view.Layout != "board" {
		t.Errorf("Create() Layout = %q, want %q", view.Layout, "board")
	}
}

func TestViewCreate_New_IntegerIDFallback(t *testing.T) {
	// REST response without node_id — should fall back to integer ID as string.
	graphqlResp := `{"data": {"organization": {"projectV2": {"views": {"nodes": []}}}}}`
	restResp := `{"id": 7, "name": "Roadmap", "layout": "ROADMAP_LAYOUT"}`

	client, cleanup := mockViewServer(t, []string{graphqlResp}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, restResp)
	})
	defer cleanup()

	svc := NewViewService(client, "nightgauge", 1)
	view, err := svc.Create(context.Background(), "Roadmap", "roadmap", nil)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if view.ID != "7" {
		t.Errorf("Create() ID = %q, want %q (integer fallback)", view.ID, "7")
	}
	if view.Layout != "roadmap" {
		t.Errorf("Create() Layout = %q, want %q", view.Layout, "roadmap")
	}
}

func TestViewCreate_Existing(t *testing.T) {
	// List() returns an existing view — Create() should NOT make a REST call.
	graphqlResp := `{
		"data": {
			"organization": {
				"projectV2": {
					"views": {
						"nodes": [
							{"id": "PVT_existing", "name": "Ready Items", "layout": "BOARD_LAYOUT"}
						]
					}
				}
			}
		}
	}`

	// If REST is called, fail the test.
	restCalled := false
	client, cleanup := mockViewServer(t, []string{graphqlResp}, func(w http.ResponseWriter, r *http.Request) {
		restCalled = true
		http.Error(w, "REST should not be called for existing view", http.StatusInternalServerError)
	})
	defer cleanup()

	svc := NewViewService(client, "nightgauge", 1)
	view, err := svc.Create(context.Background(), "Ready Items", "board", nil)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if restCalled {
		t.Error("Create() made a REST call for an already-existing view (should be idempotent)")
	}
	if view.ID != "PVT_existing" {
		t.Errorf("Create() returned wrong view ID: %q", view.ID)
	}
	if view.Name != "Ready Items" {
		t.Errorf("Create() returned wrong view Name: %q", view.Name)
	}
}

func TestViewCreate_HeaderValidation(t *testing.T) {
	graphqlResp := `{"data": {"organization": {"projectV2": {"views": {"nodes": []}}}}}`
	restResp := `{"id": 1, "name": "Test", "layout": "TABLE_LAYOUT"}`

	var capturedAPIVersion string
	var capturedContentType string

	client, cleanup := mockViewServer(t, []string{graphqlResp}, func(w http.ResponseWriter, r *http.Request) {
		capturedAPIVersion = r.Header.Get("X-GitHub-Api-Version")
		capturedContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, restResp)
	})
	defer cleanup()

	svc := NewViewService(client, "nightgauge", 1)
	_, err := svc.Create(context.Background(), "Test", "table", nil)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	wantAPIVersion := "2026-03-10"
	if capturedAPIVersion != wantAPIVersion {
		t.Errorf("X-GitHub-Api-Version = %q, want %q", capturedAPIVersion, wantAPIVersion)
	}
	if capturedContentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", capturedContentType, "application/json")
	}
}

func TestViewCreate_RESTPathOrg(t *testing.T) {
	graphqlResp := `{"data": {"organization": {"projectV2": {"views": {"nodes": []}}}}}`
	restResp := `{"id": 1, "name": "Test", "layout": "BOARD_LAYOUT"}`

	var capturedPath string
	client, cleanup := mockViewServer(t, []string{graphqlResp}, func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, restResp)
	})
	defer cleanup()

	svc := NewViewService(client, "nightgauge", 3)
	_, err := svc.Create(context.Background(), "Test", "board", nil)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	wantPath := "/orgs/nightgauge/projectsV2/3/views"
	if capturedPath != wantPath {
		t.Errorf("REST path = %q, want %q", capturedPath, wantPath)
	}
}

func TestViewCreate_RESTPathUser(t *testing.T) {
	graphqlResp := `{"data": {"user": {"projectV2": {"views": {"nodes": []}}}}}`
	restResp := `{"id": 1, "name": "Test", "layout": "BOARD_LAYOUT"}`

	var capturedPath string
	client, cleanup := mockViewServer(t, []string{graphqlResp}, func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, restResp)
	})
	defer cleanup()

	svc := NewViewService(client, "markm", 4, OwnerTypeUser)
	_, err := svc.Create(context.Background(), "Test", "board", nil)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	wantPath := "/users/markm/projectsV2/4/views"
	if capturedPath != wantPath {
		t.Errorf("REST path = %q, want %q", capturedPath, wantPath)
	}
}

func TestViewCreate_RESTBodyIncludesFilter(t *testing.T) {
	graphqlResp := `{"data": {"organization": {"projectV2": {"views": {"nodes": []}}}}}`
	restResp := `{"id": 1, "name": "Ready", "layout": "BOARD_LAYOUT"}`

	var capturedBody map[string]interface{}
	client, cleanup := mockViewServer(t, []string{graphqlResp}, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, restResp)
	})
	defer cleanup()

	svc := NewViewService(client, "nightgauge", 1)
	filter := "status:Ready"
	_, err := svc.Create(context.Background(), "Ready", "board", &filter)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	if capturedBody["filter"] != "status:Ready" {
		t.Errorf("REST body filter = %v, want %q", capturedBody["filter"], "status:Ready")
	}
	if capturedBody["name"] != "Ready" {
		t.Errorf("REST body name = %v, want %q", capturedBody["name"], "Ready")
	}
	if capturedBody["layout"] != "board" {
		t.Errorf("REST body layout = %v, want %q", capturedBody["layout"], "board")
	}
}

func TestNormalizeLayout(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"BOARD_LAYOUT", "board"},
		{"TABLE_LAYOUT", "table"},
		{"ROADMAP_LAYOUT", "roadmap"},
		{"board_layout", "board"},
		{"table_layout", "table"},
		{"board", "board"},
		{"table", "table"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeLayout(tt.input)
			if got != tt.want {
				t.Errorf("normalizeLayout(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
