package doctor

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/cadence"
	"github.com/nightgauge/nightgauge/internal/execution"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

// Legacy check adapter (#2088). Every check here still produces a CheckItem
// plus the free-text errors and warnings the pre-registry doctor emitted; the
// adapter wraps that into Findings at the registry boundary. The checks are in
// one block per owning migration sub-issue, so each sibling deletes only its
// own block when it converts those checks to native findings, and the four can
// merge in any order. The removal chore (#2098) deletes this file.
//
// Severity rule for adapted checks: a legacy error is a blocker and a legacy
// warning is a warning, except that a check ADR-025 classifies as housekeeping
// or info reports at that severity, so cleanup items and context stop turning
// the status "degraded". A check that is not OK but emitted no message reports
// an info finding, so it never reads as healthy.

// legacyOutcome is what a pre-registry check computed.
type legacyOutcome struct {
	item     CheckItem
	present  bool     // false: the check wrote no row (not applicable here)
	errors   []string // blocking messages
	warnings []string // non-blocking messages
}

func legacyRow(item CheckItem) legacyOutcome { return legacyOutcome{item: item, present: true} }

// rowWarn is the common (CheckItem, warning) shape.
func rowWarn(item CheckItem, warning string) legacyOutcome {
	o := legacyRow(item)
	if warning != "" {
		o.warnings = []string{warning}
	}
	return o
}

// builtinChecks is filled by the blocks below, in source order.
var builtinChecks []Check

// legacy registers a pre-registry check through the adapter.
func legacy(id, title, group, code string, adr Severity, timeout time.Duration, deps []string,
	fn func(ctx context.Context, env *Env) legacyOutcome) {
	builtinChecks = append(builtinChecks, Check{
		ID: id, Title: title, Group: group, Code: code, Timeout: timeout, DependsOn: deps,
		Run: func(ctx context.Context, env *Env) []Finding {
			return adaptLegacy(id, code, adr, env, fn(ctx, env))
		},
	})
}

func adaptLegacy(id, code string, adr Severity, env *Env, out legacyOutcome) []Finding {
	if !out.present {
		env.SetDetail(id, "not applicable in this workspace")
		return nil
	}
	env.recordItem(id, out.item)
	detail := out.item.Detail
	if detail == "" && out.item.OK {
		detail = "ok"
	}
	env.SetDetail(id, detail)

	capSeverity := func(s Severity) Severity {
		if adr == SeverityHousekeeping || adr == SeverityInfo {
			return adr
		}
		return s
	}
	var findings []Finding
	add := func(sev Severity, msg string) {
		cause := out.item.Error
		if cause == "" || cause == msg {
			cause = out.item.Detail
		}
		ev := map[string]string{}
		if out.item.Detail != "" {
			ev["detail"] = out.item.Detail
		}
		for i, sf := range out.item.Findings {
			ev["hit."+strconv.Itoa(i)] = fmt.Sprintf("%s:%d %s %s", sf.Path, sf.Line, sf.Pattern, sf.Redacted)
		}
		findings = append(findings, Finding{
			Code:        code,
			Check:       id,
			Severity:    sev,
			Title:       msg,
			Cause:       cause,
			Evidence:    ev,
			Docs:        DocsAnchor(code),
			Fingerprint: Fingerprint(code, id, string(sev), strconv.Itoa(len(findings))),
			Remedies:    []Remedy{},
		})
	}
	for _, e := range out.errors {
		add(capSeverity(SeverityBlocker), e)
	}
	for _, w := range out.warnings {
		add(capSeverity(SeverityWarning), w)
	}
	if len(findings) == 0 && !out.item.OK {
		msg := out.item.Error
		if msg == "" {
			msg = out.item.Detail
		}
		if msg == "" {
			msg = id + " did not pass"
		}
		add(SeverityInfo, msg)
	}
	return findings
}

// recordItem keeps the legacy CheckItem for in-process callers until #2098
// deletes CheckItem.
func (e *Env) recordItem(id string, item CheckItem) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.items == nil {
		e.items = map[string]CheckItem{}
	}
	e.items[id] = item
}

// ---------------------------------------------------------------------------
// #2091 — GitHub, config, budget and credential checks
// ---------------------------------------------------------------------------

