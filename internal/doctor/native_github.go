package doctor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// GitHub, config, budget and credential checks (#2091), native ADR-025
// checks. Severities: missing auth, config or project and a failed board read
// are blockers; budget pressure, unledgered daemon traffic and a stale binary
// that diverges now are warnings; the identity summary is info. No finding,
// evidence value or remedy preview here carries a token, key or credential
// value: evidence names logins, paths, key names and redacted prefixes only.

// Remedy verbs this group declares. The remedy engine (#2093) registers them.
const (
	verbBuildCLI = "binary.build_cli"
	verbRepoInit = "repo.init"
)

// Codes this group adds beyond the ADR-025 per-check defaults.
const (
	codeReadOrg        = "NGD034" // scopes: read:org missing (advisory)
	codeCommitIdentity = "NGD035" // github_identity: App commits not attributed
)

// installCommand is the install command a manual binary remedy names.
const installCommand = "go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest"

func init() {
	native := func(id, title, group, code string, timeout time.Duration, deps []string,
		run func(ctx context.Context, env *Env) ([]Finding, string)) {
		builtinChecks = append(builtinChecks, Check{
			ID: id, Title: title, Group: group, Code: code, Timeout: timeout, DependsOn: deps,
			Run: func(ctx context.Context, env *Env) []Finding {
				fs, detail := run(ctx, env)
				env.SetDetail(id, detail)
				return fs
			},
		})
	}
	const probe = 20 * time.Second
	auth := []string{"github_auth"}

	native("binary", "nightgauge binary", "environment", "NGD001", 0, nil,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			p := probeBinary()
			if !p.found {
				env.setInstall(installMsg)
			}
			return binaryFindings(p, sourceCheckoutRoot(env.Cwd))
		})
	native("skills", "Rendered skills tree", "environment", "NGD002", 0, nil,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return skillsRootFindings(env.Cwd)
		})
	native("gh", "gh CLI", "environment", "NGD003", 0, nil,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			path, err := exec.LookPath("gh")
			return ghFindings(path, err)
		})
	native("github_auth", "GitHub authentication", "github", "NGD004", probe, nil,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			if env.Client == nil {
				return githubAuthFindings(nil, errNoClient)
			}
			return githubAuthFindings(env.tokenScopes(ctx))
		})
	native("api_user", "GitHub API user", "github", "NGD005", probe, auth,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			info, _ := env.tokenScopes(ctx)
			return apiUserFindings(info)
		})
	native("scopes", "OAuth scopes", "github", "NGD006", probe, auth,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			info, _ := env.tokenScopes(ctx)
			return scopeFindings(info)
		})
	native("rate_limit", "GitHub API rate limit", "github", "NGD007", probe, auth,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return rateLimitFindings(env.rateLimit(ctx))
		})
	// Which identity pipeline traffic is billed to, and its ceiling (#1955).
	native("github_identity", "GitHub identity", "github", "NGD009", probe, auth,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			rl, _ := env.rateLimit(ctx)
			return githubIdentityFindings(env.Client.App(), rl)
		})
	// The request ledger, written unattended (#1347).
	native("github_api_budget", "GitHub API budget", "github", "NGD008", 0, nil,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return apiBudgetFindings(env.Cwd, time.Now())
		})
	native("config", "Configuration", "config", "NGD010", 0, nil,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return configFindings(env.Cfg, env.CfgErr, env.Cwd)
		})
	// project and project_mapping read a loaded config even when config
	// reports it (defaults with no repository file); a nil config is config's
	// finding alone.
	native("project", "Project board configuration", "config", "NGD011", 0, nil,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return projectFindings(env.Cfg, env.Cwd)
		})
	// The workspace manifest's project numbers against the board
	// config.ResolveRepoProject declares (#271, #280, #313).
	native("project_mapping", "Workspace project mapping", "config", "NGD012", 0, nil,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			if env.Cfg == nil {
				return nil, "not applicable in this workspace"
			}
			report, err := readProjectMapping(env.Cfg)
			if err != nil {
				return nil, "not applicable in this workspace (no workspace manifest)"
			}
			return projectMappingFindings(report)
		})
	// Where the repo's work actually is (#280): config agreement is not
	// evidence of reachability.
	native("board_population", "Board population", "config", "NGD013", 30*time.Second,
		[]string{"github_auth", "project"},
		func(ctx context.Context, env *Env) ([]Finding, string) {
			cfg := env.Cfg
			if cfg == nil || env.Client == nil || cfg.ProjectNumber <= 0 || cfg.Owner == "" || cfg.DefaultRepo == "" {
				return nil, "not applicable in this workspace"
			}
			pop, err := readBoardPopulation(ctx, cfg, env.Client)
			return boardPopulationFindings(cfg, pop, err)
		})
	// "Can this machine run a stage at all?" (#862).
	native("ai_adapter", "AI coding agent", "environment", "NGD015", 0, nil,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return aiAdapterFindings(newAdapterProbe())
		})
	// Whether a live daemon's spending is visible (#1913).
	native("ledger_daemon_coverage", "Ledger daemon coverage", "github", "NGD023", 0, nil,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return ledgerDaemonCoverageFindings(env.Cwd, env.Now)
		})
	// Credentials committed under .nightgauge/ (#2024).
	native(trackedCredentialsCheck, "Tracked secrets", "credentials", "NGD024", 0, nil,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return trackedCredentialFindings(env.Cwd)
		})
	// A machine-file credential on a CI host (ADR-024 § 5).
	native(ciMachineCredentialsCheck, "CI machine credentials", "credentials", "NGD025", 0, nil,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return ciMachineCredentialFindings(ciGetenv)
		})
}

