package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// Remedy verbs the learning checks declare (ADR-025 § 3).
const (
	verbOutcomeInit       = "outcome.init"
	verbSurvivalSweep     = "survival.sweep"
	verbAutomationRestart = "automation.restart"
)

// codeComplexityModelMissing: the model has not been created yet. Info, not a
// warning (#2202): the model is per-checkout learned state, gitignored, and
// every writer bootstraps the deterministic baseline on first use.
const codeComplexityModelMissing = "NGD033"

// complexityModelFindings reports a missing, invalid or unsafe complexity
// model. A missing or invalid model is repaired by `outcome init` (confirm;
// the preview names the file it writes). A path that is a symlink or not a
// regular file is never written through: the remedy is manual.
func complexityModelFindings(workspaceRoot string) ([]Finding, string) {
	const check, code = "complexity_model", "NGD014"
	modelPath, err := complexityModelPath(workspaceRoot)
	if errors.Is(err, layout.ErrNotGitRepository) || errors.Is(err, layout.ErrRootNotAbsolute) {
		return nil, "skipped: not in a git repository, so there is no per-checkout model"
	}
	if err != nil {
		// The per-checkout directory itself is refused (a symlink or not a
		// directory); name where it is expected.
		modelPath = layout.CheckoutDisplay(layout.CheckoutComplexityModel)
	}
	modelDir := filepath.Dir(modelPath)
	ev := map[string]string{"path": modelPath}
	unsafe := func(title, cause string) ([]Finding, string) {
		return []Finding{newFinding(check, code, SeverityWarning, title, cause, ev, []string{modelPath, "unsafe"},
			manualRemedy("replace", "Replace the conflicting path with a regular workspace file", check,
				"Not offered as a fix: doctor never writes through a symlink or over a path it cannot inspect",
				"Remove or replace "+modelPath+" (or its directory) by hand",
				"Then run `nightgauge outcome init`"))}, title
	}
	if err != nil {
		return unsafe(fmt.Sprintf("complexity model directory is unsafe: %v", err),
			"the per-checkout directory is not a real directory, so writing the model could land anywhere")
	}
	if dirInfo, err := os.Lstat(modelDir); err == nil && dirInfo.Mode()&os.ModeSymlink != 0 {
		return unsafe(fmt.Sprintf("complexity model directory is a symlink at %s", modelDir),
			"the model directory resolves outside the workspace, so writing the model could land anywhere")
	} else if err != nil && !os.IsNotExist(err) {
		return unsafe(fmt.Sprintf("complexity model directory could not be inspected at %s: %v", modelDir, err),
			"the model directory's state is unknown")
	}

	info, err := os.Lstat(modelPath)
	switch {
	case err == nil && info.Mode().IsRegular():
		validateErr := gh.NewOutcomeService(workspaceRoot).ValidateModel()
		if validateErr == nil {
			return nil, modelPath
		}
		ev["error"] = validateErr.Error()
		return []Finding{newFinding(check, code, SeverityWarning,
				fmt.Sprintf("complexity model at %s is invalid: %v", modelPath, validateErr),
				"complexity routing reads this file and falls back blindly while it cannot be parsed",
				ev, []string{modelPath, "invalid"},
				Remedy{ID: "init", Kind: RemedyConfirm, Verb: verbOutcomeInit, Verify: check,
					Summary: "Replace the invalid model with the deterministic baseline (`nightgauge outcome init`)",
					Preview: fmt.Sprintf("move the invalid %s aside and write the deterministic baseline model to %s", modelPath, modelPath)})},
			"invalid"
	case os.IsNotExist(err):
		title := fmt.Sprintf("complexity model not yet created at %s", modelPath)
		return []Finding{newFinding(check, codeComplexityModelMissing, SeverityInfo, title,
				"the model is per-checkout learned state; the deterministic baseline is bootstrapped automatically on the first outcome record",
				ev, []string{modelPath},
				Remedy{ID: "init", Kind: RemedyConfirm, Verb: verbOutcomeInit, Verify: check,
					Summary: "Write the deterministic baseline now (`nightgauge outcome init`)",
					Preview: fmt.Sprintf("write the deterministic baseline model to %s", modelPath)})},
			"not yet created"
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return unsafe(fmt.Sprintf("complexity model is a symlink at %s", modelPath),
			"a symlinked model could be written outside the workspace")
	case err == nil:
		return unsafe(fmt.Sprintf("complexity model path is not a regular file at %s", modelPath),
			"something other than a file occupies the model path")
	default:
		return unsafe(fmt.Sprintf("complexity model could not be inspected at %s: %v", modelPath, err),
			"the model file's state is unknown")
	}
}

// complexityModelPath is the checkout's complexity model,
// CHECKOUT/complexity-model.yaml (ADR-024 § 7). It is joined onto CHECKOUT
// rather than resolved with layout.CheckoutPath so that a model that is a
// symlink is still located and reported, not refused before it is inspected.
func complexityModelPath(workspaceRoot string) (string, error) {
	dir, err := layout.CheckoutDir(workspaceRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, layout.CheckoutComplexityModel), nil
}
