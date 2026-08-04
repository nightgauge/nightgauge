// Package doctor provides environment health checks for the nightgauge pipeline.
// The DoctorResult JSON schema is stable — field names and types must not change
// after first merge. Skills parse `nightgauge doctor --json` output; any
// breaking change requires incrementing the V field.
package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/dockercompose"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// DoctorResult is the stable JSON output schema for `nightgauge doctor`.
// Schema version 1 — do not rename or remove fields after first merge.
type DoctorResult struct {
	V                   int                  `json:"v"`                              // schema version, always 1
	Healthy             bool                 `json:"healthy"`                        // true when ExitCode < 2
	ExitCode            int                  `json:"exit_code"`                      // 0=healthy, 1=warnings, 2=broken
	Checks              map[string]CheckItem `json:"checks"`                         // per-check results keyed by check name
	Warnings            []string             `json:"warnings"`                       // non-blocking issues
	Errors              []string             `json:"errors"`                         // blocking issues (ExitCode 2)
	FailedChecks        []string             `json:"failed_checks,omitempty"`        // check names that contributed to hasRequiredFailure, in order added
	InstallInstructions string               `json:"install_instructions,omitempty"` // populated when binary check fails
	// Adapters is the per-adapter health section (Issue #4031), populated only
	// when the caller requests specific adapters (e.g. `doctor --adapters
	// codex,claude`). Additive to schema v1 — never populated for the default
	// environment-only doctor run, so existing parsers are unaffected. An
	// unhealthy adapter is a warning (ExitCode 1), never a required failure:
	// an optional adapter being uninstalled must not fail the environment check.
	Adapters []AdapterHealth `json:"adapters,omitempty"`
}

// CheckItem is the result of a single environment check.
type CheckItem struct {
	OK     bool   `json:"ok"`               // true when this check passed
	Detail string `json:"detail,omitempty"` // human-readable success detail
	Error  string `json:"error,omitempty"`  // human-readable failure reason
}

// readOrgWarning returns the read:org advisory warning when scopes lacks
// organization read access (honoring the admin:org/write:org hierarchy via
// gh.HasOrgReadAccess), or "" when access is already satisfied.
func readOrgWarning(scopes []string) string {
	if gh.HasOrgReadAccess(scopes) {
		return ""
	}
	return "GitHub token does not include read:org; private organisation membership discovery may be incomplete"
}

// rateLimitCritical is the remaining-requests threshold below which the rate limit check
// reports OK=false and emits a warning. Operations will likely fail at this level.
const rateLimitCritical = 100

// rateLimitLow is the threshold below which a warning is emitted but the check still passes.
const rateLimitLow = 500

// installMsg is the actionable install instructions emitted when the binary self-check fails.
// TODO(#2735): update URL once the distribution sub-issue determines the canonical channel.
const installMsg = "nightgauge is not in PATH.\n" +
	"Install via: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest\n" +
	"Or download from: https://github.com/nightgauge/nightgauge/releases\n" +
	"Run `nightgauge doctor` after installing to verify your environment."