func init() {
	legacy("binary", "nightgauge binary", "environment", "NGD001", SeverityBlocker, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			item, resolved := checkBinary()
			o := legacyRow(item)
			if !item.OK {
				o.warnings = []string{item.Error}
				if !resolved {
					env.setInstall(installMsg)
				}
			}
			return o
		})
	legacy("skills", "Rendered skills tree", "environment", "NGD002", SeverityBlocker, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkSkillsRoot(env.Cwd))
		})
	legacy("gh", "gh CLI", "environment", "NGD003", SeverityBlocker, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			item := checkGH()
			if item.OK {
				return legacyRow(item)
			}
			return rowWarn(item, "gh CLI not found in PATH; some operations may be degraded")
		})
	legacy("github_auth", "GitHub authentication", "github", "NGD004", SeverityBlocker, 20*time.Second, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			if env.Client == nil {
				o := legacyRow(CheckItem{OK: false,
					Error: "GitHub client could not be created — check GITHUB_TOKEN env var or run `gh auth login`"})
				o.errors = []string{"GitHub authentication failed — set GITHUB_TOKEN or run `gh auth login`"}
				return o
			}
			info, err := env.tokenScopes(ctx)
			if err != nil {
				o := legacyRow(CheckItem{OK: false, Error: fmt.Sprintf("token check failed: %s", err.Error())})
				o.errors = []string{fmt.Sprintf("GitHub token check failed: %s", err.Error())}
				return o
			}
			return legacyRow(CheckItem{OK: true, Detail: fmt.Sprintf("authenticated as %s", info.Login)})
		})
	legacy("api_user", "GitHub API user", "github", "NGD005", SeverityWarning, 20*time.Second, []string{"github_auth"},
		func(ctx context.Context, env *Env) legacyOutcome {
			info, _ := env.tokenScopes(ctx)
			if info == nil || info.Login == "" {
				o := legacyRow(CheckItem{OK: false, Error: "GET /user returned empty login"})
				o.errors = []string{"GitHub API user check failed: empty login"}
				return o
			}
			return legacyRow(CheckItem{OK: true, Detail: info.Login})
		})
	legacy("scopes", "OAuth scopes", "github", "NGD006", SeverityBlocker, 20*time.Second, []string{"github_auth"},
		func(ctx context.Context, env *Env) legacyOutcome {
			info, _ := env.tokenScopes(ctx)
			if info == nil {
				return legacyRow(CheckItem{OK: false, Error: "token scopes unavailable"})
			}
			switch {
			case !info.Valid:
				msg := fmt.Sprintf("missing required scopes: %s", strings.Join(info.MissingScopes, ", "))
				o := legacyRow(CheckItem{OK: false, Error: msg})
				o.errors = []string{msg}
				return o
			case !info.ScopesAdvertised:
				return legacyRow(CheckItem{OK: true, Detail: "not advertised (fine-grained or App token): permissions are per repository, checked by the operations that need them"})
			default:
				return rowWarn(CheckItem{OK: true, Detail: strings.Join(info.Scopes, ", ")}, readOrgWarning(info.Scopes))
			}
		})
	legacy("rate_limit", "GitHub API rate limit", "github", "NGD007", SeverityWarning, 20*time.Second, []string{"github_auth"},
		func(ctx context.Context, env *Env) legacyOutcome {
			rl, err := env.rateLimit(ctx)
			if err != nil {
				return rowWarn(CheckItem{OK: false, Error: fmt.Sprintf("rate limit check failed: %s", err.Error())},
					"could not check GitHub API rate limit")
			}
			detail := fmt.Sprintf("remaining: %d/%d", rl.Remaining, rl.Limit)
			switch {
			case rl.Remaining < rateLimitCritical:
				return rowWarn(CheckItem{OK: false, Detail: detail, Error: fmt.Sprintf("API rate limit critically low: %d remaining", rl.Remaining)},
					fmt.Sprintf("GitHub API rate limit critically low: %d remaining (operations may fail)", rl.Remaining))
			case rl.Remaining < rateLimitLow:
				return rowWarn(CheckItem{OK: true, Detail: fmt.Sprintf("%s (below %d — consider waiting before long pipeline runs)", detail, rateLimitLow)},
					fmt.Sprintf("GitHub API rate limit low: %d remaining", rl.Remaining))
			default:
				return legacyRow(CheckItem{OK: true, Detail: detail})
			}
		})
	// github_identity — which identity pipeline traffic is billed to, and that
	// identity's hourly ceiling (#1955).
	legacy("github_identity", "GitHub identity", "github", "NGD009", SeverityBlocker, 20*time.Second, []string{"github_auth"},
		func(ctx context.Context, env *Env) legacyOutcome {
			ceiling := ""
			if rl, err := env.rateLimit(ctx); err == nil && rl != nil {
				ceiling = fmt.Sprintf(", GraphQL ceiling %d/hr", rl.Limit)
			}
			app := env.Client.App()
			if app == nil {
				return legacyRow(CheckItem{OK: true, Detail: "personal token (the user's own rate-limit pool)" + ceiling})
			}
			item := CheckItem{OK: true, Detail: app.String() + ceiling}
			if _, _, ok := app.CommitIdentity(); !ok {
				item.Detail += "; commits keep the generic pipeline author until github_auth.app.slug and bot_user_id are set"
				return rowWarn(item, "github_auth.app has no slug/bot_user_id: pipeline commits are not attributed to the App")
			}
			return legacyRow(item)
		})
	// The GitHub API budget (#1347): reads the request ledger, which is written
	// unattended.
	legacy("github_api_budget", "GitHub API budget", "github", "NGD008", SeverityWarning, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkGitHubAPIBudget(env.Cwd, time.Now()))
		})
	legacy("config", "Configuration", "config", "NGD010", SeverityBlocker, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			switch {
			case env.Cfg == nil && env.CfgErr != nil:
				msg := "configuration failed to load: " + env.CfgErr.Error()
				o := legacyRow(CheckItem{OK: false, Error: msg})
				o.errors = []string{msg}
				return o
			case env.Cfg == nil:
				return rowWarn(CheckItem{OK: false, Detail: "no .nightgauge/config.yaml found (fresh repository)"},
					"no .nightgauge/config.yaml — run `nightgauge repo-init` to configure")
			}
			// #2205: name the files actually loaded, and never pass on defaults
			// or a user-global file when this repository was never onboarded.
			loaded, hasRepoConfig := loadedConfigFiles(env.Cwd)
			loadedDesc := "built-in defaults only"
			if len(loaded) > 0 {
				loadedDesc = strings.Join(loaded, ", ")
			}
			if hasRepoConfig {
				return legacyRow(CheckItem{OK: true, Detail: "configuration loaded from " + loadedDesc})
			}
			msg := "no repository config (.nightgauge/config.yaml) — run /nightgauge:repo-init (or `nightgauge repo-init`); loaded: " + loadedDesc
			return rowWarn(CheckItem{OK: false, Detail: "loaded: " + loadedDesc, Error: msg}, msg)
		})
	legacy("project", "Project board configuration", "config", "NGD011", SeverityBlocker, 0, []string{"config"},
		func(ctx context.Context, env *Env) legacyOutcome {
			cfg := env.Cfg
			if cfg == nil {
				return rowWarn(CheckItem{OK: false, Detail: "no configuration (fresh repository)"},
					"project number not set — run `nightgauge repo-init`")
			}
			if cfg.ProjectNumber == 0 || cfg.Owner == "" {
				msg := "project number or owner not set in .nightgauge/config.yaml"
				o := legacyRow(CheckItem{OK: false, Error: msg})
				o.errors = []string{msg}
				return o
			}
			return legacyRow(CheckItem{OK: true, Detail: fmt.Sprintf("project %d (owner: %s)", cfg.ProjectNumber, cfg.Owner)})
		})
	// project_mapping cross-checks the workspace manifest's project numbers
	// against the board config.ResolveRepoProject declares (#271, #280, #313).
	// A mismatch is a blocker; a repo no config declares a board for is a
	// warning. No workspace manifest (single-repo mode) writes no row.
	legacy("project_mapping", "Workspace project mapping", "config", "NGD012", SeverityBlocker, 0, []string{"config"},
		func(ctx context.Context, env *Env) legacyOutcome {
			if env.Cfg == nil {
				return legacyOutcome{}
			}
			report, err := checkProjectMapping(env.Cfg)
			if err != nil {
				return legacyOutcome{}
			}
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
				o := legacyRow(CheckItem{OK: false, Error: strings.Join(mismatches, "; ")})
				o.errors = mismatches
				o.warnings = unverifiable
				return o
			case len(unverifiable) > 0:
				o := legacyRow(CheckItem{OK: false,
					Detail: fmt.Sprintf("%d repo(s) could not be cross-checked", len(unverifiable)),
					Error:  strings.Join(unverifiable, "; ")})
				o.warnings = unverifiable
				return o
			default:
				return legacyRow(CheckItem{OK: true, Detail: "workspace manifest and runtime config agree"})
			}
		})
	// board_population asks the forge where the repo's work actually is (#280):
	// config agreement is not evidence of reachability.
	legacy("board_population", "Board population", "config", "NGD013", SeverityWarning, 30*time.Second,
		[]string{"github_auth", "project"},
		func(ctx context.Context, env *Env) legacyOutcome {
			cfg := env.Cfg
			if cfg == nil || env.Client == nil || cfg.ProjectNumber <= 0 || cfg.Owner == "" || cfg.DefaultRepo == "" {
				return legacyOutcome{}
			}
			pop, err := checkBoardPopulation(ctx, cfg, env.Client)
			switch {
			case err != nil:
				// "I could not look" is a warning, never a failure and never a pass.
				return rowWarn(CheckItem{OK: false,
					Detail: "could not verify which board holds the repo's issues", Error: err.Error()},
					fmt.Sprintf("board population unverified: %s", err.Error()))
			case pop.OpenIssues > 0 && pop.OnBoard == 0:
				msg := fmt.Sprintf(
					"project %d holds 0 of %s/%s's %d open issues — the scheduler polls a board that has none of this repo's work",
					cfg.ProjectNumber, cfg.Owner, cfg.DefaultRepo, pop.OpenIssues)
				if len(pop.ElsewhereBoards) > 0 {
					msg += fmt.Sprintf("; those issues are on project(s) %s", joinInts(pop.ElsewhereBoards))
				}
				o := legacyRow(CheckItem{OK: false, Error: msg})
				o.errors = []string{msg}
				return o
			default:
				return legacyRow(CheckItem{OK: true, Detail: fmt.Sprintf("project %d holds %d of %d open issues",
					cfg.ProjectNumber, pop.OnBoard, pop.OpenIssues)})
			}
		})
	// "Can this machine run a stage at all?" (#862), asked on every run.
	legacy("ai_adapter", "AI coding agent", "environment", "NGD015", SeverityWarning, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkAIAdapterAvailable(newAdapterProbe()))
		})
	// The other half of the lease question (#1913): the lease says a daemon is
	// alive, this says whether its spending is visible.
	legacy("ledger_daemon_coverage", "Ledger daemon coverage", "github", "NGD023", SeverityWarning, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkLedgerDaemonCoverage(env.Cwd, env.Now))
		})
	// Credentials committed under .nightgauge/ (#2024).
	legacy(trackedCredentialsCheck, "Tracked secrets", "credentials", "NGD024", SeverityBlocker, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkTrackedCredentials(env.Cwd))
		})
	// A machine-file credential on a CI host (ADR-024 § 5).
	legacy(ciMachineCredentialsCheck, "CI machine credentials", "credentials", "NGD025", SeverityWarning, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkCIMachineCredentials(ciGetenv))
		})
}

