/**
 * terminalKind.ts — the TypeScript interpreter for the canonical terminal-kind
 * rule table (#306).
 *
 * THIS WHOLE FILE IS THE GUARDED SURFACE, and that is why it is a file of its
 * own rather than a region of failureClassifier.ts.
 *
 * The corpus and the derived stress set are both built FROM the table's
 * vocabulary, so neither can see a rule invented HERE for a marker the table has
 * never heard of. Round 2's evasion was exactly that. Round 3 answered it with a
 * text scan over a window — from `function matchTerminalKindRule` to
 * `const TERMINAL_KIND_PREDICATES` — and round 3's review walked around the
 * window twice: once by putting the branch in `signalTerminalKind` (below the
 * lower edge, and the highest-authority function in the system), once by
 * declaring `const DEFERRED_MARKER = "…"` one line ABOVE the upper edge and
 * referencing it from inside. Both left every suite green while the fleet
 * reacted to a kind the run record did not carry.
 *
 * So the scan is now the entire module against an explicit allowlist
 * (terminalKind.corpusParity.test.ts): every string literal in this file must be
 * one of a handful of declared, non-marker strings, and there is no line to
 * hoist to. Everything that matches an error text lives here — the matcher, the
 * predicates, the three public entry points and the stress derivation — so
 * "guarded" and "does matching" are the same set. `classifyFailureCategory`
 * stayed in failureClassifier.ts precisely because it is a different taxonomy
 * with its own literals, and mixing the two would make the allowlist meaningless.
 *
 * Both languages read one table: Go embeds internal/terminalkind/table.json and
 * terminalKindTable.generated.ts is its generated view, byte-checked against it.
 */

import { MODEL_REGISTRY } from "../../eval/modelRegistry.js";
import {
  TERMINAL_KIND_TABLE,
  TERMINAL_KIND_PREDICATE_REF,
  TERMINAL_KIND_WORD_BOUNDARY_REF,
  type TerminalKindRule,
} from "./terminalKindTable.generated.js";
import type { TerminalFailureKind } from "./failureClassifier.js";

/**
 * Diagnostics, assembled from constants so no message has to be spelled inline
 * at a throw site. Neither is a marker: nothing compares an error text against
 * them.
 */
const UNKNOWN_PREDICATE =
  "terminal-kind table references a predicate with no TypeScript implementation in terminalKind.ts: ";
const NO_PROBES_TRUE = "terminal-kind predicate declares no probes_true: ";

/** The derived-input vocabulary — the verbatim twin of stress.go's. */
const EMPTY = "";
const NO_MARKER_SENTENCE = "nothing in this sentence resembles a terminal marker";
const CRASH_PREFIX = "exit 1: ";
const PAIR_SEPARATOR = " | ";
const TERM_SEPARATOR = " ";
const BOUNDARY_VIOLATION_AFFIX = "s";

/** A rule matched against an error text, or `undefined` when nothing matched. */
function matchTerminalKindRule(errorText: string | undefined): TerminalKindRule | undefined {
  if (!errorText) return undefined;
  const t = errorText.toLowerCase();
  for (const rule of TERMINAL_KIND_TABLE.rules) {
    for (const clause of rule.clauses) {
      if (clauseSatisfied(t, clause)) return rule;
    }
  }
  return undefined;
}

function clauseSatisfied(lowered: string, clause: string[]): boolean {
  for (const term of clause) {
    if (term.startsWith(TERMINAL_KIND_PREDICATE_REF)) {
      const name = term.slice(TERMINAL_KIND_PREDICATE_REF.length);
      const predicate = TERMINAL_KIND_PREDICATES[name];
      // Fail loudly on an unknown predicate. Silently evaluating to false would
      // disable a rule with no visible symptom — exactly the class of silent
      // divergence the table exists to remove.
      if (!predicate) throw new Error(UNKNOWN_PREDICATE + name);
      if (!predicate(lowered)) return false;
      continue;
    }
    if (term.startsWith(TERMINAL_KIND_WORD_BOUNDARY_REF)) {
      if (!containsWordBounded(lowered, term.slice(TERMINAL_KIND_WORD_BOUNDARY_REF.length))) {
        return false;
      }
      continue;
    }
    if (!lowered.includes(term)) return false;
  }
  return true;
}