// errNoClient is github_auth's cause when no GitHub client could be built.
var errNoClient = fmt.Errorf("GitHub client could not be created — check GITHUB_TOKEN env var or run `gh auth login`")

// binaryProbe is what probeBinary observed at check time.
type binaryProbe struct {
	resolved        ResolvedBinary
	found           bool
	detail          string
	resolvedVersion string
	recordedPath    string
	recordedVersion string
	stale           string // non-empty: the resolved binary diverges now
}

// binaryFindings reports a missing binary (blocker) or one that diverges from
// the recorded install right now (warning). The stale remedy rebuilds with
// `make build-cli` inside the source checkout, else names the install command.
func binaryFindings(p binaryProbe, checkout string) ([]Finding, string) {
	const check, code = "binary", "NGD001"
	install := manualRemedy("install", "Install nightgauge on PATH", check,
		"Run `"+installCommand+"`",
		"Or download a release from https://github.com/nightgauge/nightgauge/releases",
		"Run `nightgauge doctor` to verify")
	install.Links = []string{"https://github.com/nightgauge/nightgauge/releases"}
	if !p.found {
		return []Finding{newFinding(check, code, SeverityBlocker,
			"nightgauge binary not found where the hooks look",
			"the hooks resolve no nightgauge binary from this directory, so every hook fails",
			map[string]string{
				"searched": "NIGHTGAUGE_BIN, PATH, repo bin/, canonical-repo bin/, VSCode extension bundle, ~/go/bin",
				"bundles":  strings.TrimPrefix(bundleInventory(p.resolved.Bundles), "; "),
			},
			[]string{"missing"}, install)}, p.detail
	}
	if p.stale == "" {
		return nil, p.detail
	}
	ev := map[string]string{
		"resolved_path":    p.resolved.Path,
		"resolved_step":    string(p.resolved.Step),
		"resolved_version": p.resolvedVersion,
		"recorded_version": p.recordedVersion,
	}
	if p.recordedPath != "" {
		ev["recorded_path"] = p.recordedPath
	}
	remedy := install
	remedy.ID, remedy.Summary = "reinstall", "Reinstall nightgauge so the hooks resolve the current build"
	if checkout != "" {
		remedy = Remedy{ID: "build", Kind: RemedyAuto, Verb: verbBuildCLI, Verify: check, Reversible: true,
			Summary: "Rebuild the CLI from this source checkout with `make build-cli`",
			Preview: "make build-cli in " + checkout + " (rewrites " + filepath.Join(checkout, "bin", "nightgauge") + ")"}
	}
	return []Finding{newFinding(check, code, SeverityWarning, p.stale,
		"the binary the hooks resolve from here reports a different version than the recorded install, so hooks run a different build",
		ev, []string{p.resolved.Path}, remedy)}, p.detail
}

