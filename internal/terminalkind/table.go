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

// Table is the parsed canonical rule table.
type Table struct {
	Comment           []string    `json:"$comment"`
	SchemaVersion     int         `json:"schema_version"`
	Predicates        []Predicate `json:"predicates"`
	DeadTerms         []DeadTerm  `json:"dead_terms"`
	KindsWithoutRules []string    `json:"kinds_without_rules"`
	Rules             []Rule      `json:"rules"`
}

// PredicateRef is the prefix that marks a term as a named-predicate reference
// rather than a literal. No literal in the table may start with it — the
// schema lint enforces that, so the two term kinds can never be confused.
const PredicateRef = "@"

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
	if tb.SchemaVersion != 1 {
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
		if len(r.Clauses) == 0 {
			return nil, fmt.Errorf("rule %q has no clauses", r.ID)
		}
		for _, clause := range r.Clauses {
			if len(clause) == 0 {
				return nil, fmt.Errorf("rule %q has an empty clause", r.ID)
			}
			for _, term := range clause {
				if term == "" {
					return nil, fmt.Errorf("rule %q has an empty term", r.ID)
				}
				if name, ok := strings.CutPrefix(term, PredicateRef); ok {
					if _, known := predicates[name]; !known {
						return nil, fmt.Errorf("rule %q references unknown predicate %q", r.ID, name)
					}
				}
			}
		}
	}
	for _, p := range tb.Predicates {
		if _, ok := predicates[p.Name]; !ok {
			return nil, fmt.Errorf("table declares predicate %q with no Go implementation", p.Name)
		}
	}
	return &tb, nil
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
		if !strings.Contains(lowered, term) {
			return false
		}
	}
	return true
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

// SignalKind returns the kind the extension may forward to the Go scheduler
// over IPC, or "" to defer.
//
// It runs the FULL ladder and then answers only when the WINNING rule is
// declared `signal: true`. That is what makes the signal side incapable of
// disagreeing with the record: its answer is either "" or exactly Classify's
// answer — bounded above (it can never name a different kind) and below (when
// the winning rule is in the declared subset it MUST answer). Skipping
// non-signal rules instead would reintroduce disagreement, because a
// lower-precedence signal rule could then claim text that a higher-precedence
// non-signal rule owns.
func SignalKind(errorText string) string {
	if r, ok := Load().Match(errorText); ok && r.Signal {
		return r.Kind
	}
	return ""
}
