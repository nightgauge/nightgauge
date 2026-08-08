// Package terminalkind owns terminal-kind classification: turning a failed
// stage's error text into the `terminal_kind` that drives the scheduler's
// recovery routing.
//
// THE LADDER IS DATA. table.json is the single, canonical, ordered rule table;
// this package is a thin interpreter over it and holds no matching literals of
// its own. Every other consumer interprets the SAME table:
//
//	Go   orchestrator.ClassifyTerminalKind delegates to Classify below.
//	SDK  packages/nightgauge-sdk/src/analysis/health/terminalKindTable.generated.ts
//	     is generated from table.json (`make generate-terminal-kind-table`) and
//	     interpreted by classifyTerminalKind.
//	Ext  packages/nightgauge-vscode/src/services/terminalKindSignal.ts runs the
//	     same ladder and answers only for rules marked `signal: true`.
//
// Before #306 those were three hand-written ladders held aligned by comments,
// and they disagreed on 19 of 98 real and synthetic inputs. A ladder that is
// data cannot drift, because there is only one of it.
//
// TWO QUALIFICATIONS ON "THE SIGNAL CANNOT DISAGREE WITH THE RECORD", both
// stated here because the bound is quoted elsewhere and an unqualified version
// of it would be false:
//
//   - The table declares `signal_extensions`: reaction-only rules, consulted
//     only when the rule table itself projects no signal. They are the ONE
//     deliberate record-vs-reaction divergence, each carrying its reason and
//     pinned by corpus rows. See SignalExtension and SignalKind.
//   - The bound is per-language and assumes ASCII matching text. Go folds with
//     strings.ToLower and TypeScript with String.prototype.toLowerCase; the two
//     disagree on 56 code points, and on U+0130 the fold changes length, which
//     can split an ASCII literal in one language and not the other. Every term
//     in the table is ASCII and no producer in this repo emits
//     Turkish-locale-uppercased failure text, so this is a stated assumption
//     rather than an observed defect.
package terminalkind

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed table.json
var tableJSON []byte

// RawTableJSON returns the canonical table bytes exactly as embedded. Used by
// the TypeScript code generator and by the drift check that keeps the
// generated module byte-identical to this file.
func RawTableJSON() []byte {
	out := make([]byte, len(tableJSON))
	copy(out, tableJSON)
	return out
}

// Predicate documents a named predicate the table may reference as a `@name`
// term. The implementations live in predicates.go (Go) and in the SDK
// classifier (TypeScript); Probes pin them against each other.
type Predicate struct {
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Go                string   `json:"go"`
	TS                string   `json:"ts"`
	WhyBothSidesAgree string   `json:"why_both_sides_agree"`
	ProbesTrue        []string `json:"probes_true"`
	ProbesFalse       []string `json:"probes_false"`
}

// DeadTerm declares a term that can never be satisfied, with the reason it is
// nevertheless preserved. The schema lint requires every term carrying an
// uppercase character to be declared here, because terms are matched against
// lowercased text.
type DeadTerm struct {
	Term string `json:"term"`
	Rule string `json:"rule"`
	Why  string `json:"why"`
}

// Rule is one rung of the ladder: a kind, the disjunction of conjunctions that
// selects it, and the reason it sits where it sits.
type Rule struct {
	ID      string     `json:"id"`
	Kind    string     `json:"kind"`
	Signal  bool       `json:"signal"`
	Clauses [][]string `json:"clauses"`
	Why     string     `json:"why"`
}

