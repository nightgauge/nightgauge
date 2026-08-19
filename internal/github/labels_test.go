package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNewLabelService(t *testing.T) {
	client := NewClientWithToken("test-token")
	svc := NewLabelService(client, "nightgauge", "nightgauge")
	if svc == nil {
		t.Fatal("NewLabelService returned nil")
	}
	if svc.client != client {
		t.Error("LabelService.client is not the provided client")
	}
	if svc.owner != "nightgauge" {
		t.Errorf("LabelService.owner = %q, want %q", svc.owner, "nightgauge")
	}
	if svc.repo != "nightgauge" {
		t.Errorf("LabelService.repo = %q, want %q", svc.repo, "nightgauge")
	}
}

func TestLabelList(t *testing.T) {
	listResp := `{
		"data": {
			"repository": {
				"labels": {
					"nodes": [
						{"id": "MDU6TGFiZWwx", "name": "bug", "description": "Something wrong", "color": "d73a4a"},
						{"id": "MDU6TGFiZWwy", "name": "feature", "description": "New feature", "color": "a2eeef"}
					]
				}
			}
		}
	}`

	client, cleanup := mockGraphQLServer(t, listResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	labels, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("List() returned %d labels, want 2", len(labels))
	}
	if labels[0].Name != "bug" {
		t.Errorf("labels[0].Name = %q, want %q", labels[0].Name, "bug")
	}
	if labels[0].ID != "MDU6TGFiZWwx" {
		t.Errorf("labels[0].ID = %q, want %q", labels[0].ID, "MDU6TGFiZWwx")
	}
	if labels[0].Color != "d73a4a" {
		t.Errorf("labels[0].Color = %q, want %q", labels[0].Color, "d73a4a")
	}
	if labels[1].Name != "feature" {
		t.Errorf("labels[1].Name = %q, want %q", labels[1].Name, "feature")
	}
}