/**
 * Contains `lit` with a non-word character (or a string edge) on each side —
 * the twin of `containsWordBounded` in internal/terminalkind/table.go.
 *
 * The table uses it for the one rule whose pre-#306 implementation was a
 * `\b…\b` regex, so that restoring the rule restores its precision too: plain
 * containment would also fire on `usage limits` and `usage limited`, and the
 * kind it produces triggers a global quota cooldown.
 */
function containsWordBounded(lowered: string, lit: string): boolean {
  for (let from = 0; from + lit.length <= lowered.length;) {
    const i = lowered.indexOf(lit, from);
    if (i < 0) return false;
    if (!isWordChar(lowered, i - 1) && !isWordChar(lowered, i + lit.length)) return true;
    from = i + 1;
  }
  return false;
}

function isWordChar(s: string, i: number): boolean {
  if (i < 0 || i >= s.length) return false;
  const c = s.charCodeAt(i);
  return c === 95 || (c >= 48 && c <= 57) || (c >= 97 && c <= 122);
}

/**
 * Registry-derived "names a specific model" gate — IDs, display names, tiers.
 * The Go twin is `mentionsRegistryModel` in internal/terminalkind/predicates.go
 * and iterates `models.All()`; the two registries are the same file
 * (packages/nightgauge-sdk/src/eval/model-registry.json is canonical,
 * internal/models/model-registry.json is a byte copy with a Go parity test).
 *
 * It is inside the guarded module deliberately: round 3 left it outside the
 * fence, and a single `if (t.includes("…")) return true;` here silently made
 * every one of the six clauses that reference it fire for a marker the table
 * never declared.
 */
function mentionsRegistryModel(t: string): boolean {
  return mentionsAnyModel(MODEL_REGISTRY, t);
}

/**
 * The registry fields the predicate is allowed to read. Declaring the parameter
 * this narrowly makes a widening a COMPILE error — `m.provider` does not exist
 * on this type — which is the half of the fence a literal scan cannot give:
 * reading one more field needs no string literal, moves no golden row, and no
 * corpus row can see it.
 *
 * The other half is behavioural and shared with Go:
 * internal/terminalkind/testdata/predicate-registry-poison.json declares a
 * synthetic model whose every non-read field carries a unique sentinel, and
 * terminalKind.predicateFields.test.ts asserts no sentinel makes the predicate
 * fire while every declared read field does. The Go twin runs the same fixture
 * against `mentionsAnyModel` in internal/terminalkind/predicates.go, so the two
 * implementations are pinned to ONE field set.
 */
interface RegistryModelIdentity {
  readonly id?: string;
  readonly display_name?: string;
  readonly tiers?: readonly string[];
}

function mentionsAnyModel(registry: readonly RegistryModelIdentity[], t: string): boolean {
  const tiers = new Set<string>();
  for (const m of registry) {
    if (m.id && t.includes(m.id.toLowerCase())) return true;
    if (m.display_name && t.includes(m.display_name.toLowerCase())) return true;
    for (const tier of m.tiers ?? []) tiers.add(tier.toLowerCase());
  }
  for (const tier of tiers) {
    if (t.includes(tier)) return true;
  }
  return false;
}

/**
 * Named predicates the table may reference as `@name` terms.
 *
 * One entry, deliberately: a predicate exists only for a condition that cannot
 * be written as literal containment. Everything else is a literal in
 * table.json, where review and the generated module can both see it. Each
 * predicate declares probes_true / probes_false in the table, and BOTH
 * languages assert them.
 */
const TERMINAL_KIND_PREDICATES: Record<string, (lowered: string) => boolean> = {
  mentions_registry_model: mentionsRegistryModel,
};

/**
 * Classify the *kind* of terminal failure from an error message.
 *
 * Interprets the canonical table, so this returns exactly what Go's
 * `ClassifyTerminalKind` returns for the same input — not by mirroring it, but
 * by reading the same rules in the same order. `terminal_kind` in the run
 * record is Go's answer; this is how an SDK consumer reproduces it.
 *
 * Returns `undefined` when no rule matches; callers can fall back to
 * `"subagent_crash"` (the most generic kind) or leave the field absent.
 *
 * @param errorText - Error message or stack trace from the failed stage
 * @returns The terminal failure kind, or undefined when unclassifiable
 */
export function classifyTerminalKind(
  errorText: string | undefined
): TerminalFailureKind | undefined {
  return matchTerminalKindRule(errorText)?.kind as TerminalFailureKind | undefined;
}

