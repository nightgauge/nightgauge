// Cross-forge contract test pinning that CIService.GetCheckStatus returns
// the same canonical State enum on both adapters for an equivalent input
// fixture. The github side is exercised via a stubbed GraphQL endpoint;
// the gitlab side via stubbed REST endpoints. Both adapters are wired
// through their public constructors.
package forge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/gitlab"
)

// stateParityCase pairs a logical "pipeline outcome" with the
// adapter-specific vocabulary each forge returns for that outcome.
type stateParityCase struct {
	name              string
	githubRollupState string // value of pr.commits.nodes[0].commit.statusCheckRollup.state
	gitlabPipeStatus  string // GitLab pipeline status JSON value
	wantState         string // canonical forge State the adapter must surface
}

// githubPRStubServer returns a minimal GraphQL stub that replies to the
// `pullRequest` query with a configurable StatusCheckRollup.State. The
// shurcooL/graphql client wraps responses in {"data": ...}, so we encode
// the matching shape.
func githubPRStubServer(t *testing.T, rollupState string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"repository": map[string]any{
					"pullRequest": map[string]any{
						"id":             "PR_1",
						"number":         42,
						"title":          "test",
						"body":           "",
						"state":          "OPEN",
						"headRefName":    "feat",
						"baseRefName":    "main",
						"url":            "https://github.com/o/r/pull/42",
						"mergeable":      "MERGEABLE",
						"isDraft":        false,
						"reviewDecision": "APPROVED",
						"additions":      0,
						"deletions":      0,
						"labels":         map[string]any{"nodes": []any{}},
						"commits": map[string]any{
							"nodes": []any{
								map[string]any{
									"commit": map[string]any{
										"statusCheckRollup": map[string]any{
											"state": rollupState,
										},
									},
								},
							},
						},
					},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// gitlabPipelinesStubServer returns a stub REST server that replies to
// the MR pipelines list, the by-ref pipelines list, and the per-pipeline
// jobs list. The pipelineStatus parameter controls the head pipeline
// status; jobs mirror it so the rollup semantics match.
func gitlabPipelinesStubServer(t *testing.T, pipelineStatus string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests/42/pipelines", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 99, "status": pipelineStatus, "sha": "abc"},
		})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 99, "status": pipelineStatus, "sha": "abc"},
		})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines/99/jobs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "name": "lint", "stage": "test", "status": pipelineStatus, "allow_failure": false},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestParityContract_CIGetCheckStatus pins the cross-forge contract that
// GetCheckStatus returns the same canonical State enum on GitHub and
// GitLab fixtures. Each row exercises one logical outcome (success,
// failure, pending) in each adapter's native vocabulary.
func TestParityContract_CIGetCheckStatus(t *testing.T) {
	cases := []stateParityCase{
		{name: "success", githubRollupState: "SUCCESS", gitlabPipeStatus: "success", wantState: "SUCCESS"},
		{name: "failure", githubRollupState: "FAILURE", gitlabPipeStatus: "failed", wantState: "FAILURE"},
		{name: "pending", githubRollupState: "PENDING", gitlabPipeStatus: "running", wantState: "PENDING"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ghSrv := githubPRStubServer(t, tc.githubRollupState)
			ghClient := github.NewClientWithURL("tok", ghSrv.URL)
			ghCI := github.NewCIService(ghClient)
			ghStatus, err := ghCI.GetCheckStatus(context.Background(), "o", "r", 42)
			if err != nil {
				t.Fatalf("github GetCheckStatus: %v", err)
			}

			glSrv := gitlabPipelinesStubServer(t, tc.gitlabPipeStatus)
			glClient := gitlab.NewClient(glSrv.URL, "tok")
			glCI := gitlab.NewCIService(glClient)
			glStatus, err := glCI.GetCheckStatus(context.Background(), "o", "r", 42)
			if err != nil {
				t.Fatalf("gitlab GetCheckStatus: %v", err)
			}

			if ghStatus.State != tc.wantState {
				t.Errorf("github State = %q, want %q", ghStatus.State, tc.wantState)
			}
			if glStatus.State != tc.wantState {
				t.Errorf("gitlab State = %q, want %q", glStatus.State, tc.wantState)
			}
			if ghStatus.State != glStatus.State {
				t.Errorf("cross-forge mismatch: github=%q gitlab=%q (logical %s)",
					ghStatus.State, glStatus.State, tc.name)
			}

			// IsTerminal must agree across forges for the same logical
			// outcome — the pr-merge gate keys on it.
			if ghStatus.IsTerminal != glStatus.IsTerminal {
				t.Errorf("IsTerminal mismatch: github=%v gitlab=%v (logical %s)",
					ghStatus.IsTerminal, glStatus.IsTerminal, tc.name)
			}
		})
	}
}

// TestParityContract_CIGetCheckStatus_VocabularyTable documents the
// 11→4 collapse the GitLab adapter performs and confirms each row maps
// to the same canonical state GitHub would emit for the equivalent
// outcome. This is the table-driven parity contract — the
// `pipelineStatusToForgeState` map in internal/gitlab/ci.go is the
// authoritative source; this test pins it cross-forge.
func TestParityContract_CIGetCheckStatus_VocabularyTable(t *testing.T) {
	// Each row: GitLab's vocabulary entry → canonical forge state. The
	// canonical states themselves are GitHub's vocabulary, so equivalent
	// GitHub rollup state would be the same string.
	rows := []struct {
		gitlab string
		want   string
	}{
		{"success", "SUCCESS"},
		{"failed", "FAILURE"},
		{"canceled", "ERROR"},
		{"skipped", "SUCCESS"},
		{"pending", "PENDING"},
		{"running", "PENDING"},
		{"created", "PENDING"},
		{"manual", "PENDING"},
		{"scheduled", "PENDING"},
	}

	for _, row := range rows {
		t.Run(fmt.Sprintf("gitlab=%s", row.gitlab), func(t *testing.T) {
			glSrv := gitlabPipelinesStubServer(t, row.gitlab)
			glClient := gitlab.NewClient(glSrv.URL, "tok")
			glCI := gitlab.NewCIService(glClient)
			glStatus, err := glCI.GetCheckStatus(context.Background(), "o", "r", 42)
			if err != nil {
				t.Fatalf("gitlab GetCheckStatus: %v", err)
			}
			if glStatus.State != row.want {
				t.Errorf("gitlab %q → %q, want %q", row.gitlab, glStatus.State, row.want)
			}

			// Cross-check: feeding the canonical GitHub vocabulary back
			// through the GitHub adapter must produce the same state.
			ghSrv := githubPRStubServer(t, row.want)
			ghClient := github.NewClientWithURL("tok", ghSrv.URL)
			ghCI := github.NewCIService(ghClient)
			ghStatus, err := ghCI.GetCheckStatus(context.Background(), "o", "r", 42)
			if err != nil {
				t.Fatalf("github GetCheckStatus: %v", err)
			}
			if ghStatus.State != row.want {
				t.Errorf("github %q → %q, want %q (round-trip)", row.want, ghStatus.State, row.want)
			}
		})
	}
}
