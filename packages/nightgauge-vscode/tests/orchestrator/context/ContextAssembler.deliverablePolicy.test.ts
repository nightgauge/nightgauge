/**
 * The context-file validator, under the one policy (#1182).
 *
 * Before this, `validateStageContextOutput` logged EVERY schema mismatch as
 * `(non-fatal, continuing)` and returned `{ error: null }`. The Go
 * post-condition gate, reading the same file, failed the stage. Which of the two
 * a deliverable met decided whether a defect was a log line or a $6.12 loss, and
 * nothing about the work differed.
 *
 * These tests pin the three dispositions at THIS validation point. The Go suite
 * (`internal/orchestrator/gates/deliverable_policy_gate_test.go`) pins the same
 * three at the gate, and both are driven by the shared corpus in
 * `schemas/deliverable-policy-corpus-v1.json`.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

import type { Logger } from "../../../src/utils/logger";

vi.mock("fs");
vi.mock("../../../src/utils/skillRunner", () => ({
  findSkillFile: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
}));
vi.mock("../../../src/utils/routingDecision", () => ({
  makeRoutingDecision: vi.fn(),
  buildPickupRecommendation: vi.fn(),
  DEFAULT_ROUTING_CONFIG: {},
}));
vi.mock("../../../src/utils/changeAnalyzer", () => ({
  analyzeChange: vi.fn(),
}));
vi.mock("../../../src/utils/zodErrorFormatter", () => ({
  formatZodErrorsForPrompt: vi.fn().mockReturnValue(""),
}));

import * as fs from "fs";

import { ContextAssembler } from "../../../src/orchestrator/context/ContextAssembler";

const mockExistsSync = vi.mocked(fs.existsSync);
const mockReadFileSync = vi.mocked(fs.readFileSync);
const mockWriteFileSync = vi.mocked(fs.writeFileSync);

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

/** Stage the deliverable a run wrote, and return the assembler that reads it. */
function stage(body: unknown): { assembler: ContextAssembler; logger: Logger } {
  const logger = makeLogger();
  mockExistsSync.mockReturnValue(true);
  mockReadFileSync.mockReturnValue(JSON.stringify(body) as never);
  mockWriteFileSync.mockImplementation(() => undefined);
  return {
    assembler: new ContextAssembler(logger, () => "/workspace", null),
    logger,
  };
}