// sourceCheckoutRoot returns the nightgauge source checkout containing dir
// (a go.mod declaring this module and a Makefile with build-cli), or "".
func sourceCheckoutRoot(dir string) string {
	if dir == "" {
		return ""
	}
	root := repoRootOf(dir)
	mod, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	defer mod.Close()
	sc := bufio.NewScanner(mod)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "module github.com/nightgauge/nightgauge" {
		return ""
	}
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil || !strings.Contains(string(mk), "\nbuild-cli:") {
		return ""
	}
	return root
}

// ghFindings reports a missing gh CLI.
func ghFindings(path string, err error) ([]Finding, string) {
	if err == nil {
		return nil, path
	}
	const check, code = "gh", "NGD003"
	return []Finding{newFinding(check, code, SeverityBlocker, "gh CLI not found in PATH",
		"operations that shell out to `gh` cannot run",
		map[string]string{"lookup": "PATH"}, []string{"missing"},
		manualRemedy("install", "Install the GitHub CLI", check,
			"Install gh from https://cli.github.com/ and make sure it is on PATH",
			"Run `gh auth login`"))}, "gh CLI not found in PATH"
}

// authRemedy is the operator's route to a working credential.
func authRemedy(check string) Remedy {
	r := manualRemedy("login", "Provide a valid GitHub credential", check,
		"Run `gh auth login`, or export GITHUB_TOKEN with a valid token",
		"Or configure github_auth.app for a GitHub App installation",
		"Run `nightgauge doctor` to verify")
	r.Links = []string{"https://docs.github.com/en/authentication"}
	return r
}

// githubAuthFindings reports a missing or rejected credential as a blocker.
// The evidence carries the error class, never the token.
func githubAuthFindings(info *gh.TokenScopeInfo, err error) ([]Finding, string) {
	const check, code = "github_auth", "NGD004"
	if err == nil && info != nil {
		return nil, "authenticated as " + info.Login
	}
	reason := "no GitHub client could be created"
	ev := map[string]string{"state": "no_client"}
	if err != nil && err != errNoClient {
		reason = "the token was rejected or could not be checked: " + err.Error()
		ev = map[string]string{"state": "token_check_failed", "error": err.Error()}
		if status := httpStatusIn(err.Error()); status != "" {
			ev["http_status"] = status
		}
	}
	return []Finding{newFinding(check, code, SeverityBlocker,
		"GitHub authentication failed — set GITHUB_TOKEN or run `gh auth login`",
		reason, ev, []string{ev["state"]}, authRemedy(check))}, reason
}

// httpStatusIn extracts a 4xx/5xx status code named in an error message.
func httpStatusIn(msg string) string {
	for _, f := range strings.FieldsFunc(msg, func(r rune) bool { return r < '0' || r > '9' }) {
		if n, err := strconv.Atoi(f); err == nil && len(f) == 3 && n >= 400 && n < 600 {
			return f
		}
	}
	return ""
}

// apiUserFindings reports an authenticated user GitHub would not name.
func apiUserFindings(info *gh.TokenScopeInfo) ([]Finding, string) {
	if info != nil && info.Login != "" {
		return nil, info.Login
	}
	const check, code = "api_user", "NGD005"
	return []Finding{newFinding(check, code, SeverityWarning,
		"GitHub API user check failed: empty login",
		"GET /user returned no login, so actions cannot be attributed to a user",
		map[string]string{"login": ""}, []string{"empty-login"}, authRemedy(check))}, "GET /user returned empty login"
}

