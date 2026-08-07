package terminalkind

// Terminal-kind behaviour corpus — the evidence side of #306.
//
// table.json says what the rules ARE; this corpus says what they must MEAN for
// concrete failure text, and every row carries a written argument for its
// answer. The same file is read by the SDK and the extension suites:
//
//	packages/nightgauge-sdk/tests/analysis/health/failureClassifier.corpusParity.test.ts
//	packages/nightgauge-vscode/tests/services/terminalKindSignal.corpusParity.test.ts
//
// `expected` is what the run record will say; `expected_signal` is what the
// extension may forward to Go over IPC ("" = defer). Because all three sides
// interpret ONE table, a row that fails here fails identically there — the
// corpus no longer has to catch drift between implementations (there is only
// one), it has to catch the table being WRONG.
//
// Complements failureClassifier.parity.test.ts (#229), which diffs the kind
// VOCABULARY across the two languages.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type corpusCase struct {
	ID             string `json:"id"`
	Input          string `json:"input"`
	Expected       string `json:"expected"`
	ExpectedSignal string `json:"expected_signal"`
	Source         string `json:"source"`
	Producer       string `json:"producer"`
	Rationale      string `json:"rationale"`
}

type terminalKindCorpus struct {
	Comment []string     `json:"$comment"`
	Cases   []corpusCase `json:"cases"`
}

type capturedShape struct {
	Text        string   `json:"text"`
	Origin      string   `json:"origin"`
	Occurrences int      `json:"occurrences"`
	Markers     []string `json:"markers"`
}

type capturedShapes struct {
	Generator string          `json:"generator"`
	Shapes    []capturedShape `json:"shapes"`
}

const corpusDir = "testdata"

func loadTerminalKindCorpus(t *testing.T) terminalKindCorpus {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(corpusDir, "corpus.json"))
	if err != nil {
		t.Fatalf("read corpus.json: %v", err)
	}
	var c terminalKindCorpus
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("parse corpus.json: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus.json lists no cases")
	}
	return c
}

func loadCapturedShapes(t *testing.T) capturedShapes {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(corpusDir, "captured-shapes.json"))
	if err != nil {
		t.Fatalf("read captured-shapes.json: %v", err)
	}
	var s capturedShapes
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("parse captured-shapes.json: %v", err)
	}
	if len(s.Shapes) == 0 {
		t.Fatal("captured-shapes.json lists no shapes")
	}
	return s
}

// TestCorpus_Classify is the behaviour assertion: every corpus input must
// classify to the kind the corpus pins.
func TestCorpus_Classify(t *testing.T) {
	corpus := loadTerminalKindCorpus(t)
	for _, tc := range corpus.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			if got := Classify(tc.Input); got != tc.Expected {
				t.Errorf("Classify(%q)\n  got:      %q\n  expected: %q\n  why this row exists: %s",
					tc.Input, got, tc.Expected, tc.Rationale)
			}
		})
	}
}

// TestCorpus_SignalProjection pins the signal subset from BOTH sides, which is
// what makes the extension incapable of overriding the record.
//
// Upper bound: a non-empty signal must equal the recorded kind — the signal
// side runs the same ladder and only projects the winning rule, so it can never
// name a different one. Lower bound: when the winning rule is declared
// `signal: true` the signal side MUST answer; silence there would be a rule
// leaving the subset unnoticed.
//
// Both are per-row data, so flipping `signal` on any rule turns every row that
// rule wins red. That is the guard round 2 lacked: its extension suite asserted
// only that eight kinds appeared SOMEWHERE, a lower bound that an extra rule
// could not violate.
func TestCorpus_SignalProjection(t *testing.T) {
	corpus := loadTerminalKindCorpus(t)
	tb := Load()
	for _, tc := range corpus.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			got := SignalKind(tc.Input)
			if got != tc.ExpectedSignal {
				t.Errorf("SignalKind(%q)\n  got:      %q\n  expected: %q",
					tc.Input, got, tc.ExpectedSignal)
			}
			r, ok := tb.Match(tc.Input)
			wantSignal := ""
			source := ""
			if ok && r.Signal {
				wantSignal, source = r.Kind, r.ID
			} else if e, isExt := tb.MatchSignalExtension(tc.Input); isExt {
				wantSignal, source = e.Kind, e.ID
			}
			if got != wantSignal {
				t.Errorf("%s: signal projection %q does not follow from the winning rule %q",
					tc.ID, got, r.ID)
			}
			if got != "" && got != tc.Expected && source != extensionSourceOf(tb, tc.Input) {
				t.Errorf("%s: the signal (%q) disagrees with the record (%q) and no declared "+
					"signal extension produces it — the interpreter is broken",
					tc.ID, got, tc.Expected)
			}
		})
	}
}

