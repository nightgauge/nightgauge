package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/time/rate"

	"github.com/nightgauge/nightgauge/internal/forge"
)

func dependencyStateClient(t *testing.T, response string, query *string) *IssueService {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if query != nil {
			*query = req.Query
		}
		fmt.Fprint(w, response)
	}))
	t.Cleanup(srv.Close)
	c := NewClientWithURL("test-token", srv.URL)
	c.limiter = rate.NewLimiter(rate.Inf, 1)
	return NewIssueService(c)
}

func TestGetDependencyStates_MapsEveryOutcome(t *testing.T) {
	var query string
	svc := dependencyStateClient(t, `{"data":{"repository":{
		"i1":{"__typename":"Issue","state":"OPEN"},
		"i2":{"__typename":"Issue","state":"CLOSED"},
		"i3":{"__typename":"PullRequest","state":"OPEN","merged":false},
		"i4":{"__typename":"PullRequest","state":"MERGED","merged":true},
		"i5":{"__typename":"PullRequest","state":"CLOSED","merged":false},
		"i6":null,
		"i7":null,
		"i8":{"__typename":"Discussion"}
	}},"errors":[{"type":"NOT_FOUND","path":["repository","i6"],"message":"gone"}]}`, &query)

	got, err := svc.GetDependencyStates(context.Background(), "acme", "widgets", []int{8, 7, 6, 5, 4, 3, 2, 1, 1, 0})
	if err != nil {
		t.Fatalf("GetDependencyStates: %v", err)
	}
	want := map[int]string{
		1: forge.DepStateOpen, 2: forge.DepStateClosed, 3: forge.DepStatePROpen,
		4: forge.DepStatePRMerged, 5: forge.DepStatePRClosed, 6: forge.DepStateMissing,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("states = %+v, want %+v (a null alias with no NOT_FOUND has no verdict)", got, want)
	}
	if strings.Count(query, "issueOrPullRequest(number:") != 8 {
		t.Errorf("query does not read each number once:\n%s", query)
	}
	for _, field := range []string{"body", "labels", "title", "subIssues", "blockedBy", "blocking"} {
		if strings.Contains(query, field) {
			t.Errorf("query selects %s:\n%s", field, query)
		}
	}
}

func TestGetDependencyStates_DoubtFailsTheRead(t *testing.T) {
	for name, response := range map[string]string{
		"null repository":      `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","path":["repository"],"message":"no repo"}]}`,
		"alias forbidden":      `{"data":{"repository":{"i1":null}},"errors":[{"type":"FORBIDDEN","path":["repository","i1"],"message":"no"}]}`,
		"not found no alias":   `{"data":{"repository":{"i1":null}},"errors":[{"type":"NOT_FOUND","message":"no"}]}`,
		"empty without errors": `{"data":{"repository":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := dependencyStateClient(t, response, nil).GetDependencyStates(context.Background(), "acme", "widgets", []int{1})
			if err == nil {
				t.Fatalf("states = %+v, want an error", got)
			}
		})
	}
}

func TestGetDependencyStates_NoNumbersNoRequest(t *testing.T) {
	var query string
	got, err := dependencyStateClient(t, `{}`, &query).GetDependencyStates(context.Background(), "acme", "widgets", []int{0, -1})
	if err != nil || len(got) != 0 || query != "" {
		t.Fatalf("got %+v, err %v, query %q", got, err, query)
	}
}