// scopeFindings reports missing required scopes (blocker) and the read:org
// advisory (warning, NGD034).
func scopeFindings(info *gh.TokenScopeInfo) ([]Finding, string) {
	const check, code = "scopes", "NGD006"
	switch {
	case info == nil:
		return []Finding{unverifiableFinding(check, code, SeverityWarning, "token scopes",
			"the token-scope probe returned nothing")}, "token scopes unavailable"
	case !info.Valid:
		missing := strings.Join(info.MissingScopes, ", ")
		return []Finding{newFinding(check, code, SeverityBlocker,
			"missing required scopes: "+missing,
			"the classic token lacks scopes pipeline operations need",
			map[string]string{"missing": missing, "granted": strings.Join(info.Scopes, ", ")},
			[]string{missing},
			manualRemedy("refresh", "Grant the missing scopes", check,
				"Run `gh auth refresh -s "+strings.Join(info.MissingScopes, ",")+"`",
				"Or issue a token with those scopes and export it as GITHUB_TOKEN"))}, "missing: " + missing
	case !info.ScopesAdvertised:
		return nil, "not advertised (fine-grained or App token): permissions are per repository, checked by the operations that need them"
	}
	detail := strings.Join(info.Scopes, ", ")
	if msg := readOrgWarning(info.Scopes); msg != "" {
		return []Finding{newFinding(check, codeReadOrg, SeverityWarning, msg,
			"without read:org, private organisation memberships are invisible to the token",
			map[string]string{"granted": detail}, []string{"read:org"},
			manualRemedy("refresh", "Add the read:org scope", check, "Run `gh auth refresh -s read:org`"))}, detail
	}
	return nil, detail
}

// rateLimitFindings reports a low or critical remaining rate limit.
func rateLimitFindings(rl *gh.RateLimitInfo, err error) ([]Finding, string) {
	const check, code = "rate_limit", "NGD007"
	if err != nil || rl == nil {
		reason := "the rate-limit probe returned nothing"
		if err != nil {
			reason = "rate limit check failed: " + err.Error()
		}
		return []Finding{unverifiableFinding(check, code, SeverityWarning, "GitHub API rate limit", reason)},
			"could not check GitHub API rate limit"
	}
	detail := fmt.Sprintf("remaining: %d/%d", rl.Remaining, rl.Limit)
	level := ""
	switch {
	case rl.Remaining < rateLimitCritical:
		level = "critical"
	case rl.Remaining < rateLimitLow:
		level = "low"
	default:
		return nil, detail
	}
	title := fmt.Sprintf("GitHub API rate limit low: %d remaining", rl.Remaining)
	if level == "critical" {
		title = fmt.Sprintf("GitHub API rate limit critically low: %d remaining (operations may fail)", rl.Remaining)
	}
	return []Finding{newFinding(check, code, SeverityWarning, title,
		fmt.Sprintf("below %d remaining, long pipeline runs may exhaust the quota", rateLimitLow),
		map[string]string{"level": level, "remaining": strconv.Itoa(rl.Remaining), "limit": strconv.Itoa(rl.Limit),
			"reset": time.Unix(rl.ResetAt, 0).UTC().Format(time.RFC3339)},
		[]string{level},
		manualRemedy("wait", "Wait for the rate limit to reset before long runs", check,
			"Wait until the reset time in the evidence",
			"Run `nightgauge api-usage --since 1h --by op` to find the heaviest caller"))}, detail
}

// githubIdentityFindings always reports the identity summary as info, and
// warns when a GitHub App is configured without its commit identity.
func githubIdentityFindings(app *gh.AppCredentials, rl *gh.RateLimitInfo) ([]Finding, string) {
	const check, code = "github_identity", "NGD009"
	ev := map[string]string{"kind": "personal_token"}
	summary := "personal token (the user's own rate-limit pool)"
	identity := []string{"personal_token"}
	if app != nil {
		ev = map[string]string{"kind": "github_app", "app": app.String()}
		summary = app.String()
		identity = []string{"github_app", app.String()}
	}
	if rl != nil {
		ev["graphql_ceiling_per_hour"] = strconv.Itoa(rl.Limit)
		summary += fmt.Sprintf(", GraphQL ceiling %d/hr", rl.Limit)
	}
	out := []Finding{newFinding(check, code, SeverityInfo,
		"pipeline GitHub traffic is billed to: "+summary,
		"the identity that authenticates pipeline traffic owns the rate-limit pool it spends",
		ev, identity)}
	if app != nil {
		if _, _, ok := app.CommitIdentity(); !ok {
			out = append(out, newFinding(check, codeCommitIdentity, SeverityWarning,
				"github_auth.app has no slug/bot_user_id: pipeline commits are not attributed to the App",
				"commits keep the generic pipeline author until github_auth.app.slug and bot_user_id are set",
				map[string]string{"app": app.String()}, []string{app.String()},
				manualRemedy("configure", "Set the App's commit identity", check,
					"Set github_auth.app.slug and github_auth.app.bot_user_id in the machine config")))
		}
	}
	return out, summary
}