// RunDoctor performs a full environment health check and returns a structured result.
//
// client may be nil when GitHub authentication failed; all auth-dependent checks
// will report failure in that case. cfg may be nil for fresh repositories that
// have not yet run `nightgauge repo-init`; config/project checks are
// downgraded to warnings rather than required failures in that case.
//
// adapters is the optional set of execution adapters to health-check (Issue
// #4031). When non-empty, result.Adapters is populated with deterministic
// per-adapter binary/version/MCP facts and each unhealthy adapter adds a
// warning. When empty/nil, the adapter section is omitted entirely (the
// default environment-only doctor behavior).
func RunDoctor(ctx context.Context, cfg *config.Config, client *gh.Client, adapters []string) DoctorResult {
	result := DoctorResult{
		V:      1,
		Checks: make(map[string]CheckItem),
	}

	var warnings []string
	var errors []string
	hasRequiredFailure := false

	// --- binary (warning) ---
	// Self-check: is `nightgauge` callable by name from the shell PATH?
	binaryCheck := checkBinary()
	result.Checks["binary"] = binaryCheck
	if !binaryCheck.OK {
		warnings = append(warnings, "nightgauge binary not found in PATH")
		result.InstallInstructions = installMsg
	}

	// --- gh (warning) ---
	ghCheck := checkGH()
	result.Checks["gh"] = ghCheck
	if !ghCheck.OK {
		warnings = append(warnings, "gh CLI not found in PATH; some operations may be degraded")
	}

	// --- github_auth / api_user / scopes / rate_limit (required unless client nil) ---
	if client == nil {
		result.Checks["github_auth"] = CheckItem{
			OK:    false,
			Error: "GitHub client could not be created — check GITHUB_TOKEN env var or run `gh auth login`",
		}
		result.Checks["api_user"] = CheckItem{OK: false, Error: "skipped: no authenticated client"}
		result.Checks["scopes"] = CheckItem{OK: false, Error: "skipped: no authenticated client"}
		result.Checks["rate_limit"] = CheckItem{OK: false, Error: "skipped: no authenticated client"}
		errors = append(errors, "GitHub authentication failed — set GITHUB_TOKEN or run `gh auth login`")
		hasRequiredFailure = true
		result.FailedChecks = append(result.FailedChecks, "github_auth")
	} else {
		scopeInfo, err := client.CheckTokenScopes(ctx)
		if err != nil {
			result.Checks["github_auth"] = CheckItem{OK: false, Error: fmt.Sprintf("token check failed: %s", err.Error())}
			result.Checks["api_user"] = CheckItem{OK: false, Error: "skipped: auth check failed"}
			result.Checks["scopes"] = CheckItem{OK: false, Error: "skipped: auth check failed"}
			errors = append(errors, fmt.Sprintf("GitHub token check failed: %s", err.Error()))
			hasRequiredFailure = true
			result.FailedChecks = append(result.FailedChecks, "github_auth")
		} else {
			result.Checks["github_auth"] = CheckItem{OK: true, Detail: fmt.Sprintf("authenticated as %s", scopeInfo.Login)}

			// api_user — required
			if scopeInfo.Login == "" {
				result.Checks["api_user"] = CheckItem{OK: false, Error: "GET /user returned empty login"}
				errors = append(errors, "GitHub API user check failed: empty login")
				hasRequiredFailure = true
				result.FailedChecks = append(result.FailedChecks, "api_user")
			} else {
				result.Checks["api_user"] = CheckItem{OK: true, Detail: scopeInfo.Login}
			}

			// scopes — required
			if !scopeInfo.Valid {
				scopeErr := fmt.Sprintf("missing required scopes: %s", strings.Join(scopeInfo.MissingScopes, ", "))
				result.Checks["scopes"] = CheckItem{OK: false, Error: scopeErr}
				errors = append(errors, scopeErr)
				hasRequiredFailure = true
				result.FailedChecks = append(result.FailedChecks, "scopes")
			} else {
				result.Checks["scopes"] = CheckItem{OK: true, Detail: strings.Join(scopeInfo.Scopes, ", ")}
				if w := readOrgWarning(scopeInfo.Scopes); w != "" {
					warnings = append(warnings, w)
				}
			}
		}

		// rate_limit — warning only (never causes ExitCode 2)
		rl, err := client.GetRateLimit(ctx)
		if err != nil {
			result.Checks["rate_limit"] = CheckItem{OK: false, Error: fmt.Sprintf("rate limit check failed: %s", err.Error())}
			warnings = append(warnings, "could not check GitHub API rate limit")
		} else {
			detail := fmt.Sprintf("remaining: %d/%d", rl.Remaining, rl.Limit)
			if rl.Remaining < rateLimitCritical {
				result.Checks["rate_limit"] = CheckItem{OK: false, Detail: detail, Error: fmt.Sprintf("API rate limit critically low: %d remaining", rl.Remaining)}
				warnings = append(warnings, fmt.Sprintf("GitHub API rate limit critically low: %d remaining (operations may fail)", rl.Remaining))
			} else if rl.Remaining < rateLimitLow {
				result.Checks["rate_limit"] = CheckItem{OK: true, Detail: fmt.Sprintf("%s (below %d — consider waiting before long pipeline runs)", detail, rateLimitLow)}
				warnings = append(warnings, fmt.Sprintf("GitHub API rate limit low: %d remaining", rl.Remaining))
			} else {
				result.Checks["rate_limit"] = CheckItem{OK: true, Detail: detail}
			}
		}
	}

	// --- config (required; downgraded to warning for fresh/nil config) ---
	if cfg == nil {
		result.Checks["config"] = CheckItem{OK: false, Detail: "no .nightgauge/config.yaml found (fresh repository)"}
		result.Checks["project"] = CheckItem{OK: false, Detail: "no configuration (fresh repository)"}
		warnings = append(warnings, "no .nightgauge/config.yaml — run `nightgauge repo-init` to configure")
		warnings = append(warnings, "project number not set — run `nightgauge repo-init`")
	} else {
		result.Checks["config"] = CheckItem{OK: true, Detail: "configuration loaded"}

		// project — required when config exists
		if cfg.ProjectNumber == 0 || cfg.Owner == "" {
			projectErr := "project number or owner not set in .nightgauge/config.yaml"
			result.Checks["project"] = CheckItem{OK: false, Error: projectErr}
			errors = append(errors, projectErr)
			hasRequiredFailure = true
			result.FailedChecks = append(result.FailedChecks, "project")
		} else {
			result.Checks["project"] = CheckItem{OK: true, Detail: fmt.Sprintf("project %d (owner: %s)", cfg.ProjectNumber, cfg.Owner)}
		}
	}

	// --- project_mapping (required when a workspace manifest exists and cfg is loaded) ---
	// Cross-checks the workspace manifest's repositories[].project_number
	// (Source A) against the board config.ResolveRepoProject declares for that
	// same repo (Source B — its own .nightgauge/config.yaml, or an
	// autonomous.repositories.<repo>.project_number override). A mismatch means
	// issues get filed against a board the scheduler never polls (#271) —
	// always a misconfiguration, never a warning-only path.
	//
	// A repo no config declares a board for is reported SEPARATELY and as a
	// warning (#280). It used to be dropped silently, which let this check
	// report "agree" about repos it never compared — but it is not the same
	// condition as a disagreement: nothing is misrouted yet, the manifest value
	// is simply unverified, and the scheduler falls back to the workspace
	// default board for that repo. Failing hard on it would overstate the
	// consequence exactly as the old silence understated it.
	//
	// Both statements now come from ONE lookup (#313). This check does not
	// assert anything about the scheduler on its own authority: the scheduler
	// builds its repo set from the same resolver, and the fallback board named
	// in the warning is the number that resolver returned.
	if cfg != nil {
		if report, mmErr := checkProjectMapping(cfg); mmErr == nil {
			mismatches := make([]string, 0, len(report.Mismatches))
			for _, m := range report.Mismatches {
				mismatches = append(mismatches, m.String())
			}
			unverifiable := make([]string, 0, len(report.Unresolvable))
			for _, u := range report.Unresolvable {
				unverifiable = append(unverifiable, u.String())
			}
			switch {
			case len(mismatches) > 0:
				detail := strings.Join(mismatches, "; ")
				result.Checks["project_mapping"] = CheckItem{OK: false, Error: detail}
				errors = append(errors, mismatches...)
				hasRequiredFailure = true
				result.FailedChecks = append(result.FailedChecks, "project_mapping")
				// Unverifiable repos still deserve a mention alongside.
				warnings = append(warnings, unverifiable...)
			case len(unverifiable) > 0:
				result.Checks["project_mapping"] = CheckItem{
					OK:     false,
					Detail: fmt.Sprintf("%d repo(s) could not be cross-checked", len(unverifiable)),
					Error:  strings.Join(unverifiable, "; "),
				}
				warnings = append(warnings, unverifiable...)
			default:
				result.Checks["project_mapping"] = CheckItem{OK: true, Detail: "workspace manifest and runtime config agree"}
			}
		}
		// mmErr != nil means no workspace manifest was found (single-repo mode) — check omitted.
	}

	// --- board_population (required when config and a client are available) ---
	// Config agreement is not evidence of reachability (#280). Two config
	// sources can name the same board perfectly while every one of the repo's
	// issues lives on a different one — at which point the scheduler polls an
	// empty board, reports "0 candidates", and every other check here passes.
	// This is the only check that asks the forge where the work actually is.
	if cfg != nil && client != nil && cfg.ProjectNumber > 0 && cfg.Owner != "" && cfg.DefaultRepo != "" {
		switch pop, popErr := checkBoardPopulation(ctx, cfg, client); {
		case popErr != nil:
			// "I could not look" is a warning, never a failure and never a
			// pass — the whole point of this check is that silence must not
			// read as health.
			result.Checks["board_population"] = CheckItem{
				OK:     false,
				Detail: "could not verify which board holds the repo's issues",
				Error:  popErr.Error(),
			}
			warnings = append(warnings, fmt.Sprintf("board population unverified: %s", popErr.Error()))
		case pop.OpenIssues > 0 && pop.OnBoard == 0:
			msg := fmt.Sprintf(
				"project %d holds 0 of %s/%s's %d open issues — the scheduler polls a board that has none of this repo's work",
				cfg.ProjectNumber, cfg.Owner, cfg.DefaultRepo, pop.OpenIssues)
			if len(pop.ElsewhereBoards) > 0 {
				msg += fmt.Sprintf("; those issues are on project(s) %s", joinInts(pop.ElsewhereBoards))
			}
			result.Checks["board_population"] = CheckItem{OK: false, Error: msg}
			errors = append(errors, msg)
			hasRequiredFailure = true
			result.FailedChecks = append(result.FailedChecks, "board_population")
		default:
			result.Checks["board_population"] = CheckItem{
				OK: true,
				Detail: fmt.Sprintf("project %d holds %d of %d open issues",
					cfg.ProjectNumber, pop.OnBoard, pop.OpenIssues),
			}
		}
	}

	// --- orphaned docker compose projects (warning only) ---
	// Issue #3050: per-issue compose stacks (`issue-NNN`) whose worktree no
	// longer exists indicate a leaked teardown. Surface them so the operator
	// can run `nightgauge cleanup`. Skipped silently when docker is
	// unavailable.
	orphans := findOrphanedComposeProjects(ctx)
	if len(orphans) > 0 {
		names := make([]string, 0, len(orphans))
		for _, p := range orphans {
			names = append(names, p.Name)
		}
		result.Checks["compose_orphans"] = CheckItem{
			OK:     false,
			Detail: fmt.Sprintf("%d orphaned issue-* compose project(s)", len(orphans)),
			Error:  fmt.Sprintf("orphaned compose projects: %s — run `nightgauge cleanup`", strings.Join(names, ", ")),
		}
		warnings = append(warnings,
			fmt.Sprintf("orphaned docker compose project(s) detected (%s) — run `nightgauge cleanup`",
				strings.Join(names, ", ")))
	}

	// --- per-adapter health (Issue #4031, opt-in) ---
	// Deterministic binary/version/MCP facts for the requested adapters. An
	// unhealthy adapter is surfaced as a warning (degraded, ExitCode 1) — never
	// a required failure, since an optional adapter that the operator does not
	// use being uninstalled must not break the environment health verdict.
	if len(adapters) > 0 {
		result.Adapters = CheckAdapters(adapters)
		for _, a := range result.Adapters {
			if !a.OK {
				detail := a.Remediation
				if detail == "" {
					detail = "adapter not ready"
				}
				warnings = append(warnings, fmt.Sprintf("adapter %q not ready: %s", a.Adapter, detail))
			}
		}
	}

	// --- compute final health ---
	result.Warnings = warnings
	result.Errors = errors

	switch {
	case hasRequiredFailure:
		result.Healthy = false
		result.ExitCode = 2
	case len(warnings) > 0:
		result.Healthy = true
		result.ExitCode = 1
	default:
		result.Healthy = true
		result.ExitCode = 0
	}

	return result
}

