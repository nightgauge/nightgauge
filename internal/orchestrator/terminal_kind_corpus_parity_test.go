package orchestrator

// Terminal-kind classification parity — Go side (#306).
//
// Terminal-kind classification exists three times (Go here, the SDK's
// classifyTerminalKind, the extension's classifyTerminalKindForSignal) and each
// copy decides how the fleet reacts to a failure. Before this suite they were
// held aligned by comments, so a pattern added to one ladder silently diverged
// the others: the same run could be recorded as one kind and reacted to as
// another, with both sides individually green.
//
// This suite pins THIS side against the shared corpus in
// testdata/terminal-kind/corpus.json. Its twins read the same file:
//
//	packages/nightgauge-sdk/tests/analysis/health/failureClassifier.corpusParity.test.ts
//	packages/nightgauge-vscode/tests/services/terminalKindSignal.corpusParity.test.ts
//
// The corpus holds GO's answer, because Go writes the run record. Editing a
// matcher here therefore fails this suite until the corpus is updated — and
// updating the corpus means editing the `rationale` that argues for the old
// answer, which is the review moment this exists to force.
//
// Complements failureClassifier.parity.test.ts (#229), which diffs the kind
// VOCABULARY across the two languages. Names were in sync while behaviour had
// drifted; this suite covers behaviour.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type corpusDivergence struct {
	SDK     string `json:"sdk"`
	Signal  string `json:"signal"`
	Why     string `json:"why"`
	Tracked string `json:"tracked"`
}

type corpusCase struct {
	ID              string            `json:"id"`
	Input           string            `json:"input"`
	Expected        string            `json:"expected"`
	Source          string            `json:"source"`
	Producer        string            `json:"producer"`
	Rationale       string            `json:"rationale"`
	KnownDivergence *corpusDivergence `json:"known_divergence"`
}

type terminalKindCorpus struct {
	NoMatcherKinds []string     `json:"no_matcher_kinds"`
	Cases          []corpusCase `json:"cases"`
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

const corpusDir = "testdata/terminal-kind"

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

// TestTerminalKindCorpus_GoClassifier is the parity assertion itself: every
// corpus input must classify to the kind the corpus pins.
func TestTerminalKindCorpus_GoClassifier(t *testing.T) {
	corpus := loadTerminalKindCorpus(t)
	for _, tc := range corpus.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			got := ClassifyTerminalKind(tc.Input)
			if got != tc.Expected {
				t.Errorf("ClassifyTerminalKind(%q)\n  got:      %q\n  expected: %q\n  why this row exists: %s",
					tc.Input, got, tc.Expected, tc.Rationale)
			}
		})
	}
}

// TestTerminalKindCorpus_ResolveHonoursGateKind pins the one place a corpus
// expectation is legitimately overridden: a gate that ran and reported its own
// structured kind wins over prose classification.
func TestTerminalKindCorpus_ResolveHonoursGateKind(t *testing.T) {
	corpus := loadTerminalKindCorpus(t)
	for _, tc := range corpus.Cases {
		if got := ResolveTerminalKind(false, "", tc.Input); got != tc.Expected {
			t.Errorf("%s: ResolveTerminalKind(no gate) = %q, want %q", tc.ID, got, tc.Expected)
		}
		if got := ResolveTerminalKind(true, TerminalKindAbandonedCommit, tc.Input); got != TerminalKindAbandonedCommit {
			t.Errorf("%s: a gate-sourced kind must win over text classification, got %q", tc.ID, got)
		}
	}
}

var terminalKindConstRe = regexp.MustCompile(`(?m)^\s*TerminalKind\w+\s*=\s*"([a-z_]+)"`)

// TestTerminalKindCorpus_CoversEveryKind is what makes the guard real rather
// than aspirational: adding a kind to failure_handler.go fails here until the
// corpus gains a row for it — and that row then fails the SDK and signal suites
// until those ladders learn it too.
func TestTerminalKindCorpus_CoversEveryKind(t *testing.T) {
	corpus := loadTerminalKindCorpus(t)

	src, err := os.ReadFile("failure_handler.go")
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
	for _, k := range corpus.NoMatcherKinds {
		if !declared[k] {
			t.Errorf("no_matcher_kinds lists %q, which is not a declared TerminalKind constant", k)
		}
		exempt[k] = true
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
			"Add a row (with a rationale) to %s/corpus.json, or list the kind in no_matcher_kinds "+
			"if it is set structurally and never derived from error text.", missing, corpusDir)
	}

	for _, k := range corpus.NoMatcherKinds {
		if covered[k] {
			t.Errorf("kind %q is listed in no_matcher_kinds but a corpus row expects it from error text", k)
		}
	}
}

var classifierLiteralRe = regexp.MustCompile(`strings\.Contains\(t,\s*"((?:[^"\\]|\\.)*)"\)`)

// unexercisableLiterals names every matcher literal that CANNOT appear in a
// corpus input, with the reason. It is an allowlist of exactly one entry, and
// the test below fails if a listed literal disappears from the source or turns
// out to be exercisable after all — so this stays a documented exception rather
// than a place to park inconvenient coverage gaps.
var unexercisableLiterals = map[string]string{
	"exitSignalSource": "ClassifyTerminalKind lowercases its input before matching, so a " +
		"literal carrying capitals can never match: this branch is dead. It is deliberately " +
		"NOT mirrored in the SDK, and corpus row " +
		"`runaway-progress-exit-signal-source-dead-branch` pins the kind the dead branch " +
		"leaves behind.",
}

