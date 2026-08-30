/**
 * The deliverable-schema policy — TypeScript half (#1182).
 *
 * A stage deliverable that does not match its schema had two possible fates and
 * no way to predict which. The context-file validator here logged
 * `(non-fatal, continuing)` and shipped the run; the Go post-condition gate
 * failed the stage and discarded the work. Across one ten-run session that was
 * eight mismatches in five fields — seven warnings and one $6.12 loss — and the
 * difference between them was not severity. It was which validator happened to
 * look at that particular field.
 *
 * This module is the same closed rule table the Go binary carries
 * (`internal/deliverable/policy.go`), so both validation points reach the same
 * verdict about the same defect. The conformance corpus at
 * `schemas/deliverable-policy-corpus-v1.json` is read by BOTH suites: a rule
 * added on one side and not the other fails the other side's tests, which is the
 * only thing that keeps two implementations of one policy honest.
 *
 * Three dispositions, and every mismatch lands in exactly one:
 *
 * - `repaired`    — every value the schema wants is already in the file; only
 *                   the shape or the vocabulary is wrong, and the mapping is a
 *                   hand-written entry in the table. Rewritten, and recorded.
 * - `quarantined` — a value is genuinely absent in a field no consumer's control
 *                   flow reads. The entry is REMOVED rather than forwarded
 *                   malformed, and the field is named in `untrustworthy` so the
 *                   gate-metrics record and the learning corpus can see that
 *                   what they are reading is incomplete.
 * - `fatal`       — a value a consumer needs is absent, or present in a form no
 *                   closed rule can map without guessing. Both points fail.
 *
 * No rule infers, partially fills, or asks a model. A rule that cannot prove its
 * repair is TOTAL returns `fatal` instead.
 *
 * @see docs/CONTEXT_ARCHITECTURE.md
 */

/** Identifies the rule table. Must match `deliverable.PolicyVersion` in Go. */
export const POLICY_VERSION = "1";

/** The key under which the policy records what it did, in the deliverable. */
export const POLICY_MARKER_FIELD = "_deliverable_policy";

export type Disposition = "repaired" | "quarantined" | "fatal";

export interface PolicyNote {
  /** Dotted path of the offending field, e.g. `gate_metrics.0.result`. */
  field: string;
  disposition: Disposition;
  /** Stable rule id — the contract the shared corpus asserts against. */
  rule: string;
  /** Operator-facing evidence: what was found and what was done. */
  detail: string;
}

export type PolicyVerdict = "clean" | "repaired" | "quarantined" | "fatal";

export interface PolicyOutcome {
  /** The normalized document. `null` when the input was not a JSON object. */
  doc: Record<string, unknown> | null;
  notes: PolicyNote[];
  /** Worst disposition present. Both validation points branch on this. */
  verdict: PolicyVerdict;
  /** `false` only for `fatal` — repairs and quarantines proceed. */
  ok: boolean;
  /** True when `doc` differs from the input and should be written back. */
  changed: boolean;
  /** Top-level fields an entry was dropped from; incomplete when read. */
  untrustworthy: string[];
}

/**
 * The contract version this build implements for each deliverable (#1177).
 *
 * Stamped onto the document rather than trusted from it: a skill writing its own
 * version number is making a claim, and #1177's run claimed `"1.5"` against an
 * include that says `"1.8"` — a different, older contract recalled from training
 * rather than read. Stamping turns the claim into a fact and records the
 * correction, so a stage running on a remembered schema stays visible.
 */
const CANONICAL_SCHEMA_VERSION: Record<string, string> = {
  dev: "1.9",
  validate: "2.6",
};

export function canonicalSchemaVersion(stage: string): string | undefined {
  return CANONICAL_SCHEMA_VERSION[stage];
}

/**
 * Maps a pipeline stage name to the deliverable kind the policy governs.
 * Returns `undefined` for stages with no policy (their deliverables are left
 * entirely to Zod).
 */
