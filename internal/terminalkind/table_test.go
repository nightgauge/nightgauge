package terminalkind

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
				term = strings.TrimPrefix(term, WordBoundaryRef)
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
// for EVERY input the table can describe, the signal is either silent, or
// identical to the record, or the kind of a DECLARED signal extension.
//
// The third branch is the only way the two can differ, it is data rather than
// code, and it is bounded: an extension is consulted only when the rule ladder
// projects no signal, so it can never overrule a kind the record names through
// a signal rule.
func TestSignalNeverContradictsTheRecord(t *testing.T) {
	tb := Load()
	for _, in := range StressInputs(tb) {
		s := SignalKind(in)
		if s == "" || s == Classify(in) {
			continue
		}
		e, ok := tb.MatchSignalExtension(in)
		if !ok || e.Kind != s {
			t.Fatalf("signal %q contradicts record %q for %q, and no declared signal extension "+
				"produces it", s, Classify(in), in)
		}
		if r, matched := tb.Match(in); matched && r.Signal {
			t.Fatalf("signal extension %q claimed %q, which the signal RULE %q already answers for "+
				"— extensions must never overrule a kind the record names", e.ID, in, r.ID)
		}
	}
}

// TestSignalExtensionsAreDeclaredAndBounded keeps the one deliberate divergence
// honest at the table level.
func TestSignalExtensionsAreDeclaredAndBounded(t *testing.T) {
	tb := Load()
	for _, e := range tb.SignalExtensions {
		if strings.TrimSpace(e.Why) == "" {
			t.Errorf("signal extension %q has no `why`. It is the only place the fleet's reaction "+
				"may differ from the run record; the argument for it is the whole justification.",
				e.ID)
		}
		// An extension that duplicates a signal rule's kind for text that rule
		// already claims would be unreachable; one that duplicates a NON-signal
		// rule's kind would be a signal flag written the long way round.
		for _, r := range tb.Rules {
			if r.Kind != e.Kind || r.Signal {
				continue
			}
			t.Errorf("signal extension %q produces %q, which the non-signal rule %q also produces. "+
				"Mark that rule `signal: true` instead of routing around it.", e.ID, e.Kind, r.ID)
		}
		fired := false
		for _, clause := range e.Clauses {
			if SignalKind(tb.sampleClause(clause)) == e.Kind {
				fired = true
			}
		}
		if !fired {
			t.Errorf("no clause of signal extension %q can fire — a rule above it claims every "+
				"input it describes, so it is dead code", e.ID)
		}
	}
}

// packageLiteralAllowlist is the COMPLETE set of string literals this package's
// non-test source is allowed to contain — declared here, in one place, and
// asserted as an exact set (see TestMatchingSurfaceHasNoUndeclaredLiterals).
//
// Not one of them is a failure-text marker: they are error-message formats, the
// predicate-reference prefix, one rule ID that two recovery paths ask for by
// name, and the derivation vocabulary the stress generator composes inputs
// from. Every marker the classifier compares an input against comes out of
// table.json.
var packageLiteralAllowlist = map[string]bool{
	// table.go — Parse/Load diagnostics and the schema's own vocabulary.
	"":                  true,
	"@":                 true,
	"~":                 true,
	"cost-cap-exceeded": true,
	// predicates.go — the one predicate NAME, which table.json references as
	// `@mentions_registry_model` and Parse refuses to accept without.
	"mentions_registry_model":                               true,
	"terminalkind: embedded table.json is invalid: %v":      true,
	"unsupported schema_version %d":                         true,
	"table has no rules":                                    true,
	"rule with empty id or kind":                            true,
	"duplicate rule id %q":                                  true,
	"signal extension with empty id or kind":                true,
	"rule %q has no clauses":                                true,
	"rule %q has an empty clause":                           true,
	"rule %q has an empty term":                             true,
	"rule %q references unknown predicate %q":               true,
	"table declares predicate %q with no Go implementation": true,
	"terminalkind: no rule with id %q":                      true,
	// stress.go — the derived-input vocabulary. Mirrored verbatim in the
	// TypeScript twin; changing either without the other fails the golden.
	"nothing in this sentence resembles a terminal marker": true,
	"exit 1: ": true,
	" | ":      true,
	" ":        true,
	"terminalkind: predicate %q declares no probes_true":                                       true,
	"GENERATED by `make generate-terminal-kind-table` from internal/terminalkind/table.json.":  true,
	"Do not edit by hand — the drift check regenerates it and compares byte for byte.":         true,
	"Every input is derived from the table itself (see StressInputs in stress.go and its":      true,
	"verbatim TypeScript twin), so this file is the table's complete behaviour under its own":  true,
	"vocabulary: `kind` is what the run record will say, `signal` is what the extension may":   true,
	"forward over IPC — the winning rule's kind when that rule is in the signal subset, else":  true,
	"a declared signal_extension's kind, else empty. The rows where `signal` is non-empty and": true,
	"differs from `kind` are exactly the declared extensions.":                                 true,
	"Go, the SDK and the extension each derive the same inputs and must reproduce these":       true,
	"answers exactly. A table edit shows up here as an explicit before/after of every input":   true,
	"whose routing changed, which is the review surface a hand-written ladder never had.":      true,
}