// repoInitRemedy is the confirm remedy for a missing or incomplete config.
func repoInitRemedy(check, cwd string) Remedy {
	return Remedy{ID: "repo-init", Kind: RemedyConfirm, Verb: verbRepoInit, Verify: check,
		Summary: "Configure this repository with `nightgauge repo-init`",
		Preview: "nightgauge repo-init in " + cwd + ": writes .nightgauge/config.yaml (owner, repository, project board) " +
			"and may create or link a GitHub project board; nothing is written before you confirm"}
}

// configFindings reports a refused, missing or defaults-only configuration as
// a blocker (#2205: never pass on defaults or a user-global file alone).
func configFindings(cfg *config.Config, cfgErr error, cwd string) ([]Finding, string) {
	const check, code = "config", "NGD010"
	switch {
	case cfg == nil && cfgErr != nil:
		msg := "configuration failed to load: " + cfgErr.Error()
		return []Finding{newFinding(check, code, SeverityBlocker, msg,
			"a configuration file exists but was refused",
			map[string]string{"state": "invalid", "error": cfgErr.Error()}, []string{"invalid"},
			manualRemedy("fix", "Correct the configuration file", check,
				"Fix the problem the error names in .nightgauge/config.yaml",
				"Move any credential out of the committed file into the environment or the machine tier"),
			repoInitRemedy(check, cwd))}, msg
	case cfg == nil:
		return []Finding{newFinding(check, code, SeverityBlocker,
			"no .nightgauge/config.yaml — run `nightgauge repo-init` to configure",
			"this repository has never been onboarded",
			map[string]string{"state": "missing", "expected": config.ProjectConfigPath(cwd)}, []string{"missing"},
			repoInitRemedy(check, cwd))}, "no .nightgauge/config.yaml found (fresh repository)"
	}
	loaded, hasRepoConfig := loadedConfigFiles(cwd)
	loadedDesc := "built-in defaults only"
	if len(loaded) > 0 {
		loadedDesc = strings.Join(loaded, ", ")
	}
	if hasRepoConfig {
		return nil, "configuration loaded from " + loadedDesc
	}
	return []Finding{newFinding(check, code, SeverityBlocker,
		"no repository config (.nightgauge/config.yaml) — run /nightgauge:repo-init (or `nightgauge repo-init`); loaded: "+loadedDesc,
		"only built-in defaults or a user-global file loaded; this repository was never onboarded",
		map[string]string{"state": "no_repo_config", "loaded": loadedDesc, "expected": config.ProjectConfigPath(cwd)},
		[]string{"no_repo_config"}, repoInitRemedy(check, cwd))}, "loaded: " + loadedDesc
}

// projectFindings reports a missing project owner or number as a blocker.
func projectFindings(cfg *config.Config, cwd string) ([]Finding, string) {
	const check, code = "project", "NGD011"
	if cfg == nil {
		// The config finding already reports this, with the same remedy.
		return nil, "not applicable: no configuration (see config)"
	}
	if cfg.ProjectNumber == 0 || cfg.Owner == "" {
		return []Finding{newFinding(check, code, SeverityBlocker,
			"project number or owner not set in .nightgauge/config.yaml",
			"the scheduler has no board to poll",
			map[string]string{"owner": cfg.Owner, "project_number": strconv.Itoa(cfg.ProjectNumber)},
			[]string{"unset"}, repoInitRemedy(check, cwd))}, "project owner or number missing"
	}
	return nil, fmt.Sprintf("project %d (owner: %s)", cfg.ProjectNumber, cfg.Owner)
}

