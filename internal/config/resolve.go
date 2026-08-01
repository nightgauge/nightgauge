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

// FindWorkspaceProjectMappingMismatches cross-validates every
// repositories[].project_number entry in the .vscode/nightgauge-workspace.yaml
// manifest (walked up from startDir) against ResolveRepoProjectNumber for
// that same repo, returning one structured mismatch per disagreeing repo.
// Shared by internal/doctor's project_mapping check, the orchestrator
// scheduler's startup warning, and the stranded-ready-items sweep producer
// (#271) so there is exactly one implementation of the cross-check. Returns
// a non-nil error only when no workspace manifest exists (single-repo mode —
// not a failure, just "nothing to check").
func FindWorkspaceProjectMappingMismatches(cfg *Config, startDir string) ([]ProjectMappingMismatch, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no config loaded")
	}
	wsRoot, err := workspace.DetectWorkspaceRoot(startDir)
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(wsRoot, ".vscode", "nightgauge-workspace.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Repositories []workspaceManifestRepo `yaml:"repositories"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	var mismatches []ProjectMappingMismatch
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
			// No runtime mapping at all is its own (separate) problem —
			// surfaced by resolveErr's own message, not this check.
			continue
		}
		if resolved != r.ProjectNumber {
			mismatches = append(mismatches, ProjectMappingMismatch{
				Repo:            r.Name,
				ManifestProject: r.ProjectNumber,
				ResolvedProject: resolved,
			})
		}
	}
	return mismatches, nil
}

// CheckWorkspaceProjectMapping is FindWorkspaceProjectMappingMismatches
// rendered as human-readable strings, for callers (doctor, scheduler) that
// only need the message.
func CheckWorkspaceProjectMapping(cfg *Config, startDir string) ([]string, error) {
	mismatches, err := FindWorkspaceProjectMappingMismatches(cfg, startDir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(mismatches))
	for _, m := range mismatches {
		out = append(out, m.String())
	}
	return out, nil
}
