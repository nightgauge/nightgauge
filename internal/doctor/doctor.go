// Package doctor provides environment health checks for the nightgauge pipeline.
//
// Checks are registered in a Registry (registry.go) and emit ADR-025 Findings
// (finding.go). `nightgauge doctor --json` emits JSON v2: `findings[]` and
// `summary`, plus the top-level fields skills/_shared/PREFLIGHT.md reads,
// derived from the findings with unchanged meaning.
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
	"time"

	"github.com/nightgauge/nightgauge/internal/cadence"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/dockercompose"
	"github.com/nightgauge/nightgauge/internal/execution"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// SchemaVersion is the `v` field of `nightgauge doctor --json`.
const SchemaVersion = 2

// DoctorResult is the JSON v2 output of `nightgauge doctor` (ADR-025 § 5).
// Healthy, ExitCode, FailedChecks, Errors, Warnings and InstallInstructions
// are derived from Findings: failed_checks and errors come from blockers,
// warnings from warnings; housekeeping and info feed neither.
type DoctorResult struct {
	V                   int       `json:"v"`
	Findings            []Finding `json:"findings"`
	Summary             Summary   `json:"summary"`
	Healthy             bool      `json:"healthy"`   // true when ExitCode < 2
	ExitCode            int       `json:"exit_code"` // 0 healthy, 1 warnings, 2 blocker
	FailedChecks        []string  `json:"failed_checks"`
	Errors              []string  `json:"errors"`
	Warnings            []string  `json:"warnings"`
	InstallInstructions string    `json:"install_instructions"`
	// Adapters is the per-adapter health section (#4031), populated only when
	// the caller requests adapters (`doctor --adapters codex,claude`).
	Adapters []AdapterHealth `json:"adapters,omitempty"`

	// Results is every registered check's outcome in registry order; the
	// human renderer walks it, so every registered check renders.
	Results []CheckResult `json:"-"`
	// Checks keeps the legacy per-check rows for in-process callers until
	// #2098 deletes CheckItem. It is not part of JSON v2.
	Checks map[string]CheckItem `json:"-"`
}

// CheckItem is a legacy check's row, wrapped into Findings by
// legacy_checks.go. #2098 deletes it.
type CheckItem struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
	// Findings is the structured form of a tracked_secrets failure (#2024).
	Findings []SecretFinding `json:"findings,omitempty"`
}

// doctorOwner / doctorRepo are the nil-safe accessors the cadence probes need:
// RunDoctor is called with a nil cfg on an unconfigured workspace, which is
// precisely a workspace worth reporting on rather than crashing over.
func doctorOwner(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Owner
}

// loadedConfigFiles lists the config files config.Load reads for
// workspaceRoot, in precedence order, and whether a repository-level file
// (.nightgauge/config.yaml or the legacy config.json) is among them. Without a
// repository file config.Load returns built-in defaults.
func loadedConfigFiles(workspaceRoot string) ([]string, bool) {
	exists := func(p string) bool {
		info, err := os.Stat(p)
		return err == nil && info.Mode().IsRegular()
	}
	project := config.ProjectConfigPath(workspaceRoot)
	if !exists(project) {
		legacy := filepath.Join(workspaceRoot, ".nightgauge", "config.json")
		if exists(legacy) {
			return []string{legacy}, true
		}
		return nil, false
	}
	var files []string
	if machine, err := config.MachineConfigPath(); err == nil && exists(machine) {
		files = append(files, machine)
	}
	files = append(files, project)
	if local := config.LocalConfigPath(workspaceRoot); exists(local) {
		files = append(files, local)
	}
	return files, true
}

// cadenceScope scopes the built-in cadence registry to this workspace (#2199):
// core's own release workflow only in the core repo, and the autonomous loop
// only where autonomous mode is configured or has ever run.
func cadenceScope(cfg *config.Config, workspaceRoot string) cadence.Scope {
	scope := cadence.Scope{}
	if owner, repo := doctorOwner(cfg), doctorRepo(cfg); owner != "" && repo != "" {
		scope.Repo = owner + "/" + repo
	}
	if cfg != nil && cfg.Autonomous != nil {
		scope.Autonomous = autonomousCovers(cfg.Autonomous.ResolvedEnabledRepos(doctorOwner(cfg)), scope.Repo)
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, ".nightgauge", "autonomous", "state.json")); err == nil {
		scope.Autonomous = true
	}
	return scope
}

// autonomousCovers reports whether autonomous mode scans repo. An empty
// enabled_repos list means every configured repo; otherwise the repo must be
// listed (#2218). A machine-wide autonomous block limited to other repos must
// not register the autonomous-loop cadence here.
func autonomousCovers(enabled []string, repo string) bool {
	if len(enabled) == 0 {
		return true
	}
	for _, r := range enabled {
		if repo != "" && strings.EqualFold(r, repo) {
			return true
		}
	}
	return false
}