// projectMappingFindings reports a manifest/config board mismatch (blocker)
// and a repository that cannot be cross-checked (warning, unverifiable).
func projectMappingFindings(report config.ProjectMappingReport) ([]Finding, string) {
	const check, code = "project_mapping", "NGD012"
	var out []Finding
	for _, m := range report.Mismatches {
		s := m.String()
		out = append(out, newFinding(check, code, SeverityBlocker, s,
			"the workspace manifest and the runtime config name different project boards, so the scheduler polls one the manifest does not",
			map[string]string{"mismatch": s}, []string{s},
			manualRemedy("align", "Make the workspace manifest and the runtime config name one board", check,
				"Edit .vscode/nightgauge-workspace.yaml or .nightgauge/config.yaml so both name the same project_number",
				"Run `nightgauge project resolve --json` to see what config resolves")))
	}
	for _, u := range report.Unresolvable {
		s := u.String()
		f := unverifiableFinding(check, code, SeverityWarning, "workspace project mapping", s)
		f.Fingerprint = Fingerprint(code, check, "unverifiable", s)
		out = append(out, f)
	}
	switch {
	case len(report.Mismatches) > 0:
		return out, fmt.Sprintf("%d mismatch(es)", len(report.Mismatches))
	case len(report.Unresolvable) > 0:
		return out, fmt.Sprintf("%d repo(s) could not be cross-checked", len(report.Unresolvable))
	}
	return nil, "workspace manifest and runtime config agree"
}

// boardPopulationFindings reports a failed board read or a board holding none
// of the repo's open issues as a blocker. Deep permission diagnosis is #2094.
func boardPopulationFindings(cfg *config.Config, pop boardPopulation, err error) ([]Finding, string) {
	const check, code = "board_population", "NGD013"
	board := fmt.Sprintf("%s/%d", cfg.Owner, cfg.ProjectNumber)
	if err != nil {
		return []Finding{newFinding(check, code, SeverityBlocker,
			"board read failed: "+err.Error(),
			"the configured project board could not be read, so the scheduler cannot see its work",
			map[string]string{"state": "read_failed", "board": board, "repo": cfg.Owner + "/" + cfg.DefaultRepo, "error": err.Error()},
			[]string{board, "read_failed"},
			manualRemedy("access", "Give the credential read access to the project board", check,
				"Confirm project "+strconv.Itoa(cfg.ProjectNumber)+" exists under "+cfg.Owner,
				"Confirm the token or App can read projects (classic token: `project` scope)",
				"Run `nightgauge doctor` to verify"))}, "could not verify which board holds the repo's issues"
	}
	if pop.OpenIssues > 0 && pop.OnBoard == 0 {
		msg := fmt.Sprintf(
			"project %d holds 0 of %s/%s's %d open issues — the scheduler polls a board that has none of this repo's work",
			cfg.ProjectNumber, cfg.Owner, cfg.DefaultRepo, pop.OpenIssues)
		ev := map[string]string{"state": "empty", "board": board, "open_issues": strconv.Itoa(pop.OpenIssues)}
		if len(pop.ElsewhereBoards) > 0 {
			msg += fmt.Sprintf("; those issues are on project(s) %s", joinInts(pop.ElsewhereBoards))
			ev["elsewhere"] = joinInts(pop.ElsewhereBoards)
		}
		return []Finding{newFinding(check, code, SeverityBlocker, msg,
			"config agreement is not reachability: the configured board holds none of this repo's open work",
			ev, []string{board, "empty"},
			manualRemedy("align", "Point config at the board holding the work, or add the issues to this board", check,
				"Set project_number in .nightgauge/config.yaml to the board named in the evidence",
				"Or add the repository's open issues to project "+strconv.Itoa(cfg.ProjectNumber)))}, msg
	}
	return nil, fmt.Sprintf("project %d holds %d of %d open issues", cfg.ProjectNumber, pop.OnBoard, pop.OpenIssues)
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
// The URLs follow the canonical distribution channel.
const installMsg = "nightgauge is not in PATH.\n" +
	"Install via: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest\n" +
	"Or download from: https://github.com/nightgauge/nightgauge/releases\n" +
	"Run `nightgauge doctor` after installing to verify your environment."

// probeBinary reports which binary the HOOKS would resolve from the current
// working directory, via the six-step cascade documented in ResolveBinary
// (mirroring guard.sh): $NIGHTGAUGE_BIN, PATH, repo bin/, canonical-repo bin/,
// VSCode extension bundle, or ~/go/bin.
//
// The cascade is cwd-dependent — steps 2 and 3 run `git rev-parse` in the
// caller's directory — so the same hook script legitimately runs different
// binaries in different repos. #356: that was invisible, which let a merged
// hook fix sit inert everywhere except the repo it was built in. Reporting the
// resolved path, the resolving step, the binary's own version, and the
// extension-bundle inventory makes it inspectable from wherever the operator
// is standing.
//
// found distinguishes "not installed" (a blocker; install instructions apply)
// from "installed but diverging" (a warning: a binary that mostly works beats
// a hook that hard-fails).
//
// binaryIsolatedEnv declares that the PATH-resolved binary deliberately
// differs from the VSCode extension bundle (for example an isolated dogfood
// build), downgrading the cross-step version mismatch to info (#2201).
const binaryIsolatedEnv = "NIGHTGAUGE_BINARY_ISOLATED"

// isUnversionedBuild reports whether a `nightgauge version` first line names
// a build with no release version ("dev" or "dev+<vcs revision>").
func isUnversionedBuild(versionLine string) bool {
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(versionLine), "nightgauge"))
	return v == "dev" || strings.HasPrefix(v, "dev+")
}

