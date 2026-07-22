package github

import (
	"context"
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
