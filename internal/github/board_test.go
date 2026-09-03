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

// TestBoardService_GetItem_SendsRepoScopedFilter pins the exact Projects V2
// `items(query:)` string GetItem asks the server for. The pre-fix shape
// (`owner/repo#N`) is not parsed by the project item search: GitHub answers
// it with zero rows and no error, so a Ready issue read as "not found on
// board" and `nightgauge run <issue>` never dispatched. The old test could
// not catch that — it stubbed an empty page and asserted ErrNotFound, which
// the broken query also produced. This one reads the variables the client
// actually sent.
func TestBoardService_GetItem_SendsRepoScopedFilter(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]interface{} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery, _ = req.Variables["query"].(string)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"organization": map[string]interface{}{
					"projectV2": map[string]interface{}{
						"id":    "PVT_X",
						"title": "Board",
						"items": map[string]interface{}{
							"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
							"nodes":    []interface{}{},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := NewClientWithURL("test-token", srv.URL)
	b := NewBoardService(c, "nightgauge", 3)
	_, _ = b.GetItem(context.Background(), "nightgauge", "nightgauge", 1116)

	const want = "repo:nightgauge/nightgauge #1116"
	if gotQuery != want {
		t.Fatalf("GetItem sent items(query: %q), want %q — the bare owner/repo#N slug is ignored by the Projects V2 search", gotQuery, want)
	}
}

// TestBoardService_GetItem_ReturnsMatchingRow verifies that a row the server
// returns for the filter comes back as a BoardItem with its identity fields
// populated, and that a same-number row from another repository on a shared
// board is not mistaken for it.
func TestBoardService_GetItem_ReturnsMatchingRow(t *testing.T) {
	issueNode := func(id, repo string, number int, title string) map[string]interface{} {
		return map[string]interface{}{
			"id": id,
			"content": map[string]interface{}{
				"__typename": "Issue",
				"number":     number,
				"title":      title,
				"state":      "OPEN",
				"url":        "https://github.com/" + repo + "/issues/1116",
				"repository": map[string]interface{}{"nameWithOwner": repo},
				"labels":     map[string]interface{}{"nodes": []interface{}{}},
				"subIssues":  map[string]interface{}{"nodes": []interface{}{}},
			},
			"fieldValues": map[string]interface{}{"nodes": []interface{}{}},
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"organization": map[string]interface{}{
					"projectV2": map[string]interface{}{
						"id":    "PVT_X",
						"title": "Board",
						"items": map[string]interface{}{
							"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
							"nodes": []interface{}{
								issueNode("PVTI_other", "nightgauge/nightgauge-platform", 1116, "same number, other repo"),
								issueNode("PVTI_want", "nightgauge/nightgauge", 1116, "the row we asked for"),
							},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := NewClientWithURL("test-token", srv.URL)
	b := NewBoardService(c, "nightgauge", 3)
	item, err := b.GetItem(context.Background(), "nightgauge", "nightgauge", 1116)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.ID != "PVTI_want" || item.Number != 1116 || item.Repo != "nightgauge/nightgauge" {
		t.Fatalf("GetItem returned %+v, want the nightgauge/nightgauge#1116 row (PVTI_want)", item)
	}
}