// ---------------------------------------------------------------------------
// #2089 — workspace-hygiene checks
// ---------------------------------------------------------------------------

func init() {
	// Per-issue compose stacks whose worktree no longer exists.
	legacy("compose_orphans", "Orphaned compose projects", "hygiene", "NGD016", SeverityHousekeeping, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			orphans, determined := findOrphanedComposeProjects(ctx, env.Cwd)
			switch {
			case !determined:
				// Never report "no orphans" from an unreadable worktree set (#280, #323).
				msg := "orphaned compose projects unverifiable: could not read the active worktree set across the workspace's repo roots — not inside a git repository or workspace, or `git worktree list` failed. Do NOT run `nightgauge cleanup` on this basis; it would tear down live runs' stacks"
				return rowWarn(CheckItem{OK: false, Detail: "could not determine which issues have an active worktree", Error: msg}, msg)
			case len(orphans) > 0:
				names := make([]string, 0, len(orphans))
				for _, p := range orphans {
					names = append(names, p.Name)
				}
				return rowWarn(CheckItem{OK: false,
					Detail: fmt.Sprintf("%d orphaned issue-* compose project(s)", len(orphans)),
					Error:  fmt.Sprintf("orphaned compose projects: %s — run `nightgauge cleanup`", strings.Join(names, ", "))},
					fmt.Sprintf("orphaned docker compose project(s) detected (%s) — run `nightgauge cleanup`", strings.Join(names, ", ")))
			}
			return legacyRow(CheckItem{OK: true, Detail: "no orphaned issue-* compose projects"})
		})
	legacy("worktree_leaks", "Leaked worktrees", "hygiene", "NGD017", SeverityHousekeeping, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkLeakedWorktrees(env.Cwd, env.Now, mergedPRDoor(ctx, env.Client)))
		})
	// A merged branch whose worktree is already gone (#912).
	legacy("stranded_branches", "Stranded branches", "hygiene", "NGD018", SeverityHousekeeping, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkStrandedBranches(env.Cwd, mergedPRDoor(ctx, env.Client)))
		})
	legacy("pipeline_stashes", "Pipeline stashes", "hygiene", "NGD019", SeverityHousekeeping, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkPipelineStashes(env.Cwd, env.Now))
		})
	// Work from a killed stage preserved under a WIP ref (#1105).
	legacy("preserved_wip", "Preserved WIP refs", "hygiene", "NGD020", SeverityHousekeeping, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkPreservedWip(env.Cwd, env.Now))
		})
	// A stage that is never killed leaks itself (#341). Report-only.
	legacy("orphaned_processes", "Orphaned processes", "hygiene", "NGD021", SeverityHousekeeping, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkOrphanedProcesses(env.Cwd, env.Now))
		})
	// The scheduler lease (#1349): a wedged holder blocks every start here.
	legacy("serve_lease", "Serve lease", "hygiene", "NGD022", SeverityWarning, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkServeLease(env.Cwd, env.Now))
		})
}

