package orchestrator

import (
	"strings"

	"github.com/nightgauge/nightgauge/internal/intelligence/failure"
)

// escalationClassifier is stateless; one instance is enough.
var escalationClassifier = failure.NewClassifier()

// permissionPhrases are the WORD-shaped auth signatures this gate will act on.
//
// failure.Classifier decides the category; this list decides whether the
// category was reached on evidence that survives free-form stage output. The
// classifier's permission clauses include the bare HTTP codes "401" and "403",
// which are correct for the curated stderr it was written for and catastrophic
// here: this gate's input is a stage's raw output tail, full of issue numbers
// and temp paths. `#401` and `/tmp/TestFoo403.../` both match "403", and the
// first version of this gate duly refused to escalate two of three issues in an
// existing wave test purely because of their numbers.
//
// The codes are still honored in their unambiguous written forms below. Nothing
// is changed in the shared classifier — its bare-code clauses remain right for
// its own callers.
var permissionPhrases = []string{
	"unauthorized",
	"forbidden",
	"permission denied",
	"invalid auth method",
	"authentication required",
	"authentication failed",
	"could not read username",
	"could not read password",
	"invalid username or password",
	"bad credentials",
	"ssh: unable to authenticate",
	"http 401",
	"http 403",
	"status 401",
	"status 403",
	"401 unauthorized",
	"403 forbidden",
}

// EscalationBlockedByCategory reports whether a stage failure is of a class no
// stronger model can fix, and if so why (a short reason for the log).
//
// Model escalation exists for CAPABILITY shortfalls: the stage produced a wrong
// or incomplete answer, and a stronger model plausibly produces a better one.
// A credential failure is not a capability shortfall. Escalating on one buys a
// second full dispatch of the same prompt against the same missing credential,
// which fails at the same line — the #878 observation was a 67,610-character
// prompt re-sent at a higher tier that died 44 seconds later at the identical
// `git push` with `invalid auth method`.
//
// Deliberately narrow: only failure.CatPermission blocks. CatInfra (network,
// DNS, connection refused) is NOT blocked even though a stronger model cannot
// fix those either, because on the scheduler's paths the escalation branch is
// also the RETRY branch — blocking it there would silently remove a retry from
// genuinely transient failures, a different change from the one #878 asks for.
//
// Matching is PER LINE. Callers pass everything they have — the stage's error
// text AND its captured output tail — and the classifier's ladder is
// first-match-wins over the whole string, so a single line mentioning "network"
// or "timeout" anywhere in a 200-line tail would otherwise claim the verdict
// and hide the auth failure underneath it.
func EscalationBlockedByCategory(texts ...string) (bool, string) {
	for _, text := range texts {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !containsAnyFold(line, permissionPhrases) {
				continue
			}
			if cls := escalationClassifier.Classify("", 1, line); cls.Category == failure.CatPermission {
				return true, string(cls.Category) + ": " + cls.Description
			}
		}
	}
	return false, ""
}

func containsAnyFold(s string, needles []string) bool {
	lower := strings.ToLower(s)
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

// firstCauseTailLines bounds how far back into a stage's captured output the
// first-cause scan looks. The tail itself is already bounded (200 lines); this
// keeps the scan to the part that plausibly ended the stage rather than
// resurrecting an error the stage recovered from twenty minutes earlier.
const firstCauseTailLines = 40

// firstCauseFromOutputTail returns the line in a stage's captured output that
// names the failure the stage actually hit, or "" when the tail names none.
//
// It exists for the post-condition sites (#878): a check that can only observe
// "the output context is missing" must not become the recorded cause when the
// stage's own output already said why. Scoped to permission-class failures on
// purpose — those are the ones where the recorded cause was demonstrably a
// downstream symptom AND where the misattribution is expensive, because the
// same blindness also drives a pointless model escalation. Widening it to every
// classifiable line would let an error the stage logged and RECOVERED from
// claim the run.
//
// The LAST matching line wins: a stage that hits the same wall twice is
// reported at the point it gave up.
func firstCauseFromOutputTail(tail string) string {
	if strings.TrimSpace(tail) == "" {
		return ""
	}
	lines := strings.Split(tail, "\n")
	if len(lines) > firstCauseTailLines {
		lines = lines[len(lines)-firstCauseTailLines:]
	}
	found := ""
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if blocked, _ := EscalationBlockedByCategory(ln); blocked {
			found = ln
		}
	}
	return found
}
