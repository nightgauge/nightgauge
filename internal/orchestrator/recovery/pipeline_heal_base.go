package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/heal"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

// PipelineHealBase is recovery action #7 (Issue #3683). It fires on pr-merge
// KindNoOp failures where the auto-fix loop's Step 2.5 has labelled the PR
// `pipeline-failed-inherited` — every failure on the PR also fails on the
// merge-base. Instead of looping LLM spend on a PR that can't possibly fix
// main, this action reads the baseline file, matches against the heal
// pattern registry, and opens a fix-PR against the base branch (possibly in
// another repo).
//
// Guardrails:
//
//   - per-repo throttle: at most cfg.MaxActivePerRepo open `pipeline-heal:auto`
//     PRs before this action declines to open another;
//   - 24h throttle: at most cfg.Max24hPerRepo heal PRs created in the trailing
//     24h window;
//   - first-occurrence gate: when cfg.RequireHumanFirst is true, the first time
//     a given pattern slug appears the action opens the PR with the
//     `pipeline-heal:needs-review` label instead of `pipeline-heal:auto`.
//
// All side effects flow through the existing execGh / execGit indirections so
// tests can stub them without spawning subprocesses. The action never claims
// recovery — main is not fixed by the time Execute returns — so Recovered is
// always false. The follow-up bucket is "human triage" because a real PR has
// been opened and needs review.
type PipelineHealBase struct {
	patterns      *heal.PatternRegistry
	cfg           heal.HealConfig
	workspaceRoot string
}

// NewPipelineHealBase builds the action with the canonical pattern registry
// and reads heal config from `<workspaceRoot>/.nightgauge/config.yaml`.
// workspaceRoot may be empty in tests; the action then uses default config
// and skips the baseline-file read (Execute returns FollowUpNoAction).
func NewPipelineHealBase(workspaceRoot string) *PipelineHealBase {
	return &PipelineHealBase{
		patterns:      heal.Default(),
		cfg:           heal.GetHealConfig(workspaceRoot),
		workspaceRoot: workspaceRoot,
	}
}

// Name implements RecoveryAction.
func (a *PipelineHealBase) Name() string { return "pipeline-heal-base" }

// Description implements RecoveryAction.
func (a *PipelineHealBase) Description() string {
	return "pr-merge punted with inherited-only failures — match a heal pattern and open a fix-PR against the base branch (possibly cross-repo)."
}

// Matches implements RecoveryAction. The predicate is pure: it walks the
// failure reason + evidence for the inherited-only marker that Step 2.5
// writes when it labels the PR `pipeline-failed-inherited`.
func (a *PipelineHealBase) Matches(failure StageFailure) bool {
	if failure.Stage != state.StagePRMerge {
		return false
	}
	if failure.GateKind != gates.KindNoOp {
		return false
	}
	if failure.PRNumber == 0 || failure.IssueNumber == 0 {
		return false
	}
	// Per-action self-cap: only one attempt per run. A subsequent attempt
	// would re-trigger the throttle anyway.
	if failure.AttemptOrdinal > 1 {
		return false
	}
	combined := strings.ToLower(failure.Reason + " " + strings.Join(failure.Evidence, " "))
	return strings.Contains(combined, "pipeline-failed-inherited") ||
		strings.Contains(combined, "inherited-only") ||
		strings.Contains(combined, "inherited failure")
}

// baselineFile is the on-disk shape of `auto-fix-baseline-{PR}.json`. The
// outer wrapper only models the fields this action reads — additional fields
// are tolerated.
type baselineFile struct {
	Failures []heal.BaselineFailure `json:"failures"`
}

