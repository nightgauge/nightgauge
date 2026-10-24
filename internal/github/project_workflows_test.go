package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func workflowsServer(t *testing.T, wantRoot, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), wantRoot+(`(login`)) {
			t.Errorf("query does not target %s: %s", wantRoot, b)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListProjectWorkflows(t *testing.T) {
	nodes := `{"nodes":[{"name":"Item closed","enabled":true},{"name":"Auto-archive items","enabled":false}]}`
	cases := []struct {
		name      string
		ownerType OwnerType
		root      string
		body      string
	}{
		{"org", OwnerTypeOrg, "organization", `{"data":{"organization":{"projectV2":{"workflows":` + nodes + `}}}}`},
		{"user", OwnerTypeUser, "user", `{"data":{"user":{"projectV2":{"workflows":` + nodes + `}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := workflowsServer(t, tc.root, tc.body)
			got, err := ListProjectWorkflows(context.Background(), NewClientWithURL("tok", srv.URL), "o", tc.ownerType, 7)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0] != (ProjectWorkflow{"Item closed", true}) || got[1] != (ProjectWorkflow{"Auto-archive items", false}) {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestListProjectWorkflowsError(t *testing.T) {
	srv := workflowsServer(t, "organization", `{"errors":[{"message":"Could not resolve to a ProjectV2"}]}`)
	_, err := ListProjectWorkflows(context.Background(), NewClientWithURL("tok", srv.URL), "o", OwnerTypeOrg, 7)
	if err == nil || !strings.Contains(err.Error(), "project 7") {
		t.Errorf("err = %v", err)
	}
}
