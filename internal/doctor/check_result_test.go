package doctor

import "strings"

// checkResult returns check id's result from a run. ran is false when the
// check did not register or was not applicable in this workspace.
func checkResult(res DoctorResult, id string) (r CheckResult, ran bool) {
	for _, r := range res.Results {
		if r.ID != id {
			continue
		}
		if strings.HasPrefix(r.Detail, "not applicable") {
			return CheckResult{}, false
		}
		return r, true
	}
	return CheckResult{}, false
}

// checkPassed reports whether r completed and raised no blocker or warning.
// Housekeeping and info findings do not fail a check.
func checkPassed(r CheckResult) bool {
	if r.Status == StatusSkipped || r.Status == StatusTimeout {
		return false
	}
	for _, f := range r.Findings {
		if f.Severity == SeverityBlocker || f.Severity == SeverityWarning {
			return false
		}
	}
	return true
}
