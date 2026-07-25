package forgetypes

import "time"

// CheckStatus represents the status of CI checks for a PR.
type CheckStatus struct {
	PRNumber           int           `json:"prNumber"`
	State              string        `json:"state"`
	Total              int           `json:"total"`
	Completed          int           `json:"completed"`
	Successful         int           `json:"successful"`
	Failed             int           `json:"failed"`
	Pending            int           `json:"pending"`
	Checks             []CheckDetail `json:"checks"`
	IsTerminal         bool          `json:"isTerminal"`
	ElapsedSecs        int           `json:"elapsedSecs"`
	RequiredPassed     bool          `json:"requiredPassed"`
	RequiredCheckNames []string      `json:"requiredCheckNames"`
	// MergedExternally is true when the PR was merged out-of-band while CI was
	// still pending. WaitForChecks returns SUCCESS + MergedExternally=true so
	// callers can distinguish "CI passed" from "PR was already merged."
	MergedExternally bool `json:"mergedExternally,omitempty"`
}

// CheckDetail represents a single CI check.
type CheckDetail struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Required   bool   `json:"required"`
	// CompletedAt is the RFC3339 instant the check reached its conclusion,
	// empty while it is still queued or running. It is how a caller answers
	// "how long has this been failing?" without keeping state of its own —
	// which is what lets the default-branch producer hold a grace period over
	// a failure that is about to be re-run green.
	CompletedAt string `json:"completedAt,omitempty"`
	// DetailsURL points at the run's page on the forge. A card whose only
	// honest affordance is "go look" needs somewhere to send the operator.
	DetailsURL string `json:"detailsUrl,omitempty"`
	// HeadSHA is the commit the check ran against — the commit that introduced
	// the failure, when the branch has not moved since.
	HeadSHA string `json:"headSha,omitempty"`
}

// WaitConfig configures CI wait polling.
type WaitConfig struct {
	Timeout            time.Duration
	PollInterval       time.Duration
	RequiredCheckNames []string
	OnProgress         func(status *CheckStatus)
}

// CIRunLog represents downloaded CI run logs.
type CIRunLog struct {
	RunID   int64  `json:"runId"`
	Status  string `json:"status"`
	Content string `json:"content"`
	URL     string `json:"url"`
}
