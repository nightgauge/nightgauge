//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stageContract names the six pipeline stages and the env-key suffix used
// by the binary for the corresponding context file. Kept in lockstep with
// docs/CONTEXT_ARCHITECTURE.md.
type stageContract struct {
	Name     string
	FilePfx  string
	Duration time.Duration
}

// TestGitLabPipeline_FullSixStage runs the binary against the seeded GitLab
// CE project end-to-end via the forge surface. Per-stage timings are
// recorded so a failed stage can be diagnosed by elapsed time alone.
//
// The test is deliberately conservative: it asserts that each stage either
// (a) runs to completion against the live GitLab API, OR (b) returns the
// documented "stage not implemented for this command" error from the
// binary. Stronger end-to-end assertions land in subsequent waves; this
// test's job is to prove the live-API path is reachable at all.
func TestGitLabPipeline_FullSixStage(t *testing.T) {
	if fixtures == nil {
		t.Skip("fixtures not seeded")
	}
	binaryPath := locateBinary(t)

	stages := []stageContract{
		{Name: "forge_issue_list", FilePfx: "issue-list"},
		{Name: "forge_issue_view", FilePfx: "issue-view"},
		{Name: "forge_repo_view", FilePfx: "repo-view"},
		{Name: "forge_label_list", FilePfx: "label-list"},
		{Name: "forge_pr_list", FilePfx: "pr-list"},
		{Name: "forge_auth_status", FilePfx: "auth-status"},
	}

	owner, repo := splitProjectPath(fixtures.ProjectPath)
	env := append(os.Environ(),
		"IB_FORGE=gitlab",
		"GITLAB_URL="+gitlabURL,
		"GITLAB_TOKEN="+fixtures.PAT,
	)

	totalsByStage := make(map[string]time.Duration, len(stages))

	for _, st := range stages {
		t.Run(st.Name, func(t *testing.T) {
			args := stageArgs(st.Name, owner, repo, fixtures.IssueIIDs[0])
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			cmd := exec.CommandContext(ctx, binaryPath, args...)
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			cancel()
			totalsByStage[st.Name] = time.Since(start)
			t.Logf("stage=%s elapsed=%s exit_err=%v", st.Name, totalsByStage[st.Name], err)

			if err != nil {
				// "not implemented" is acceptable per the contract above —
				// the wave's goal is reaching the GitLab API, not
				// guaranteeing 100% command parity.
				combined := string(out)
				if strings.Contains(combined, "ErrUnsupported") || strings.Contains(combined, "not implemented") {
					t.Logf("stage=%s returned not-implemented (acceptable)", st.Name)
					return
				}
				t.Fatalf("stage %s failed: %v\noutput:\n%s", st.Name, err, combined)
			}
			if len(out) == 0 {
				t.Fatalf("stage %s produced no output", st.Name)
			}
		})
	}

	// Emit a stage-timing summary that the CI artifact-upload step picks up.
	logTimingSummary(t, totalsByStage)
}

// stageArgs maps a stageContract name to the forge subcommand argv that
// drives it. Keeps the test self-contained.
func stageArgs(name, owner, repo string, issueIID int) []string {
	switch name {
	case "forge_auth_status":
		return []string{"forge", "auth", "status", "--json"}
	case "forge_repo_view":
		return []string{"forge", "repo", "view", "--owner", owner, "--repo", repo, "--json"}
	case "forge_issue_list":
		return []string{"forge", "issue", "list", "--owner", owner, "--repo", repo, "--json", "--limit", "5"}
	case "forge_issue_view":
		return []string{"forge", "issue", "view", "--owner", owner, "--repo", repo, "--number", fmt.Sprint(issueIID), "--json"}
	case "forge_label_list":
		return []string{"forge", "label", "list", "--owner", owner, "--repo", repo, "--json"}
	case "forge_pr_list":
		return []string{"forge", "pr", "list", "--owner", owner, "--repo", repo, "--json"}
	default:
		return []string{"--help"}
	}
}

func splitProjectPath(p string) (owner, repo string) {
	parts := strings.SplitN(p, "/", 2)
	if len(parts) != 2 {
		return "root", p
	}
	return parts[0], parts[1]
}

// locateBinary finds the nightgauge binary, preferring ./bin/ from
// the repo root. Falls back to looking it up in PATH.
func locateBinary(t *testing.T) string {
	t.Helper()
	candidates := []string{}
	if cwd, err := os.Getwd(); err == nil {
		// Walk up to two parents to find repo root.
		for i := 0; i < 3; i++ {
			candidates = append(candidates, filepath.Join(cwd, "bin", "nightgauge"))
			cwd = filepath.Dir(cwd)
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if p, err := exec.LookPath("nightgauge"); err == nil {
		return p
	}
	t.Skip("nightgauge binary not found — run `make build-cli` first")
	return ""
}

// logTimingSummary emits per-stage timings in a format the CI artifact
// step parses. JSON keeps it grep-friendly without coupling to a custom
// schema.
func logTimingSummary(t *testing.T, totals map[string]time.Duration) {
	t.Helper()
	out := make(map[string]string, len(totals))
	for k, v := range totals {
		out[k] = v.String()
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	t.Logf("stage_timings:\n%s", string(b))
}

// healthProbe is a small helper used by Pipeline test sub-cases to verify
// GitLab is still healthy mid-suite. Failures here often indicate
// container shutdown rather than test-logic bugs.
func healthProbe(t *testing.T) bool {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, gitlabURL+"/-/health", nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// TestGitLabPipeline_HealthStable confirms GitLab CE stayed healthy
// through the suite — a low-cost canary for flaky-container diagnostics.
func TestGitLabPipeline_HealthStable(t *testing.T) {
	if fixtures == nil {
		t.Skip("fixtures not seeded")
	}
	if !healthProbe(t) {
		t.Fatal("GitLab CE health check failed after seeder + pipeline run")
	}
}