func probeBinary() binaryProbe {
	resolved := ResolveBinary()
	p := binaryProbe{resolved: resolved}
	if resolved.Path == "" {
		// The bundle inventory is at its MOST actionable here — "N bundles
		// installed, none runnable" is a different problem from "nothing
		// installed" — so it is reported rather than discarded.
		p.detail = "nightgauge not found via NIGHTGAUGE_BIN, PATH, repo bin/, canonical-repo bin/, VSCode extension bundle, or ~/go/bin" + bundleInventory(resolved.Bundles)
		return p
	}
	p.found = true

	detail := fmt.Sprintf("hooks resolve %s from this directory (via %s)", resolved.Path, resolved.Step)
	p.resolvedVersion = binaryVersion(resolved.Path)
	if p.resolvedVersion != "" {
		detail += fmt.Sprintf("; reports version %s", p.resolvedVersion)
	}

	// An earlier cascade step can resolve successfully while silently running
	// a different build from the extension VS Code records. Doctor is on-demand,
	// so it can afford the second exec that guard.sh must not pay on every tool
	// call. RecordedUsed guarantees SelectedPath is the runnable recorded binary,
	// rather than an unrecorded fallback. Versions are compared now, at check
	// time, never read from a cache.
	if scan := resolved.Bundles; scan.RecordedUsed && scan.SelectedPath != "" && scan.SelectedPath != resolved.Path {
		recordedVersion := binaryVersion(scan.SelectedPath)
		if recordedVersion != "" {
			detail += fmt.Sprintf("; recorded VSCode bundle binary %s reports version %s", scan.SelectedPath, recordedVersion)
			switch {
			case p.resolvedVersion == "" || p.resolvedVersion == recordedVersion:
			case isUnversionedBuild(p.resolvedVersion) || isUnversionedBuild(recordedVersion):
				// #2201: a plain `go build` stamps no release version, so
				// "stale" is a claim nothing here can back. Say so, as detail.
				detail += "; unversioned build (cannot compare with the recorded bundle)"
			case os.Getenv(binaryIsolatedEnv) != "":
				detail += fmt.Sprintf("; differs from the recorded bundle, intentionally isolated (%s set)", binaryIsolatedEnv)
			default:
				p.recordedPath, p.recordedVersion = scan.SelectedPath, recordedVersion
				p.stale = fmt.Sprintf(
					"stale binary: hooks resolve %s via %s, reporting version %s; the recorded VSCode extension bundle binary %s reports version %s",
					resolved.Path, resolved.Step, p.resolvedVersion, scan.SelectedPath, recordedVersion,
				)
			}
		}
	}
	p.detail = detail + bundleInventory(resolved.Bundles)

	if resolved.Step == StepVSCodeExtension && resolved.Bundles.Divergence != DivergenceNone {
		// The detail is kept: the resolving step and the resolved binary's own
		// version are the two facts that answer "is this actually an old
		// build?" (#356 AC3 — report on EVERY outcome).
		p.recordedPath, p.recordedVersion = "", resolved.Bundles.RecordedVersion
		p.stale = divergenceMessage(resolved)
	}
	return p
}

