package ci

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// RequiredCheckConfigMismatch describes a required status check whose
// workflow job is declared `continue-on-error: true`. Such a check reports
// its real conclusion to branch rules regardless of continue-on-error (which
// only shields the workflow run), so when the underlying command fails the
// merge is deterministically unwinnable until a human changes repo config —
// either the check leaves the required set or the job drops
// continue-on-error. This exact mismatch caused the bowlsheet#233 pr-merge
// dead-end loop (#184).
type RequiredCheckConfigMismatch struct {
	Check        string `json:"check"`
	WorkflowPath string `json:"workflow_path"`
	JobKey       string `json:"job_key"`
	Failing      bool   `json:"failing"`
	Remediation  string `json:"remediation"`
}

// mismatchWorkflowYAML is the minimal workflow projection the probe needs.
// Kept separate from workflowYAML (command discovery) so neither parser
// accretes the other's fields.
type mismatchWorkflowYAML struct {
	Jobs map[string]mismatchJobYAML `yaml:"jobs"`
}

type mismatchJobYAML struct {
	Name string `yaml:"name"`
	// continue-on-error may be a bool or a ${{ }} expression; only a literal
	// true is treated as a mismatch (expressions can't be evaluated here).
	ContinueOnError interface{} `yaml:"continue-on-error"`
}

// DetectRequiredCheckConfigMismatches cross-references required check names
// against the repo's workflow files and returns the checks whose producing
// job is continue-on-error: true. Callers decide severity: a mismatch on a
// currently failing check is a non-retryable config blocker.
func DetectRequiredCheckConfigMismatches(workdir string, requiredChecks []string) []RequiredCheckConfigMismatch {
	if len(requiredChecks) == 0 {
		return nil
	}

	var mismatches []RequiredCheckConfigMismatch
	for _, wfPath := range listWorkflowFiles(workdir) {
		data, err := os.ReadFile(wfPath)
		if err != nil {
			continue
		}
		var wf mismatchWorkflowYAML
		if err := yaml.Unmarshal(data, &wf); err != nil {
			continue
		}

		for jobKey, job := range wf.Jobs {
			if !isLiteralTrue(job.ContinueOnError) {
				continue
			}
			for _, check := range requiredChecks {
				if !checkMatchesJob(check, jobKey, job.Name) {
					continue
				}
				rel, relErr := filepath.Rel(workdir, wfPath)
				if relErr != nil {
					rel = wfPath
				}
				mismatches = append(mismatches, RequiredCheckConfigMismatch{
					Check:        check,
					WorkflowPath: rel,
					JobKey:       jobKey,
					Remediation: fmt.Sprintf(
						"required check %q can never satisfy branch rules while its underlying command fails: job %q in %s is continue-on-error: true. Remove %q from required checks or drop continue-on-error.",
						check, jobKey, rel, check),
				})
			}
		}
	}
	return mismatches
}

// listWorkflowFiles returns all .yml/.yaml files under .github/workflows.
func listWorkflowFiles(workdir string) []string {
	dir := filepath.Join(workdir, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(entry.Name())
		if ext == ".yml" || ext == ".yaml" {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	return files
}

// isLiteralTrue reports whether a continue-on-error value is the literal
// boolean true (YAML bool or the string "true").
func isLiteralTrue(v interface{}) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return strings.EqualFold(strings.TrimSpace(val), "true")
	default:
		return false
	}
}

// checkMatchesJob reports whether a required check context plausibly names
// the given workflow job. Check contexts derive from the job's display name
// (or key when unnamed) plus optional decorations — matrix suffixes
// ("Sentry Smoke (integration)") or workflow prefixes ("CI / build") — so a
// case-insensitive containment match on the job identity is used.
func checkMatchesJob(check, jobKey, jobName string) bool {
	checkLower := strings.ToLower(strings.TrimSpace(check))
	if checkLower == "" {
		return false
	}
	for _, candidate := range []string{jobName, jobKey} {
		candLower := strings.ToLower(strings.TrimSpace(candidate))
		if candLower == "" {
			continue
		}
		if checkLower == candLower || strings.Contains(checkLower, candLower) {
			return true
		}
	}
	return false
}
