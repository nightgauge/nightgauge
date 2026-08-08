package terminalkind

import (
	"fmt"
	"strings"
)

// StressInputs derives a deterministic input set FROM the table, so that every
// literal, every clause boundary and every ordered rule pair is exercised
// without anyone hand-authoring a fixture row for it.
//
// This replaces the round-2 "every matcher literal appears in some corpus
// input" guard, which was satisfiable by negative rows and by rows a higher
// rule already claimed. Here the check is behavioural: classify every derived
// input and compare against the committed golden
// (testdata/stress-golden.json). Deleting a clause, reordering two rules or
// widening a literal all change at least one answer, so all three show up as a
// golden diff in review rather than as silence.
//
// THE ALGORITHM IS PART OF THE CONTRACT. The SDK reimplements it verbatim
// (stressInputs in failureClassifier.ts) and both sides assert their derived
// list equals the golden's, so a divergence in the generator itself is caught
// too. Any change here must be made in both places and the golden regenerated.
//
// Order is significant and stable: first the two baselines, then rule by rule
// and clause by clause (each clause contributing its sample, the crash-wrapped
// and uppercased twins, each term alone, and — for a `~term` — the input that
// contains the literal but breaks its word boundary), then the signal
// extensions the same way, then every ordered pair of rules. Duplicates are
// dropped, keeping the FIRST occurrence.
func StressInputs(tb *Table) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}

	add("")
	add("nothing in this sentence resembles a terminal marker")

	for _, r := range tb.Rules {
		for _, clause := range r.Clauses {
			addClauseSamples(tb, add, clause)
		}
	}

	// The signal extensions are matched by the reaction path only, so they need
	// the same per-clause coverage or the golden would say nothing about them.
	for _, e := range tb.SignalExtensions {
		for _, clause := range e.Clauses {
			addClauseSamples(tb, add, clause)
		}
	}

	// Precedence matrix: every ordered pair of rules, composed. Any reordering,
	// insertion or deletion changes at least one of these answers.
	for _, a := range tb.Rules {
		for _, b := range tb.Rules {
			if a.ID == b.ID {
				continue
			}
			add(tb.sampleClause(a.Clauses[0]) + " | " + tb.sampleClause(b.Clauses[0]))
		}
	}

	return out
}

// addClauseSamples emits every derived input for one clause. Factored out so
// rules and signal extensions get IDENTICAL coverage — an extension whose
// clauses were sampled more thinly than a rule's would be a hole in the golden
// exactly where the one declared divergence lives.
func addClauseSamples(tb *Table, add func(string), clause []string) {
	s := tb.sampleClause(clause)
	add(s)
	// Under the crash-fallback prefix: `exit ` is the last rule's literal and
	// scheduler.SetStageError puts it in front of almost every real stage
	// error, so this is the shape that actually reaches the classifier.
	add("exit 1: " + s)
	// Matching is case-insensitive; the uppercase twin proves it, and proves it
	// identically in both languages.
	add(strings.ToUpper(s))
	// Clause boundary: each term on its own. For a multi-term clause these MUST
	// NOT satisfy it — that is what pins the conjunction, which the round-2
	// literal diff could not see.
	for _, term := range clause {
		add(tb.sampleClause([]string{term}))
	}
	// TERM-KIND boundary, for a `~term`. Everything above renders a word-bounded
	// literal exactly as a plain one — sampleClause strips the marker — so the
	// whole derived set answered the same with the `~` and without it, and the
	// two-character deletion that turns a word-bounded term into plain
	// containment moved no derived answer at all. These inputs are the ones the
	// two semantics disagree about: the literal with a word character glued to
	// its right edge, which plain containment claims and the boundary must not.
	for i, term := range clause {
		if !strings.HasPrefix(term, WordBoundaryRef) {
			continue
		}
		add(tb.sampleClauseViolatingBoundary(clause, i))
	}
}

// sampleClause renders a clause as text that satisfies it: literals verbatim,
// predicate references replaced by the predicate's first declared true-probe.
func (tb *Table) sampleClause(clause []string) string {
	return tb.renderClause(clause, -1)
}

// sampleClauseViolatingBoundary renders a clause the way sampleClause does but
// glues boundaryViolationSuffix to the literal of the `~term` at idx, so the
// result CONTAINS that literal without satisfying its word boundary.
func (tb *Table) sampleClauseViolatingBoundary(clause []string, idx int) string {
	return tb.renderClause(clause, idx)
}

// boundaryViolationSuffix is one word character. Appending it to any literal
// preserves containment and destroys the right-hand word boundary, which is the
// entire difference between a `~term` and a plain one.
const boundaryViolationSuffix = "s"

func (tb *Table) renderClause(clause []string, violate int) string {
	parts := make([]string, 0, len(clause))
	for i, term := range clause {
		if name, ok := strings.CutPrefix(term, PredicateRef); ok {
			parts = append(parts, tb.probeTrue(name))
			continue
		}
		lit := strings.TrimPrefix(term, WordBoundaryRef)
		if i == violate {
			lit += boundaryViolationSuffix
		}
		parts = append(parts, lit)
	}
	return strings.Join(parts, " ")
}

func (tb *Table) probeTrue(name string) string {
	for _, p := range tb.Predicates {
		if p.Name == name && len(p.ProbesTrue) > 0 {
			return p.ProbesTrue[0]
		}
	}
	panic(fmt.Sprintf("terminalkind: predicate %q declares no probes_true", name))
}

// StressCase is one row of the committed golden.
type StressCase struct {
	Input  string `json:"input"`
	Kind   string `json:"kind"`
	Signal string `json:"signal"`
}

// StressGolden is the generated behaviour snapshot: every derived input with
// the kind the table gives it and the kind the signal subset may forward.
type StressGolden struct {
	Comment []string     `json:"$comment"`
	Cases   []StressCase `json:"cases"`
}

// BuildStressGolden computes the golden from the table.
func BuildStressGolden(tb *Table) StressGolden {
	inputs := StressInputs(tb)
	cases := make([]StressCase, 0, len(inputs))
	for _, in := range inputs {
		var kind, signal string
		if r, ok := tb.Match(in); ok {
			kind = r.Kind
			if r.Signal {
				signal = r.Kind
			}
		}
		if signal == "" {
			if e, ok := tb.MatchSignalExtension(in); ok {
				signal = e.Kind
			}
		}
		cases = append(cases, StressCase{Input: in, Kind: kind, Signal: signal})
	}
	return StressGolden{
		Comment: []string{
			"GENERATED by `make generate-terminal-kind-table` from internal/terminalkind/table.json.",
			"Do not edit by hand — the drift check regenerates it and compares byte for byte.",
			"",
			"Every input is derived from the table itself (see StressInputs in stress.go and its",
			"verbatim TypeScript twin), so this file is the table's complete behaviour under its own",
			"vocabulary: `kind` is what the run record will say, `signal` is what the extension may",
			"forward over IPC — the winning rule's kind when that rule is in the signal subset, else",
			"a declared signal_extension's kind, else empty. The rows where `signal` is non-empty and",
			"differs from `kind` are exactly the declared extensions.",
			"",
			"Go, the SDK and the extension each derive the same inputs and must reproduce these",
			"answers exactly. A table edit shows up here as an explicit before/after of every input",
			"whose routing changed, which is the review surface a hand-written ladder never had.",
		},
		Cases: cases,
	}
}