func doctorRepo(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.DefaultRepo
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
// client may be nil when GitHub authentication failed; auth-dependent checks
// are then skipped. cfg may be nil for fresh repositories that have not yet run
// `nightgauge repo-init`. adapters is the optional set of execution adapters to
// health-check (#4031).
func RunDoctor(ctx context.Context, cfg *config.Config, client *gh.Client, adapters []string) DoctorResult {
	return RunDoctorWithConfigError(ctx, cfg, nil, client, adapters)
}

// RunDoctorWithConfigError is RunDoctor for a caller that loaded cfg itself
// and got cfgErr. A nil cfg with a non-nil cfgErr is a config that exists and
// was refused — for example a plaintext token in the committed file (#2023) —
// and is reported as a failed config check rather than as a fresh repository.
func RunDoctorWithConfigError(ctx context.Context, cfg *config.Config, cfgErr error, client *gh.Client, adapters []string) DoctorResult {
	cwd, _ := os.Getwd()
	env := &Env{Cfg: cfg, CfgErr: cfgErr, Client: client, Cwd: cwd, Now: time.Now(), Adapters: adapters}
	results := Runner{}.Run(ctx, DefaultRegistry(), env)
	res := BuildResult(results)
	env.mu.Lock()
	res.InstallInstructions = env.install
	res.Adapters = env.adapter
	res.Checks = make(map[string]CheckItem, len(env.items))
	for k, v := range env.items {
		res.Checks[k] = v
	}
	env.mu.Unlock()
	return res
}

// DefaultRegistry returns the built-in checks in render order.
func DefaultRegistry() *Registry {
	reg := NewRegistry()
	for _, c := range builtinChecks {
		reg.MustRegister(c)
	}
	return reg
}

func (e *Env) setInstall(msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.install = msg
}

func (e *Env) setAdapters(h []AdapterHealth) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.adapter = h
}

// BuildResult derives a JSON v2 result from check results (ADR-025 § 5).
// Every finding is redacted here, before any surface renders it.
func BuildResult(results []CheckResult) DoctorResult {
	res := DoctorResult{
		V:            SchemaVersion,
		Findings:     []Finding{},
		FailedChecks: []string{},
		Errors:       []string{},
		Warnings:     []string{},
		Results:      make([]CheckResult, len(results)),
	}
	failed := map[string]bool{}
	for i, r := range results {
		for j := range r.Findings {
			r.Findings[j] = RedactFinding(r.Findings[j])
		}
		r.Detail = config.RedactSecretString(r.Detail)
		res.Results[i] = r
		for _, f := range r.Findings {
			res.Findings = append(res.Findings, f)
			switch f.Severity {
			case SeverityBlocker:
				res.Errors = append(res.Errors, f.Title)
				if !failed[f.Check] {
					failed[f.Check] = true
					res.FailedChecks = append(res.FailedChecks, f.Check)
				}
			case SeverityWarning:
				res.Warnings = append(res.Warnings, f.Title)
			}
		}
	}
	sort.SliceStable(res.Findings, func(a, b int) bool {
		return res.Findings[a].Severity.rank() < res.Findings[b].Severity.rank()
	})
	res.Summary = Summarize(res.Findings)
	res.ExitCode = ExitCodeFor(res.Summary)
	res.Healthy = res.ExitCode < 2
	return res
}

// ExitCodeFor maps a summary to the ADR-025 exit code: 2 for any blocker, 1
// for warnings only, else 0. Housekeeping and info never change it.
func ExitCodeFor(s Summary) int {
	switch {
	case s.Blocker > 0:
		return 2
	case s.Warning > 0:
		return 1
	default:
		return 0
	}
}

// RedactFinding passes a finding's text and evidence through the shared
// config redactor (ADR-025 § 8).
func RedactFinding(f Finding) Finding {
	f.Title = config.RedactSecretString(f.Title)
	f.Cause = config.RedactSecretString(f.Cause)
	f.Evidence = config.RedactEvidence(f.Evidence)
	if f.Evidence == nil {
		f.Evidence = map[string]string{}
	}
	if f.Remedies == nil {
		f.Remedies = []Remedy{}
	}
	return f
}

// checkBinary reports which binary the HOOKS would resolve from the current
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
// The second return value reports whether the binary resolved at all, so the
// caller can distinguish "not installed" (install instructions apply) from
// "installed but diverging from the install record" (they do not).
//
// This check is warning-only — never a required failure — since discovering
// the binary is what this command itself is; a missing binary can only be
// observed by the environment that ran `doctor` in the first place. A
// diverging bundle is likewise a warning: a binary that mostly works beats a
// hook that hard-fails.
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