// checkBinary reports whether the `nightgauge` binary is resolvable via the
// six-step cascade documented in ResolveBinary (mirroring guard.sh):
// $NIGHTGAUGE_BIN, PATH, repo bin/, canonical-repo bin/, VSCode extension
// bundle, or ~/go/bin. This check is warning-only — never a required
// failure — since discovering the binary is what this command itself is;
// a missing binary can only be observed by the environment that ran `doctor`
// in the first place.
func checkBinary() CheckItem {
	resolved := ResolveBinary()
	if resolved.Path == "" {
		return CheckItem{OK: false, Error: "nightgauge not found via NIGHTGAUGE_BIN, PATH, repo bin/, canonical-repo bin/, VSCode extension bundle, or ~/go/bin"}
	}
	return CheckItem{OK: true, Detail: fmt.Sprintf("%s (resolved via %s)", resolved.Path, resolved.Step)}
}

// checkGH reports whether the `gh` CLI is reachable via PATH.
func checkGH() CheckItem {
	path, err := exec.LookPath("gh")
	if err != nil {
		return CheckItem{OK: false, Error: "gh CLI not found in PATH"}
	}
	return CheckItem{OK: true, Detail: path}
}

// findOrphanedComposeProjects returns the set of `issue-NNN` compose
// projects whose corresponding git worktree no longer exists. Returns an
// empty slice when docker isn't available — the check is best-effort.
func findOrphanedComposeProjects(ctx context.Context) []dockercompose.Project {
	projects, err := dockercompose.ListIssueProjects(ctx)
	if err != nil || len(projects) == 0 {
		return nil
	}
	active := activeWorktreeIssues()
	var orphans []dockercompose.Project
	for _, p := range projects {
		if !active[p.IssueNumber] {
			orphans = append(orphans, p)
		}
	}
	return orphans
}