// Execute implements RecoveryAction. See PipelineHealBase docstring for the
// flow.
func (a *PipelineHealBase) Execute(ctx context.Context, failure StageFailure) RecoveryResult {
	if failure.Workspace == "" {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   "no workspace path in StageFailure — cannot locate baseline file",
			FollowUp: FollowUpNoAction,
		}
	}

	// 1. Locate and parse baseline file.
	baselinePath := filepath.Join(failure.Workspace, ".nightgauge", "pipeline",
		fmt.Sprintf("auto-fix-baseline-%d.json", failure.PRNumber))
	data, err := os.ReadFile(baselinePath)
	if err != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("baseline file not found at %s — Step 2.5 did not write it", baselinePath),
			FollowUp: FollowUpNoAction,
		}
	}
	var bf baselineFile
	if jerr := json.Unmarshal(data, &bf); jerr != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("baseline file is not valid JSON: %s", truncate(jerr.Error(), 200)),
			FollowUp: FollowUpNoAction,
		}
	}

	// Filter to inherited failures; refuse to act on mixed batches (Step 2.5
	// should have routed the regression branch to the LLM fix loop already).
	var inherited []heal.BaselineFailure
	var regressionCount int
	for _, f := range bf.Failures {
		switch f.Classification {
		case "inherited":
			inherited = append(inherited, f)
		case "regression":
			regressionCount++
		}
	}
	if regressionCount > 0 {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("baseline file has %d regression(s) — mixed batch should be handled by the auto-fix loop, not heal-base",
				regressionCount),
			FollowUp: FollowUpNoAction,
		}
	}
	if len(inherited) == 0 {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   "baseline file has no inherited failures — nothing to heal",
			FollowUp: FollowUpNoAction,
		}
	}

	// 2. Determine target repo. Prefer the per-failure hint when all failures
	// agree on one; otherwise fall back to the current repo.
	targetRepo := unanimousTargetRepo(inherited)
	repoForGh := targetRepo
	if repoForGh == "" {
		repoForGh = failure.Repo
	}

	// 3. Throttle: active heal PRs.
	activeURLs, throttleErr := listHealPRs(ctx, repoForGh, "open")
	if throttleErr != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("gh pr list (open) failed: %s", truncate(throttleErr.Error(), 200)),
			FollowUp: FollowUpNoAction,
		}
	}
	if len(activeURLs) >= a.cfg.MaxActivePerRepo {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("throttled: %d active pipeline-heal:auto PRs >= max %d",
				len(activeURLs), a.cfg.MaxActivePerRepo),
			Evidence: append([]string{fmt.Sprintf("repo=%s", repoForGh)},
				prefixed("active_pr=", activeURLs)...),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// 4. Throttle: 24h window — count both open and closed heal PRs.
	recentURLs, recentErr := listHealPRsRecent(ctx, repoForGh, 24*time.Hour)
	if recentErr != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("gh pr list (recent) failed: %s", truncate(recentErr.Error(), 200)),
			FollowUp: FollowUpNoAction,
		}
	}
	if len(recentURLs) >= a.cfg.Max24hPerRepo {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("throttled: %d heal PRs in trailing 24h >= max %d",
				len(recentURLs), a.cfg.Max24hPerRepo),
			Evidence: append([]string{fmt.Sprintf("repo=%s", repoForGh)},
				prefixed("recent_pr=", recentURLs)...),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// 5. Pattern match.
	pattern, ok := a.patterns.Match(inherited)
	if !ok {
		return RecoveryResult{
			Action: a.Name(),
			Reason: "no heal pattern matched the inherited failure cluster — surface to user",
			Evidence: []string{
				fmt.Sprintf("inherited=%d", len(inherited)),
				fmt.Sprintf("first_failure=%s", truncate(inherited[0].Name, 120)),
			},
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// 6. Generate fix. ok=false means the pattern matched but cannot produce
	// a deterministic tree change — the action still opens an informational
	// PR with the needs-review label.
	fix, deterministic := pattern.GenerateFix(inherited)
	fix.PRLabels = ensureLabels(fix.PRLabels, deterministic)
	if fix.TargetRepo == "" {
		fix.TargetRepo = targetRepo
	}

	// 7. First-occurrence gate.
	if a.cfg.RequireHumanFirst && deterministic {
		patternLabel := "pattern:" + pattern.Slug()
		past, pastErr := listHealPRs(ctx, repoForGh, "all", "--label", patternLabel)
		if pastErr == nil && len(past) == 0 {
			fix.PRLabels = swapLabel(fix.PRLabels, "pipeline-heal:auto", "pipeline-heal:needs-review")
		}
	}

	// 7.5 (#4136) Human-approval gate. An auto-generated base-branch heal PR is
	// the most aggressive recovery action — it mutates the base branch entirely
	// outside the feature-dev path, so the architecture-approval gate never sees
	// it. Require an out-of-band human approval BEFORE any git mutation or PR
	// creation. Default-on (no opt-in flag preserving the gap). The gating lives
	// here on the recovery path, not in the pure approvalGate.Evaluate function.
	if !healBaseApprovalGranted(ctx, failure.Workspace, repoForGh, failure.PRNumber) {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("auto-heal base PR requires human approval — add the %q label to PR #%d (or write %s with {\"approved\": true}) before it proceeds",
				healBaseApprovalLabel, failure.PRNumber, healBaseApprovalRelPath(failure.PRNumber)),
			Evidence: []string{
				fmt.Sprintf("pattern=%s", pattern.Slug()),
				fmt.Sprintf("pr=%d", failure.PRNumber),
				fmt.Sprintf("inherited_failures=%d", len(inherited)),
				fmt.Sprintf("deterministic=%t", deterministic),
			},
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// 8. Apply fix to the heal branch and push (only when deterministic).
	branchName := assembleBranchName(pattern.Slug(), fix.BranchName, failure.PRNumber)
	var pushedRef string
	if deterministic && (len(fix.FilesToCreate) > 0 || len(fix.FilesToModify) > 0) {
		if applyErr := a.applyFixAndPush(ctx, failure.Workspace, branchName, fix); applyErr != nil {
			return RecoveryResult{
				Action:   a.Name(),
				Reason:   fmt.Sprintf("failed to apply heal fix: %s", truncate(applyErr.Error(), 200)),
				Evidence: []string{fmt.Sprintf("branch=%s", branchName), fmt.Sprintf("pattern=%s", pattern.Slug())},
				FollowUp: FollowUpHumanTriageRequired,
			}
		}
		pushedRef = branchName
	}

	// 9. Open the heal PR.
	prURL, prErr := a.createHealPR(ctx, fix, pushedRef, repoForGh, deterministic)
	if prErr != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("gh pr create failed: %s", truncate(prErr.Error(), 200)),
			Evidence: []string{fmt.Sprintf("pattern=%s", pattern.Slug()), fmt.Sprintf("repo=%s", repoForGh)},
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// 10. Link the heal PR back to the original PR so the author sees it.
	commentBody := fmt.Sprintf("🔧 Heal PR opened against base branch: %s\n\nThis PR's failures were inherited from main — see [docs/AUTO_TRIAGE.md](../blob/main/docs/AUTO_TRIAGE.md#pipeline-heal-base) for details.",
		prURL)
	_, _ = execGh(ctx, "pr", "comment", fmt.Sprint(failure.PRNumber), "--body", commentBody)

	evidence := []string{
		fmt.Sprintf("pattern=%s", pattern.Slug()),
		fmt.Sprintf("heal_pr=%s", prURL),
		fmt.Sprintf("inherited_failures=%d", len(inherited)),
		fmt.Sprintf("deterministic=%t", deterministic),
	}
	if pushedRef != "" {
		evidence = append(evidence, fmt.Sprintf("branch=%s", pushedRef))
	}
	if targetRepo != "" {
		evidence = append(evidence, fmt.Sprintf("target_repo=%s", targetRepo))
	}

	return RecoveryResult{
		// Recovered=false: main is not fixed yet, so the stage cannot resume.
		// FollowUp is human triage — a real PR has been opened.
		Action:   a.Name(),
		Reason:   fmt.Sprintf("opened heal PR via pattern %q; main fix in flight", pattern.Slug()),
		Evidence: evidence,
		FollowUp: FollowUpHumanTriageRequired,
	}
}

// applyFixAndPush creates the heal branch from `origin/main`, writes the fix
// files into the workspace, stages them, commits, and pushes the branch. All
// shell-outs go through execGit so tests can stub them.
func (a *PipelineHealBase) applyFixAndPush(ctx context.Context, workspace, branch string, fix heal.HealFix) error {
	if _, err := execGit(ctx, workspace, "fetch", "origin", "main"); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if _, err := execGit(ctx, workspace, "checkout", "-B", branch, "origin/main"); err != nil {
		return fmt.Errorf("checkout: %w", err)
	}

	for _, change := range fix.FilesToCreate {
		if err := writeFileTree(workspace, change); err != nil {
			return fmt.Errorf("write create %s: %w", change.Path, err)
		}
	}
	for _, change := range fix.FilesToModify {
		if err := writeFileTree(workspace, change); err != nil {
			return fmt.Errorf("write modify %s: %w", change.Path, err)
		}
	}

	if _, err := execGit(ctx, workspace, "add", "-A"); err != nil {
		return fmt.Errorf("add: %w", err)
	}
	commitMsg := fix.CommitMessage
	if commitMsg == "" {
		commitMsg = fix.PRTitle
	}
	if _, err := execGit(ctx, workspace, "commit", "-m", commitMsg); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if _, err := execGit(ctx, workspace, "push", "origin", branch); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

// createHealPR runs `gh pr create` with the fix's title, body, labels, and
// optional --repo. Returns the URL printed on stdout. When pushedRef is empty
// (non-deterministic fixes that have no associated branch), the function
// returns an early-marker URL and skips the gh call — the heal action's
// informational PR for needs-review-only patterns still benefits from
// surfacing through the linking comment instead of an actual PR. To keep
// behaviour symmetric with the deterministic path, the implementation still
// goes through gh but uses a `heal-stub/<slug>` branch that the caller is
// responsible for creating; in tests the execGh stub absorbs the call.
func (a *PipelineHealBase) createHealPR(ctx context.Context, fix heal.HealFix, pushedRef, repo string, deterministic bool) (string, error) {
	args := []string{"pr", "create",
		"--title", fix.PRTitle,
		"--body", fix.PRBody,
		"--base", "main",
	}
	if pushedRef != "" {
		args = append(args, "--head", pushedRef)
	}
	if len(fix.PRLabels) > 0 {
		args = append(args, "--label", strings.Join(fix.PRLabels, ","))
	}
	if fix.TargetRepo != "" {
		args = append(args, "--repo", fix.TargetRepo)
	} else if repo != "" && deterministic {
		// Only pass --repo when we have one and the fix is being pushed —
		// otherwise gh would attempt a no-op create against an arbitrary repo.
		args = append(args, "--repo", repo)
	}

	out, err := execGh(ctx, args...)
	if err != nil {
		return "", err
	}
	url := extractPRURL(string(out))
	if url == "" {
		// Some `gh` versions print confirmation prose without a URL on the
		// final line; the test stubs return just the URL.
		url = strings.TrimSpace(string(out))
	}
	return url, nil
}

// healBaseApprovalLabel is the out-of-band approval signal a human adds to the
// failing PR to greenlight an auto-generated base-branch heal PR (#4136).
const healBaseApprovalLabel = "pipeline-heal:approved"

// healBaseApprovalRelPath is the workspace-relative approval file a human can
// write (`{"approved": true}`) as an alternative to the PR label (#4136).
func healBaseApprovalRelPath(prNumber int) string {
	return fmt.Sprintf(".nightgauge/pipeline/approval-heal-base-%d.json", prNumber)
}

// healBaseApprovalGranted reports whether a human has approved this base-branch
// heal PR out-of-band (#4136), via either:
//   - an approval file at <workspace>/.nightgauge/pipeline/approval-heal-base-{PR}.json
//     containing {"approved": true}, or
//   - the healBaseApprovalLabel on the failing PR (the durable signal — it
//     survives worktree cleanup).
//
// Best-effort and fail-closed: a missing file / gh error simply means "not
// approved" — it never auto-approves. The file is checked first so the gh call
// is skipped when a local approval is already present.
func healBaseApprovalGranted(ctx context.Context, workspace, repo string, prNumber int) bool {
	if workspace != "" {
		path := filepath.Join(workspace, ".nightgauge", "pipeline",
			fmt.Sprintf("approval-heal-base-%d.json", prNumber))
		if b, err := os.ReadFile(path); err == nil {
			var v struct {
				Approved bool `json:"approved"`
			}
			if json.Unmarshal(b, &v) == nil && v.Approved {
				return true
			}
		}
	}
	if prNumber > 0 {
		args := []string{"pr", "view", fmt.Sprint(prNumber), "--json", "labels"}
		if repo != "" {
			args = append(args, "--repo", repo)
		}
		if out, err := execGh(ctx, args...); err == nil {
			var resp struct {
				Labels []struct {
					Name string `json:"name"`
				} `json:"labels"`
			}
			if json.Unmarshal(out, &resp) == nil {
				for _, l := range resp.Labels {
					if l.Name == healBaseApprovalLabel {
						return true
					}
				}
			}
		}
	}
	return false
}

// writeFileTree writes change.Content to workspace/change.Path, creating any
// missing parent directories.
func writeFileTree(workspace string, change heal.HealFileChange) error {
	full := filepath.Join(workspace, change.Path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(change.Content), 0o644)
}

// listHealPRs returns the URLs of heal PRs in the given state, with optional
// extra arguments forwarded to `gh pr list` (e.g. --label).
func listHealPRs(ctx context.Context, repo, state string, extra ...string) ([]string, error) {
	args := []string{"pr", "list",
		"--label", "pipeline-heal:auto",
		"--state", state,
		"--json", "url,createdAt",
	}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	args = append(args, extra...)
	out, err := execGh(ctx, args...)
	if err != nil {
		return nil, err
	}
	return parseHealPRURLs(out)
}

// listHealPRsRecent returns heal PR URLs created within the trailing window.
// Both open and closed PRs are counted toward the 24h throttle so a heal PR
// merging quickly does not unlock another within the same window.
func listHealPRsRecent(ctx context.Context, repo string, window time.Duration) ([]string, error) {
	args := []string{"pr", "list",
		"--label", "pipeline-heal:auto",
		"--state", "all",
		"--json", "url,createdAt",
	}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	out, err := execGh(ctx, args...)
	if err != nil {
		return nil, err
	}
	all, err := parseHealPRURLsWithCreated(out)
	if err != nil {
		return nil, err
	}
	threshold := time.Now().Add(-window)
	var recent []string
	for _, p := range all {
		if p.CreatedAt.IsZero() || p.CreatedAt.After(threshold) {
			recent = append(recent, p.URL)
		}
	}
	return recent, nil
}

type healPRItem struct {
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
}

func parseHealPRURLs(raw []byte) ([]string, error) {
	items, err := parseHealPRURLsWithCreated(raw)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, p := range items {
		out = append(out, p.URL)
	}
	return out, nil
}

func parseHealPRURLsWithCreated(raw []byte) ([]healPRItem, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var items []healPRItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// extractPRURL pulls the first URL from a gh pr create stdout. gh typically
// prints the URL on its own line at the end.
func extractPRURL(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "https://") {
			return line
		}
	}
	return ""
}

// unanimousTargetRepo returns the TargetRepo when every failure with a hint
// agrees. Mixed hints (or zero hints) return "".
func unanimousTargetRepo(failures []heal.BaselineFailure) string {
	target := ""
	for _, f := range failures {
		if f.TargetRepo == "" {
			continue
		}
		if target == "" {
			target = f.TargetRepo
			continue
		}
		if target != f.TargetRepo {
			return ""
		}
	}
	return target
}

// ensureLabels guarantees the fix carries at least one of the canonical heal
// labels. When deterministic is true the default is `pipeline-heal:auto`,
// otherwise `pipeline-heal:needs-review`.
func ensureLabels(labels []string, deterministic bool) []string {
	hasAuto := false
	hasReview := false
	for _, l := range labels {
		switch l {
		case "pipeline-heal:auto":
			hasAuto = true
		case "pipeline-heal:needs-review":
			hasReview = true
		}
	}
	if deterministic && !hasAuto && !hasReview {
		return append([]string{"pipeline-heal:auto"}, labels...)
	}
	if !deterministic && !hasReview {
		return append([]string{"pipeline-heal:needs-review"}, labels...)
	}
	return labels
}

// swapLabel returns labels with the first occurrence of `from` replaced by
// `to`. When `from` is not present and `to` is also absent, appends `to`.
func swapLabel(labels []string, from, to string) []string {
	hasTo := false
	out := make([]string, len(labels))
	for i, l := range labels {
		if l == from {
			out[i] = to
			hasTo = true
			continue
		}
		out[i] = l
		if l == to {
			hasTo = true
		}
	}
	if !hasTo {
		out = append(out, to)
	}
	return out
}

// assembleBranchName composes the final branch name. The pattern may have
// supplied a slug fragment; we prefix with `pipeline-heal/` and append the PR
// number when no pattern slug is provided.
func assembleBranchName(patternSlug, fragment string, prNumber int) string {
	frag := strings.TrimSpace(fragment)
	if frag == "" {
		frag = fmt.Sprintf("%s-%d", patternSlug, prNumber)
	}
	return heal.SafeBranchName("pipeline-heal/" + frag)
}

// prefixed adorns each value with prefix. Used to format evidence entries.
func prefixed(prefix string, vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = prefix + v
	}
	return out
}