/**
 * The kind a consumer may forward to the Go scheduler as a signal, or
 * `undefined` to defer to Go's own classification.
 *
 * TWO STAGES, IN THIS ORDER — the twin of `SignalKind` in
 * internal/terminalkind/table.go.
 *
 * First the FULL ladder runs and the answer is the WINNING rule's kind only when
 * that rule is declared `signal: true`. Skipping non-signal rules instead would
 * reintroduce disagreement, because a lower-precedence signal rule could then
 * claim text a higher-precedence non-signal rule owns.
 *
 * Only if that yields nothing are the table's declared `signal_extensions`
 * consulted. They are the ONE deliberate record-vs-reaction divergence, they are
 * data rather than code, and their position AFTER the projection is the bound:
 * an extension can only claim text the signal SUBSET already ignores. Read that
 * exactly — an extension can never overrule a kind projected by a `signal: true`
 * RULE, which is narrower than "a kind the record names". When the winning rule
 * is not in the subset the record still names a kind, and the one declared
 * extension deliberately names a different one on top of it: a usage limit that
 * names a model records `model_unavailable` and reacts
 * `rate_limit_quota_exhausted`.
 *
 * THE ORDER OF THESE TWO STATEMENTS IS THE WHOLE BOUND, AND IT IS PINNED.
 * Swapping them is a four-line reorder with no literal and no import, and it
 * used to move no answer at all — the derived set composed rules with rules
 * only, so nothing in the system carried both a signal marker and extension
 * wording. `terminalKindStressInputs` now composes every `signal: true` rule
 * with every extension clause in both orders, and the corpus carries
 * `order-signal-rule-beats-extension-*` rows on real wording.
 *
 * The VSCode extension consumes this through `services/terminalKindSignal.ts`;
 * Go's NotifyComplete uses a non-empty answer VERBATIM, which is why both the
 * bound and its one exception have to be structural.
 */
export function signalTerminalKind(errorText: string | undefined): TerminalFailureKind | undefined {
  const rule = matchTerminalKindRule(errorText);
  if (rule?.signal) return rule.kind as TerminalFailureKind;
  return matchSignalExtension(errorText)?.kind as TerminalFailureKind | undefined;
}

function matchSignalExtension(
  errorText: string | undefined
): { id: string; kind: string } | undefined {
  if (!errorText) return undefined;
  const t = errorText.toLowerCase();
  for (const extension of TERMINAL_KIND_TABLE.signal_extensions) {
    for (const clause of extension.clauses) {
      if (clauseSatisfied(t, clause)) return extension;
    }
  }
  return undefined;
}

/**
 * Prefers a gate-sourced structured terminal kind over prose classification
 * of the synthesized error text (Issue #9). Mirrors Go's
 * `ResolveTerminalKind` in `internal/orchestrator/failure_handler.go` — a
 * two-line precedence rule with no matching in it, which is why it is written
 * out rather than tabulated. Falls back to `classifyTerminalKind` for non-gate
 * failures and for gate failures that didn't set a structured kind (including
 * all historical records persisted before `terminal_kind` existed on
 * `StageGateResult`).
 */
export function resolveTerminalKind(
  gateRan: boolean,
  gateTerminalKind: string | undefined,
  errorText: string | undefined
): TerminalFailureKind | undefined {
  if (gateRan && gateTerminalKind) {
    return gateTerminalKind as TerminalFailureKind;
  }
  return classifyTerminalKind(errorText);
}

/**
 * Derive a deterministic input set FROM the table — the verbatim TypeScript
 * twin of `StressInputs` in internal/terminalkind/stress.go.
 *
 * Both languages derive the same list and compare their answers against one
 * committed golden (internal/terminalkind/testdata/stress-golden.json), which is
 * how the two interpreters are proved equivalent without a live bridge between
 * them: if the derivations differed the input lists would not match, and if the
 * interpreters differed the answers would not.
 *
 * THE ALGORITHM IS PART OF THE CONTRACT. Changing it here means changing
 * stress.go and regenerating the golden. Order is significant and stable —
 * the two baselines, then rule by rule and clause by clause, then the signal
 * extensions the same way, then every ordered pair of rules, then every
 * `signal: true` rule composed with every extension clause in both orders — and
 * duplicates keep their FIRST occurrence.
 */
