// UI-bearing surface detection (#4193) — a deterministic sibling to Classify,
// following the same pattern as ClassifyForCI (internal/ci) and RelaxDecision
// (internal/orchestrator/gates): one shared primitive (Classify), multiple
// consumer-specific decisions built on top of it. This one answers "does this
// diff touch frontend code in a UI-bearing repo" for feature-validate's
// verify-ui gate trigger.
package changeClassifier

import (
	"fmt"

	"github.com/nightgauge/nightgauge/internal/intelligence/scopeDriftGate"
)

// UIBearingRepos maps a repo's directory-basename identifier to the glob
// patterns (scopeDriftGate.MatchPath syntax) that mark a changed file as
// touching that repo's UI surface. Only repos with a bundled verify-ui flow
// belong here — see skills/nightgauge-verify-ui/flows/. Patterns are
// deliberately inclusive (over-matching costs an explicit "no flow" skip
// reason, not a false block; under-matching silently loses coverage).
func DefaultUIBearingRepos() map[string][]string {
	return map[string][]string{
		"acme-dashboard": {"src/**", "public/**", "index.html", "*.css"},
		"acmeweb":                  {"src/**", "public/**", "*.css"},
		"acme-site":                 {"layouts/**", "static/**", "content/**", "data/**", "*.html"},
		"acme-mobile":   {"lib/**/*.dart", "web/**"},
	}
}

// TouchesUISurface reports whether changedFiles touch the UI-bearing surface
// of repoName, and why. It is pure and deterministic: same input -> same
// output, no I/O, no LLM.
//
// A change classifying as DocsOnly/ConfigOnly/Empty is never UI-relevant
// regardless of repo (preserves fast-track economics — #4193 AC). A repo
// absent from repos is never UI-bearing (e.g. the nightgauge pipeline
// repo itself has no browser surface to verify).
func TouchesUISurface(changedFiles []string, repoName string, repos map[string][]string) (bool, string) {
	class := ClassifyDefault(changedFiles)
	if class == DocsOnly || class == ConfigOnly || class == Empty {
		return false, fmt.Sprintf("change class %q is not UI-relevant", class)
	}

	patterns, ok := repos[repoName]
	if !ok {
		return false, fmt.Sprintf("repo %q is not a registered UI-bearing repo", repoName)
	}

	for _, f := range changedFiles {
		if f == "" {
			continue
		}
		if scopeDriftGate.MatchPath(f, patterns) {
			return true, fmt.Sprintf("file %q matches a UI pattern for repo %q", f, repoName)
		}
	}
	return false, fmt.Sprintf("no changed file matches a UI pattern for repo %q", repoName)
}
