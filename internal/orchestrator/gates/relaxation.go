package gates

import (
	"context"

	changeClassifier "github.com/nightgauge/nightgauge/internal/intelligence/changeClassifier"
)

// Gate relaxation (#4128): for a verified-trivial change the PR gates' retry +
// sleep loops are pure overhead. The scheduler computes an AUTHORITATIVE
// classification of the real post-dev diff (not the predictive issue-pickup
// route) and, when the repo has opted in, marks the gate context as relaxed.
// Because the classification runs against the actually-changed files, a "docs"
// issue that really edited source classifies as source/mixed and is NOT relaxed
// — the classifier itself is the drift-revoke safety check.

type relaxedKey struct{}

// WithRelaxed returns a context carrying the gate-relaxation flag. The scheduler
// sets it before invoking a gate's Verify; gates read it via Relaxed.
func WithRelaxed(ctx context.Context, relaxed bool) context.Context {
	return context.WithValue(ctx, relaxedKey{}, relaxed)
}

// Relaxed reports whether the current gate run was relaxed for a trivial change.
// Default false (no value present) — gates run their full retry/sleep behavior.
func Relaxed(ctx context.Context) bool {
	v, ok := ctx.Value(relaxedKey{}).(bool)
	return ok && v
}

// RelaxDecision is the deterministic relaxation predicate. It classifies the
// real changed files and returns (relaxed, change_class):
//
//   - relaxed is true ONLY when the authoritative class is in relaxClasses
//     (e.g. ["docs_only","config_only"]); an empty relaxClasses (the default)
//     never relaxes — relaxation is strictly opt-in per repo.
//   - the returned class is always reported (for telemetry), even when not
//     relaxed, so the audit trail records what the diff actually was.
//
// Drift-revoke is intrinsic: ClassifyDefault runs on the ACTUAL changed files,
// so a change that touched source yields source/mixed and is never relaxed.
func RelaxDecision(changedFiles []string, relaxClasses []string) (bool, string) {
	class := changeClassifier.ClassifyDefault(changedFiles)
	for _, rc := range relaxClasses {
		if string(class) == rc {
			return true, string(class)
		}
	}
	return false, string(class)
}