// TestCorpus_ExercisesEveryRule requires each rung of the ladder to WIN at
// least one corpus row. Winning is the point: round 2's literal-coverage guard
// accepted a literal that merely APPEARED in some input, which negative rows and
// rows claimed by an earlier rule satisfied without exercising anything.
//
// Rule-level rather than literal-level on purpose. Literal-level coverage is
// now structural — StressInputs derives an input for every clause and every
// term straight from the table, so no fixture row has to stand in for it.
func TestCorpus_ExercisesEveryRule(t *testing.T) {
	corpus := loadTerminalKindCorpus(t)
	tb := Load()

	won := map[string]bool{}
	for _, tc := range corpus.Cases {
		if r, ok := tb.Match(tc.Input); ok {
			won[r.ID] = true
		}
	}

	var missing []string
	for _, r := range tb.Rules {
		if !won[r.ID] {
			missing = append(missing, r.ID)
		}
	}
	if len(missing) > 0 {
		t.Errorf("rules that no corpus row exercises: %v\n"+
			"Every rung needs at least one input it actually claims, with a rationale saying why "+
			"that text must route that way. Add rows to %s/corpus.json.", missing, corpusDir)
	}
}

// TestEveryConjunctionHalfIsPinnedByANegativeRow makes the README's guarantee
// TRUE BY CONSTRUCTION instead of by fixture diligence.
//
// The failure it exists for is round 2's finding 6 and round 3's finding 2: an
// AND quietly widened into an OR. `["api error", "connection refused"]` becoming
// `["connection refused"]` is a live routing change on a signal:true rule — any
// stage failure mentioning a refused port now classifies api_connection_lost and
// the fleet applies transient-blip recovery — and after regenerating the golden
// it left every suite green, visible only as three shortened lines and one
// deletion inside a 7,000-line generated file.
//
// So the requirement is DERIVED from the table rather than remembered: for every
// multi-term clause and every term in it, widen that clause to the term alone
// and re-classify the whole corpus. At least one row must change its answer. If
// none does, the corpus cannot see that half of that conjunction, and the test
// prints the exact row to add.
//
// A @predicate half is handled the same way: widening
// `["usage limit", "@mentions_registry_model"]` to `["@mentions_registry_model"]`
// demands a row that names a model and is NOT model_unavailable, and widening it
// to `["usage limit"]` demands one with the phrase and no model named.
func TestEveryConjunctionHalfIsPinnedByANegativeRow(t *testing.T) {
	corpus := loadTerminalKindCorpus(t)
	tb := Load()

	dead := map[string]bool{}
	for _, d := range tb.DeadTerms {
		dead[d.Term] = true
	}

	conjunctions := 0
	for ri, rule := range tb.Rules {
		for ci, clause := range rule.Clauses {
			if len(clause) < 2 {
				continue
			}
			conjunctions++
			for _, term := range clause {
				// A declared dead term cannot fire even alone, so no input can
				// pin it. TestDeadTermsAreUnreachable proves that separately;
				// the live half of the same clause is still required below.
				if dead[term] {
					continue
				}
				widened := widenClause(tb, ri, ci, term)
				changed := ""
				for _, tc := range corpus.Cases {
					got := ""
					if r, ok := widened.Match(tc.Input); ok {
						got = r.Kind
					}
					if got != tc.Expected {
						changed = tc.ID
						break
					}
				}
				if changed != "" {
					continue
				}
				t.Errorf("clause %v of rule %q has NO corpus row pinning the half %q.\n"+
					"Widening the clause to just that term changes no expectation, so the "+
					"conjunction could be turned into a disjunction — a live routing change — with "+
					"every suite green.\n"+
					"Add a row whose input satisfies %q WITHOUT satisfying the rest of the clause; "+
					"the derived probe %q classifies as %q today, which is the expectation to pin.",
					clause, rule.ID, term, term,
					tb.sampleClause([]string{term}), Classify(tb.sampleClause([]string{term})))
			}
		}
	}
	if conjunctions == 0 {
		t.Fatal("no multi-term clause found — either the table lost every conjunction or this " +
			"guard stopped finding them")
	}
}

