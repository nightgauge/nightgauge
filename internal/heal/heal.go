// Package heal contains the deterministic pattern registry consumed by the
// `pipeline-heal-base` recovery action (Issue #3683). A HealPattern is a pure
// predicate over inherited-only baseline failures plus a fix generator that
// returns a minimal patch against the affected base branch. The registry is an
// allowlist — only patterns compiled into the binary can produce a heal PR.
//
// Determinism rule: pattern code lives in `internal/` and MUST NOT make LLM
// calls. New patterns require a human-reviewed PR.
package heal

// BaselineFailure is one entry from the `auto-fix-baseline-{PR}.json` file
// written by the pr-merge auto-fix loop's Step 2.5. Only the fields the heal
// registry consumes are modelled here — additional fields are tolerated by
// JSON decoding.
type BaselineFailure struct {
	// Name is the check or test identifier (e.g. "vitest packages/db/seed").
	Name string `json:"name"`
	// Classification is "inherited" | "regression". The PipelineHealBase
	// recovery action filters to inherited entries before pattern matching.
	Classification string `json:"classification"`
	// FailureType buckets the failure (e.g. "test", "build", "typecheck").
	FailureType string `json:"failure_type"`
	// Details is a stderr snippet from the failing check — patterns scan this
	// for narrow keyword conjunctions to keep false positives low.
	Details string `json:"details"`
	// TargetRepo is an optional cross-repo hint populated by the producer when
	// the failure name carries a repo prefix (e.g. for inter-repo workspace
	// failures). When non-empty the heal PR is created against that repo.
	TargetRepo string `json:"target_repo,omitempty"`
}

// HealFix is the proposed fix produced by a HealPattern's GenerateFix. Empty
// FilesToCreate and FilesToModify means the pattern wants to open an
// informational PR (or comment) without applying a tree change — that path is
// signalled by returning ok=false from GenerateFix.
type HealFix struct {
	// BranchName is the branch the action will push the fix to. Conventionally
	// `pipeline-heal/<short-cause-hash>`. The pattern provides the slug; the
	// action prefixes and disambiguates.
	BranchName string
	// CommitMessage is the single commit message for the fix.
	CommitMessage string
	// FilesToCreate holds new files to add. Path is relative to the workspace
	// root; Content is the full file contents.
	FilesToCreate []HealFileChange
	// FilesToModify holds existing files to update. Path is relative to the
	// workspace root; Content is the full file contents (not a patch).
	FilesToModify []HealFileChange
	// PRTitle is the title of the heal PR.
	PRTitle string
	// PRBody is the heal PR body. Patterns should include the failing test
	// names and a link to the originating evidence so reviewers can verify the
	// fix without re-running the loop.
	PRBody string
	// PRLabels always includes either "pipeline-heal:auto" (auto-merge eligible)
	// or "pipeline-heal:needs-review" (first occurrence / non-auto-mergeable).
	// The recovery action appends throttle / first-occurrence labels.
	PRLabels []string
	// TargetRepo is "owner/repo" or "" for the current repo. The recovery
	// action passes it through to `gh pr create --repo`.
	TargetRepo string
	// DiffLineEstimate is the pattern's estimate of the patch size in lines.
	// Used by the auto-merge gate's budget check; patterns that exceed budget
	// stay open for human review.
	DiffLineEstimate int
}

// HealFileChange describes one file's change in a HealFix.
type HealFileChange struct {
	Path    string
	Content string
}

// HealPattern is the contract every registered pattern implements. Matches
// MUST be pure (no IO) so the registry can short-circuit non-matches cheaply.
// GenerateFix may return (HealFix{}, false) to signal that the pattern matched
// but cannot deterministically produce a fix — the caller opens an
// informational PR with `pipeline-heal:needs-review` instead.
type HealPattern interface {
	Slug() string
	Description() string
	Matches(failures []BaselineFailure) bool
	GenerateFix(failures []BaselineFailure) (HealFix, bool)
}
