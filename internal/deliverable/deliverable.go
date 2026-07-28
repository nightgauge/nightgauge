// Package deliverable answers one question about a change: did the pipeline
// ever execute the thing this change built?
//
// It exists because `validation_status` is computed as "passed unless something
// actively failed", so a tier that never ran cannot lower the verdict (#152). A
// run whose entire deliverable was a new end-to-end suite therefore reported
// `validation_status: "passed"`, opened a PR, and merged — with the suite never
// executed once. The 1,361 unit tests that did pass were the pre-existing suite
// and touched none of the new files.
//
// The stage was not dishonest. It wrote `e2e_tests.ran: false` and a truthful
// reason naming the plan scope. The truth was in the artifact; the roll-up
// discarded it. So this package does not second-guess what the skill reported
// about what ran — it re-derives the verdict from two facts that are not
// self-granted exemptions: the files the change introduced, and the per-tier
// ran flags.
//
// That separation is the point. feature-planning writes a plan declaring a
// deliverable out of scope, and feature-validate reads that same plan and skips
// it; the exemption is internally consistent and externally invisible. A check
// that lives outside both, and consults neither the plan nor the roll-up, is
// the only kind that can catch it.
package deliverable

import (
	"path"
	"sort"
	"strings"
)

// Tier is a test execution tier, matching the per-tier blocks in
// validate-{N}.json (`unit_tests`, `integration_tests`, `e2e_tests`).
type Tier string

const (
	TierUnit        Tier = "unit"
	TierIntegration Tier = "integration"
	TierE2E         Tier = "e2e"
)

// Execution records which tiers actually executed. These come straight from the
// `ran` flags the validate artifact already carries — the sub-fields the skill
// reports factually, not the roll-up it computes.
type Execution struct {
	Unit        bool
	Integration bool
	E2E         bool
}

func (e Execution) ran(t Tier) bool {
	switch t {
	case TierUnit:
		return e.Unit
	case TierIntegration:
		return e.Integration
	case TierE2E:
		return e.E2E
	}
	return false
}

// TestArtifact is a file the change introduced that is recognisably part of a
// test suite, together with the tiers capable of executing it.
type TestArtifact struct {
	// Path is the repo-relative path as reported by git.
	Path string
	// Tiers are the tiers that could execute this file. Usually one; a
	// directory whose meaning differs by ecosystem yields several (see
	// tiersFor).
	Tiers []Tier
}

// exercised reports whether ANY tier capable of running this file ran.
//
// Any, not all, on purpose. An ambiguous file must not raise the alarm merely
// because one of its candidate interpretations was idle — this check is only
// worth having if operators trust it, and a check that cries wolf on ordinary
// runs gets muted, which costs more than the bug it was added to catch.
func (a TestArtifact) exercised(e Execution) bool {
	for _, t := range a.Tiers {
		if e.ran(t) {
			return true
		}
	}
	return false
}

// Unexercised returns the test artifacts this change introduced whose tier
// never executed, sorted by path for a stable card/PR annotation.
//
// changedFiles is the change's own file set (`git diff --name-only
// base...head`) — ground truth from git, not the skill's self-report of what it
// wrote. A file that is not recognisably a test contributes nothing: this
// answers "you built a test and never ran it", which is unambiguous, and
// deliberately does not attempt "you built a feature and nothing covers it",
// which is a coverage question with no deterministic answer.
func Unexercised(changedFiles []string, ran Execution) []TestArtifact {
	var out []TestArtifact
	for _, f := range changedFiles {
		tiers := tiersFor(f)
		if len(tiers) == 0 {
			continue
		}
		a := TestArtifact{Path: f, Tiers: tiers}
		if a.exercised(ran) {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Tiers returns the distinct tiers across a set of artifacts, in the canonical
// unit → integration → e2e order, for naming the gap in one line.
func Tiers(artifacts []TestArtifact) []Tier {
	seen := map[Tier]bool{}
	for _, a := range artifacts {
		for _, t := range a.Tiers {
			seen[t] = true
		}
	}
	var out []Tier
	for _, t := range []Tier{TierUnit, TierIntegration, TierE2E} {
		if seen[t] {
			out = append(out, t)
		}
	}
	return out
}

// e2eDirs are directory segments whose every file is suite material, so a
// fixture or page-object added beside a spec still counts as unexercised.
var e2eDirs = map[string]bool{
	"e2e":        true,
	"cypress":    true,
	"playwright": true,
}

// tiersFor classifies one path into the tiers that could execute it, or nil if
// it is not test material at all.
//
// Ordered most-specific first. The `integration_test` case is the interesting
// one: Flutter puts device-driven end-to-end suites there, while most other
// ecosystems mean the integration tier by that name. Rather than guess an
// ecosystem, it returns BOTH — combined with exercised()'s any-tier rule, the
// file is flagged only when neither tier ran, which is true regardless of which
// reading is right.
func tiersFor(p string) []Tier {
	p = strings.TrimPrefix(path.Clean(strings.ReplaceAll(p, "\\", "/")), "./")
	lower := strings.ToLower(p)
	base := path.Base(lower)
	segs := strings.Split(path.Dir(lower), "/")

	hasSeg := func(name string) bool {
		for _, s := range segs {
			if s == name {
				return true
			}
		}
		return false
	}

	// A directory that is wholly a suite: every file in it is suite material.
	for _, s := range segs {
		if e2eDirs[s] {
			return []Tier{TierE2E}
		}
	}
	if hasSeg("integration_test") {
		return []Tier{TierIntegration, TierE2E}
	}

	// Beyond this point the filename itself must read as a test.
	if !looksLikeTestFile(base) {
		return nil
	}

	switch {
	case strings.Contains(base, ".e2e."):
		return []Tier{TierE2E}
	case strings.Contains(base, ".integration."), strings.Contains(base, "_integration_test."):
		return []Tier{TierIntegration}
	case hasSeg("integration"):
		return []Tier{TierIntegration}
	case hasSeg("unit"):
		return []Tier{TierUnit}
	case path.Ext(base) == ".feature":
		// Gherkin. Whichever runner owns it, it is never a unit test — no unit
		// runner executes a .feature file — so letting a green unit suite
		// satisfy it would reintroduce exactly the laundering this package
		// exists to stop. Ambiguous between the two tiers that DO run it.
		return []Tier{TierIntegration, TierE2E}
	}

	// Everything left is a unit test: a file under a plain test directory, and
	// equally a test-named file outside any of them (Go's co-located
	// convention, and co-located *.test.ts).
	return []Tier{TierUnit}
}

// looksLikeTestFile recognises the naming conventions that unambiguously mark a
// file as a test across the ecosystems the pipeline runs against.
func looksLikeTestFile(base string) bool {
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	switch ext {
	case ".go", ".dart", ".py", ".rb", ".exs", ".rs", ".java", ".kt":
		if strings.HasSuffix(stem, "_test") {
			return true
		}
	}
	if ext == ".py" && strings.HasPrefix(stem, "test_") {
		return true
	}

	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".dart":
		if strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") ||
			strings.Contains(stem, ".test.") || strings.Contains(stem, ".spec.") ||
			strings.HasSuffix(stem, "_test") {
			return true
		}
	}

	// Cypress/Playwright feature files.
	if ext == ".feature" {
		return true
	}
	return false
}
