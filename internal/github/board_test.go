package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/pkg/types"
)

func TestPriorityFromLabels(t *testing.T) {
	tests := []struct {
		labels []string
		want   types.Priority
	}{
		{[]string{"priority:critical"}, types.PriorityP0},
		{[]string{"priority:high"}, types.PriorityP1},
		{[]string{"priority:medium"}, types.PriorityP2},
		{[]string{"priority:low"}, types.PriorityP3},
		{[]string{"type:feature"}, ""},
		{[]string{"priority:high", "priority:critical"}, types.PriorityP1}, // first match wins
		{nil, ""},
	}

	for _, tt := range tests {
		got := priorityFromLabels(tt.labels)
		if got != tt.want {
			t.Errorf("priorityFromLabels(%v) = %q, want %q", tt.labels, got, tt.want)
		}
	}
}

func TestSizeFromLabels(t *testing.T) {
	tests := []struct {
		labels []string
		want   types.Size
	}{
		{[]string{"size:XS"}, types.SizeXS},
		{[]string{"size:S"}, types.SizeS},
		{[]string{"size:M"}, types.SizeM},
		{[]string{"size:L"}, types.SizeL},
		{[]string{"size:XL"}, types.SizeXL},
		{[]string{"type:feature"}, ""},
		{nil, ""},
	}

	for _, tt := range tests {
		got := sizeFromLabels(tt.labels)
		if got != tt.want {
			t.Errorf("sizeFromLabels(%v) = %q, want %q", tt.labels, got, tt.want)
		}
	}
}

func TestNewBoardService(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewBoardService(client, "nightgauge", 5)
	if svc == nil {
		t.Fatal("NewBoardService returned nil")
	}
	if svc.owner != "nightgauge" {
		t.Errorf("owner = %q, want %q", svc.owner, "nightgauge")
	}
	if svc.projectNumber != 5 {
		t.Errorf("projectNumber = %d, want %d", svc.projectNumber, 5)
	}
}

// TestHasTypeEpicLabel verifies the canonical-label check that gates IsEpic.
// An epic with the type:epic label must be flagged IsEpic=true even when it
// has zero sub-issues, so views render it as a group header instead of
// filtering it out (Issue #3329).
func TestHasTypeEpicLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"label present", []string{"type:epic", "priority:high"}, true},
		{"label only", []string{"type:epic"}, true},
		{"label absent", []string{"type:feature", "priority:high"}, false},
		{"epic-prefix non-match", []string{"type:epic-thing"}, false},
		{"empty labels", []string{}, false},
		{"nil labels", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasTypeEpicLabel(tt.labels); got != tt.want {
				t.Errorf("hasTypeEpicLabel(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}

// TestBoardService_GetItem_ReturnsNotFoundWhenItemAbsent verifies the new
// GetItem path emits forge.ErrNotFound when the issue is not on the bound
// board. Uses a stub GraphQL server returning an empty items list.
func TestBoardService_GetItem_ReturnsNotFoundWhenItemAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"organization": map[string]interface{}{
					"projectV2": map[string]interface{}{
						"id":    "PVT_X",
						"title": "Board",
						"items": map[string]interface{}{
							"pageInfo": map[string]interface{}{
								"hasNextPage": false,
								"endCursor":   "",
							},
							"nodes": []interface{}{},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := NewClientWithURL("test-token", srv.URL)
	b := NewBoardService(c, "nightgauge", 1)
	_, err := b.GetItem(context.Background(), "nightgauge", "nightgauge", 99)
	if !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestEmptyBoardItemsSerializesToArray verifies that an empty BoardItem slice
// serializes to JSON [] (not null). Go nil slices serialize to null, which
// breaks TypeScript callers that iterate the result. Issue #1888.
func TestEmptyBoardItemsSerializesToArray(t *testing.T) {
	// make([]T, 0) produces a non-nil empty slice → JSON []
	items := make([]types.BoardItem, 0)
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if string(data) != "[]" {
		t.Errorf("empty BoardItem slice serialized to %s, want []", string(data))
	}

	// Contrast: var items []T (nil slice) → JSON null — this is the bug we fixed
	var nilItems []types.BoardItem
	nilData, _ := json.Marshal(nilItems)
	if string(nilData) != "null" {
		t.Errorf("nil slice serialized to %s, expected null (this documents the Go behavior)", string(nilData))
	}
}