// activeWorktreeIssues parses `git worktree list --porcelain` to derive the
// set of issue numbers currently represented by an active worktree. Returns
// an empty map on any error so the doctor degrades gracefully outside a
// repo.
func activeWorktreeIssues() map[int]bool {
	out := map[int]bool{}
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	data, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		base := filepath.Base(path)
		idx := strings.LastIndex(base, "issue-")
		if idx < 0 {
			continue
		}
		tail := base[idx+len("issue-"):]
		if tail == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(tail, "%d", &n); err == nil && n > 0 {
			out[n] = true
		}
	}
	return out
}

// checkProjectMapping cross-validates the workspace manifest's
// repositories[].project_number entries against config.ResolveRepoProjectNumber
// via the shared config.CheckWorkspaceProjectMapping helper. Returns a
// non-nil error only when no workspace manifest exists (single-repo mode —
// not an error condition, just "nothing to check").
// boardPopulation is what the configured board actually holds for its repo.
type boardPopulation struct {
	// OpenIssues is the repo's open issue count.
	OpenIssues int
	// OnBoard is how many of them are on the configured board.
	OnBoard int
	// ElsewhereBoards names the boards that DO hold the repo's issues, sampled
	// when the configured board holds none. Empty means the sample found no
	// board at all (the issues are on no project), which is a different and
	// less alarming state than "they are on the wrong board".
	ElsewhereBoards []int
}