// mergedPRDoor builds the merged-PR second door from doctor's own client
// (#916). A nil client yields the closed door and the content test alone.
func mergedPRDoor(ctx context.Context, client *gh.Client) mergedPRDoorFactory {
	return func(repoRoot string) execution.MergedPRLookup {
		lookup := gh.NewMergedPRLookupForRoot(ctx, func() (*gh.Client, error) { return client, nil }, repoRoot)
		if lookup == nil {
			return nil
		}
		return lookup
	}
}

// ---------------------------------------------------------------------------
// #2090 — learning and automation checks
// ---------------------------------------------------------------------------

func init() {
	// Outcome recording bootstraps this file; doctor advertises `outcome init`.
	legacy("complexity_model", "Complexity model", "learning", "NGD014", SeverityWarning, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkComplexityModel(env.Cwd))
		})
	// Absence detectors (#992, #1019, #994, #996): work that should have been
	// observed by now and was not.
	legacy("survival_backlog", "Survival backlog", "learning", "NGD026", SeverityInfo, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			window := survival.DefaultWindowDays
			if env.Cfg != nil {
				window = env.Cfg.Pipeline.ResolveSurvivalWindowDays()
			}
			return rowWarn(checkSurvivalBacklog(env.Cwd, env.Now, window))
		})
	legacy("survival_coverage", "Survival coverage", "learning", "NGD027", SeverityInfo, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkSurvivalCoverage(env.Cwd))
		})
	legacy("corpus_calibration", "Corpus calibration", "learning", "NGD028", SeverityInfo, 0, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			return rowWarn(checkCorpusCalibration(env.Cwd))
		})
	legacy("scheduled_automations", "Scheduled automations", "learning", "NGD029", SeverityWarning, 30*time.Second, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			var declared []cadence.ConfigAutomation
			if env.Cfg != nil {
				declared = env.Cfg.Cadence
			}
			return rowWarn(checkScheduledAutomations(ctx, map[cadence.EvidenceKind]cadenceProbe{
				cadence.EvidenceAutonomousState: autonomousStateEvidence(env.Cwd),
				cadence.EvidenceWorkflowRun:     workflowRunEvidence(env.Client, doctorOwner(env.Cfg), doctorRepo(env.Cfg)),
			}, cadenceScope(env.Cfg, env.Cwd), declared, env.Now))
		})
}

// ---------------------------------------------------------------------------
// #2092 — adapter health
// ---------------------------------------------------------------------------

func init() {
	// Per-adapter health, only when --adapters names adapters. An
	// unhealthy adapter is a warning, never a blocker.
	legacy("adapters", "Adapter health", "adapters", "NGD100", SeverityWarning, 30*time.Second, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			if len(env.Adapters) == 0 {
				return legacyOutcome{}
			}
			health := CheckAdapters(env.Adapters)
			env.setAdapters(health)
			o := legacyRow(CheckItem{OK: true, Detail: fmt.Sprintf("%d adapter(s) checked", len(health))})
			for _, a := range health {
				if !a.OK {
					detail := a.Remediation
					if detail == "" {
						detail = "adapter not ready"
					}
					o.warnings = append(o.warnings, fmt.Sprintf("adapter %q not ready: %s", a.Adapter, detail))
				}
				for _, w := range a.Warnings {
					o.warnings = append(o.warnings, fmt.Sprintf("adapter %q: %s", a.Adapter, w))
				}
			}
			return o
		})
}
