// Package survival implements the post-merge survival outcome model (#4151,
// spike #4134): it captures a survival record keyed by a merged PR's merge
// commit SHA, then finalizes that record deterministically — survived,
// reverted, broke, or unobserved — by observing whether the merged code held
// up on the base branch.
//
// This package is CAPTURE + DETECTION ONLY. It does NOT feed calibration math
// (that is the deferred #4152/#4153 work): it produces ground-truth survival
// verdicts and persists them to a separate append-only store, leaving the
// merge-time outcome journal (internal/github/outcome.go) untouched.
//
// Determinism: every decision here is a pure function of (record, now, window,
// observation). The only non-deterministic surface is the Detector interface,
// whose GitHub-backed implementation lives outside this package so the verdict
// state machine is fully unit-testable against a mock observer.
package survival

import "time"

// Verdict is the lifecycle state of a survival record.
type Verdict string

const (
	// Pending — captured at merge, not yet finalized. Carries no signal.
	Pending Verdict = "pending"
	// Survived — terminal, weak-positive: the observation window elapsed with no
	// revert and no ancestry-correlated breakage. NOTE: capture+detect only wires
	// this verdict; rewarding it in calibration is deferred (#4153).
	Survived Verdict = "survived"
	// Reverted — terminal, negative: a `This reverts commit <sha>` commit landed
	// on the base branch. Proven ground truth.
	Reverted Verdict = "reverted"
	// Broke — terminal, negative: an ancestry-correlated main-CI failure was
	// attributed to the merge (descendant of the merge AND green at the merge).
	Broke Verdict = "broke"
	// Unobserved — terminal, NO signal: the record was never re-observed before
	// 2×window elapsed (e.g. a low-traffic repo with no further reconcile). It is
	// explicitly NOT counted as survived — censored data must never read as proof.
	Unobserved Verdict = "unobserved"
)

// IsTerminal reports whether the verdict is a finalized (non-pending) state.
func (v Verdict) IsTerminal() bool { return v != Pending && v != "" }

// Evidence constants name *why* a record reached its terminal verdict. They are
// stable strings so downstream calibration (#4152) and audits can bucket them.
const (
	EvidenceRevertCommit     = "reverts-commit"            // a Reverts <sha> commit on the base branch
	EvidenceAncestryCI       = "ancestry-ci-failure"       // a descendant main-CI failure, green at merge
	EvidenceWindowClean      = "window-elapsed-clean"      // window closed, no negative evidence → survived
	EvidenceWindowUnobserved = "window-elapsed-unobserved" // aged past 2×window without observation
)

// Kind is the fixed discriminator written on every survival record so the
// store (and any shared journal it might later join) can distinguish survival
// events from other record kinds.
const Kind = "survival"

// Record is a single post-merge survival observation, keyed by the merge commit
// SHA (the stable join key from #4133, independent of branch deletion / PR
// renumbering). Persisted as one JSON object per line in the append-only store.
type Record struct {
	Kind           string  `json:"kind"`             // always Kind ("survival")
	MergeCommitSHA string  `json:"merge_commit_sha"` // join key (#4133)
	IssueNumber    int     `json:"issue_number"`
	PRNumber       int     `json:"pr_number"`
	Repo           string  `json:"repo"`      // "owner/name"
	BaseRef        string  `json:"base_ref"`  // base branch the merge landed on (default "main")
	MergedAt       string  `json:"merged_at"` // ISO-8601 (#4133)
	Verdict        Verdict `json:"verdict"`
	ObservedAt     string  `json:"observed_at,omitempty"` // when finalized (RFC3339)
	Evidence       string  `json:"evidence,omitempty"`    // one of the Evidence* constants
}

// DefaultBaseRef is the base branch survival detection scans. Every pipeline PR
// targets main, so revert/CI attribution is anchored there.
const DefaultBaseRef = "main"

// NewPending builds a fresh pending record for a just-merged single-issue PR.
// BaseRef defaults to DefaultBaseRef when empty.
func NewPending(repo string, issueNumber, prNumber int, mergeSHA, mergedAt, baseRef string) Record {
	if baseRef == "" {
		baseRef = DefaultBaseRef
	}
	return Record{
		Kind:           Kind,
		MergeCommitSHA: mergeSHA,
		IssueNumber:    issueNumber,
		PRNumber:       prNumber,
		Repo:           repo,
		BaseRef:        baseRef,
		MergedAt:       mergedAt,
		Verdict:        Pending,
	}
}

// mergedTime parses MergedAt as an RFC3339 timestamp. ok is false when MergedAt
// is empty or unparseable — callers treat that as "cannot determine window" and
// leave the record pending rather than guessing.
func (r Record) mergedTime() (t time.Time, ok bool) {
	if r.MergedAt == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, r.MergedAt)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
