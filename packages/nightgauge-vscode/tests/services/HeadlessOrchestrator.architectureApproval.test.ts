/**
 * HeadlessOrchestrator.architectureApproval.test.ts
 *
 * Tests for verifyArchitectureApproval() — the deterministic pre-check that runs
 * the `nightgauge approval-gate` binary BEFORE feature-dev launches, so a
 * high-impact-but-unapproved decision halts cleanly (with an actionable
 * "awaiting approval" alert) instead of the skill's inline gate being swallowed
 * by the deterministic-context fallback and bleeding ~$20 into feature-validate
 * as a confusing "missing-implementation" (#4220).
 *
 * @see Issue #4222 - approval gate should halt at feature-dev, not leak into validate
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import { ARCHITECTURE_APPROVAL_REQUIRED_MARKER } from "../../src/utils/failureComment";
import type { Logger } from "../../src/utils/logger";

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

// BinaryResolver returns a fake path, or null when the test wants the
// "binary unresolved → fail-open" branch.
const { binaryResolves } = vi.hoisted(() => ({ binaryResolves: { value: true } }));
vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: {
    fromVSCode: () => ({
      resolve: async () => (binaryResolves.value ? "/fake/nightgauge" : null),
    }),
  },
}));

// child_process.execFile — substitute for `nightgauge approval-gate <N> --json`.
// `scenario` selects how the gate binary responds.
const { scenario } = vi.hoisted(() => ({
  scenario: { value: "requires-approval" as string },
}));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    if (typeof cmd === "string" && args && args[0] === "approval-gate") {
      switch (scenario.value) {
        case "requires-approval": {
          // Exit 1 with the JSON verdict on stdout.
          const err: any = new Error("requires approval");
          err.code = 1;
          err.stdout = JSON.stringify({
            high_impact: true,
            requires_approval: true,
            approval_granted: false,
            reasons: ["production-touching change — irreversible blast radius (#4135)"],
          });
          err.stderr = "";
          return Promise.reject(err);
        }
        case "approved": // high-impact but already approved → exit 0
          return Promise.resolve({
            stdout: JSON.stringify({
              high_impact: true,
              requires_approval: false,
              approval_granted: true,
            }),
            stderr: "",
          });
        case "not-high-impact": // exit 0, proceed
          return Promise.resolve({
            stdout: JSON.stringify({ high_impact: false, requires_approval: false }),
            stderr: "",
          });
        case "binary-error": {
          // Non-approval failure (e.g. exit 2, no parseable stdout) → fail-open.
          const err: any = new Error("binary blew up");
          err.code = 2;
          return Promise.reject(err);
        }
        case "unparseable-approval": {
          const err: any = new Error("requires approval");
          err.code = 1;
          err.stdout = "not json {";
          return Promise.reject(err);
        }
        case "disabled-plain-text":
          // Pre-fix binaries printed a plain-text line (not JSON) on the
          // gate-disabled path with exit 0. Exit 0 IS the proceed verdict —
          // this must return null WITHOUT logging a "binary error".
          return Promise.resolve({
            stdout:
              "architecture-approval gate disabled (pipeline.architecture_approval.enabled=false)\n",
            stderr: "",
          });
      }
    }
    return Promise.resolve({ stdout: "{}", stderr: "" });
  };

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: "", stderr: "" });

  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    execSync: vi.fn().mockReturnValue(""),
    execFileSync: vi.fn().mockReturnValue("{}"),
  };
});

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

describe("HeadlessOrchestrator.verifyArchitectureApproval (Issue #4222)", () => {
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    binaryResolves.value = true;
    scenario.value = "requires-approval";
    logger = makeLogger();
  });

  it("returns an actionable Error (with the marker + reason) when approval is required", async () => {
    scenario.value = "requires-approval";
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    const result = await (orch as any).verifyArchitectureApproval(173);
    expect(result).toBeInstanceOf(Error);
    expect((result as Error).message).toContain(ARCHITECTURE_APPROVAL_REQUIRED_MARKER);
    expect((result as Error).message).toMatch(/production-touching/);
    expect((result as Error).message).toMatch(/approved:architecture/);
    // Explicitly reassures the user no spend leaked.
    expect((result as Error).message).toMatch(/NO development or validation cost/i);
  });

  it("returns null when the decision is high-impact but already approved", async () => {
    scenario.value = "approved";
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    expect(await (orch as any).verifyArchitectureApproval(173)).toBeNull();
  });

  it("returns null when the decision is not high-impact", async () => {
    scenario.value = "not-high-impact";
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    expect(await (orch as any).verifyArchitectureApproval(87)).toBeNull();
  });

  it("fails open (null) when the binary cannot be resolved", async () => {
    binaryResolves.value = false;
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    expect(await (orch as any).verifyArchitectureApproval(173)).toBeNull();
  });

  it("fails open (null) on a non-approval binary error", async () => {
    scenario.value = "binary-error";
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    expect(await (orch as any).verifyArchitectureApproval(173)).toBeNull();
  });

  it("fails open (null) when the exit-1 payload is unparseable", async () => {
    scenario.value = "unparseable-approval";
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    expect(await (orch as any).verifyArchitectureApproval(173)).toBeNull();
  });

  it("treats exit-0 with non-JSON stdout as proceed — no spurious 'binary error' log", async () => {
    // Regression: the gate-disabled path of older binaries printed plain text
    // on exit 0; the pre-check's JSON.parse threw and mislabeled every
    // disabled-gate run as "binary error" (bowlsheet dogfooding, 2026-07-11).
    scenario.value = "disabled-plain-text";
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    expect(await (orch as any).verifyArchitectureApproval(237)).toBeNull();
    const debugCalls = (logger.debug as ReturnType<typeof vi.fn>).mock.calls.map((c) =>
      String(c[0])
    );
    expect(debugCalls.some((m) => m.includes("binary error"))).toBe(false);
  });
});
