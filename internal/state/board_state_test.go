package state

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
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
		itemStatus:       "In progress", // provisioned spelling; see fieldsResp
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
			// Spelled exactly as gh.DefaultFieldSchema provisions the "Status"
			// column (#413) — "In progress"/"In review", not "In Progress"/
			// "In Review". Do not "correct" the casing back: with the legacy
			// spelling here every SetStatus in this file misses the exact-match
			// lookup and lands on SetSingleSelectField's case-insensitive
			// fallback, so the default fixture silently stops covering the
			// happy path and emits a [WARN] per write instead. The fold path is
			// covered deliberately and in isolation by
			// TestSetSingleSelectField_CaseInsensitiveFallback and friends in
			// internal/github/project_test.go; it does not need incidental
			// coverage here. TestMockBoardOptionsMatchProvisionedSchema below
			// pins this list so it cannot drift again.
			"options": []interface{}{
				map[string]interface{}{"id": "opt_backlog", "name": "Backlog"},
				map[string]interface{}{"id": "opt_ready", "name": "Ready"},
				map[string]interface{}{"id": "opt_inprog", "name": "In progress"},
				map[string]interface{}{"id": "opt_inrev", "name": "In review"},
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

// The constructor must wire BOTH services. This used to assert on owner /
// projectNumber / ownerType fields, which only restated the arguments it had
// just passed; the property that matters now is that neither half is left nil,
// because a nil board or project service is a nil-pointer panic at the first
// read or write rather than a visible construction error (#848).
func TestNewBoardStateServiceForClient_WiresBothServices(t *testing.T) {
	client := gh.NewClientWithToken("test")
	svc := NewBoardStateServiceForClient(client, "nightgauge", 5)
	if svc == nil {
		t.Fatal("NewBoardStateServiceForClient returned nil")
	}
	if svc.projSvc == nil {
		t.Error("projSvc is nil — every write would panic")
	}
	if svc.board == nil {
		t.Error("board is nil — GetPipelineStage and readItemStatus would panic")
	}
}

func TestNewBoardStateServiceForClient_UserOwnerType(t *testing.T) {
	client := gh.NewClientWithToken("test")
	svc := NewBoardStateServiceForClient(client, "user1", 1, gh.OwnerTypeUser)
	if svc == nil {
		t.Fatal("NewBoardStateServiceForClient returned nil")
	}
	if svc.projSvc == nil || svc.board == nil {
		t.Fatal("both services must be wired for a user-owned board too")
	}
}

// The injecting constructor is what lets the IPC daemon hand in cache-wrapped
// services. It must keep exactly what it was given — a constructor that
// rebuilt either half would put the daemon's writes back outside the wrapper,
// which is the defect #848 exists to close.
func TestNewBoardStateService_KeepsTheServicesItWasGiven(t *testing.T) {
	client := gh.NewClientWithToken("test")
	board := gh.NewBoardService(client, "nightgauge", 5)
	proj := gh.NewProjectService(client, "nightgauge", 5)

	svc := NewBoardStateService(board, proj)
	if svc.board != forge.BoardService(board) {
		t.Error("board service was replaced; wrapped services would be discarded")
	}
	if svc.projSvc != forge.ProjectService(proj) {
		t.Error("project service was replaced; wrapped services would be discarded")
	}
}

func TestSetStatus_Delegates(t *testing.T) {
	srv := mockGQL(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateServiceForClient(client, "testorg", 1)

	err := svc.SetStatus(context.Background(), "item1", StatusInProgress)
	if err != nil {
		t.Fatalf("SetStatus failed: %v", err)
	}
}

func TestUpdateStatus_Delegates(t *testing.T) {
	srv := mockGQL(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateServiceForClient(client, "testorg", 1)

	err := svc.UpdateStatus(context.Background(), "item1", "Ready")
	if err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}
}

func TestSetPipelineStage_Delegates(t *testing.T) {
	srv := mockGQL(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateServiceForClient(client, "testorg", 1)

	err := svc.SetPipelineStage(context.Background(), "item1", StageFeatureDev)
	if err != nil {
		t.Fatalf("SetPipelineStage failed: %v", err)
	}
}

func TestSetPipelineStage_FieldMissing(t *testing.T) {
	srv := mockGQL(t, withoutPipelineStage())
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateServiceForClient(client, "testorg", 1)

	err := svc.SetPipelineStage(context.Background(), "item1", StageFeaturePlanning)
	if err != nil {
		t.Fatalf("expected nil for missing field, got: %v", err)
	}
}

func TestStartPipeline(t *testing.T) {
	srv := mockGQL(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateServiceForClient(client, "testorg", 1)

	err := svc.StartPipeline(context.Background(), "item1", StageIssuePickup)
	if err != nil {
		t.Fatalf("StartPipeline failed: %v", err)
	}
}

func TestCompletePipeline_ClearStage(t *testing.T) {
	srv := mockGQL(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateServiceForClient(client, "testorg", 1)

	err := svc.CompletePipeline(context.Background(), "item1", StatusDone)
	if err != nil {
		t.Fatalf("CompletePipeline failed: %v", err)
	}
}

func TestFailPipeline_SkipsInReview(t *testing.T) {
	srv := mockGQL(t, withItemStatus("item1", "In Review"))
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateServiceForClient(client, "testorg", 1)

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
	svc := NewBoardStateServiceForClient(client, "testorg", 1)

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
	svc := NewBoardStateServiceForClient(client, "testorg", 1)

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

// TestMockBoardOptionsMatchProvisionedSchema pins this file's mock "Status"
// options to the labels gh.DefaultFieldSchema actually provisions (#413).
//
// Without this pin the fixture drifts silently in the one direction that
// still passes: SetSingleSelectField falls back to a case-insensitive match,
// so a mock spelling the column "In Progress" keeps every test in this file
// green while quietly routing all of them through the fallback instead of the
// exact-match path they are meant to exercise. The only visible symptom is a
// [WARN] line per write buried in `go test -v` output — which is exactly the
// kind of signal that gets read as background noise. Asserting equality
// against the provisioner turns that silent drift into a failure, and keeps
// the fold covered only where it is covered on purpose
// (internal/github/project_test.go).
func TestMockBoardOptionsMatchProvisionedSchema(t *testing.T) {
	var provisioned []string
	for _, f := range gh.DefaultFieldSchema().SingleSelectFields {
		if f.Name == "Status" {
			for _, o := range f.Options {
				provisioned = append(provisioned, o.Name)
			}
		}
	}
	if len(provisioned) == 0 {
		t.Fatal("DefaultFieldSchema provisions no \"Status\" single-select options")
	}

	statusField := findMockField(t, "Status")
	rawOptions, ok := statusField["options"].([]interface{})
	if !ok {
		t.Fatalf("mock \"Status\" field has no options list: %#v", statusField)
	}

	mocked := make([]string, 0, len(rawOptions))
	for _, o := range rawOptions {
		opt, ok := o.(map[string]interface{})
		if !ok {
			t.Fatalf("mock option is not an object: %#v", o)
		}
		name, ok := opt["name"].(string)
		if !ok {
			t.Fatalf("mock option has no string name: %#v", opt)
		}
		mocked = append(mocked, name)
	}

	sort.Strings(provisioned)
	sort.Strings(mocked)
	if !slices.Equal(provisioned, mocked) {
		t.Errorf("mockGQL \"Status\" options %q do not match the labels DefaultFieldSchema provisions %q — "+
			"writes in this file will resolve through SetSingleSelectField's case-insensitive fallback "+
			"instead of an exact match, so these tests stop covering the provisioned-board path",
			mocked, provisioned)
	}
}

// findMockField returns the fieldsResp node for the named field, failing the
// test if the mock does not describe it.
func findMockField(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	resp := fieldsResp(&mockConfig{hasStatus: true, hasPipelineStage: true})

	data, _ := resp["data"].(map[string]interface{})
	org, _ := data["organization"].(map[string]interface{})
	project, _ := org["projectV2"].(map[string]interface{})
	fields, _ := project["fields"].(map[string]interface{})
	nodes, _ := fields["nodes"].([]interface{})
	if len(nodes) == 0 {
		t.Fatalf("fieldsResp returned no field nodes; shape changed: %#v", resp)
	}

	for _, n := range nodes {
		node, ok := n.(map[string]interface{})
		if !ok {
			continue
		}
		if node["name"] == name {
			return node
		}
	}
	t.Fatalf("fieldsResp describes no %q field", name)
	return nil
}
