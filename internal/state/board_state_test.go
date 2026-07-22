package state

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// --- Mock GraphQL server ---

type mockConfig struct {
	hasStatus        bool
	hasPipelineStage bool
	itemID           string
	itemStatus       string // status returned by items query (for readItemStatus)
}

type mockOpt func(*mockConfig)

func withoutPipelineStage() mockOpt {
	return func(c *mockConfig) { c.hasPipelineStage = false }
}

func withItemStatus(itemID, status string) mockOpt {
	return func(c *mockConfig) {
		c.itemID = itemID
		c.itemStatus = status
	}
}

// mockGQL creates a test HTTP server responding to shurcooL/graphql client
// requests with pre-configured project field metadata and mutation acks.
func mockGQL(t *testing.T, opts ...mockOpt) *httptest.Server {
	t.Helper()
	cfg := &mockConfig{
		hasStatus:        true,
		hasPipelineStage: true,
		itemID:           "item1",
		itemStatus:       "In Progress",
	}
	for _, o := range opts {
		o(cfg)
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		var resp map[string]interface{}

		switch {
		case strings.Contains(req.Query, "mutation"):
			resp = mutationResp()
		case strings.Contains(req.Query, "fields("):
			resp = fieldsResp(cfg)
		case strings.Contains(req.Query, "items("):
			resp = itemsResp(cfg)
		default:
			http.Error(w, "unrecognized query", 400)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func mutationResp() map[string]interface{} {
	return map[string]interface{}{
		"data": map[string]interface{}{
			"updateProjectV2ItemFieldValue": map[string]interface{}{
				"clientMutationId": nil,
			},
		},
	}
}

func fieldsResp(cfg *mockConfig) map[string]interface{} {
	nodes := []interface{}{}
	if cfg.hasStatus {
		nodes = append(nodes, map[string]interface{}{
			"__typename": "ProjectV2SingleSelectField",
			"id":         "PVTSSF_status",
			"name":       "Status",
			"options": []interface{}{
				map[string]interface{}{"id": "opt_backlog", "name": "Backlog"},
				map[string]interface{}{"id": "opt_ready", "name": "Ready"},
				map[string]interface{}{"id": "opt_inprog", "name": "In Progress"},
				map[string]interface{}{"id": "opt_inrev", "name": "In Review"},
				map[string]interface{}{"id": "opt_done", "name": "Done"},
			},
		})
	}
	if cfg.hasPipelineStage {
		nodes = append(nodes, map[string]interface{}{
			"__typename": "ProjectV2Field",
			"id":         "PVTF_stage",
			"name":       "Pipeline Stage",
			"dataType":   "TEXT",
		})
	}

	return map[string]interface{}{
		"data": map[string]interface{}{
			"organization": map[string]interface{}{
				"projectV2": map[string]interface{}{
					"id": "PVT_test123",
					"fields": map[string]interface{}{
						"nodes": nodes,
					},
				},
			},
		},
	}
}

func itemsResp(cfg *mockConfig) map[string]interface{} {
	return map[string]interface{}{
		"data": map[string]interface{}{
			"organization": map[string]interface{}{
				"projectV2": map[string]interface{}{
					"id":    "PVT_test123",
					"title": "Test Project",
					"items": map[string]interface{}{
						"pageInfo": map[string]interface{}{
							"hasNextPage": false,
							"endCursor":   "",
						},
						"nodes": []interface{}{
							map[string]interface{}{
								"id": cfg.itemID,
								"content": map[string]interface{}{
									"__typename": "Issue",
									"number":     42,
									"title":      "Test Issue",
									"state":      "OPEN",
									"url":        "https://github.com/test/test/issues/42",
									"createdAt":  "2026-01-01T00:00:00Z",
									"updatedAt":  "2026-01-01T00:00:00Z",
									"labels":     map[string]interface{}{"nodes": []interface{}{}},
									"repository": map[string]interface{}{"nameWithOwner": "test/test"},
									"subIssues":  map[string]interface{}{"nodes": []interface{}{}},
									"blockedBy":  map[string]interface{}{"nodes": []interface{}{}},
									"blocking":   map[string]interface{}{"nodes": []interface{}{}},
									"parent":     map[string]interface{}{"number": 0, "title": ""},
								},
								"fieldValues": map[string]interface{}{
									"nodes": []interface{}{
										map[string]interface{}{
											"__typename": "ProjectV2ItemFieldSingleSelectValue",
											"name":       cfg.itemStatus,
											"field": map[string]interface{}{
												"name": "Status",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// --- Tests ---

func TestNewBoardStateService(t *testing.T) {
	client := gh.NewClientWithToken("test")
	svc := NewBoardStateService(client, "nightgauge", 5)
	if svc == nil {
		t.Fatal("NewBoardStateService returned nil")
	}
	if svc.projSvc == nil {
		t.Fatal("projSvc should not be nil")
	}
	if svc.owner != "nightgauge" {
		t.Errorf("owner = %q, want %q", svc.owner, "nightgauge")
	}
	if svc.projectNumber != 5 {
		t.Errorf("projectNumber = %d, want %d", svc.projectNumber, 5)
	}
}

func TestNewBoardStateService_UserOwnerType(t *testing.T) {
	client := gh.NewClientWithToken("test")
	svc := NewBoardStateService(client, "user1", 1, gh.OwnerTypeUser)
	if svc == nil {
		t.Fatal("NewBoardStateService returned nil")
	}
	if svc.ownerType != gh.OwnerTypeUser {
		t.Errorf("ownerType = %q, want %q", svc.ownerType, gh.OwnerTypeUser)
	}
}

func TestSetStatus_Delegates(t *testing.T) {
	srv := mockGQL(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateService(client, "testorg", 1)

	err := svc.SetStatus(context.Background(), "item1", StatusInProgress)
	if err != nil {
		t.Fatalf("SetStatus failed: %v", err)
	}
}

func TestUpdateStatus_Delegates(t *testing.T) {
	srv := mockGQL(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateService(client, "testorg", 1)

	err := svc.UpdateStatus(context.Background(), "item1", "Ready")
	if err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}
}

func TestSetPipelineStage_Delegates(t *testing.T) {
	srv := mockGQL(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateService(client, "testorg", 1)

	err := svc.SetPipelineStage(context.Background(), "item1", StageFeatureDev)
	if err != nil {
		t.Fatalf("SetPipelineStage failed: %v", err)
	}
}

func TestSetPipelineStage_FieldMissing(t *testing.T) {
	srv := mockGQL(t, withoutPipelineStage())
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateService(client, "testorg", 1)

	err := svc.SetPipelineStage(context.Background(), "item1", StageFeaturePlanning)
	if err != nil {
		t.Fatalf("expected nil for missing field, got: %v", err)
	}
}

func TestStartPipeline(t *testing.T) {
	srv := mockGQL(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateService(client, "testorg", 1)

	err := svc.StartPipeline(context.Background(), "item1", StageIssuePickup)
	if err != nil {
		t.Fatalf("StartPipeline failed: %v", err)
	}
}

func TestCompletePipeline_ClearStage(t *testing.T) {
	srv := mockGQL(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateService(client, "testorg", 1)

	err := svc.CompletePipeline(context.Background(), "item1", StatusDone)
	if err != nil {
		t.Fatalf("CompletePipeline failed: %v", err)
	}
}

func TestFailPipeline_SkipsInReview(t *testing.T) {
	srv := mockGQL(t, withItemStatus("item1", "In Review"))
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateService(client, "testorg", 1)

	changed, err := svc.FailPipeline(context.Background(), "item1", StatusReady)
	if err != nil {
		t.Fatalf("FailPipeline error: %v", err)
	}
	if changed {
		t.Error("FailPipeline should NOT change status when item is In Review")
	}
}

func TestFailPipeline_Reverts(t *testing.T) {
	srv := mockGQL(t, withItemStatus("item1", "In Progress"))
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateService(client, "testorg", 1)

	changed, err := svc.FailPipeline(context.Background(), "item1", StatusReady)
	if err != nil {
		t.Fatalf("FailPipeline error: %v", err)
	}
	if !changed {
		t.Error("FailPipeline should change status when item is In Progress")
	}
}

func TestConcurrentFieldWrites(t *testing.T) {
	srv := mockGQL(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateService(client, "testorg", 1)

	// Run with go test -race ./internal/state/... to detect race conditions.
	// Multiple goroutines write through the shared projSvc concurrently.
	var wg sync.WaitGroup
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = svc.SetStatus(ctx, "item1", StatusInProgress)
		}()
		go func() {
			defer wg.Done()
			_ = svc.SetPipelineStage(ctx, "item1", StageFeatureDev)
		}()
	}
	wg.Wait()
	// Test goal: no race detector violations
}
