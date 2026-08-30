import * as fs from "node:fs";
import * as path from "node:path";

import { describe, expect, it } from "vitest";

import {
  applyDeliverablePolicy,
  canonicalSchemaVersion,
  deliverableKindForStage,
  POLICY_MARKER_FIELD,
  POLICY_VERSION,
  stampPolicyMarker,
  type PolicyOutcome,
} from "../../context/deliverablePolicy.js";
import { DevContextSchema } from "../../context/schemas/dev.js";
import { ValidateContextSchema } from "../../context/schemas/validate.js";

/**
 * The shared conformance corpus. The SAME file drives
 * `internal/deliverable/policy_test.go`.
 *
 * Two implementations of one policy drift in silence — which is the entire
 * defect #1182 describes, one level up. This corpus is what turns a drift into a
 * failing suite instead of a failing run: add a rule in Go and not here (or the
 * reverse) and one of the two suites goes red immediately.
 */
interface CorpusCase {
  id: string;
  stage: string;
  input: unknown;
  expect: {
    verdict: string;
    untrustworthy?: string[];
    notes: { field: string; disposition: string; rule: string }[];
    doc?: Record<string, unknown>;
  };
}

function repoRoot(): string {
  let dir = process.cwd();
  for (let i = 0; i < 10; i++) {
    if (fs.existsSync(path.join(dir, "go.mod"))) return dir;
    const parent = path.dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  throw new Error("could not locate repo root");
}

const corpusPath = path.join(repoRoot(), "schemas", "deliverable-policy-corpus-v1.json");
const corpus = JSON.parse(fs.readFileSync(corpusPath, "utf-8")) as {
  policy_version: string;
  cases: CorpusCase[];
};

describe("deliverable policy — shared conformance corpus", () => {
  it("is written against this rule table", () => {
    expect(corpus.policy_version).toBe(POLICY_VERSION);
    expect(corpus.cases.length).toBeGreaterThan(0);
  });

  for (const c of corpus.cases) {
    it(c.id, () => {
      const out = applyDeliverablePolicy(c.stage, c.input);

      expect(out.verdict).toBe(c.expect.verdict);
      expect(
        out.notes.map((n) => ({
          field: n.field,
          disposition: n.disposition,
          rule: n.rule,
        }))
      ).toEqual(c.expect.notes);

      // Every note has to be actionable — a rule id with no evidence tells an
      // operator nothing about what to fix.
      for (const note of out.notes) {
        expect(note.detail.trim().length).toBeGreaterThan(0);
      }

      if (c.expect.untrustworthy) {
        expect(out.untrustworthy).toEqual(c.expect.untrustworthy);
      }
      if (c.expect.doc) {
        expect(out.doc).toEqual(c.expect.doc);
      }
    });
  }
});

describe("one policy, one consequence (#1182)", () => {
  /**
   * The #1182 table, field by field. Before this policy the consequence of each
   * row was decided by which validator happened to look at it first; now it is
   * decided by the defect.
   */
  const table: {
    row: string;
    kind: string;
    doc: Record<string, unknown>;
    verdict: string;
  }[] = [
    {
      row: "dev-170 quality_checks.type_check invalid option",
      kind: "dev",
      doc: { schema_version: "1.8", quality_checks: { type_check: "not run" } },
      verdict: "repaired",
    },
    {
      row: "validate-170 skipped_phases[i] expected object, got string",
      kind: "validate",
      doc: { schema_version: "2.6", skipped_phases: ["lint"] },
      verdict: "quarantined",
    },
    {
      row: "dev-340 quality_checks.security_review invalid option",
      kind: "dev",
      doc: { schema_version: "1.8", quality_checks: { security_review: "N/A" } },
      verdict: "repaired",
    },
    {
      row: "validate-340 gate_metrics[].gate_name undefined",
      kind: "validate",
      doc: { schema_version: "2.6", gate_metrics: [{ name: "lint", result: "pass" }] },
      verdict: "quarantined",
    },
    {
      row: "validate-340 gate_metrics[].result invalid option",
      kind: "validate",
      doc: { schema_version: "2.6", gate_metrics: [{ gate_name: "judges", result: "failed" }] },
      verdict: "repaired",
    },
    {
      row: "validate-232 gate_metrics[0].gate_name undefined",
      kind: "validate",
      doc: { schema_version: "2.6", gate_metrics: [{ result: "pass" }] },
      verdict: "quarantined",
    },
    {
      row: "dev-210 files_changed expected object, got array (repairable)",
      kind: "dev",
      doc: {
        schema_version: "1.8",
        files_changed: ["a.ts"],
        files_modified: ["a.ts"],
      },
      verdict: "repaired",
    },
    {
      row: "dev files_changed array with no manifest (unrepairable)",
      kind: "dev",
      doc: { schema_version: "1.8", files_changed: ["a.ts"] },
      verdict: "fatal",
    },
  ];

  for (const { row, kind, doc, verdict } of table) {
    it(`${row} → ${verdict}`, () => {
      expect(applyDeliverablePolicy(kind, doc).verdict).toBe(verdict);
    });
  }

  it("a repaired or quarantined deliverable proceeds; a fatal one does not", () => {
    for (const { kind, doc, verdict } of table) {
      const out = applyDeliverablePolicy(kind, doc);
      expect(out.ok).toBe(verdict !== "fatal");
    }
  });
});

describe("the policy output satisfies the schema it is repairing", () => {
  /**
   * The repair is worthless if the repaired document still fails Zod. This is
   * the load-bearing assertion for #1176: the deliverable that cost $6.12 must
   * come out the far side readable.
   */
  it("repairs dev-210's manifest into something DevContextSchema accepts", () => {
    const out = applyDeliverablePolicy("dev", {
      schema_version: "1.5",
      issue_number: 210,
      files_changed: ["docs/PRODUCT_REQUIREMENTS.md", "docs/a.md"],
      files_created: [],
      files_modified: ["docs/PRODUCT_REQUIREMENTS.md", "docs/a.md"],
      committed: false,
      commit_sha: null,
    });
    expect(out.ok).toBe(true);
    const parsed = DevContextSchema.safeParse(out.doc);
    expect(parsed.success).toBe(true);
  });

  it("repairs the quality-check vocabulary into something DevContextSchema accepts", () => {
    const out = applyDeliverablePolicy("dev", {
      schema_version: "1.8",
      issue_number: 170,
      quality_checks: { type_check: "not run", security_review: "N/A", dead_code_scan: "not_run" },
    });
    expect(DevContextSchema.safeParse(out.doc).success).toBe(true);
  });

  it("quarantines the nameless gate metric into something ValidateContextSchema accepts", () => {
    const out = applyDeliverablePolicy("validate", {
      schema_version: "2.6",
      issue_number: 232,
      validation_status: "passed",
      gate_metrics: [{ result: "pass" }, { gate_name: "lint", result: "pass" }],
    });
    expect(ValidateContextSchema.safeParse(out.doc).success).toBe(true);
    expect((out.doc?.gate_metrics as unknown[]).length).toBe(1);
  });

  it("accepts a judge 'fail' — the deliverable enum and the record enum are one list", () => {
    const parsed = ValidateContextSchema.safeParse({
      schema_version: "2.6",
      issue_number: 233,
      validation_status: "passed",
      gate_metrics: [{ gate_name: "judges", result: "fail" }],
    });
    expect(parsed.success).toBe(true);
  });
});

describe("the repair is recorded, and only when there was one (#1176)", () => {
  it("stamps a marker naming every repair and quarantine", () => {
    const out = applyDeliverablePolicy("validate", {
      schema_version: "1.5",
      gate_metrics: [{ result: "pass" }, { gate_name: "lint", result: "passed" }],
    });
    stampPolicyMarker(out, new Date("2026-08-29T12:00:00Z"));
    const marker = out.doc?.[POLICY_MARKER_FIELD] as Record<string, unknown>;
    expect(marker).toBeDefined();
    expect(marker.policy_version).toBe(POLICY_VERSION);
    expect(marker.applied_at).toBe("2026-08-29T12:00:00Z");
    expect((marker.repairs as unknown[]).length).toBe(2);
    expect((marker.quarantined as unknown[]).length).toBe(1);
    expect(marker.untrustworthy).toEqual(["gate_metrics"]);
  });

  it("leaves a clean deliverable unmarked — the marker's presence is the signal", () => {
    const out = applyDeliverablePolicy("dev", {
      schema_version: "1.8",
      files_changed: { created: [], modified: ["a.ts"], deleted: [] },
    });
    expect(out.changed).toBe(false);
    stampPolicyMarker(out, new Date());
    expect(out.doc?.[POLICY_MARKER_FIELD]).toBeUndefined();
  });

  it("never mutates the caller's document", () => {
    const input = { schema_version: "1.5", files_changed: ["a.ts"], files_modified: ["a.ts"] };
    applyDeliverablePolicy("dev", input);
    expect(input.schema_version).toBe("1.5");
    expect(Array.isArray(input.files_changed)).toBe(true);
  });
});

describe("schema_version is asserted, not authored (#1177)", () => {
  it("overwrites a remembered older contract and records the correction", () => {
    const out: PolicyOutcome = applyDeliverablePolicy("dev", { schema_version: "1.5" });
    expect(out.doc?.schema_version).toBe(canonicalSchemaVersion("dev"));
    expect(out.notes[0]?.rule).toBe("any.schema_version.stamped");
    expect(out.notes[0]?.detail).toContain("1.5");
  });

  it("agrees with the version the skill include authors", () => {
    const root = repoRoot();
    const includes: Record<string, string> = {
      dev: path.join(root, "skills/nightgauge-feature-dev/_includes/context-and-epilogue.md"),
      validate: path.join(
        root,
        "skills/nightgauge-feature-validate/_includes/context-and-board.md"
      ),
    };
    for (const [kind, file] of Object.entries(includes)) {
      const raw = fs.readFileSync(file, "utf-8");
      expect(raw).toContain(`schema_version: "${canonicalSchemaVersion(kind)}"`);
    }
  });

  it("maps pipeline stage names onto deliverable kinds", () => {
    expect(deliverableKindForStage("feature-dev")).toBe("dev");
    expect(deliverableKindForStage("feature-validate")).toBe("validate");
    expect(deliverableKindForStage("pr-create")).toBeUndefined();
  });
});
