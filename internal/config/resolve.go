package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	workspace "github.com/nightgauge/nightgauge/internal/knowledge/workspace"
	yaml "gopkg.in/yaml.v3"
)

// ResolveRepoProjectNumber resolves the GitHub Project V2 board number that
// autonomous.repositories.<repo>.project_number designates for ownerPart/repoPart —
// the single authoritative "which project board does this repo use?" answer
// (Source B). It is shared by the CLI (`nightgauge project resolve --repo`,
// `nightgauge project add`) and by cross-checks in internal/doctor and
// internal/orchestrator so there is exactly one implementation of the
// merge-tier lookup (#271).
//
// When ownerPart/repoPart names the local config's default repo (or cfg has
// no default repo configured), the local cfg.ProjectNumber is authoritative
// and is returned unconditionally — cross-repo mapping only applies when a
// different repo is named. Returns an error naming the exact config path to
// fix when no mapping exists for a genuinely cross-repo target.
func ResolveRepoProjectNumber(cfg *Config, ownerPart, repoPart string) (int, error) {
	if cfg == nil {
		return 0, fmt.Errorf("no config loaded")
	}
	localRepo := cfg.Owner + "/" + cfg.DefaultRepo
	targetRepo := ownerPart + "/" + repoPart
	if cfg.DefaultRepo == "" || targetRepo == localRepo {
		return cfg.ProjectNumber, nil
	}
	if cfg.Autonomous != nil {
		if rc := cfg.Autonomous.Repositories[targetRepo]; rc != nil && rc.ProjectNumber > 0 {
			return rc.ProjectNumber, nil
		}
		if rc := cfg.Autonomous.Repositories[repoPart]; rc != nil && rc.ProjectNumber > 0 {
			return rc.ProjectNumber, nil
		}
	}
	return 0, fmt.Errorf("no project board mapping for --repo %s (configure autonomous.repositories.%s.project_number in .nightgauge/config.yaml)", targetRepo, repoPart)
}

// workspaceManifestRepo is the subset of a .vscode/nightgauge-workspace.yaml
// repositories[] entry needed to cross-check its project_number (Source A)
// against the runtime-resolved config (Source B).
type workspaceManifestRepo struct {
	Name          string `yaml:"name"`
	ProjectNumber int    `yaml:"project_number"`
}

// ProjectMappingMismatch is one repo whose workspace-manifest project_number
// (Source A) disagrees with the runtime-resolved project_number (Source B).
type ProjectMappingMismatch struct {
	// Repo is the manifest entry's repositories[].name, verbatim.
	Repo string
	// ManifestProject is the workspace-manifest project_number (Source A).
	ManifestProject int
	// ResolvedProject is the runtime-resolved project_number (Source B).
	ResolvedProject int
}

// String renders the standard human-readable mismatch message shared by
// doctor and the scheduler startup warning.
func (m ProjectMappingMismatch) String() string {
	return fmt.Sprintf(
		"project mapping mismatch for %s: workspace yaml says project %d, runtime config resolves to %d — see nightgauge doctor",
		m.Repo, m.ManifestProject, m.ResolvedProject)
}

// ProjectMappingUnresolved is one manifest repo that could not be checked at
// all because Source B has no mapping for it. This is NOT a clean bill of
// health — the comparison never happened — but it is also not a total outage,
// and the distinction matters (#280).
//
// ResolveRepoProjectNumber refuses to guess for a cross-repo target on
// purpose: defaulting there silently misrouted new issues to the primary
// board (#3232), so every caller that FILES something (issue-create, board
// sync, `nightgauge project resolve`) must fail loudly instead.
//
// The autonomous scheduler does not use this resolver. It builds one
// depgraph.RepoConfig per repo from the single top-level project number, so
// it polls that board for every repo regardless. Reporting this condition as
// "the scheduler polls no board" would therefore be false. What is true is
// narrower and still worth saying: issue creation and board sync targeting
// this repo will error until a mapping exists.
type ProjectMappingUnresolved struct {
	// Repo is the manifest entry's repositories[].name, verbatim.
	Repo string
	// ManifestProject is the workspace-manifest project_number (Source A).
	ManifestProject int
	// Err is the resolver's own message, carried rather than discarded.
	Err string
}

// String renders the human-readable message for a repo that has no runtime
// board mapping at all. It states the consequence that actually follows —
// see the type comment for why it does not mention the scheduler.
func (u ProjectMappingUnresolved) String() string {
	return fmt.Sprintf(
		"project mapping unverifiable for %s: workspace yaml says project %d, but runtime config has no mapping (%s) — the manifest value is unchecked, and issue creation or board sync targeting this repo will fail until autonomous.repositories.%s.project_number is set",
		u.Repo, u.ManifestProject, u.Err, u.Repo)
}