// widenClause returns a copy of tb in which one clause is replaced by a single
// one of its terms — the AND→OR mutation, applied one half at a time.
func widenClause(tb *Table, ruleIdx, clauseIdx int, term string) *Table {
	out := *tb
	out.Rules = make([]Rule, len(tb.Rules))
	copy(out.Rules, tb.Rules)
	r := out.Rules[ruleIdx]
	clauses := make([][]string, len(r.Clauses))
	copy(clauses, r.Clauses)
	clauses[clauseIdx] = []string{term}
	r.Clauses = clauses
	out.Rules[ruleIdx] = r
	return &out
}

var terminalKindConstRe = regexp.MustCompile(`(?m)^\s*TerminalKind\w+\s*=\s*"([a-z_]+)"`)

// TestCorpus_CoversEveryKind keeps the taxonomy and the table in step: adding a
// TerminalKind constant fails here until the table gains a rule for it (and the
// corpus a row), or the kind is declared as one that is never derived from text.
func TestCorpus_CoversEveryKind(t *testing.T) {
	corpus := loadTerminalKindCorpus(t)
	tb := Load()

	src, err := os.ReadFile(filepath.Join("..", "orchestrator", "failure_handler.go"))
	if err != nil {
		t.Fatalf("read failure_handler.go: %v", err)
	}
	declared := map[string]bool{}
	for _, m := range terminalKindConstRe.FindAllStringSubmatch(string(src), -1) {
		declared[m[1]] = true
	}
	if len(declared) == 0 {
		t.Fatal("found no TerminalKind* constants — the extraction regex has drifted from the source")
	}

	exempt := map[string]bool{}
	for _, k := range tb.KindsWithoutRules {
		if !declared[k] {
			t.Errorf("kinds_without_rules lists %q, which is not a declared TerminalKind constant", k)
		}
		exempt[k] = true
	}

	ruleKinds := map[string]bool{}
	for _, r := range tb.Rules {
		if !declared[r.Kind] {
			t.Errorf("rule %q produces %q, which is not a declared TerminalKind constant", r.ID, r.Kind)
		}
		if exempt[r.Kind] {
			t.Errorf("kind %q is listed in kinds_without_rules but rule %q produces it", r.Kind, r.ID)
		}
		ruleKinds[r.Kind] = true
	}

	covered := map[string]bool{}
	for _, tc := range corpus.Cases {
		if tc.Expected == "" {
			continue
		}
		if !declared[tc.Expected] {
			t.Errorf("case %q expects %q, which is not a declared TerminalKind constant", tc.ID, tc.Expected)
		}
		covered[tc.Expected] = true
	}

	var missing []string
	for kind := range declared {
		if !covered[kind] && !exempt[kind] {
			missing = append(missing, kind)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("terminal kinds with no corpus row: %v\n"+
			"Add a rule to table.json and a row to %s/corpus.json, or list the kind in "+
			"kinds_without_rules if it is set structurally and never derived from error text.",
			missing, corpusDir)
	}
}

// TestCorpus_CapturedRowsAreEvidence ties #166's evidence rule to something
// checkable: a row may claim to be real telemetry only if it appears verbatim in
// the committed capture output, and a captured shape may not be quietly dropped
// from the corpus.
//
// Be precise about the strength of this. captured-shapes.json is a tracked,
// generated file with no checksum, and the capture script cannot run in CI (it
// needs the operator's local workspace roots), so this does not PROVE the script
// emitted a string — appending to that file would satisfy it. What it buys is
// that the evidence and its use move together in one reviewable diff. It is a
// review gate, not a signature.
func TestCorpus_CapturedRowsAreEvidence(t *testing.T) {
	corpus := loadTerminalKindCorpus(t)
	shapes := loadCapturedShapes(t)

	captured := map[string]bool{}
	for _, s := range shapes.Shapes {
		captured[s.Text] = true
	}

	inCorpus := map[string]bool{}
	capturedRows := 0
	for _, tc := range corpus.Cases {
		switch tc.Source {
		case "captured":
			capturedRows++
			inCorpus[tc.Input] = true
			if !captured[tc.Input] {
				t.Errorf("case %q is marked source=captured but its input is not in captured-shapes.json.\n"+
					"Real shapes come from scripts/capture-terminal-kind-fixture.sh; a hand-authored string "+
					"belongs to source=synthetic with a `producer` naming the emitting call site.", tc.ID)
			}
		case "synthetic":
			if tc.Producer == "" {
				t.Errorf("case %q is synthetic and must name the `producer` it was modelled on", tc.ID)
			}
		default:
			t.Errorf("case %q has source %q; expected \"captured\" or \"synthetic\"", tc.ID, tc.Source)
		}
	}

	for _, s := range shapes.Shapes {
		if !inCorpus[s.Text] {
			t.Errorf("captured shape has no corpus row (%d occurrences): %q\n"+
				"A newly observed real failure shape must be classified deliberately, not ignored.",
				s.Occurrences, s.Text)
		}
	}

	if capturedRows == 0 {
		t.Error("the corpus contains no captured rows — the core matrix must be real shapes (#166)")
	}
}

// TestCorpus_RowsAreWellFormed keeps the corpus arguable: every expectation
// carries a reason, so changing one means changing an argument.
func TestCorpus_RowsAreWellFormed(t *testing.T) {
	corpus := loadTerminalKindCorpus(t)
	tb := Load()

	seen := map[string]bool{}
	extensionRows := map[string]int{}
	sawDefault := false
	for _, tc := range corpus.Cases {
		if tc.ID == "" {
			t.Fatalf("a case has no id: %q", tc.Input)
		}
		if seen[tc.ID] {
			t.Errorf("duplicate case id %q", tc.ID)
		}
		seen[tc.ID] = true

		// Long enough that "n/a" and "see above" do not pass. The rationale is
		// the artifact a future edit has to argue with.
		if len(strings.TrimSpace(tc.Rationale)) < 60 {
			t.Errorf("case %q needs a rationale explaining WHY this input must classify as %q "+
				"(got %d chars)", tc.ID, tc.Expected, len(strings.TrimSpace(tc.Rationale)))
		}

		if tc.ExpectedSignal != "" && tc.ExpectedSignal != tc.Expected {
			// The ONLY legal way for the two to differ: a declared signal
			// extension claimed the input. Anything else is a row asserting
			// something the projection cannot produce.
			e, ok := tb.MatchSignalExtension(tc.Input)
			switch {
			case !ok:
				t.Errorf("case %q declares expected_signal %q against expected %q, and no declared "+
					"signal extension matches its input. The signal side projects the SAME winning "+
					"rule, so outside signal_extensions the two can only differ by the signal being "+
					"empty.", tc.ID, tc.ExpectedSignal, tc.Expected)
			case e.Kind != tc.ExpectedSignal:
				t.Errorf("case %q declares expected_signal %q, but the signal extension that claims "+
					"its input (%q) produces %q", tc.ID, tc.ExpectedSignal, e.ID, e.Kind)
			default:
				extensionRows[e.ID]++
			}
		}

		if tc.Expected == "" {
			sawDefault = true
		}
	}

	if !sawDefault {
		t.Error("the corpus must pin the unknown/default case (expected \"\") — " +
			"it is what every unmatched failure in production falls back to")
	}

	// Every declared extension must be EXERCISED by a row that shows the
	// divergence, not merely permitted by the rule above. Without this, deleting
	// the extension would only shrink the golden.
	for _, e := range tb.SignalExtensions {
		if extensionRows[e.ID] == 0 {
			t.Errorf("signal extension %q has no corpus row where expected_signal differs from "+
				"expected. The divergence it exists to create is unpinned, so deleting it would "+
				"change no expectation anywhere.", e.ID)
		}
	}
}

// extensionSourceOf reports the id of the declared signal extension that claims
// errorText, or "" when none does.
func extensionSourceOf(tb *Table, errorText string) string {
	if e, ok := tb.MatchSignalExtension(errorText); ok {
		return e.ID
	}
	return ""
}