export function deliverableKindForStage(stage: string): string | undefined {
  if (stage === "feature-dev" || stage === "dev") return "dev";
  if (stage === "feature-validate" || stage === "validate") return "validate";
  return undefined;
}

/**
 * The closed synonym table for the four `quality_checks` verdicts. Every entry
 * is a rename of a value that means the same thing — never a downgrade, never an
 * interpretation of a different outcome.
 */
const QUALITY_CHECK_VOCABULARY: Record<string, string> = {
  "not run": "not_run",
  "not-run": "not_run",
  notrun: "not_run",
  not_applicable: "not_run",
  "not applicable": "not_run",
  "n/a": "not_run",
  na: "not_run",
  none: "not_run",
  pass: "passed",
  passing: "passed",
  ok: "passed",
  fail: "failed",
  failing: "failed",
  skip: "skipped",
};

const QUALITY_CHECK_CANONICAL = new Set(["passed", "failed", "skipped", "not_run"]);

const QUALITY_CHECK_FIELDS = ["code_standards", "security_review", "type_check", "dead_code_scan"];

/**
 * The closed synonym table for `gate_metrics[].result`. The canonical set is the
 * SAME one the gate-metrics record accepts; that alignment is the actual fix for
 * `validate-340`, and this table only covers vocabulary drift on top of it.
 */
const GATE_RESULT_VOCABULARY: Record<string, string> = {
  passed: "pass",
  passing: "pass",
  ok: "pass",
  failed: "fail",
  failing: "fail",
  caught: "catch",
};

const GATE_RESULT_CANONICAL = new Set(["pass", "catch", "fail"]);

