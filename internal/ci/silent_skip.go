package ci

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SilentSkipRisk describes a workflow step whose `if:` condition depends on
// an earlier conditional step in the same job succeeding, without itself
// carrying a status-check function (always(), success(), failure(),
// cancelled()). GitHub Actions implicitly ANDs a bare step-level `if:` with
// success(), so once the earlier conditional step fails, this step is
// silently SKIPPED rather than FAILED — a required check then reports no
// verdict at all. This is the class of bug that let a failing early step in
// the `go` job hide the OpenCode integration regression (#1817).
type SilentSkipRisk struct {
	WorkflowPath string `json:"workflow_path"`
	JobKey       string `json:"job_key"`
	StepName     string `json:"step_name"`
	Remediation  string `json:"remediation"`
}

// silentSkipWorkflowYAML is the minimal workflow projection the probe needs.
// Kept separate from the other workflow projections in this package so no
// parser accretes fields it does not use.
type silentSkipWorkflowYAML struct {
	Jobs map[string]silentSkipJobYAML `yaml:"jobs"`
}

type silentSkipJobYAML struct {
	Name  string               `yaml:"name"`
	Steps []silentSkipStepYAML `yaml:"steps"`
}

type silentSkipStepYAML struct {
	Name string `yaml:"name"`
	If   string `yaml:"if"`
}

// statusCheckFunctions are the GitHub Actions expression functions that
// override the implicit success() ANDed onto every step-level `if:`.
var statusCheckFunctions = []string{"always(", "success(", "failure(", "cancelled("}

// DetectSilentSkipRisk scans every job in every workflow file under
// .github/workflows for steps that inherit the silent-skip anti-pattern: a
// conditional step, following an earlier conditional step in the same job,
// whose own `if:` has no status-check function. An earlier UNCONDITIONAL
// step failing already fails the whole job outright regardless of any later
// step's `if:`, so only conditional-after-conditional sequences are in this
// risk class.
func DetectSilentSkipRisk(workdir string) []SilentSkipRisk {
	var risks []SilentSkipRisk
	for _, wfPath := range listWorkflowFiles(workdir) {
		data, err := os.ReadFile(wfPath)
		if err != nil {
			continue
		}
		var wf silentSkipWorkflowYAML
		if err := yaml.Unmarshal(data, &wf); err != nil {
			continue
		}

		rel, relErr := filepath.Rel(workdir, wfPath)
		if relErr != nil {
			rel = wfPath
		}

		for jobKey, job := range wf.Jobs {
			sawEarlierConditional := false
			for _, step := range job.Steps {
				cond := strings.TrimSpace(step.If)
				if cond == "" {
					continue
				}
				if sawEarlierConditional && !hasStatusCheckFunction(cond) {
					name := step.Name
					if name == "" {
						name = "(unnamed step)"
					}
					risks = append(risks, SilentSkipRisk{
						WorkflowPath: rel,
						JobKey:       jobKey,
						StepName:     name,
						Remediation: fmt.Sprintf(
							"this step's `if:` implicitly depends on success() and will be silently skipped, not failed, if an earlier conditional step in job %q fails — add `!cancelled()` or `always()` to the condition",
							jobKey),
					})
				}
				sawEarlierConditional = true
			}
		}
	}
	return risks
}

// hasStatusCheckFunction reports whether a step's `if:` expression contains
// one of the status-check functions that override the implicit success().
func hasStatusCheckFunction(cond string) bool {
	lower := strings.ToLower(cond)
	for _, fn := range statusCheckFunctions {
		if strings.Contains(lower, fn) {
			return true
		}
	}
	return false
}