export function terminalKindStressInputs(): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  const add = (s: string): void => {
    if (!seen.has(s)) {
      seen.add(s);
      out.push(s);
    }
  };

  add(EMPTY);
  add(NO_MARKER_SENTENCE);

  for (const rule of TERMINAL_KIND_TABLE.rules) {
    for (const clause of rule.clauses) addClauseSamples(add, clause);
  }

  for (const extension of TERMINAL_KIND_TABLE.signal_extensions) {
    for (const clause of extension.clauses) addClauseSamples(add, clause);
  }

  for (const a of TERMINAL_KIND_TABLE.rules) {
    for (const b of TERMINAL_KIND_TABLE.rules) {
      if (a.id === b.id) continue;
      add(sampleClause(a.clauses[0]) + PAIR_SEPARATOR + sampleClause(b.clauses[0]));
    }
  }

  // STAGE precedence: every `signal: true` rule composed with every extension
  // CLAUSE, in both orders of appearance — the twin of the last block of
  // StressInputs. The pair matrix above composes rules with rules only, so
  // before these rows no input in the system carried both a signal marker and
  // extension wording, and swapping the two statements of `signalTerminalKind`
  // (a four-line reorder, no literal, no import) moved nothing: the production
  // path would answer `rate_limit_quota_exhausted` for
  // `[adapter-auth-failed] usage limit reached`, which is the case this file's
  // own doc calls impossible.
  for (const rule of TERMINAL_KIND_TABLE.rules) {
    if (!rule.signal) continue;
    for (const extension of TERMINAL_KIND_TABLE.signal_extensions) {
      for (const clause of extension.clauses) {
        const ruleSample = sampleClause(rule.clauses[0]);
        const extensionSample = sampleClause(clause);
        add(ruleSample + PAIR_SEPARATOR + extensionSample);
        add(extensionSample + PAIR_SEPARATOR + ruleSample);
      }
    }
  }

  return out;
}

/**
 * Every derived input for one clause — the twin of `addClauseSamples` in
 * stress.go. Rules and signal extensions share it so an extension's clauses can
 * never be sampled more thinly than a rule's.
 *
 * The last group is the TERM-KIND boundary, ON BOTH EDGES. Everything else
 * renders a `~term` exactly as a plain one, so the whole derived set used to
 * answer identically with the marker and without it; these inputs are the ones
 * the two semantics disagree about — the literal with a word character glued to
 * an edge. Two of them, because `containsWordBounded` is a conjunction of two
 * independent tests: with only the right-edge input, deleting the left conjunct
 * (`!isWordChar(lowered, i - 1) && `) shipped a strictly wider matcher than
 * main's `\b…\b` with the whole suite green and the golden byte-identical.
 */
function addClauseSamples(add: (s: string) => void, clause: string[]): void {
  const s = sampleClause(clause);
  add(s);
  add(CRASH_PREFIX + s);
  add(s.toUpperCase());
  for (const term of clause) add(sampleClause([term]));
  for (const [i, term] of clause.entries()) {
    if (!term.startsWith(TERMINAL_KIND_WORD_BOUNDARY_REF)) continue;
    add(renderClause(clause, i, false));
    add(renderClause(clause, i, true));
  }
}

function sampleClause(clause: string[]): string {
  return renderClause(clause, -1, false);
}

/**
 * `BOUNDARY_VIOLATION_AFFIX` is one word character: gluing it to either edge of
 * a literal preserves containment and destroys the word boundary on that side,
 * which is the entire difference between a `~term` and a plain one.
 */
function renderClause(clause: string[], violate: number, atLeftEdge: boolean): string {
  return clause
    .map((term, i) => {
      if (term.startsWith(TERMINAL_KIND_PREDICATE_REF)) {
        return probeTrue(term.slice(TERMINAL_KIND_PREDICATE_REF.length));
      }
      const lit = term.startsWith(TERMINAL_KIND_WORD_BOUNDARY_REF)
        ? term.slice(TERMINAL_KIND_WORD_BOUNDARY_REF.length)
        : term;
      if (i !== violate) return lit;
      return atLeftEdge ? BOUNDARY_VIOLATION_AFFIX + lit : lit + BOUNDARY_VIOLATION_AFFIX;
    })
    .join(TERM_SEPARATOR);
}

function probeTrue(name: string): string {
  const p = TERMINAL_KIND_TABLE.predicates.find((x) => x.name === name);
  if (!p || p.probes_true.length === 0) {
    throw new Error(NO_PROBES_TRUE + name);
  }
  return p.probes_true[0];
}