func TestLabelList_Empty(t *testing.T) {
	listResp := `{"data": {"repository": {"labels": {"nodes": []}}}}`

	client, cleanup := mockGraphQLServer(t, listResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	labels, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(labels) != 0 {
		t.Errorf("List() returned %d labels, want 0", len(labels))
	}
}

func TestLabelCreate_New(t *testing.T) {
	// mockGraphQLServer sequences responses:
	// 1st call = List() (listLabelsQuery) → empty
	// 2nd call = GetRepositoryID → repo node ID
	// 3rd call = createLabel mutation → new label
	listResp := `{"data": {"repository": {"labels": {"nodes": []}}}}`
	repoIDResp := `{"data": {"repository": {"id": "R_kgDOHNxxx"}}}`
	createResp := `{
		"data": {
			"createLabel": {
				"label": {
					"id": "MDU6TGFiZWwz",
					"name": "priority:critical",
					"description": "Critical priority",
					"color": "ff0000"
				}
			}
		}
	}`

	client, cleanup := mockGraphQLServer(t, listResp, repoIDResp, createResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	label, err := svc.Create(context.Background(), "priority:critical", "Critical priority", "ff0000")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if label.ID != "MDU6TGFiZWwz" {
		t.Errorf("Create() ID = %q, want %q", label.ID, "MDU6TGFiZWwz")
	}
	if label.Name != "priority:critical" {
		t.Errorf("Create() Name = %q, want %q", label.Name, "priority:critical")
	}
	if label.Color != "ff0000" {
		t.Errorf("Create() Color = %q, want %q", label.Color, "ff0000")
	}
}

func TestLabelCreate_Existing(t *testing.T) {
	// Create() with an existing label returns it without calling createLabel mutation.
	// Only one response needed: List() returns existing label.
	listResp := `{
		"data": {
			"repository": {
				"labels": {
					"nodes": [
						{"id": "MDU6TGFiZWwx", "name": "bug", "description": "A bug", "color": "d73a4a"}
					]
				}
			}
		}
	}`

	client, cleanup := mockGraphQLServer(t, listResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	label, err := svc.Create(context.Background(), "bug", "A bug", "d73a4a")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if label.ID != "MDU6TGFiZWwx" {
		t.Errorf("Create() returned wrong ID: %q", label.ID)
	}
	if label.Name != "bug" {
		t.Errorf("Create() returned wrong Name: %q", label.Name)
	}
}

func TestLabelCreate_DefaultColor(t *testing.T) {
	// When color is empty, Create() defaults to "cccccc".
	listResp := `{"data": {"repository": {"labels": {"nodes": []}}}}`
	repoIDResp := `{"data": {"repository": {"id": "R_kgDOHNxxx"}}}`
	createResp := `{
		"data": {
			"createLabel": {
				"label": {
					"id": "MDU6TGFiZWw5",
					"name": "new-label",
					"description": "",
					"color": "cccccc"
				}
			}
		}
	}`

	client, cleanup := mockGraphQLServer(t, listResp, repoIDResp, createResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	label, err := svc.Create(context.Background(), "new-label", "", "")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if label.Color != "cccccc" {
		t.Errorf("Create() Color = %q, want default %q", label.Color, "cccccc")
	}
}

func TestLabelDelete(t *testing.T) {
	deleteResp := `{"data": {"deleteLabel": {"clientMutationId": null}}}`

	client, cleanup := mockGraphQLServer(t, deleteResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	if err := svc.Delete(context.Background(), "MDU6TGFiZWwx"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}

// recordingGraphQLServer replays responses in order (repeating the last) and
// captures every request body, so a test can assert which operations actually
// reached the API — the only way to prove the idempotent path mutates nothing.
func recordingGraphQLServer(t *testing.T, bodies *[]string, responses ...string) (*Client, func()) {
	t.Helper()
	var mu sync.Mutex
	var callIdx int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		*bodies = append(*bodies, string(raw))
		idx := callIdx
		callIdx++
		mu.Unlock()
		if idx >= len(responses) {
			idx = len(responses) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, responses[idx])
	}))
	return NewClientWithURL("test-token", srv.URL), srv.Close
}

func labelListResp(entries string) string {
	return `{"data":{"repository":{"labels":{"nodes":[` + entries + `]}}}}`
}

const areaVscodeNode = `{"id":"LA_old","name":"area:vscode","description":"VS Code extension","color":"c5def5"}`

func TestLabelRename_PreservesNodeID(t *testing.T) {
	updateResp := `{"data":{"updateLabel":{"label":{"id":"LA_old","name":"component:vscode","description":"VS Code extension","color":"7057ff"}}}}`
	var bodies []string
	client, cleanup := recordingGraphQLServer(t, &bodies, labelListResp(areaVscodeNode), updateResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	label, err := svc.Rename(context.Background(), "area:vscode", "component:vscode", "", "7057ff")
	if err != nil {
		t.Fatalf("Rename() error: %v", err)
	}
	if label.Name != "component:vscode" {
		t.Errorf("Name = %q, want %q", label.Name, "component:vscode")
	}
	// The whole point of rename-over-recreate: the node ID is unchanged, so
	// every issue carrying the label stays labelled.
	if label.ID != "LA_old" {
		t.Errorf("ID = %q, want the original %q — a new ID means issues were detached", label.ID, "LA_old")
	}
	if label.Color != "7057ff" {
		t.Errorf("Color = %q, want %q", label.Color, "7057ff")
	}
	if len(bodies) != 2 {
		t.Fatalf("made %d API calls, want 2 (list + update)", len(bodies))
	}
	if !strings.Contains(bodies[1], "updateLabel") {
		t.Errorf("second call was not updateLabel: %s", bodies[1])
	}
	// Description was not supplied, so it must not appear in the mutation at
	// all — sending an empty string would wipe the label's text.
	if strings.Contains(bodies[1], `"description"`) {
		t.Errorf("omitted --description leaked into the mutation and would clear it: %s", bodies[1])
	}
}

func TestLabelRename_AlreadyRenamedIsIdempotent(t *testing.T) {
	renamed := `{"id":"LA_old","name":"component:vscode","description":"VS Code extension","color":"7057ff"}`
	var bodies []string
	client, cleanup := recordingGraphQLServer(t, &bodies, labelListResp(renamed))
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	label, err := svc.Rename(context.Background(), "area:vscode", "component:vscode", "", "")
	if err != nil {
		t.Fatalf("Rename() on an already-renamed label must succeed, got: %v", err)
	}
	if label.Name != "component:vscode" {
		t.Errorf("Name = %q, want %q", label.Name, "component:vscode")
	}
	if len(bodies) != 1 {
		t.Fatalf("made %d API calls, want 1 (list only) — the idempotent path must not mutate", len(bodies))
	}
}

func TestLabelRename_TargetNameOccupied(t *testing.T) {
	occupied := `{"id":"LA_new","name":"component:vscode","description":"","color":"7057ff"}`
	var bodies []string
	client, cleanup := recordingGraphQLServer(t, &bodies, labelListResp(areaVscodeNode+","+occupied))
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	_, err := svc.Rename(context.Background(), "area:vscode", "component:vscode", "", "")
	if err == nil {
		t.Fatal("Rename() onto an occupied name must fail rather than silently merge")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want it to name the collision", err)
	}
	if len(bodies) != 1 {
		t.Errorf("made %d API calls, want 1 (list only) — a rejected rename must not mutate", len(bodies))
	}
}

func TestLabelRename_NotFound(t *testing.T) {
	var bodies []string
	client, cleanup := recordingGraphQLServer(t, &bodies, labelListResp(""))
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	_, err := svc.Rename(context.Background(), "area:vscode", "component:vscode", "", "")
	if err == nil {
		t.Fatal("Rename() of a missing label must fail")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want a not-found message", err)
	}
}

func TestLabelRename_SameNameUpdatesColorOnly(t *testing.T) {
	updateResp := `{"data":{"updateLabel":{"label":{"id":"LA_old","name":"area:vscode","description":"VS Code extension","color":"7057ff"}}}}`
	var bodies []string
	client, cleanup := recordingGraphQLServer(t, &bodies, labelListResp(areaVscodeNode), updateResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	label, err := svc.Rename(context.Background(), "area:vscode", "area:vscode", "", "7057ff")
	if err != nil {
		t.Fatalf("Rename() to the same name must be allowed for a colour-only edit: %v", err)
	}
	if label.Color != "7057ff" {
		t.Errorf("Color = %q, want %q", label.Color, "7057ff")
	}
}

func TestLabelRename_RequiresBothNames(t *testing.T) {
	svc := NewLabelService(NewClientWithToken("t"), "nightgauge", "nightgauge")
	if _, err := svc.Rename(context.Background(), "", "component:vscode", "", ""); err == nil {
		t.Error("empty old name must be rejected")
	}
	if _, err := svc.Rename(context.Background(), "area:vscode", "", "", ""); err == nil {
		t.Error("empty new name must be rejected")
	}
}
