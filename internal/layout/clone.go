package layout

import (
	"errors"
	"fmt"
	"path/filepath"
)

// ErrRootNotAbsolute is returned when a class resolver is handed an empty or
// relative repository root. Resolving against such a root would place the
// directory relative to the process's working directory, whichever repository
// (or none) that happens to be, so the resolvers refuse it.
var ErrRootNotAbsolute = errors.New("layout: repository root must be an absolute path")

// legacyDataDir is the working-tree directory that holds the per-clone
// classes today. ADR-024 § 7 moves them to CLONE = <git-common-dir>/nightgauge;
// until that move lands the resolvers return the current location unchanged.
const legacyDataDir = ".nightgauge"

// PipelineStateDir is the directory of the pipeline class: per-issue stage
// contexts, runtime snapshots, run history and traces.
// Today: <root>/.nightgauge/pipeline.
func PipelineStateDir(root string) (string, error) { return classDir(root, "pipeline") }

// PlansDir is the directory of issue-keyed implementation plans.
// Today: <root>/.nightgauge/plans.
func PlansDir(root string) (string, error) { return classDir(root, "plans") }

// RetrosDir is the directory of issue-keyed retrospectives.
// Today: <root>/.nightgauge/retros.
func RetrosDir(root string) (string, error) { return classDir(root, "retros") }

// CloneLogsDir is the directory of per-clone logs (for example
// autonomous-exits.jsonl). Today: <root>/.nightgauge/logs.
func CloneLogsDir(root string) (string, error) { return classDir(root, "logs") }

// classDir validates root and joins the class under the per-clone data
// directory. The error wraps ErrRootNotAbsolute and names the class so a
// caller's log says which resolution failed.
func classDir(root, class string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: resolving %s directory from %q", ErrRootNotAbsolute, class, root)
	}
	return filepath.Join(root, legacyDataDir, class), nil
}
