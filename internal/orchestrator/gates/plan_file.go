package gates

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// ErrPlanNotContained marks a plan_file that does not resolve to a regular
// file inside the clone's plans directory.
var ErrPlanNotContained = errors.New("plan_file does not resolve to a regular file inside the plans directory")

// PlanFilePath returns the path a planning context's plan_file names.
// feature-planning writes the plan into the clone's plans directory
// (layout.PlansDir; ADR-024 § 7) and records its absolute path; a relative
// plan_file is taken from that directory. workspace may be the checkout or any
// of its worktrees: they share one plans directory.
func PlanFilePath(workspace, planFile string) (string, error) {
	if filepath.IsAbs(planFile) {
		return filepath.Clean(planFile), nil
	}
	plans, err := plansDir(workspace)
	if err != nil {
		return "", err
	}
	return filepath.Join(plans, planFile), nil
}

// ResolvePlanFile resolves plan_file (PlanFilePath) with EvalSymlinks and
// returns it when it is a regular file inside the plans directory. The path is
// model-authored, so nothing outside that directory is read on its say-so: a
// path outside it, a symlink escaping it, and a path that does not resolve all
// return an error wrapping ErrPlanNotContained.
func ResolvePlanFile(workspace, planFile string) (string, error) {
	plans, err := plansDir(workspace)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPlanNotContained, err)
	}
	plansResolved, err := filepath.EvalSymlinks(plans)
	if err != nil {
		return "", fmt.Errorf("%w: plans directory %s does not resolve: %v", ErrPlanNotContained, plans, err)
	}
	candidate, err := PlanFilePath(workspace, planFile)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPlanNotContained, err)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPlanNotContained, err)
	}
	rel, err := filepath.Rel(plansResolved, resolved)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s resolves to %s, outside %s",
			ErrPlanNotContained, planFile, resolved, plansResolved)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s is not a regular file", ErrPlanNotContained, resolved)
	}
	return resolved, nil
}

// plansDir is layout.PlansDir for workspace, made absolute first. An empty
// workspace is refused rather than read as the process's working directory.
func plansDir(workspace string) (string, error) {
	if workspace == "" {
		return "", errors.New("resolve plans directory: empty workspace")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace %q: %w", workspace, err)
	}
	return layout.PlansDir(abs)
}
