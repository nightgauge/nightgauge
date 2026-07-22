package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// epicStubServer wires a programmable GET /issues/:iid handler that
// returns a configurable BlockedBy graph for each issue. PlanWaves issues
// one /issues/:iid call per number it was given, plus one /links call per
// issue; the stub mirrors both.
type epicStubServer struct {
	srv    *stubGitLabServer
	graph  map[int][]int  // iid → blocker iids
	titles map[int]string // iid → title (optional)
}

func newEpicStubServer(t *testing.T, graph map[int][]int) *epicStubServer {
	t.Helper()
	srv := newStubServer(t)
	es := &epicStubServer{srv: srv, graph: graph, titles: map[int]string{}}

	srv.mux.HandleFunc("/api/v4/license", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404"}`, 404)
	})

	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues/", func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/api/v4/projects/o/r/issues/"
		path := r.URL.Path
		if !strings.HasPrefix(path, prefix) {
			http.NotFound(w, r)
			return
		}
		rest := path[len(prefix):]
		if strings.HasSuffix(rest, "/links") {
			iid := parseIIDPrefix(rest)
			out := []rawIssueLink{}
			for _, blockerIID := range es.graph[iid] {
				out = append(out, rawIssueLink{
					ID:          int64(blockerIID * 10),
					IID:         blockerIID,
					IssueLinkID: int64(blockerIID * 10),
					Title:       fmt.Sprintf("Issue %d", blockerIID),
					State:       "opened",
					LinkType:    linkTypeIsBlockedBy,
				})
			}
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		// Bare /issues/<iid>
		iid := parseIntStrict(rest)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(MarshalRawIssue(iid, fmt.Sprintf("Issue %d", iid), nil)))
	})

	return es
}

func TestEpicService_PlanWaves_FiveIssueGraph(t *testing.T) {
	// Topology: 1 → 2,3 → 4 → 5
	graph := map[int][]int{
		1: {},
		2: {1},
		3: {1},
		4: {2, 3},
		5: {4},
	}
	es := newEpicStubServer(t, graph)
	c := NewClient(es.srv.srv.URL, "tok")
	svc := NewEpicService(c)

	res, err := svc.PlanWaves(context.Background(), "o", "r", []int{1, 2, 3, 4, 5})
	if err != nil {
		t.Fatalf("PlanWaves: %v", err)
	}
	if res.SubIssueCount != 5 {
		t.Errorf("SubIssueCount = %d, want 5", res.SubIssueCount)
	}
	if len(res.Waves) != 4 {
		t.Fatalf("Waves = %d, want 4", len(res.Waves))
	}
}

func TestEpicService_PlanWaves_EmptyGraph(t *testing.T) {
	es := newEpicStubServer(t, map[int][]int{})
	c := NewClient(es.srv.srv.URL, "tok")
	svc := NewEpicService(c)

	res, err := svc.PlanWaves(context.Background(), "o", "r", nil)
	if err != nil {
		t.Fatalf("PlanWaves: %v", err)
	}
	if res.SubIssueCount != 0 {
		t.Errorf("SubIssueCount = %d, want 0", res.SubIssueCount)
	}
	if len(res.Waves) != 0 {
		t.Errorf("Waves = %d, want 0", len(res.Waves))
	}
}

func TestEpicService_PlanWaves_IsolatedIssuesCollapseToSingleWave(t *testing.T) {
	// Three issues with no inter-dependencies → single wave
	graph := map[int][]int{
		10: {},
		11: {},
		12: {},
	}
	es := newEpicStubServer(t, graph)
	c := NewClient(es.srv.srv.URL, "tok")
	svc := NewEpicService(c)

	res, err := svc.PlanWaves(context.Background(), "o", "r", []int{10, 11, 12})
	if err != nil {
		t.Fatalf("PlanWaves: %v", err)
	}
	if len(res.Waves) != 1 {
		t.Fatalf("Waves = %d, want 1", len(res.Waves))
	}
	if len(res.Waves[0].Issues) != 3 {
		t.Errorf("wave 0 size = %d, want 3", len(res.Waves[0].Issues))
	}
}
