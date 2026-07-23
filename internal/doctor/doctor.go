// Package doctor provides environment health checks for the nightgauge pipeline.
// The DoctorResult JSON schema is stable — field names and types must not change
// after first merge. Skills parse `nightgauge doctor --json` output; any
// breaking change requires incrementing the V field.
package doctor

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
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
	} else {
		scopeInfo, err := client.CheckTokenScopes(ctx)
		if err != nil {
			result.Checks["github_auth"] = CheckItem{OK: false, Error: fmt.Sprintf("token check failed: %s", err.Error())}
			result.Checks["api_user"] = CheckItem{OK: false, Error: "skipped: auth check failed"}
			result.Checks["scopes"] = CheckItem{OK: false, Error: "skipped: auth check failed"}
			errors = append(errors, fmt.Sprintf("GitHub token check failed: %s", err.Error()))
			hasRequiredFailure = true
		} else {
			result.Checks["github_auth"] = CheckItem{OK: true, Detail: fmt.Sprintf("authenticated as %s", scopeInfo.Login)}

			// api_user — required
			if scopeInfo.Login == "" {
				result.Checks["api_user"] = CheckItem{OK: false, Error: "GET /user returned empty login"}
				errors = append(errors, "GitHub API user check failed: empty login")
				hasRequiredFailure = true
			} else {
				result.Checks["api_user"] = CheckItem{OK: true, Detail: scopeInfo.Login}
			}

			// scopes — required
			if !scopeInfo.Valid {
				scopeErr := fmt.Sprintf("missing required scopes: %s", strings.Join(scopeInfo.MissingScopes, ", "))
				result.Checks["scopes"] = CheckItem{OK: false, Error: scopeErr}
				errors = append(errors, scopeErr)
				hasRequiredFailure = true
			} else {
				result.Checks["scopes"] = CheckItem{OK: true, Detail: strings.Join(scopeInfo.Scopes, ", ")}
				if !containsScope(scopeInfo.Scopes, "read:org") {
					warnings = append(warnings, "GitHub token does not include read:org; private organisation membership discovery may be incomplete")
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
		} else {
			result.Checks["project"] = CheckItem{OK: true, Detail: fmt.Sprintf("project %d (owner: %s)", cfg.ProjectNumber, cfg.Owner)}
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

func containsScope(scopes []string, expected string) bool {
	for _, scope := range scopes {
		if scope == expected {
			return true
		}
	}
	return false
}

// checkBinary reports whether the `nightgauge` binary is reachable via PATH.
func checkBinary() CheckItem {
	path, err := exec.LookPath("nightgauge")
	if err != nil {
		return CheckItem{OK: false, Error: "nightgauge not found in PATH"}
	}
	return CheckItem{OK: true, Detail: path}
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