// SignalExtension is a rule the REACTION path has and the RECORD path does not.
//
// It is the one declared way the kind the fleet reacts to may differ from the
// kind the run record carries, and it exists because deleting the difference
// silently was itself a regression: the pre-#306 extension ladder mapped a bare
// Anthropic session/usage-limit line to rate_limit_quota_exhausted (#3792) as
// defense-in-depth for the non-stream-json paths, where the record ladder
// answers nothing at all and the run would otherwise book a lifetime failure
// and a cascade strike for an upstream quota window that clears on its own.
//
// THE BOUND, STATED EXACTLY. Extensions are consulted ONLY when the rule table
// projects no signal of its own (see SignalKind), so an extension can never
// overrule a kind projected by a `signal: true` RULE — the widest it can reach
// is text the signal SUBSET ignores.
//
// That is narrower than "can never overrule a kind the record names", and the
// difference is shipped behaviour rather than a hypothetical: when the winning
// rule is NOT in the signal subset the record still names a kind, and the
// extension may name a different one on top of it. `Claude Opus 4.5 usage limit
// reached; resets at 5pm` records model_unavailable (a plan restriction) and
// reacts rate_limit_quota_exhausted (an environmental window) — main's exact
// behaviour, pinned by the corpus rows order-model-unavailable-beats-quota-
// wording and model-unavailable-predicate-by-model-id.
//
// Every extension is pinned by corpus rows whose expected_signal deliberately
// differs from expected, which the corpus well-formedness test permits for
// declared extensions and for nothing else, and every CLAUSE of every extension
// must be necessary to some row (TestEveryExtensionClauseIsPinnedByACorpusRow).
type SignalExtension struct {
	ID      string     `json:"id"`
	Kind    string     `json:"kind"`
	Clauses [][]string `json:"clauses"`
	Why     string     `json:"why"`
}

// Table is the parsed canonical rule table.
type Table struct {
	Comment           []string          `json:"$comment"`
	SchemaVersion     int               `json:"schema_version"`
	Predicates        []Predicate       `json:"predicates"`
	DeadTerms         []DeadTerm        `json:"dead_terms"`
	KindsWithoutRules []string          `json:"kinds_without_rules"`
	Rules             []Rule            `json:"rules"`
	SignalExtensions  []SignalExtension `json:"signal_extensions"`
}

// PredicateRef is the prefix that marks a term as a named-predicate reference
// rather than a literal. No literal in the table may start with it — the
// schema lint enforces that, so the term kinds can never be confused.
const PredicateRef = "@"

// WordBoundaryRef marks a term as a WORD-BOUNDED literal: satisfied only when
// the text contains it with a non-word character (or nothing) on each side.
//
// It exists for exactly one reason, and the reason is behaviour preservation.
// The pre-#306 extension ladder matched the session/usage-limit wording with
// `/\b(?:session|usage)\s+limit\b/i`, and plain containment is a strictly wider
// test: `usage limits`, `usage limited` and `session limits` all contain
// `usage limit`/`session limit` while the regex rejects them. That widening is
// not free — the kind it produces triggers a GLOBAL quota cooldown — so the
// restored rule keeps the boundary the original had.
//
// A word character is [0-9a-z_] against the already-lowercased text. Anything
// else, including any byte outside ASCII, is a boundary; the TypeScript twin
// makes the same test on UTF-16 code units and agrees for the same reason.
//
// ONE DISCLOSED NARROWING against that regex, and it is the separator rather
// than the boundary: `\s+` is one-or-more whitespace, while the term `~usage
// limit` is a literal and requires EXACTLY ONE SPACE. `usage  limit`,
// `usage\tlimit` and `usage\nlimit` matched on main and do not match here. The
// shape is reachable — skillRunner's extractTailError joins the last three
// non-empty lines with "\n", so a phrase split across lines arrives as
// `usage\nlimit` — and the loss is one-directional (the reaction goes silent and
// the run books a crash for a quota window). It is not closed here because a
// whitespace-run term kind is a second matching semantic that both interpreters
// would have to reproduce character-for-character; it is pinned instead by the
// corpus row boundary-negative-usage-limit-double-space, so the current answer
// is a written expectation rather than an accident.
const WordBoundaryRef = "~"

var load = sync.OnceValue(func() *Table {
	tb, err := Parse(tableJSON)
	if err != nil {
		// The table is embedded at build time, so this is a build artifact
		// defect, not a runtime condition — the same contract as
		// regexp.MustCompile. TestTableIsWellFormed catches it before merge.
		panic(fmt.Sprintf("terminalkind: embedded table.json is invalid: %v", err))
	}
	return tb
})

