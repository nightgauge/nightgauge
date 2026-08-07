package terminalkind

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// GeneratedTSPath is the SDK module rendered from table.json, relative to the
// repository root. The extension consumes it through `@nightgauge/sdk` rather
// than reading across package boundaries at runtime.
const GeneratedTSPath = "packages/nightgauge-sdk/src/analysis/health/terminalKindTable.generated.ts"

// StressGoldenPath is the generated behaviour snapshot, relative to the
// repository root.
const StressGoldenPath = "internal/terminalkind/testdata/stress-golden.json"

// RenderTypeScript emits the SDK's generated table module.
//
// The payload is re-serialized from the PARSED table rather than copied from
// the source bytes, so the output depends only on the table's content:
// reformatting table.json (prettier, key order, indentation) cannot produce a
// spurious drift failure, while any change to a rule, a clause or the order
// does.
func RenderTypeScript(tb *Table) ([]byte, error) {
	payload, err := marshalIndentTable(tb)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	fmt.Fprint(&b, `/**
 * terminalKindTable.generated.ts — GENERATED. DO NOT EDIT.
 *
 * Source:    internal/terminalkind/table.json
 * Generator: cmd/terminalkind-codegen (`+"`make generate-terminal-kind-table`"+`)
 *
 * This is the canonical terminal-kind rule table (#306), the single definition
 * of how a failed stage's error text becomes a `+"`terminal_kind`"+`. Go embeds the
 * same file; this module is how TypeScript sees it. Hand-editing it is caught
 * by the drift check wired into .husky/pre-commit, scripts/ci-local.sh and
 * TestGeneratedTypeScriptIsInSync — the way to change classification is to edit
 * table.json and regenerate, which updates every consumer at once.
 *
 * See failureClassifier.ts for the interpreter and
 * internal/terminalkind/table.go for its Go twin.
 */

/** A named check the table references as a `+"`@name`"+` term because it cannot be a literal. */
export interface TerminalKindPredicateDoc {
  name: string;
  description: string;
  go: string;
  ts: string;
  why_both_sides_agree: string;
  /** Strings the predicate MUST accept — asserted in Go and in TypeScript. */
  probes_true: string[];
  /** Strings the predicate MUST reject — asserted in Go and in TypeScript. */
  probes_false: string[];
}

/** A term that can never be satisfied, preserved verbatim with its reason. */
export interface TerminalKindDeadTerm {
  term: string;
  rule: string;
  why: string;
}

/** One rung of the ladder. Clauses are OR-ed; the terms of a clause are AND-ed. */
export interface TerminalKindRule {
  id: string;
  kind: string;
  /** Whether the extension may forward this rule's kind to Go over IPC. */
  signal: boolean;
  clauses: string[][];
  why: string;
}

export interface TerminalKindTable {
  $comment: string[];
  schema_version: number;
  predicates: TerminalKindPredicateDoc[];
  dead_terms: TerminalKindDeadTerm[];
  kinds_without_rules: string[];
  rules: TerminalKindRule[];
}

/** The prefix marking a term as a named-predicate reference. */
export const TERMINAL_KIND_PREDICATE_REF = "@";

export const TERMINAL_KIND_TABLE: TerminalKindTable = `)
	b.Write(payload)
	fmt.Fprint(&b, ";\n")
	return b.Bytes(), nil
}

func marshalIndentTable(tb *Table) ([]byte, error) {
	b, err := json.MarshalIndent(tb, "", "  ")
	if err != nil {
		return nil, err
	}
	return b, nil
}

// RenderStressGolden emits the generated behaviour snapshot.
func RenderStressGolden(tb *Table) ([]byte, error) {
	b, err := json.MarshalIndent(BuildStressGolden(tb), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