// TestMatchingSurfaceHasNoUndeclaredLiterals closes the one hole the corpus and
// the derived stress set structurally cannot see: both are built FROM the
// table's vocabulary, so a rule hand-written into an INTERPRETER for a marker
// the table has never heard of is invisible to them.
//
// Round 3 guarded two functions by name — `Match` and `satisfied` — and both of
// round 3's own evasions went around that fence rather than through it: one by
// putting the branch in `Classify` (the function that WRITES the run record),
// the other by declaring `const deferredMarker = "…"` one line above the
// window and referencing it from inside. So the guard is now the whole package,
// as an EXACT SET: every string literal in every non-test file here must appear
// in packageLiteralAllowlist above, and every allowlist entry must still be
// present. There is no line to hoist to, and no function to move to.
//
// Literals are collected with go/parser rather than a regex, so comments,
// escapes and raw strings cannot be used to smuggle one past the scan. Rendering
// the generated TypeScript — which is nothing but literals — was moved to
// internal/terminalkind/codegen for exactly this reason.
func TestMatchingSurfaceHasNoUndeclaredLiterals(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected exactly one non-test package here, found %d", len(pkgs))
	}

	found := map[string]bool{}
	files := 0
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			files++
			// Import paths and struct tags are string literals the language
			// requires and that no expression can compare an input against.
			// Everything else is in scope.
			structural := map[token.Pos]bool{}
			ast.Inspect(file, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.ImportSpec:
					if v.Path != nil {
						structural[v.Path.Pos()] = true
					}
				case *ast.Field:
					if v.Tag != nil {
						structural[v.Tag.Pos()] = true
					}
				}
				return true
			})
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING || structural[lit.Pos()] {
					return true
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Errorf("%s: unparseable string literal %s", name, lit.Value)
					return true
				}
				found[v] = true
				if !packageLiteralAllowlist[v] {
					t.Errorf("%s:%d contains the undeclared string literal %q.\n"+
						"Markers belong in table.json, where every language reads them; a literal "+
						"here is a rule only Go has, and neither the corpus nor the derived stress "+
						"set can see it. If it is genuinely not a marker, add it to "+
						"packageLiteralAllowlist and say why in the comment above it.",
						filepath.Base(name), fset.Position(lit.Pos()).Line, v)
				}
				return true
			})
		}
	}
	if files < 3 {
		t.Fatalf("only %d non-test files scanned — the walk is broken and this guard now "+
			"checks almost nothing", files)
	}
	for lit := range packageLiteralAllowlist {
		if !found[lit] {
			t.Errorf("packageLiteralAllowlist declares %q, which no longer appears in the "+
				"package. Delete the entry — a stale allowlist is a hole waiting for the string "+
				"to come back somewhere else.", lit)
		}
	}
}

// TestOrchestratorEntryPointIsAPureDelegation guards the function that actually
// writes `terminal_kind` into the run record. It lives outside this package, so
// the literal scan above cannot see it, and round 3 asserted nothing about it at
// all: inserting `if strings.Contains(…) { return TerminalKindStallKill }` at the
// top of it passed the entire Go suite.
func TestOrchestratorEntryPointIsAPureDelegation(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "orchestrator", "failure_handler.go"))
	if err != nil {
		t.Fatalf("read failure_handler.go: %v", err)
	}
	for decl, want := range map[string]string{
		"func ClassifyTerminalKind(errorText string) string {": "return terminalkind.Classify(errorText)",
	} {
		body := goFuncBody(t, string(src), decl)
		got := strings.Join(strings.Fields(body), " ")
		if got != "{ "+want+" }" {
			t.Errorf("%s must be exactly `%s` and nothing else.\n  got: %s\n"+
				"This is the function whose answer becomes the run record. A condition here is a "+
				"rule only Go has, on the authoritative side, invisible to the table, the corpus "+
				"and the derived stress set alike.", decl, want, got)
		}
	}
}

// goFuncBody returns the brace-balanced body of a function, comments stripped.
func goFuncBody(t *testing.T, src, decl string) string {
	t.Helper()
	start := strings.Index(src, decl)
	if start < 0 {
		t.Fatalf("%s not found — it was renamed and this guard now checks nothing", decl)
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
				term = strings.TrimPrefix(term, WordBoundaryRef)
				if !seen[term] && !seen[strings.Join(clause, " ")] {
					t.Errorf("no stress input isolates term %q of rule %q", term, r.ID)
				}
			}
		}
	}
}