func checkBinary() (CheckItem, bool) {
	resolved := ResolveBinary()
	if resolved.Path == "" {
		// The bundle inventory is at its MOST actionable here — "N bundles
		// installed, none runnable" is a different problem from "nothing
		// installed" — so it is reported rather than discarded.
		return CheckItem{
			OK:    false,
			Error: "nightgauge not found via NIGHTGAUGE_BIN, PATH, repo bin/, canonical-repo bin/, VSCode extension bundle, or ~/go/bin" + bundleInventory(resolved.Bundles),
		}, false
	}

	detail := fmt.Sprintf("hooks resolve %s from this directory (via %s)", resolved.Path, resolved.Step)
	resolvedVersion := binaryVersion(resolved.Path)
	if resolvedVersion != "" {
		detail += fmt.Sprintf("; reports version %s", resolvedVersion)
	}

	// An earlier cascade step can resolve successfully while silently running
	// a different build from the extension VS Code records. Doctor is on-demand,
	// so it can afford the second exec that guard.sh must not pay on every tool
	// call. RecordedUsed guarantees SelectedPath is the runnable recorded binary,
	// rather than an unrecorded fallback.
	var crossStepWarning string
	if scan := resolved.Bundles; scan.RecordedUsed && scan.SelectedPath != "" && scan.SelectedPath != resolved.Path {
		recordedVersion := binaryVersion(scan.SelectedPath)
		if recordedVersion != "" {
			detail += fmt.Sprintf("; recorded VSCode bundle binary %s reports version %s", scan.SelectedPath, recordedVersion)
			switch {
			case resolvedVersion == "" || resolvedVersion == recordedVersion:
			case isUnversionedBuild(resolvedVersion) || isUnversionedBuild(recordedVersion):
				// #2201: a plain `go build` stamps no release version, so
				// "stale" is a claim nothing here can back. Say so, as info.
				detail += "; unversioned build (cannot compare with the recorded bundle)"
			case os.Getenv(binaryIsolatedEnv) != "":
				detail += fmt.Sprintf("; differs from the recorded bundle, intentionally isolated (%s set)", binaryIsolatedEnv)
			default:
				crossStepWarning = fmt.Sprintf(
					"stale binary: hooks resolve %s via %s, reporting version %s; the recorded VSCode extension bundle binary %s reports version %s",
					resolved.Path, resolved.Step, resolvedVersion, scan.SelectedPath, recordedVersion,
				)
			}
		}
	}
	detail += bundleInventory(resolved.Bundles)

	if resolved.Step == StepVSCodeExtension && resolved.Bundles.Divergence != DivergenceNone {
		// Detail is kept, not discarded: the resolving step and the resolved
		// binary's own version are the two facts that answer "is this actually
		// an old build?", and this is precisely the outcome an operator is
		// investigating (AC3 — report on EVERY outcome).
		return CheckItem{OK: false, Detail: detail, Error: divergenceMessage(resolved)}, true
	}
	if crossStepWarning != "" {
		return CheckItem{OK: false, Detail: detail, Error: crossStepWarning}, true
	}

	return CheckItem{OK: true, Detail: detail}, true
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

// checkGH reports whether the `gh` CLI is reachable via PATH.
func checkGH() CheckItem {
	path, err := exec.LookPath("gh")
	if err != nil {
		return CheckItem{OK: false, Error: "gh CLI not found in PATH"}
	}
	return CheckItem{OK: true, Detail: path}
}

// findOrphanedComposeProjects returns the set of `issue-NNN` compose projects
// whose corresponding git worktree no longer exists, plus whether the
// active-worktree set backing that judgement was DETERMINED.
//
// determined=false means the orphan list is meaningless and must not be
// reported: with no readable worktree set every compose project looks orphaned,
// so the check would name a live run's stack and tell the operator to run
// `nightgauge cleanup` on it. That is #280's defect — a doctor message
// asserting something about a path it never consulted — with #296's
// consequences one indirection later, since the operator's hand carries out the
// teardown the scheduler now refuses to. Callers surface it as unverifiable.
//
// Returns (nil, true) when docker isn't available or no projects exist: there
// is genuinely nothing to be wrong about, which is a determined answer.
func findOrphanedComposeProjects(ctx context.Context, startDir string) ([]dockercompose.Project, bool) {
	projects, err := dockercompose.ListIssueProjects(ctx)
	if err != nil || len(projects) == 0 {
		return nil, true
	}
	active, determined := execution.ActiveWorktreeIssues(config.WorkspaceRepoRoots(startDir))
	if !determined {
		return nil, false
	}
	var orphans []dockercompose.Project
	for _, p := range projects {
		if !active[p.IssueNumber] {
			orphans = append(orphans, p)
		}
	}
	return orphans, true
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

func checkProjectMapping(cfg *config.Config) (config.ProjectMappingReport, error) {
	wd, err := os.Getwd()
	if err != nil {
		return config.ProjectMappingReport{}, err
	}
	return config.FindWorkspaceProjectMappingMismatches(cfg, wd)
}
