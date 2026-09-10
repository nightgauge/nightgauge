package doctor

import (
	"fmt"
	"os"
	"path/filepath"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

func checkComplexityModel(workspaceRoot string) (CheckItem, string) {
	modelPath := filepath.Join(workspaceRoot, ".nightgauge", "complexity-model.yaml")
	modelDir := filepath.Dir(modelPath)
	if dirInfo, err := os.Lstat(modelDir); err == nil && dirInfo.Mode()&os.ModeSymlink != 0 {
		warning := fmt.Sprintf("complexity model directory is a symlink at %s — replace it with a directory inside the workspace before running `nightgauge outcome init`", modelDir)
		return CheckItem{OK: false, Error: warning}, warning
	} else if err != nil && !os.IsNotExist(err) {
		warning := fmt.Sprintf("complexity model directory could not be inspected at %s: %v", modelDir, err)
		return CheckItem{OK: false, Error: warning}, warning
	}

	info, err := os.Lstat(modelPath)
	if err == nil && info.Mode().IsRegular() {
		if validateErr := gh.NewOutcomeService(workspaceRoot).ValidateModel(); validateErr != nil {
			warning := fmt.Sprintf("complexity model at %s is invalid: %v — repair or remove it, then run `nightgauge outcome init`", modelPath, validateErr)
			return CheckItem{OK: false, Error: warning}, warning
		}
		return CheckItem{OK: true, Detail: modelPath}, ""
	}

	remediation := fmt.Sprintf("complexity model missing at %s — run `nightgauge outcome init` from the workspace root", modelPath)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		remediation = fmt.Sprintf("complexity model is a symlink at %s — replace it with a regular workspace file before running `nightgauge outcome init`", modelPath)
	} else if err == nil {
		remediation = fmt.Sprintf("complexity model path is not a regular file at %s — remove the conflicting path, then run `nightgauge outcome init`", modelPath)
	} else if !os.IsNotExist(err) {
		remediation = fmt.Sprintf("complexity model could not be inspected at %s: %v", modelPath, err)
	}
	return CheckItem{OK: false, Error: remediation}, remediation
}
