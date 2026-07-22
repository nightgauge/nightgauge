// Package disciplineScore computes a per-repo verification-readiness /
// discipline score (#4100) and the autonomy gate built on it.
//
// The "New SDLC" whitepaper warns that AI is a force multiplier of WHATEVER
// engineering culture it lands in — it amplifies a weak culture (thin tests, no
// CI, no specs) as readily as a strong one. IB's "run everything, walk away"
// autonomy assumes its gates suffice regardless of the host repo's discipline.
// Dropped on a low-discipline repo, those gates over-trust themselves.
//
// This score reads deterministic repo signals (real test suite? CI? process
// docs?) and lets the autonomous scheduler down-rank or refuse full autonomy on
// an under-prepared repo, steering it toward human-in-the-loop. Compute is pure;
// GatherSignals (signals.go) does the filesystem IO.
package disciplineScore

import "fmt"

// DisciplineInput is the set of deterministic repo signals.
type DisciplineInput struct {
	HasTestFiles          bool // test files exist (*_test.go, *.test.ts, *.spec.ts, test_*.py, …)
	TestCommandConfigured bool // a runnable test command exists (go.mod, package.json scripts.test, Makefile test:)
	CIWorkflowCount       int  // CI workflow files (.github/workflows/*, .gitlab-ci.yml)
	HasIssueTemplates     bool // .github/ISSUE_TEMPLATE present (a proxy for spec discipline)
	HasProcessDocs        bool // CONTRIBUTING.md / AGENTS.md / CLAUDE.md present
}

// DisciplineResult is the computed score and its interpretation.
type DisciplineResult struct {
	Score     int      `json:"score"`     // 0–100
	Readiness string   `json:"readiness"` // ready | thin | unready
	Breakdown []string `json:"breakdown,omitempty"`
	Gaps      []string `json:"gaps,omitempty"`
}

// Weights. Verification carries half the score — the whitepaper's "verification
// is the new craft": a repo with no real test suite is exactly where autonomous
// gates over-trust themselves.
const (
	weightTestFiles   = 30
	weightTestCommand = 20
	weightCI          = 30
	weightProcessDocs = 10
	weightTemplates   = 10

	// Readiness band thresholds.
	readyAt = 70
	thinAt  = 40
)

// Compute returns the discipline score (0–100), its readiness band, and a
// human-readable breakdown. Pure: identical input → identical output.
func Compute(in DisciplineInput) DisciplineResult {
	score := 0
	var breakdown, gaps []string

	add := func(ok bool, weight int, have, missing string) {
		if ok {
			score += weight
			breakdown = append(breakdown, fmt.Sprintf("+%d %s", weight, have))
		} else {
			gaps = append(gaps, missing)
		}
	}

	add(in.HasTestFiles, weightTestFiles, "test files present", "no test files found")
	add(in.TestCommandConfigured, weightTestCommand, "test command configured", "no runnable test command")
	add(in.CIWorkflowCount > 0, weightCI, fmt.Sprintf("CI workflows (%d)", in.CIWorkflowCount), "no CI workflows")
	add(in.HasProcessDocs, weightProcessDocs, "process docs (CONTRIBUTING/AGENTS/CLAUDE)", "no process docs")
	add(in.HasIssueTemplates, weightTemplates, "issue templates", "no issue templates")

	readiness := "unready"
	switch {
	case score >= readyAt:
		readiness = "ready"
	case score >= thinAt:
		readiness = "thin"
	}

	return DisciplineResult{Score: score, Readiness: readiness, Breakdown: breakdown, Gaps: gaps}
}
