package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFindItemID_WrapsErrIssueNotOnBoard pins the sentinel at its source.
//
// The post-merge hook repairs a missing board row by matching this error with
// errors.Is (#691). Matching on message text would break silently the day the
// wording changed, and the repair would quietly revert to the warning-and-exit-0
// behaviour the issue was filed about — so the wrap is asserted here, on the
// real code path, rather than only in the hook's own fake.
func TestFindItemID_WrapsErrIssueNotOnBoard(t *testing.T) {
	// The issue exists but its projectItems connection contains no row for this
	// board — the ad-hoc-`gh issue create` shape. An empty data object decodes
	// to exactly that.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{}})
	}))
	defer srv.Close()

	p := NewProjectService(NewClientWithURL("test-token", srv.URL), "nightgauge", 3)

	err := p.SyncStatus(context.Background(), "nightgauge", "nightgauge", 722, "Done")
	if err == nil {
		t.Fatal("SyncStatus succeeded for an issue with no board row")
	}
	if !errors.Is(err, ErrIssueNotOnBoard) {
		t.Errorf("err = %v, want it to wrap ErrIssueNotOnBoard", err)
	}
}
