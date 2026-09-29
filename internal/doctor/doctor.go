// Package doctor provides environment health checks for the nightgauge pipeline.
//
// Checks are registered in a Registry (registry.go) and emit ADR-025 Findings
// (finding.go). `nightgauge doctor --json` emits JSON v2: `findings[]` and
// `summary`, plus the top-level fields skills/_shared/PREFLIGHT.md reads,
// derived from the findings with unchanged meaning.
package doctor

import (
	"context"
	"os"
	"path/filepath"
	"sort"
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
	return runRegistry(ctx, DefaultRegistry(), env)
}

// runRegistry runs reg in env and builds the JSON v2 result.
func runRegistry(ctx context.Context, reg *Registry, env *Env) DoctorResult {
	results := Runner{}.Run(ctx, reg, env)
	res := BuildResult(results)
	env.mu.Lock()
	res.InstallInstructions = env.install
	res.Adapters = env.adapter
	env.mu.Unlock()
	return res
}

// builtinChecks is filled by each check group's init, in file order.
var builtinChecks []Check

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

// RedactFinding passes a finding's text, evidence and remedy text through the
// shared config redactor (ADR-025 § 8). It returns a copy; the input's
// remedies are not modified.
func RedactFinding(f Finding) Finding {
	f.Title = config.RedactSecretString(f.Title)
	f.Cause = config.RedactSecretString(f.Cause)
	f.Evidence = config.RedactEvidence(f.Evidence)
	if f.Evidence == nil {
		f.Evidence = map[string]string{}
	}
	remedies := make([]Remedy, len(f.Remedies))
	for i, r := range f.Remedies {
		remedies[i] = redactRemedy(r)
	}
	f.Remedies = remedies
	return f
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