// Load returns the embedded canonical table.
func Load() *Table { return load() }

// Parse decodes and validates a table. Validation is deliberately strict:
// an unknown predicate reference must fail loudly rather than silently
// evaluate to false, which would disable a rule with no visible symptom.
func Parse(b []byte) (*Table, error) {
	var tb Table
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tb); err != nil {
		return nil, err
	}
	if tb.SchemaVersion != 2 {
		return nil, fmt.Errorf("unsupported schema_version %d", tb.SchemaVersion)
	}
	if len(tb.Rules) == 0 {
		return nil, fmt.Errorf("table has no rules")
	}
	seen := map[string]bool{}
	for _, r := range tb.Rules {
		if r.ID == "" || r.Kind == "" {
			return nil, fmt.Errorf("rule with empty id or kind")
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
		if err := validateClauses(r.ID, r.Clauses); err != nil {
			return nil, err
		}
	}
	for _, e := range tb.SignalExtensions {
		if e.ID == "" || e.Kind == "" {
			return nil, fmt.Errorf("signal extension with empty id or kind")
		}
		if seen[e.ID] {
			return nil, fmt.Errorf("duplicate rule id %q", e.ID)
		}
		seen[e.ID] = true
		if err := validateClauses(e.ID, e.Clauses); err != nil {
			return nil, err
		}
	}
	for _, p := range tb.Predicates {
		if _, ok := predicates[p.Name]; !ok {
			return nil, fmt.Errorf("table declares predicate %q with no Go implementation", p.Name)
		}
	}
	return &tb, nil
}

func validateClauses(id string, clauses [][]string) error {
	if len(clauses) == 0 {
		return fmt.Errorf("rule %q has no clauses", id)
	}
	for _, clause := range clauses {
		if len(clause) == 0 {
			return fmt.Errorf("rule %q has an empty clause", id)
		}
		for _, term := range clause {
			if term == "" {
				return fmt.Errorf("rule %q has an empty term", id)
			}
			if name, ok := strings.CutPrefix(term, PredicateRef); ok {
				if _, known := predicates[name]; !known {
					return fmt.Errorf("rule %q references unknown predicate %q", id, name)
				}
				continue
			}
			if lit, ok := strings.CutPrefix(term, WordBoundaryRef); ok && lit == "" {
				return fmt.Errorf("rule %q has an empty term", id)
			}
		}
	}
	return nil
}

// Match returns the first rule whose clauses are satisfied by errorText, in
// table order. This is the ONLY matching implementation in Go — everything
// else in this package projects its result.
func (tb *Table) Match(errorText string) (Rule, bool) {
	if errorText == "" {
		return Rule{}, false
	}
	t := strings.ToLower(errorText)
	for _, r := range tb.Rules {
		for _, clause := range r.Clauses {
			if satisfied(t, clause) {
				return r, true
			}
		}
	}
	return Rule{}, false
}

func satisfied(lowered string, clause []string) bool {
	for _, term := range clause {
		if name, ok := strings.CutPrefix(term, PredicateRef); ok {
			if !predicates[name](lowered) {
				return false
			}
			continue
		}
		if lit, ok := strings.CutPrefix(term, WordBoundaryRef); ok {
			if !containsWordBounded(lowered, lit) {
				return false
			}
			continue
		}
		if !strings.Contains(lowered, term) {
			return false
		}
	}
	return true
}

// containsWordBounded reports whether lowered contains lit with a non-word
// character (or a string edge) on both sides. The TypeScript twin in
// terminalKind.ts is character-for-character the same test.
func containsWordBounded(lowered, lit string) bool {
	for from := 0; from <= len(lowered)-len(lit); {
		i := strings.Index(lowered[from:], lit)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(lit)
		if !isWordByte(lowered, i-1) && !isWordByte(lowered, end) {
			return true
		}
		from = i + 1
	}
	return false
}

func isWordByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')
}