/** The document the validator wrote back, if it wrote one. */
function writtenBack(): Record<string, unknown> | null {
  const call = mockWriteFileSync.mock.calls.at(-1);
  if (!call) return null;
  return JSON.parse(String(call[1])) as Record<string, unknown>;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("validateStageContextOutput — repairable shape (#1176)", () => {
  const devDeliverable = {
    schema_version: "1.5",
    issue_number: 210,
    files_changed: ["docs/PRODUCT_REQUIREMENTS.md", "docs/a.md"],
    files_created: [],
    files_modified: ["docs/PRODUCT_REQUIREMENTS.md", "docs/a.md"],
    build_verification: { ran: true, status: "passed" },
  };

  it("lets the stage proceed", async () => {
    const { assembler } = stage(devDeliverable);
    const result = await assembler.validateStageContextOutput("feature-dev", 210);
    expect(result.error).toBeNull();
  });

  it("writes the repaired shape back, so the next stage reads what it expects", async () => {
    const { assembler } = stage(devDeliverable);
    await assembler.validateStageContextOutput("feature-dev", 210);

    const doc = writtenBack();
    expect(doc).not.toBeNull();
    expect(doc!.files_changed).toEqual({
      created: [],
      modified: ["docs/PRODUCT_REQUIREMENTS.md", "docs/a.md"],
      deleted: [],
    });
    expect(doc!.files_created).toBeUndefined();
  });

  it("records the repair so the bad emitter stays visible", async () => {
    const { assembler, logger } = stage(devDeliverable);
    await assembler.validateStageContextOutput("feature-dev", 210);

    const marker = writtenBack()!._deliverable_policy as Record<string, unknown>;
    expect(marker).toBeDefined();
    expect(JSON.stringify(marker.repairs)).toContain("dev.files_changed.from_sibling_manifest");

    const warned = vi.mocked(logger.warn).mock.calls.map((c) => String(c[0]));
    expect(warned.some((m) => m.includes("Deliverable policy applied"))).toBe(true);
  });

  it("stamps schema_version from the contract, not the skill's claim (#1177)", async () => {
    const { assembler } = stage(devDeliverable);
    await assembler.validateStageContextOutput("feature-dev", 210);
    expect(writtenBack()!.schema_version).toBe("1.8");
  });
});

describe("validateStageContextOutput — unrepairable (#1176 AC)", () => {
  it("fails when the created/modified split was never written", async () => {
    const { assembler } = stage({
      schema_version: "1.8",
      issue_number: 211,
      files_changed: ["src/a.ts", "src/b.ts"],
    });
    const result = await assembler.validateStageContextOutput("feature-dev", 211);
    expect(result.error).not.toBeNull();
    expect(result.error!.message).toContain("no_sibling_manifest");
  });

  it("fails rather than partially filling a manifest that misses a path", async () => {
    const { assembler } = stage({
      schema_version: "1.8",
      issue_number: 212,
      files_changed: ["src/a.ts", "src/unaccounted.ts"],
      files_created: ["src/a.ts"],
    });
    const result = await assembler.validateStageContextOutput("feature-dev", 212);
    expect(result.error).not.toBeNull();
    expect(result.error!.message).toContain("src/unaccounted.ts");
  });

  it("never writes a document back on a fatal outcome", async () => {
    const { assembler } = stage({
      schema_version: "1.8",
      issue_number: 211,
      files_changed: ["src/a.ts"],
    });
    await assembler.validateStageContextOutput("feature-dev", 211);
    expect(mockWriteFileSync).not.toHaveBeenCalled();
  });
});

describe("validateStageContextOutput — quarantine (#1182 AC 3)", () => {
  it("drops an unattributable gate metric instead of forwarding it", async () => {
    const { assembler } = stage({
      schema_version: "2.6",
      issue_number: 232,
      validation_status: "passed",
      gate_metrics: [
        { result: "pass", duration_ms: 12 },
        { gate_name: "lint", result: "pass" },
      ],
    });
    const result = await assembler.validateStageContextOutput("feature-validate", 232);

    // Telemetry with a missing value must not end a run…
    expect(result.error).toBeNull();
    // …but it must not reach the gate-metrics record either.
    const doc = writtenBack()!;
    expect(doc.gate_metrics).toEqual([{ gate_name: "lint", result: "pass" }]);
    // …and what remains must be labelled as incomplete.
    const marker = doc._deliverable_policy as Record<string, unknown>;
    expect(marker.untrustworthy).toEqual(["gate_metrics"]);
  });

  it("drops a skipped phase whose reason was never written", async () => {
    const { assembler } = stage({
      schema_version: "2.6",
      issue_number: 170,
      validation_status: "passed",
      skipped_phases: ["lint", { phase: "e2e", reason: "no framework" }],
    });
    const result = await assembler.validateStageContextOutput("feature-validate", 170);

    expect(result.error).toBeNull();
    const doc = writtenBack()!;
    expect(doc.skipped_phases).toEqual([{ phase: "e2e", reason: "no framework" }]);
    expect((doc._deliverable_policy as Record<string, unknown>).untrustworthy).toEqual([
      "skipped_phases",
    ]);
  });
});

describe("validateStageContextOutput — a healthy deliverable is left alone", () => {
  it("does not rewrite a conforming file", async () => {
    const { assembler } = stage({
      schema_version: "1.8",
      issue_number: 300,
      files_changed: { created: [], modified: ["src/a.ts"], deleted: [] },
      build_verification: { ran: true, status: "passed" },
    });
    const result = await assembler.validateStageContextOutput("feature-dev", 300);
    expect(result.error).toBeNull();
    expect(mockWriteFileSync).not.toHaveBeenCalled();
  });
});