// ProjectMappingReport is the full result of the manifest cross-check. It has
// two populated fields on purpose: "the two sources disagree" and "one source
// is missing entirely" are different conditions with different fixes, and
// collapsing the second into silence is what let a workspace report
// "manifest and runtime config agree" while three of its four repos had no
// runtime mapping at all (#280).
type ProjectMappingReport struct {
	// Mismatches are repos where both sources answered and disagreed.
	Mismatches []ProjectMappingMismatch
	// Unresolvable are repos where Source B could not answer. Never treat an
	// entry here as agreement.
	Unresolvable []ProjectMappingUnresolved
}

// OK reports whether the cross-check found nothing wrong. An unresolvable repo
// counts as wrong: it means the comparison never happened.
func (r ProjectMappingReport) OK() bool {
	return len(r.Mismatches) == 0 && len(r.Unresolvable) == 0
}

// Problems renders every finding as a flat list of human-readable lines, in a
// stable order (mismatches first, then unresolvable).
func (r ProjectMappingReport) Problems() []string {
	out := make([]string, 0, len(r.Mismatches)+len(r.Unresolvable))
	for _, m := range r.Mismatches {
		out = append(out, m.String())
	}
	for _, u := range r.Unresolvable {
		out = append(out, u.String())
	}
	return out
}

// FindWorkspaceProjectMappingMismatches cross-validates every
// repositories[].project_number entry in the .vscode/nightgauge-workspace.yaml
// manifest (walked up from startDir) against ResolveRepoProjectNumber for
// that same repo, returning a report of every disagreeing repo AND every repo
// the check could not evaluate. Shared by internal/doctor's project_mapping
// check, the orchestrator scheduler's startup warning, and the
// stranded-ready-items sweep producer (#271) so there is exactly one
// implementation of the cross-check. Returns a non-nil error only when no
// workspace manifest exists (single-repo mode — not a failure, just "nothing
// to check").
//
// An unresolvable repo is reported, never skipped (#280). The previous version
// dropped it with a comment claiming the resolver's own error surfaced it, but
// that error was discarded on the same line and nothing downstream ever saw
// it — so a workspace whose siblings had NO runtime mapping reported
// "manifest and runtime config agree", which doctor rendered as a green check.
func FindWorkspaceProjectMappingMismatches(cfg *Config, startDir string) (ProjectMappingReport, error) {
	if cfg == nil {
		return ProjectMappingReport{}, fmt.Errorf("no config loaded")
	}
	wsRoot, err := workspace.DetectWorkspaceRoot(startDir)
	if err != nil {
		return ProjectMappingReport{}, err
	}
	manifestPath := filepath.Join(wsRoot, ".vscode", "nightgauge-workspace.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return ProjectMappingReport{}, err
	}
	var manifest struct {
		Repositories []workspaceManifestRepo `yaml:"repositories"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return ProjectMappingReport{}, err
	}

	var report ProjectMappingReport
	for _, r := range manifest.Repositories {
		if r.ProjectNumber == 0 || r.Name == "" {
			continue
		}
		ownerPart, repoPart := r.Name, r.Name
		if idx := strings.Index(r.Name, "/"); idx >= 0 {
			ownerPart, repoPart = r.Name[:idx], r.Name[idx+1:]
		} else if cfg.Owner != "" {
			ownerPart = cfg.Owner
		}
		resolved, resolveErr := ResolveRepoProjectNumber(cfg, ownerPart, repoPart)
		if resolveErr != nil {
			// Report it. "I could not compare" is not "they agree" — see the
			// function comment.
			report.Unresolvable = append(report.Unresolvable, ProjectMappingUnresolved{
				Repo:            r.Name,
				ManifestProject: r.ProjectNumber,
				Err:             resolveErr.Error(),
			})
			continue
		}
		if resolved != r.ProjectNumber {
			report.Mismatches = append(report.Mismatches, ProjectMappingMismatch{
				Repo:            r.Name,
				ManifestProject: r.ProjectNumber,
				ResolvedProject: resolved,
			})
		}
	}
	return report, nil
}

// CheckWorkspaceProjectMapping is FindWorkspaceProjectMappingMismatches
// rendered as human-readable strings, for callers (doctor, scheduler) that
// only need the message. Includes unresolvable repos, so a caller that only
// prints these lines still cannot report a false all-clear.
func CheckWorkspaceProjectMapping(cfg *Config, startDir string) ([]string, error) {
	report, err := FindWorkspaceProjectMappingMismatches(cfg, startDir)
	if err != nil {
		return nil, err
	}
	return report.Problems(), nil
}
