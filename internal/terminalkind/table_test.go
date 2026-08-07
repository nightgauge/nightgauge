package terminalkind

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTableIsWellFormed is the schema lint. It exists because the table is now
// the only place classification is written down: a malformed rule cannot be
// caught by "the other ladder disagrees" any more, so it has to be caught here.
func TestTableIsWellFormed(t *testing.T) {
	tb := Load() // panics on a structural defect; Parse's validation is the first gate

	declaredDead := map[string]DeadTerm{}
	for _, d := range tb.DeadTerms {
		if d.Why == "" || d.Rule == "" {
			t.Errorf("dead_terms entry %q must name its rule and say why it is preserved", d.Term)
		}
		if d.Term == strings.ToLower(d.Term) {
			t.Errorf("dead_terms lists %q, which is all-lowercase and therefore CAN match. "+
				"A term is dead only because matching is done against lowercased text.", d.Term)
		}
		declaredDead[d.Term] = d
	}

	seenDead := map[string]bool{}
	for _, r := range tb.Rules {
		if strings.TrimSpace(r.Why) == "" {
			t.Errorf("rule %q has no `why` — order is the contract and every rung has to argue "+
				"for its position", r.ID)
		}
		for _, clause := range r.Clauses {
			for _, term := range clause {
				if strings.HasPrefix(term, PredicateRef) {
					continue
				}
				if term != strings.ToLower(term) {
					d, ok := declaredDead[term]
					if !ok {
						t.Errorf("rule %q term %q carries uppercase characters. Terms are matched "+
							"against LOWERCASED text, so it can never fire. Lowercase it (a routing "+
							"change) or declare it in dead_terms with the reason.", r.ID, term)
						continue
					}
					if d.Rule != r.ID {
						t.Errorf("dead term %q is declared against rule %q but appears in %q",
							term, d.Rule, r.ID)
					}
					seenDead[term] = true
				}
			}
		}
	}
	for term := range declaredDead {
		if !seenDead[term] {
			t.Errorf("dead_terms lists %q, which no rule contains. Delete the entry — it now "+
				"excuses nothing.", term)
		}
	}
}

