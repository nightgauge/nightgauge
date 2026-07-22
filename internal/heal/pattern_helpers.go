package heal

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// containsAny returns true when s contains at least one of substrs. The
// caller is responsible for lower-casing s when case-insensitive matching is
// desired — keeping this helper case-sensitive avoids surprises when patterns
// match on path fragments that include casing (e.g. `Drizzle/`).
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// buildNeedsReviewBody produces the PR body shared by every "needs-review"
// heal fix path. The format is intentionally human-readable and stable so
// reviewers can scan the failing tests and the reason a deterministic fix
// was not produced.
func buildNeedsReviewBody(p HealPattern, failures []BaselineFailure, reason string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Pattern: `%s`\n\n", p.Slug())
	fmt.Fprintln(&sb, p.Description())
	sb.WriteString("\n")
	sb.WriteString(reason)
	sb.WriteString("\n\nFailing tests on main:\n\n")
	sb.WriteString(formatFailingTestList(failures))
	sb.WriteString("\nThis PR is labelled `pipeline-heal:needs-review` because the fix is not deterministic from the failure logs alone. A human needs to author the actual code change before this PR can merge.\n")
	return sb.String()
}

// formatFailingTestList renders the names + a single-line details snippet
// from each failure as a markdown list. Stable order (sorted) so the body
// diffs cleanly across runs.
func formatFailingTestList(failures []BaselineFailure) string {
	if len(failures) == 0 {
		return "_(no failures recorded)_\n"
	}
	names := make([]string, 0, len(failures))
	byName := map[string]BaselineFailure{}
	for _, f := range failures {
		if _, ok := byName[f.Name]; ok {
			continue
		}
		byName[f.Name] = f
		names = append(names, f.Name)
	}
	sort.Strings(names)

	var sb strings.Builder
	for _, n := range names {
		f := byName[n]
		snippet := strings.TrimSpace(f.Details)
		if i := strings.IndexByte(snippet, '\n'); i >= 0 {
			snippet = snippet[:i]
		}
		if len(snippet) > 160 {
			snippet = snippet[:160] + "…"
		}
		if snippet == "" {
			fmt.Fprintf(&sb, "- `%s`\n", n)
		} else {
			fmt.Fprintf(&sb, "- `%s` — %s\n", n, snippet)
		}
	}
	return sb.String()
}

// shortSlug derives a stable, branch-safe short hash from arbitrary input.
// Used to disambiguate pipeline-heal/<slug>-<short> branches when the same
// pattern fires more than once.
func shortSlug(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:4])
}

// SafeBranchName returns input lowercased with characters outside
// `[a-z0-9._/-]` replaced by `-`. Exported so the recovery action can
// normalise a pattern-supplied BranchName before pushing.
func SafeBranchName(input string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(input) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '_' || r == '-' || r == '/':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