// boardPopulationSample bounds how many issues are probed for their real board
// when the configured one turns up empty. The answer is the same after two or
// three; this exists to name a destination, not to take a census.
const boardPopulationSample = 3

// checkBoardPopulation asks the forge whether the configured board holds any
// of the repo's open issues, and — when it holds none — which board(s) do.
//
// This is the only check that consults ground truth. `project` verifies the
// configured board RESOLVES; `project_mapping` verifies two config files agree
// about its number. Neither looks at membership, so both pass while the
// scheduler polls a board containing none of the repo's work (#280).
func checkBoardPopulation(ctx context.Context, cfg *config.Config, client *gh.Client) (boardPopulation, error) {
	var pop boardPopulation

	issues, err := gh.NewIssueService(client).ListIssues(ctx, cfg.Owner, cfg.DefaultRepo, nil)
	if err != nil {
		return pop, fmt.Errorf("list open issues: %w", err)
	}
	pop.OpenIssues = len(issues)
	if pop.OpenIssues == 0 {
		// No open work — an empty board is correct, not a misconfiguration.
		return pop, nil
	}

	ownerType := gh.OwnerTypeOrg
	if strings.EqualFold(cfg.OwnerType, "user") {
		ownerType = gh.OwnerTypeUser
	}
	items, _, err := gh.NewBoardService(client, cfg.Owner, cfg.ProjectNumber, ownerType).ListOpenItems(ctx)
	if err != nil {
		return pop, fmt.Errorf("list open items on project %d: %w", cfg.ProjectNumber, err)
	}
	repoSpec := cfg.Owner + "/" + cfg.DefaultRepo
	for _, item := range items {
		if item.Repo == "" || item.Repo == repoSpec {
			pop.OnBoard++
		}
	}
	if pop.OnBoard > 0 {
		return pop, nil
	}

	// The configured board is empty for this repo. Name where the work is, so
	// the error is actionable rather than merely alarming. Best-effort: a
	// failed probe degrades the message, never the verdict.
	seen := map[int]bool{}
	for i, issue := range issues {
		if i >= boardPopulationSample {
			break
		}
		nums, probeErr := gh.ProjectNumbersForIssue(ctx, client, cfg.Owner, cfg.DefaultRepo, issue.Number)
		if probeErr != nil {
			continue
		}
		for _, n := range nums {
			if n != cfg.ProjectNumber && !seen[n] {
				seen[n] = true
				pop.ElsewhereBoards = append(pop.ElsewhereBoards, n)
			}
		}
	}
	sort.Ints(pop.ElsewhereBoards)
	return pop, nil
}

// joinInts renders board numbers for a human-readable message.
func joinInts(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

func checkProjectMapping(cfg *config.Config) (config.ProjectMappingReport, error) {
	wd, err := os.Getwd()
	if err != nil {
		return config.ProjectMappingReport{}, err
	}
	return config.FindWorkspaceProjectMappingMismatches(cfg, wd)
}