// classifierMatcherLiterals returns every `strings.Contains(t, "…")` literal in
// the two functions that make up the classifier, in source order, deduped.
func classifierMatcherLiterals(t *testing.T, src string) []string {
	t.Helper()
	body := goFuncBody(t, src, "ClassifyTerminalKind") + goFuncBody(t, src, "isModelUnavailableText")
	var out []string
	seen := map[string]bool{}
	for _, m := range classifierLiteralRe.FindAllStringSubmatch(body, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// goFuncBody returns the brace-balanced body of a top-level Go function.
func goFuncBody(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "func "+name+"(")
	if start < 0 {
		t.Fatalf("func %s not found — the classifier was renamed and this guard now checks nothing", name)
	}
	open := strings.Index(src[start:], "{")
	if open < 0 {
		t.Fatalf("func %s has no body", name)
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
				return src[open : i+1]
			}
		}
	}
	t.Fatalf("func %s body is unbalanced", name)
	return ""
}

// TestTerminalKindCorpus_ExercisesEveryMatcherLiteral is the PATTERN-level half
// of the guard. CoversEveryKind above pins the kind VOCABULARY, but adding or
// deleting a pattern ALTERNATIVE introduces no new kind — so before this test a
// one-sided edit to any of the ~114 matcher literals passed all three suites.
// That was not theoretical: 39 of them had no corpus input at all, including
// the canonical stall markers (`exceeded stage_hard_cap`, `hard cap`), the
// whole `push rejected` + `fetch first` conjunction, and every underscore kind
// alias.
//
// The literals are read out of the source rather than listed here, so a new
// alternative is covered by this guard the moment it is written.
func TestTerminalKindCorpus_ExercisesEveryMatcherLiteral(t *testing.T) {
	corpus := loadTerminalKindCorpus(t)

	src, err := os.ReadFile("failure_handler.go")
	if err != nil {
		t.Fatalf("read failure_handler.go: %v", err)
	}
	literals := classifierMatcherLiterals(t, string(src))
	if len(literals) < 80 {
		t.Fatalf("extracted only %d matcher literals — the extraction regex has drifted from the source", len(literals))
	}

	inputs := make([]string, 0, len(corpus.Cases))
	for _, tc := range corpus.Cases {
		inputs = append(inputs, strings.ToLower(tc.Input))
	}

	present := map[string]bool{}
	for _, lit := range literals {
		present[lit] = true
	}
	for lit, why := range unexercisableLiterals {
		if !present[lit] {
			t.Errorf("unexercisableLiterals lists %q, which no longer appears in the classifier.\n"+
				"Delete the exception — it now excuses nothing. Recorded reason: %s", lit, why)
		}
	}

	var uncovered []string
	for _, lit := range literals {
		if why, exempt := unexercisableLiterals[lit]; exempt {
			// Guard the exception itself: it is only legitimate while the
			// literal genuinely cannot match lowercased text.
			if lit == strings.ToLower(lit) {
				t.Errorf("literal %q is listed as unexercisable but is all-lowercase, so it CAN "+
					"match. Give it a corpus row instead. Recorded reason: %s", lit, why)
			}
			continue
		}
		covered := false
		for _, in := range inputs {
			if strings.Contains(in, lit) {
				covered = true
				break
			}
		}
		if !covered {
			uncovered = append(uncovered, lit)
		}
	}

	if len(uncovered) > 0 {
		t.Errorf("matcher literals that no corpus input exercises (%d of %d):\n  %s\n\n"+
			"Each one can be deleted from either ladder today with every suite green. Add a row "+
			"to %s/corpus.json whose input contains the literal (with a rationale saying what "+
			"the literal is for), or — if it genuinely cannot fire — add it to "+
			"unexercisableLiterals with the reason.",
			len(uncovered), len(literals), strings.Join(uncovered, "\n  "), corpusDir)
	}
}

// TestTerminalKindCorpus_CapturedRowsAreEvidence ties #166's evidence rule to
// something checkable: a row may claim to be real telemetry only if it appears
// verbatim in the committed capture output, and a captured shape may not be
// quietly dropped from the corpus.
//
// Be precise about the strength of this. captured-shapes.json is a tracked,
// generated file with no checksum, and the capture script cannot run in CI (it
// needs the operator's local workspace roots), so this does not PROVE the
// script emitted a string — appending to that file would satisfy it. What it
// buys is that the evidence and its use move together in one reviewable diff:
// promoting a hand-authored string to `captured` requires editing the evidence
// file next to the row that claims it, where a reviewer sees both. It is a
// review gate, not a signature.
func TestTerminalKindCorpus_CapturedRowsAreEvidence(t *testing.T) {
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

// TestTerminalKindCorpus_RowsAreWellFormed keeps the corpus arguable: every
// expectation carries a reason, so changing one means changing an argument.
func TestTerminalKindCorpus_RowsAreWellFormed(t *testing.T) {
	corpus := loadTerminalKindCorpus(t)

	seen := map[string]bool{}
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

		if tc.Expected == "" {
			sawDefault = true
		}

		if d := tc.KnownDivergence; d != nil {
			if d.SDK == "" && d.Signal == "" {
				t.Errorf("case %q declares known_divergence with no diverging side", tc.ID)
			}
			if d.Why == "" || d.Tracked == "" {
				t.Errorf("case %q declares known_divergence without `why` and `tracked` — "+
					"a recorded disagreement must say what it is and where it is being resolved", tc.ID)
			}
			if d.SDK == tc.Expected || d.Signal == tc.Expected {
				t.Errorf("case %q declares a divergence that matches `expected`; delete it instead", tc.ID)
			}
		}
	}

	if !sawDefault {
		t.Error("the corpus must pin the unknown/default case (expected \"\") — " +
			"it is what every unmatched failure in production falls back to")
	}
}