// TestDeadTermsAreUnreachable proves the claim the declaration makes, rather
// than trusting it: a clause containing a dead term must be unsatisfiable even
// by text built to satisfy it.
func TestDeadTermsAreUnreachable(t *testing.T) {
	tb := Load()
	for _, d := range tb.DeadTerms {
		for _, r := range tb.Rules {
			if r.ID != d.Rule {
				continue
			}
			for _, clause := range r.Clauses {
				if !contains(clause, d.Term) {
					continue
				}
				text := strings.Join(clause, " ")
				if satisfied(strings.ToLower(text), clause) {
					t.Errorf("clause %v of rule %q is declared dead via %q but IS satisfiable",
						clause, r.ID, d.Term)
				}
			}
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// TestPredicateProbes pins each named predicate against the probes the table
// declares. The SDK asserts the SAME probes against its own implementation, so
// the two cannot answer differently without one of them going red — the
// remaining silent-divergence path inside an otherwise data-driven ladder.
func TestPredicateProbes(t *testing.T) {
	tb := Load()
	if len(tb.Predicates) == 0 {
		t.Fatal("no predicates declared — if the table stopped using them, delete the mechanism")
	}
	for _, p := range tb.Predicates {
		fn, ok := predicates[p.Name]
		if !ok {
			t.Fatalf("predicate %q has no Go implementation", p.Name)
		}
		if len(p.ProbesTrue) == 0 || len(p.ProbesFalse) == 0 {
			t.Errorf("predicate %q must declare probes_true and probes_false — they are what pins "+
				"it against the TypeScript twin", p.Name)
		}
		for _, s := range p.ProbesTrue {
			if !fn(strings.ToLower(s)) {
				t.Errorf("predicate %q rejected declared true-probe %q", p.Name, s)
			}
		}
		for _, s := range p.ProbesFalse {
			if fn(strings.ToLower(s)) {
				t.Errorf("predicate %q accepted declared false-probe %q", p.Name, s)
			}
		}
	}
}

// TestSignalSubsetIsDeclared keeps the subset honest at the table level: it must
// be non-empty (otherwise the extension signals nothing and the mechanism is
// dead code) and a strict subset (otherwise it is not a subset).
func TestSignalSubsetIsDeclared(t *testing.T) {
	tb := Load()
	signal := 0
	for _, r := range tb.Rules {
		if r.Signal {
			signal++
		}
	}
	if signal == 0 {
		t.Error("no rule is marked signal — the extension would forward nothing; delete the " +
			"projection instead of leaving it inert")
	}
	if signal == len(tb.Rules) {
		t.Error("every rule is marked signal — the extension exists to catch what the TS layer " +
			"sees FIRST, not to be a second complete taxonomy")
	}
}

// TestSignalNeverContradictsTheRecord is the structural statement of #306's
// acceptance, over the derived stress set rather than over hand-written rows:
// for EVERY input the table can describe, the signal is either silent or
// identical to the record.
func TestSignalNeverContradictsTheRecord(t *testing.T) {
	tb := Load()
	for _, in := range StressInputs(tb) {
		if s := SignalKind(in); s != "" && s != Classify(in) {
			t.Fatalf("signal %q contradicts record %q for %q", s, Classify(in), in)
		}
	}
}

// TestMatcherHasNoLiterals closes the one hole the corpus and the derived
// stress set structurally cannot see: both are built FROM the table's
// vocabulary, so a rule hand-written into an INTERPRETER for a marker the table
// has never heard of is invisible to them. Round 2's evasion was exactly that,
// on the highest-authority ladder.
//
// So the matcher is required to contain no string literal at all: every marker
// it compares against must come out of table.json. The assertion is only
// meaningful because the matcher is now twenty lines of data-walking — the same
// check over a 33-block hand-written ladder would be absurd. The SDK asserts the
// same thing about its own interpreter.
func TestMatcherHasNoLiterals(t *testing.T) {
	src, err := os.ReadFile("table.go")
	if err != nil {
		t.Fatalf("read table.go: %v", err)
	}
	for _, fn := range []string{"func (tb *Table) Match(", "func satisfied("} {
		body := goFuncBody(t, string(src), fn)
		for _, lit := range stringLiteralRe.FindAllString(body, -1) {
			// The empty string is allowed: it is the "no input" / "no match"
			// sentinel, and it cannot be a marker.
			if lit == `""` {
				continue
			}
			t.Errorf("%s contains the string literal %s. Markers belong in table.json, where "+
				"every language reads them; a literal here is a rule only Go has, and neither the "+
				"corpus nor the derived stress set can see it.", fn, lit)
		}
	}
}

var stringLiteralRe = regexp.MustCompile("\"(?:[^\"\\\\]|\\\\.)*\"|`[^`]*`")

// goFuncBody returns the brace-balanced body of a function, comments stripped.
func goFuncBody(t *testing.T, src, decl string) string {
	t.Helper()
	start := strings.Index(src, decl)
	if start < 0 {
		t.Fatalf("%s not found — the matcher was renamed and this guard now checks nothing", decl)
	}
	open := strings.Index(src[start:], "{")
	if open < 0 {
		t.Fatalf("%s has no body", decl)
	}
	open += start
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return lineCommentRe.ReplaceAllString(src[open:i+1], "")
			}
		}
	}
	t.Fatalf("%s body is unbalanced", decl)
	return ""
}

var lineCommentRe = regexp.MustCompile(`(?m)//[^\n]*`)