// bundleInventory renders the step-4 VSCode-extension inventory: how many
// bundle directories are on disk and which one VS Code RECORDS as installed.
// It is appended on every outcome, including when an earlier cascade step
// wins — that is the exact confusion #356 describes, since inside a nightgauge
// checkout `bin/nightgauge` short-circuits the cascade and the bundles other
// repos will run are invisible.
//
// Versions here are opaque display strings. Nothing in this file orders them.
func bundleInventory(scan VSCodeBundleScan) string {
	n := len(scan.Bundles)
	if n == 0 && scan.RecordedDir == "" {
		return ""
	}
	out := fmt.Sprintf("; %d VSCode extension bundle dir(s) on disk", n)
	switch {
	case scan.RecordedVersion == "":
		out += ", no usable VSCode install record"
	case scan.RecordedUsed:
		out += fmt.Sprintf(", VSCode records %s as installed (step-4 selection)", scan.RecordedVersion)
	default:
		out += fmt.Sprintf(", VSCode records %s as installed (not step-4 selection)", scan.RecordedVersion)
	}
	return out
}

// divergenceMessage explains a step-4 resolution the install record does not
// confirm, naming the recorded version, the resolved version and the resolved
// path — the same three facts guard.sh writes to its side-channel log.
func divergenceMessage(resolved ResolvedBinary) string {
	scan := resolved.Bundles
	if scan.Divergence == DivergenceRecordUnusable {
		return fmt.Sprintf(
			"stale binary: VSCode records extension bundle %s as installed, but its bundled binary is missing or not executable; hooks resolve bundle %s from this directory, running %s",
			scan.RecordedVersion, scan.SelectedVersion, resolved.Path,
		)
	}
	return fmt.Sprintf(
		"stale binary: no usable VSCode install record for the nightgauge extension (~/.vscode/extensions/extensions.json); %d bundle dir(s) on disk, hooks resolve bundle %s from this directory, running %s",
		len(scan.Bundles), scan.SelectedVersion, resolved.Path,
	)
}

// binaryVersion runs `<path> version` and returns its first line, or "" when
// the probe fails or produces nothing. Bounded so a wedged binary cannot hang
// `doctor`.
//
// The exec lives here and NOT in guard.sh on purpose: `doctor` runs on demand,
// while guard.sh runs on every single tool call and already pays for one
// subprocess. Note the verb is `version`, not `--version` — there is no such
// flag.
func binaryVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if len(line) > 120 {
		line = line[:120]
	}
	return line
}

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

// readBoardPopulation asks the forge whether the configured board holds any
// of the repo's open issues, and — when it holds none — which board(s) do.
//
// This is the only check that consults ground truth. `project` verifies the
// configured board RESOLVES; `project_mapping` verifies two config files agree
// about its number. Neither looks at membership, so both pass while the
// scheduler polls a board containing none of the repo's work (#280).
func readBoardPopulation(ctx context.Context, cfg *config.Config, client *gh.Client) (boardPopulation, error) {
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
	// Membership only, so no item's relationship lists are read.
	items, _, err := gh.NewBoardService(client, cfg.Owner, cfg.ProjectNumber, ownerType).ListOpenItemsWithRelations(ctx, gh.NoRelations)
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

// readProjectMapping cross-validates the workspace manifest's
// repositories[].project_number entries against config.ResolveRepoProjectNumber.
// It returns a non-nil error only when no workspace manifest exists
// (single-repo mode — nothing to check).
func readProjectMapping(cfg *config.Config) (config.ProjectMappingReport, error) {
	wd, err := os.Getwd()
	if err != nil {
		return config.ProjectMappingReport{}, err
	}
	return config.FindWorkspaceProjectMappingMismatches(cfg, wd)
}