// RuleCostCapExceeded is the rule that the per-stage cost-cap circuit breaker
// stamps (#3002). It is named here because two recovery paths ask "did the cost
// cap kill this?" INDEPENDENTLY of what the run was finally classified as — a
// question about one rule, not about the ladder's answer.
const RuleCostCapExceeded = "cost-cap-exceeded"

// RuleFires reports whether the named rule's clauses are satisfied by
// errorText, IGNORING precedence — i.e. whether that rule would fire if it were
// consulted alone.
//
// This is not the classifier: Classify answers "what is this failure", and a
// higher rule can legitimately claim text this rule also matches. RuleFires
// answers the narrower question some recovery paths actually ask, and it exists
// so they can ask it of the table instead of keeping their own copy of the
// literals. Two Go call sites did exactly that before #306, one of them with a
// comment saying it was "duplicated here to keep the recovery package free of a
// reverse import on orchestrator" — a reason that no longer applies now the
// rules live in a leaf package.
//
// An unknown id panics: a silent false would disable a recovery path with no
// symptom, which is the failure mode this whole change exists to remove.
func RuleFires(ruleID, errorText string) bool {
	if errorText == "" {
		return false
	}
	t := strings.ToLower(errorText)
	for _, r := range Load().Rules {
		if r.ID != ruleID {
			continue
		}
		for _, clause := range r.Clauses {
			if satisfied(t, clause) {
				return true
			}
		}
		return false
	}
	panic(fmt.Sprintf("terminalkind: no rule with id %q", ruleID))
}

// Classify returns the terminal kind for errorText, or "" when no rule
// matches. Callers fall back to the most generic kind (subagent_crash).
func Classify(errorText string) string {
	if r, ok := Load().Match(errorText); ok {
		return r.Kind
	}
	return ""
}

// MatchSignalExtension returns the first declared signal extension satisfied by
// errorText. It is the reaction path's second stage and is never consulted by
// Classify.
func (tb *Table) MatchSignalExtension(errorText string) (SignalExtension, bool) {
	if errorText == "" {
		return SignalExtension{}, false
	}
	t := strings.ToLower(errorText)
	for _, e := range tb.SignalExtensions {
		for _, clause := range e.Clauses {
			if satisfied(t, clause) {
				return e, true
			}
		}
	}
	return SignalExtension{}, false
}

// SignalKind returns the kind the extension may forward to the Go scheduler
// over IPC, or "" to defer.
//
// TWO STAGES, IN THIS ORDER.
//
// First the FULL rule ladder runs and the answer is the WINNING rule's kind
// only when that rule is declared `signal: true`. That is what makes the signal
// side incapable of disagreeing with a record kind it can see: its answer is
// either "" or exactly Classify's answer — bounded above (it can never name a
// different kind than the winner) and below (when the winner is in the declared
// subset it MUST answer). Skipping non-signal rules instead would reintroduce
// disagreement, because a lower-precedence signal rule could then claim text
// that a higher-precedence non-signal rule owns.
//
// Only if that yields nothing are the declared signal_extensions consulted.
// They are the ONE deliberate record-vs-reaction divergence in the system,
// declared as data with their reason (see SignalExtension), and their placement
// after the projection is what bounds them: an extension can only claim text
// the signal subset already ignores. Note what that does and does not say — a
// kind the record names through a NON-signal rule is not protected, and the one
// declared extension deliberately overrules exactly that case.
func SignalKind(errorText string) string { return Load().SignalKind(errorText) }

// SignalKind on a parsed table is the same projection, on a table the caller
// supplies. The derived guards in corpus_test.go mutate a copy of the table and
// re-ask this question, so the projection they probe is the shipped one rather
// than a re-implementation that could drift away from it.
func (tb *Table) SignalKind(errorText string) string {
	if r, ok := tb.Match(errorText); ok && r.Signal {
		return r.Kind
	}
	if e, ok := tb.MatchSignalExtension(errorText); ok {
		return e.Kind
	}
	return ""
}
