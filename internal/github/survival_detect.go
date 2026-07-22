package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

// survivalExecGh is the indirection point for `gh`-backed survival detection so
// tests can stub GitHub CLI calls without a real binary (mirrors the
// reconcileExecGh / recovery.execGh pattern). It runs the real `gh` by default.
var survivalExecGh = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "gh", args...).Output()
}

// SurvivalDetector is the deterministic, GitHub-backed implementation of
// survival.Detector. It establishes two ground-truth signals for a merged PR:
//
//   - REVERT: a commit on the base branch whose message contains
//     `This reverts commit <merge_sha>` (git's default revert message). Pure
//     string match, highest confidence.
//   - BREAKAGE: an ancestry-correlated main-CI failure — a check that is FAILING
//     at a descendant of the merge commit but was SUCCESS at the merge commit
//     itself (spike #4134 §1.3). Conservative by construction: if the descendant
//     or the green-at-merge baseline cannot be positively established, it reports
//     NO breakage. It never treats "main is red" as proof on its own.
type SurvivalDetector struct{}

// NewSurvivalDetector constructs a SurvivalDetector.
func NewSurvivalDetector() *SurvivalDetector { return &SurvivalDetector{} }

// Observe implements survival.Detector. Revert detection takes precedence over
// breakage (it is the higher-confidence signal); both are best-effort and any
// transport error is returned so the sweep leaves the record pending.
func (d *SurvivalDetector) Observe(ctx context.Context, rec survival.Record) (survival.Observation, error) {
	owner, repo, ok := splitSurvivalRepo(rec.Repo)
	if !ok {
		return survival.Observation{}, fmt.Errorf("survival detect: malformed repo %q", rec.Repo)
	}
	base := rec.BaseRef
	if base == "" {
		base = survival.DefaultBaseRef
	}

	// --- Revert detection (primary) ------------------------------------------
	revertSHA, err := d.findRevert(ctx, owner, repo, base, rec.MergeCommitSHA, rec.MergedAt)
	if err != nil {
		return survival.Observation{}, err
	}
	if revertSHA != "" {
		return survival.Observation{RevertFound: true, RevertSHA: revertSHA}, nil
	}

	// --- Breakage detection (conservative) -----------------------------------
	broke, detail, err := d.findAncestryBreakage(ctx, owner, repo, base, rec.MergeCommitSHA)
	if err != nil {
		return survival.Observation{}, err
	}
	return survival.Observation{Broke: broke, BrokeDetail: detail}, nil
}

// findRevert scans base-branch commits since the merge for a revert of mergeSHA.
// Returns the reverting commit SHA, or "" when none is found.
func (d *SurvivalDetector) findRevert(ctx context.Context, owner, repo, base, mergeSHA, since string) (string, error) {
	args := []string{"api", "-X", "GET",
		fmt.Sprintf("repos/%s/%s/commits", owner, repo),
		"-f", "sha=" + base,
		"-f", "per_page=100",
	}
	if since != "" {
		args = append(args, "-f", "since="+since)
	}
	out, err := survivalExecGh(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("survival detect: list commits: %w", err)
	}
	var commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	if jsonErr := json.Unmarshal(out, &commits); jsonErr != nil {
		return "", fmt.Errorf("survival detect: parse commits: %w", jsonErr)
	}
	needle := "This reverts commit " + mergeSHA
	for _, c := range commits {
		if c.SHA == mergeSHA {
			continue // the merge itself is not its own revert
		}
		if strings.Contains(c.Commit.Message, needle) {
			return c.SHA, nil
		}
	}
	return "", nil
}

// findAncestryBreakage reports a main-CI regression attributable to the merge:
// a check FAILING at the base HEAD (a descendant of the merge on the linear
// squash-merged main) whose same-named check was SUCCESS at the merge commit.
// Returns (false, "", nil) — no signal — whenever the descendant or baseline
// cannot be positively established.
func (d *SurvivalDetector) findAncestryBreakage(ctx context.Context, owner, repo, base, mergeSHA string) (bool, string, error) {
	headSHA, err := d.refSHA(ctx, owner, repo, base)
	if err != nil {
		return false, "", err
	}
	// No descendant yet (HEAD is the merge itself) → cannot attribute breakage.
	if headSHA == "" || headSHA == mergeSHA {
		return false, "", nil
	}

	baselineSuccess, err := d.successChecks(ctx, owner, repo, mergeSHA)
	if err != nil {
		return false, "", err
	}
	// No green-at-merge baseline → cannot prove the merge introduced anything.
	if len(baselineSuccess) == 0 {
		return false, "", nil
	}

	failingNow, err := d.failingChecks(ctx, owner, repo, headSHA)
	if err != nil {
		return false, "", err
	}
	for _, name := range failingNow {
		if baselineSuccess[name] {
			return true, fmt.Sprintf("check %q green@%s failing@%s", name, shortSHA(mergeSHA), shortSHA(headSHA)), nil
		}
	}
	return false, "", nil
}

// refSHA resolves a ref (branch) to its current commit SHA.
func (d *SurvivalDetector) refSHA(ctx context.Context, owner, repo, ref string) (string, error) {
	out, err := survivalExecGh(ctx, "api",
		fmt.Sprintf("repos/%s/%s/commits/%s", owner, repo, ref),
		"--jq", ".sha")
	if err != nil {
		return "", fmt.Errorf("survival detect: resolve ref %q: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// successChecks returns the set of check-run names that concluded "success" at
// the given commit SHA.
func (d *SurvivalDetector) successChecks(ctx context.Context, owner, repo, sha string) (map[string]bool, error) {
	runs, err := d.checkRuns(ctx, owner, repo, sha)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, r := range runs {
		if r.Conclusion == "success" {
			set[r.Name] = true
		}
	}
	return set, nil
}

// failingChecks returns the names of check-runs that concluded in a failing
// state (failure / timed_out) at the given commit SHA.
func (d *SurvivalDetector) failingChecks(ctx context.Context, owner, repo, sha string) ([]string, error) {
	runs, err := d.checkRuns(ctx, owner, repo, sha)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, r := range runs {
		if r.Conclusion == "failure" || r.Conclusion == "timed_out" {
			names = append(names, r.Name)
		}
	}
	return names, nil
}

type checkRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// checkRuns fetches the check-runs for a commit SHA.
func (d *SurvivalDetector) checkRuns(ctx context.Context, owner, repo, sha string) ([]checkRun, error) {
	out, err := survivalExecGh(ctx, "api",
		fmt.Sprintf("repos/%s/%s/commits/%s/check-runs", owner, repo, sha),
		"-H", "Accept: application/vnd.github+json",
		"--jq", "{check_runs: [.check_runs[] | {name, status, conclusion}]}")
	if err != nil {
		return nil, fmt.Errorf("survival detect: check-runs for %s: %w", shortSHA(sha), err)
	}
	var resp struct {
		CheckRuns []checkRun `json:"check_runs"`
	}
	if jsonErr := json.Unmarshal(out, &resp); jsonErr != nil {
		return nil, fmt.Errorf("survival detect: parse check-runs: %w", jsonErr)
	}
	return resp.CheckRuns, nil
}

func splitSurvivalRepo(full string) (owner, repo string, ok bool) {
	parts := strings.Split(full, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