// TestRuleFiresIgnoresPrecedence pins the distinction RuleFires exists for.
// Classify answers "what is this failure" and a higher rule can legitimately
// claim text the cost-cap rule also matches; the recovery paths that ask "did
// the cost cap kill this" must still get true there, which is why they cannot
// simply compare Classify's answer.
func TestRuleFiresIgnoresPrecedence(t *testing.T) {
	cases := []struct {
		text  string
		fires bool
		kind  string
	}{
		{"", false, ""},
		{"[cost-cap-exceeded] Stage feature-dev terminated: cost cap exceeded", true, "budget_exceeded"},
		{"COST CAP EXCEEDED after 12m", true, "budget_exceeded"},
		// Claimed by api-overloaded for the RECORD, but the cost cap still fired.
		{"API Error: Overloaded — [cost-cap-exceeded] stage stopped", true, "api_overloaded"},
		{"exit 1: schema validation failed", false, "validation_error"},
	}
	for _, c := range cases {
		if got := RuleFires(RuleCostCapExceeded, c.text); got != c.fires {
			t.Errorf("RuleFires(%q) = %v, want %v", c.text, got, c.fires)
		}
		if got := Classify(c.text); got != c.kind {
			t.Errorf("Classify(%q) = %q, want %q", c.text, got, c.kind)
		}
	}
	if !panics(func() { RuleFires("no-such-rule", "anything") }) {
		t.Error("RuleFires must panic on an unknown rule id — a silent false would disable a " +
			"recovery path with no symptom")
	}
}

func panics(f func()) (did bool) {
	defer func() {
		if recover() != nil {
			did = true
		}
	}()
	f()
	return false
}

// TestStressGoldenIsInSync is the behaviour drift check. It is the reason a
// clause cannot be deleted or two rules swapped without a visible diff: the
// golden holds an answer for every input derived from the table.
func TestStressGoldenIsInSync(t *testing.T) {
	want, err := RenderStressGolden(Load())
	if err != nil {
		t.Fatalf("render golden: %v", err)
	}
	got, err := os.ReadFile(filepath.Join("testdata", "stress-golden.json"))
	if err != nil {
		t.Fatalf("read stress-golden.json: %v", err)
	}
	if string(got) != string(want) {
		var a, b StressGolden
		_ = json.Unmarshal(got, &a)
		_ = json.Unmarshal(want, &b)
		t.Errorf("testdata/stress-golden.json is out of sync with table.json "+
			"(committed %d cases, table produces %d).\n"+
			"Run `make generate-terminal-kind-table` and review the diff: every line that changed "+
			"is an input whose routing changed.", len(a.Cases), len(b.Cases))
	}
}

// TestGeneratedTypeScriptIsInSync is the cross-language drift check. Together
// with .husky/pre-commit and scripts/ci-local.sh it makes "add a rule to one
// consumer" impossible: the SDK's copy of the table is generated, and a hand
// edit fails here.
func TestGeneratedTypeScriptIsInSync(t *testing.T) {
	want, err := RenderTypeScript(Load())
	if err != nil {
		t.Fatalf("render TypeScript: %v", err)
	}
	path := filepath.Join("..", "..", GeneratedTSPath)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", GeneratedTSPath, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s is out of sync with internal/terminalkind/table.json.\n"+
			"Run `make generate-terminal-kind-table` and commit the result. Terminal-kind "+
			"classification is defined ONCE; the generated module is only how TypeScript sees it, "+
			"and hand-editing it is the exact drift #306 removed.", GeneratedTSPath)
	}
}

// TestStressInputsAreDerivedFromTheTable guards the generator itself: the SDK
// reimplements it and both sides compare against the same golden, so silently
// shrinking the derivation would weaken all three suites at once.
func TestStressInputsAreDerivedFromTheTable(t *testing.T) {
	tb := Load()
	inputs := StressInputs(tb)
	if len(inputs) < 1000 {
		t.Fatalf("only %d stress inputs — the derivation has been narrowed; it must cover every "+
			"clause, every term and every ordered rule pair", len(inputs))
	}
	seen := map[string]bool{}
	for _, in := range inputs {
		if seen[in] {
			t.Fatalf("duplicate stress input %q — the golden would carry it twice", in)
		}
		seen[in] = true
	}
	// Every literal in the table must appear in some derived input, by
	// construction rather than by fixture authoring.
	for _, r := range tb.Rules {
		for _, clause := range r.Clauses {
			for _, term := range clause {
				if strings.HasPrefix(term, PredicateRef) {
					continue
				}
				if !seen[term] && !seen[strings.Join(clause, " ")] {
					t.Errorf("no stress input isolates term %q of rule %q", term, r.ID)
				}
			}
		}
	}
}