function jsonShape(v: unknown): string {
  if (v === null) return "null";
  if (Array.isArray(v)) return "an array";
  switch (typeof v) {
    case "object":
      return "an object";
    case "string":
      return "a string";
    case "boolean":
      return "a boolean";
    case "number":
      return "a number";
    default:
      return "a value of an unexpected type";
  }
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function clone<T>(v: T): T {
  if (Array.isArray(v)) return v.map(clone) as unknown as T;
  if (isPlainObject(v)) {
    const out: Record<string, unknown> = {};
    for (const [k, val] of Object.entries(v)) out[k] = clone(val);
    return out as unknown as T;
  }
  return v;
}

function stringArrayField(
  doc: Record<string, unknown>,
  key: string
): { values: string[]; present: boolean } {
  const raw = doc[key];
  if (raw === undefined || raw === null || !Array.isArray(raw)) {
    return { values: [], present: false };
  }
  if (!raw.every((v) => typeof v === "string")) return { values: [], present: false };
  return { values: raw as string[], present: true };
}

/**
 * Apply the closed rule table to a decoded deliverable.
 *
 * The input is never mutated — the caller's document is evidence of what the
 * stage actually wrote.
 */
export function applyDeliverablePolicy(kind: string, decoded: unknown): PolicyOutcome {
  if (!isPlainObject(decoded)) {
    return finalize(null, [
      {
        field: "(top level)",
        disposition: "fatal",
        rule: "any.document.not_an_object",
        detail: `expected a JSON object, got ${jsonShape(decoded)}`,
      },
    ]);
  }

  const doc = clone(decoded);
  const notes: PolicyNote[] = [];

  applySchemaVersionRule(kind, doc, notes);
  if (kind === "dev") {
    applyDevFilesChangedRule(doc, notes);
    applyQualityChecksRule(doc, notes);
  } else if (kind === "validate") {
    applySkippedPhasesRule(doc, notes);
    applyGateMetricsRule(doc, notes);
  }

  return finalize(doc, notes);
}

function finalize(doc: Record<string, unknown> | null, notes: PolicyNote[]): PolicyOutcome {
  const has = (d: Disposition) => notes.some((n) => n.disposition === d);
  const verdict: PolicyVerdict = has("fatal")
    ? "fatal"
    : has("quarantined")
      ? "quarantined"
      : has("repaired")
        ? "repaired"
        : "clean";
  const untrustworthy = [
    ...new Set(
      notes.filter((n) => n.disposition === "quarantined").map((n) => n.field.split(".")[0]!)
    ),
  ].sort();
  return {
    doc,
    notes,
    verdict,
    ok: verdict !== "fatal",
    changed: has("repaired") || has("quarantined"),
    untrustworthy,
  };
}

/** Render the notes as log-facing evidence lines. */
export function summarizePolicy(outcome: PolicyOutcome): string[] {
  return outcome.notes.map((n) => `${n.disposition} [${n.rule}] ${n.field}: ${n.detail}`);
}

/**
 * Record what the policy did inside the deliverable, so a bad emitter stays
 * visible to a human reading the file and to anything that mines it. A no-op
 * when nothing changed — a healthy deliverable never grows a marker, so the
 * marker's presence is itself the signal.
 */
export function stampPolicyMarker(outcome: PolicyOutcome, now: Date): void {
  if (!outcome.doc || !outcome.changed) return;
  const entry = (n: PolicyNote) => ({ field: n.field, rule: n.rule, detail: n.detail });
  outcome.doc[POLICY_MARKER_FIELD] = {
    policy_version: POLICY_VERSION,
    applied_at: now.toISOString().replace(/\.\d{3}Z$/, "Z"),
    repairs: outcome.notes.filter((n) => n.disposition === "repaired").map(entry),
    quarantined: outcome.notes.filter((n) => n.disposition === "quarantined").map(entry),
    untrustworthy: outcome.untrustworthy,
  };
}

function applySchemaVersionRule(
  kind: string,
  doc: Record<string, unknown>,
  notes: PolicyNote[]
): void {
  const canonical = CANONICAL_SCHEMA_VERSION[kind];
  if (!canonical) return;
  const claimed = typeof doc.schema_version === "string" ? doc.schema_version : "";
  if (claimed === canonical) return;
  doc.schema_version = canonical;
  notes.push({
    field: "schema_version",
    disposition: "repaired",
    rule: "any.schema_version.stamped",
    detail:
      claimed === ""
        ? `no schema_version was written; stamped "${canonical}"`
        : `stage claimed "${claimed}"; the contract this build implements is "${canonical}"`,
  });
}

/**
 * Repair the #1176 defect: `files_changed` written as a flat array of paths
 * alongside sibling `files_created` / `files_modified` / `files_deleted` keys
 * carrying exactly the values the schema wants.
 *
 * Allowed only when provably TOTAL. The array and the union of the siblings must
 * name the same set of paths. A path the siblings do not classify has no known
 * created/modified split and inventing one is the inference this policy forbids;
 * a path the siblings claim but the array omits is a deliverable contradicting
 * itself. Both are fatal.
 */
function applyDevFilesChangedRule(doc: Record<string, unknown>, notes: PolicyNote[]): void {
  if (!("files_changed" in doc)) return;
  const raw = doc.files_changed;
  if (!Array.isArray(raw)) return; // object (canonical) or null — leave it to Zod

  if (!raw.every((v) => typeof v === "string")) {
    notes.push({
      field: "files_changed",
      disposition: "fatal",
      rule: "dev.files_changed.non_string_entries",
      detail:
        "files_changed is an array containing non-string entries; no rule maps it to {created, modified, deleted}",
    });
    return;
  }
  const declared = new Set(raw as string[]);

  const created = stringArrayField(doc, "files_created");
  const modified = stringArrayField(doc, "files_modified");
  const deleted = stringArrayField(doc, "files_deleted");
  if (!created.present && !modified.present && !deleted.present) {
    notes.push({
      field: "files_changed",
      disposition: "fatal",
      rule: "dev.files_changed.no_sibling_manifest",
      detail:
        "files_changed is a flat array and the deliverable carries no files_created/files_modified/files_deleted to classify it; the created/modified split is genuinely absent",
    });
    return;
  }

  const union = new Set([...created.values, ...modified.values, ...deleted.values]);
  const unclassified = [...declared].filter((p) => !union.has(p)).sort();
  const unclaimed = [...union].filter((p) => !declared.has(p)).sort();
  if (unclassified.length > 0 || unclaimed.length > 0) {
    const parts: string[] = [];
    if (unclassified.length > 0) {
      parts.push(`not classified by any sibling key: ${unclassified.join(", ")}`);
    }
    if (unclaimed.length > 0) {
      parts.push(`claimed by a sibling key but absent from files_changed: ${unclaimed.join(", ")}`);
    }
    notes.push({
      field: "files_changed",
      disposition: "fatal",
      rule: "dev.files_changed.sibling_manifest_incomplete",
      detail: `the sibling manifest does not account for files_changed exactly, so no total repair exists (${parts.join("; ")})`,
    });
    return;
  }

  doc.files_changed = {
    created: created.values,
    modified: modified.values,
    deleted: deleted.values,
  };
  delete doc.files_created;
  delete doc.files_modified;
  delete doc.files_deleted;
  notes.push({
    field: "files_changed",
    disposition: "repaired",
    rule: "dev.files_changed.from_sibling_manifest",
    detail: `rebuilt {created, modified, deleted} from files_created/files_modified/files_deleted; every one of the ${declared.size} paths in the array was accounted for exactly once`,
  });
}

/**
 * Normalise the `quality_checks` verdict vocabulary. A value outside the closed
 * table is QUARANTINED, not guessed at — passing an unrecognised verdict through
 * is what let `dev-170` and `dev-340` ship with a quality signal nothing could
 * read.
 */
function applyQualityChecksRule(doc: Record<string, unknown>, notes: PolicyNote[]): void {
  const qc = doc.quality_checks;
  if (!isPlainObject(qc)) return;
  for (const field of QUALITY_CHECK_FIELDS) {
    const v = qc[field];
    if (v === undefined || v === null || typeof v !== "string") continue;
    const normalized = v.trim().toLowerCase();
    if (QUALITY_CHECK_CANONICAL.has(normalized)) {
      if (normalized !== v) {
        qc[field] = normalized;
        notes.push({
          field: `quality_checks.${field}`,
          disposition: "repaired",
          rule: "any.quality_checks.vocabulary",
          detail: `"${v}" normalised to "${normalized}"`,
        });
      }
      continue;
    }
    const canonical = QUALITY_CHECK_VOCABULARY[normalized];
    if (canonical) {
      qc[field] = canonical;
      notes.push({
        field: `quality_checks.${field}`,
        disposition: "repaired",
        rule: "any.quality_checks.vocabulary",
        detail: `"${v}" is a known synonym of "${canonical}"`,
      });
      continue;
    }
    delete qc[field];
    notes.push({
      field: `quality_checks.${field}`,
      disposition: "quarantined",
      rule: "any.quality_checks.unknown_vocabulary",
      detail: `"${v}" is not a quality-check verdict and no closed rule maps it; the field was dropped rather than reported as a verdict it does not mean`,
    });
  }
}

/**
 * Quarantine `skipped_phases` entries that are not `{phase, reason}` objects.
 *
 * A bare string names the phase and omits the reason — and the reason is the
 * entire point of the field, being what a human reads to decide whether the skip
 * was legitimate. There is no total repair for a value that was never written,
 * so the entry is dropped and the field marked untrustworthy rather than
 * back-filled with an invented reason or forwarded malformed.
 */
function applySkippedPhasesRule(doc: Record<string, unknown>, notes: PolicyNote[]): void {
  const arr = doc.skipped_phases;
  if (!Array.isArray(arr)) return;
  const kept: unknown[] = [];
  arr.forEach((entry, i) => {
    if (!isPlainObject(entry)) {
      notes.push({
        field: `skipped_phases.${i}`,
        disposition: "quarantined",
        rule: "validate.skipped_phases.entry_not_an_object",
        detail: `entry is ${jsonShape(entry)}, not {phase, reason}; the reason was never written and cannot be invented`,
      });
      return;
    }
    const phase = typeof entry.phase === "string" ? entry.phase.trim() : "";
    const reason = typeof entry.reason === "string" ? entry.reason.trim() : "";
    if (phase === "" || reason === "") {
      notes.push({
        field: `skipped_phases.${i}`,
        disposition: "quarantined",
        rule: "validate.skipped_phases.incomplete_entry",
        detail:
          "entry lacks a non-empty phase or reason; a skip with no stated reason is not evidence of anything",
      });
      return;
    }
    kept.push(entry);
  });
  if (kept.length !== arr.length) doc.skipped_phases = kept;
}

/**
 * Repair the gate-result vocabulary and quarantine entries with no gate name.
 *
 * A gate metric with no `gate_name` is unattributable: the record it becomes
 * tallies hit-rates BY gate, so a nameless row can neither be counted nor
 * excluded from a count. Forwarding it — which is exactly what warn-and-continue
 * did for `validate-340` and `validate-232` — puts an uncountable row in front
 * of the gate-metrics tally and the learning corpus with nothing but a log line
 * behind it. Dropping it and naming `gate_metrics` untrustworthy is the
 * difference between tolerating a mismatch and silently propagating one.
 */
function applyGateMetricsRule(doc: Record<string, unknown>, notes: PolicyNote[]): void {
  const arr = doc.gate_metrics;
  if (!Array.isArray(arr)) return;
  const kept: unknown[] = [];
  arr.forEach((entry, i) => {
    if (!isPlainObject(entry)) {
      notes.push({
        field: `gate_metrics.${i}`,
        disposition: "quarantined",
        rule: "validate.gate_metrics.entry_not_an_object",
        detail: `entry is ${jsonShape(entry)}, not a gate metric`,
      });
      return;
    }
    const name = typeof entry.gate_name === "string" ? entry.gate_name.trim() : "";
    if (name === "") {
      notes.push({
        field: `gate_metrics.${i}`,
        disposition: "quarantined",
        rule: "validate.gate_metrics.missing_gate_name",
        detail:
          "entry has no gate_name; a gate-metrics record is tallied BY gate, so a nameless row can neither be counted nor excluded",
      });
      return;
    }
    if ("result" in entry) {
      const res = entry.result;
      if (typeof res !== "string") {
        notes.push({
          field: `gate_metrics.${i}.result`,
          disposition: "quarantined",
          rule: "validate.gate_metrics.result_not_a_string",
          detail: `result is ${jsonShape(res)}`,
        });
        return;
      }
      const normalized = res.trim().toLowerCase();
      if (GATE_RESULT_CANONICAL.has(normalized)) {
        if (normalized !== res) {
          entry.result = normalized;
          notes.push({
            field: `gate_metrics.${i}.result`,
            disposition: "repaired",
            rule: "validate.gate_metrics.result_vocabulary",
            detail: `"${res}" normalised to "${normalized}"`,
          });
        }
      } else {
        const canonical = GATE_RESULT_VOCABULARY[normalized];
        if (!canonical) {
          notes.push({
            field: `gate_metrics.${i}.result`,
            disposition: "quarantined",
            rule: "validate.gate_metrics.unknown_result",
            detail: `"${res}" is not a gate result and no closed rule maps it`,
          });
          return;
        }
        entry.result = canonical;
        notes.push({
          field: `gate_metrics.${i}.result`,
          disposition: "repaired",
          rule: "validate.gate_metrics.result_vocabulary",
          detail: `"${res}" is a known synonym of "${canonical}"`,
        });
      }
    }
    kept.push(entry);
  });
  if (kept.length !== arr.length) doc.gate_metrics = kept;
}
