package terminalkind

import (
	"fmt"
	"strings"
)

// StressInputs derives a deterministic input set FROM the table, so that every
// literal, every clause boundary, both edges of every word-bounded term, every
// ordered rule pair and every signal-rule/extension composition is exercised
// without anyone hand-authoring a fixture row for it.
//
// WHAT THE SET DOES NOT DERIVE, NOTHING CAN SEE. Twice now a mechanism shipped
// that no derived input sampled — the `~` term kind, then the LEFT half of its
// boundary and the ORDER of SignalKind's two stages — and each time deleting the
// mechanism produced a byte-identical golden and a green suite in both
// languages. Adding a mechanism to the interpreter therefore means adding the
// input that distinguishes it here, in both languages, in the same change;
// TestStressInputsAreDerivedFromTheTable asserts the two blocks below exist.
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
// and uppercased twins, each term alone, and — for a `~term` — the two inputs
// that contain the literal but break its word boundary, one per edge), then the
// signal extensions the same way, then every ordered pair of rules, then every
// signal rule composed with every extension clause in both orders. Duplicates
// are dropped, keeping the FIRST occurrence.
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

	// STAGE precedence: every `signal: true` rule composed with every extension
	// CLAUSE, in both orders of appearance.
	//
	// The matrix above pairs rules with rules, so nothing in the derived set used
	// to carry BOTH a signal marker and extension wording — and the bound this
	// whole change advertises ("an extension can never overrule a kind projected
	// by a `signal: true` rule") is enforced by nothing but the order of the two
	// statements in SignalKind. Swapping them is a four-line reorder with no
	// literal, no import and, before these inputs existed, no artifact movement
	// at all: `[adapter-auth-failed] usage limit reached` would record
	// adapter_auth_failed and make the fleet react rate_limit_quota_exhausted.
	//
	// These rows make the ordering observable. They move the golden, and they
	// feed TestSignalNeverContradictsTheRecord — which already asserts exactly
	// this ("signal extension %q claimed %q, which the signal RULE %q already
	// answers for") and was simply never handed an input that could trip it. That
	// assertion runs against the SHIPPED projection, so regenerating the golden
	// does not make the swap green again.
	//
	// Both orders are emitted because the claim is about the interpreter's stage
	// order, not the text's: matching is containment, so a reader should be able
	// to see that the answer does not depend on which phrase came first.
	for _, r := range tb.Rules {
		if !r.Signal {
			continue
		}
		for _, e := range tb.SignalExtensions {
			for _, clause := range e.Clauses {
				rule := tb.sampleClause(r.Clauses[0])
				extension := tb.sampleClause(clause)
				add(rule + " | " + extension)
				add(extension + " | " + rule)
			}
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
	// TERM-KIND boundary, for a `~term`, ON BOTH EDGES. Everything above renders
	// a word-bounded literal exactly as a plain one — sampleClause strips the
	// marker — so the whole derived set answered the same with the `~` and
	// without it, and the two-character deletion that turns a word-bounded term
	// into plain containment moved no derived answer at all. These inputs are the
	// ones the two semantics disagree about: the literal with a word character
	// glued to an edge, which plain containment claims and the boundary must not.
	//
	// BOTH edges, because containsWordBounded is a CONJUNCTION of two independent
	// tests and sampling one edge exercises one conjunct. With only the right-edge
	// input derived, deleting `!isWordByte(lowered, i-1) && ` — one contiguous
	// deletion, one line, one file — shipped a strictly wider matcher than main's
	// `\b…\b` with `go test ./...` green, `codegen --check` exit 0 and a
	// byte-identical golden, in either language independently. That is both the
	// original harm (`max_usage limit assertion failed` recording validation_failed
	// and reacting rate_limit_quota_exhausted, which parks every issue on
	// applyQuotaCooldownLocked) and a fresh Go/TS split, which is the defect class
	// #306 exists to remove.
	for i, term := range clause {
		if !strings.HasPrefix(term, WordBoundaryRef) {
			continue
		}
		add(tb.sampleClauseViolatingBoundary(clause, i, false))
		add(tb.sampleClauseViolatingBoundary(clause, i, true))
	}
}

// sampleClause renders a clause as text that satisfies it: literals verbatim,
// predicate references replaced by the predicate's first declared true-probe.
func (tb *Table) sampleClause(clause []string) string {
	return tb.renderClause(clause, -1, false)
}

// sampleClauseViolatingBoundary renders a clause the way sampleClause does but
// glues boundaryViolationAffix to one EDGE of the literal of the `~term` at idx,
// so the result CONTAINS that literal without satisfying its word boundary. The
// left edge and the right edge are separate inputs because they are separate
// conjuncts of containsWordBounded.
func (tb *Table) sampleClauseViolatingBoundary(clause []string, idx int, atLeftEdge bool) string {
	return tb.renderClause(clause, idx, atLeftEdge)
}

// boundaryViolationAffix is one word character. Gluing it to either edge of a
// literal preserves containment and destroys the word boundary on that side,
// which is the entire difference between a `~term` and a plain one.
const boundaryViolationAffix = "s"

func (tb *Table) renderClause(clause []string, violate int, atLeftEdge bool) string {
	parts := make([]string, 0, len(clause))
	for i, term := range clause {
		if name, ok := strings.CutPrefix(term, PredicateRef); ok {
			parts = append(parts, tb.probeTrue(name))
			continue
		}
		lit := strings.TrimPrefix(term, WordBoundaryRef)
		if i == violate {
			if atLeftEdge {
				lit = boundaryViolationAffix + lit
			} else {
				lit += boundaryViolationAffix
			}
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
//
// Both columns come from the SHIPPED entry points — Match for the record,
// SignalKind for the reaction — rather than from a local re-statement of what
// they do. That distinction is load-bearing: the signal column used to inline
// "the winning rule's kind if it is signal, else an extension's", which is the
// projection's stage ORDER written a second time, so swapping the two statements
// of SignalKind left the golden byte-identical and the artifact said nothing.
func BuildStressGolden(tb *Table) StressGolden {
	inputs := StressInputs(tb)
	cases := make([]StressCase, 0, len(inputs))
	for _, in := range inputs {
		kind := ""
		if r, ok := tb.Match(in); ok {
			kind = r.Kind
		}
		cases = append(cases, StressCase{Input: in, Kind: kind, Signal: tb.SignalKind(in)})
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
