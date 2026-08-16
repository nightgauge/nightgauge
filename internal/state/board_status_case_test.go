package state

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// TestBoardStatusConstantsMatchProvisionedOptions pins every BoardStatus
// constant to exactly one of the option labels DefaultFieldSchema's "Status"
// field actually provisions on a nightgauge-created board (#413).
//
// This is deliberately a table driven off the real provisioner output, not a
// hardcoded expected-string comparison: an equality-to-literal test would
// only catch today's mismatch, and would happily pass again if a future
// provisioner change (or a hand-edit to DefaultFieldSchema) drifted the
// option labels away from these constants in some new way. Deriving the
// expectation from DefaultFieldSchema() itself means the two definitions can
// never silently diverge again — a change on either side that breaks the
// pairing fails this test immediately.
func TestBoardStatusConstantsMatchProvisionedOptions(t *testing.T) {
	schema := gh.DefaultFieldSchema()

	var provisioned []string
	for _, f := range schema.SingleSelectFields {
		if f.Name != "Status" {
			continue
		}
		for _, opt := range f.Options {
			provisioned = append(provisioned, opt.Name)
		}
	}
	if len(provisioned) == 0 {
		t.Fatal("DefaultFieldSchema() has no \"Status\" field options — cannot cross-check BoardStatus constants")
	}

	allowed := make(map[string]bool, len(provisioned))
	for _, name := range provisioned {
		allowed[name] = true
	}

	constants := []struct {
		name  string
		value BoardStatus
	}{
		{"StatusBacklog", StatusBacklog},
		{"StatusReady", StatusReady},
		{"StatusInProgress", StatusInProgress},
		{"StatusInReview", StatusInReview},
		{"StatusDone", StatusDone},
	}

	for _, c := range constants {
		t.Run(c.name, func(t *testing.T) {
			if !allowed[string(c.value)] {
				t.Errorf("state.%s = %q is not one of the options DefaultFieldSchema provisions for \"Status\" (%v) — "+
					"a write using this constant will fail an exact-match lookup on a nightgauge-provisioned board",
					c.name, string(c.value), provisioned)
			}
		})
	}
}

// newProvisionedStatusServer returns a mock GraphQL server whose "Status"
// single-select field options are exactly what DefaultFieldSchema provisions
// today (Backlog, Ready, "In progress", "In review", Done) — the
// nightgauge-provisioned-board shape from #413 — and that acks any
// updateProjectV2ItemFieldValue mutation.
//
// This is a deliberately separate, minimal fixture from board_state_test.go's
// mockGQL/fieldsResp (that file is outside this fix's ownership bar and its
// "In Progress"/"In Review" fixture is intentionally left as-is here — see
// TestSetSingleSelectField_ProvisionedBoardStatusConstants in
// internal/github/project_test.go and the #413 PR notes for why).
func newProvisionedStatusServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var resp map[string]interface{}
		switch {
		case strings.Contains(req.Query, "mutation"):
			resp = map[string]interface{}{
				"data": map[string]interface{}{
					"updateProjectV2ItemFieldValue": map[string]interface{}{
						"clientMutationId": nil,
					},
				},
			}
		case strings.Contains(req.Query, "fields("):
			resp = map[string]interface{}{
				"data": map[string]interface{}{
					"organization": map[string]interface{}{
						"projectV2": map[string]interface{}{
							"id": "PVT_provisioned",
							"fields": map[string]interface{}{
								"nodes": []interface{}{
									map[string]interface{}{
										"__typename": "ProjectV2SingleSelectField",
										"id":         "PVTSSF_status",
										"name":       "Status",
										"options": []interface{}{
											map[string]interface{}{"id": "opt_backlog", "name": "Backlog"},
											map[string]interface{}{"id": "opt_ready", "name": "Ready"},
											map[string]interface{}{"id": "opt_inprog", "name": "In progress"},
											map[string]interface{}{"id": "opt_inrev", "name": "In review"},
											map[string]interface{}{"id": "opt_done", "name": "Done"},
										},
									},
								},
							},
						},
					},
				},
			}
		default:
			http.Error(w, "unrecognized query: "+req.Query, http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// TestBoardStateService_SetStatus_ProvisionedBoardOptions proves the actual
// production round trip for #413: BoardStateService.SetStatus (the primitive
// StartPipeline/CompletePipeline/FailPipeline all build on) with every
// state.BoardStatus constant, against a board provisioned exactly the way
// DefaultFieldSchema provisions it.
//
// Before the fix, StatusInProgress/StatusInReview held "In Progress"/
// "In Review" while the provisioner wrote "In progress"/"In review" — this
// test failed with "option ... not found" for both against unfixed source.
func TestBoardStateService_SetStatus_ProvisionedBoardOptions(t *testing.T) {
	srv := newProvisionedStatusServer(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test", srv.URL)
	svc := NewBoardStateService(client, "testorg", 1)

	statuses := []BoardStatus{StatusBacklog, StatusReady, StatusInProgress, StatusInReview, StatusDone}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			if err := svc.SetStatus(context.Background(), "item1", status); err != nil {
				t.Errorf("SetStatus(%q) against a nightgauge-provisioned board = %v, want nil", status, err)
			}
		})
	}
}
