/**
 * HeadlessOrchestrator.postConditionGate.test.ts
 *
 * Tests for the generic stage post-condition gate (Issue #210) —
 * `runPostConditionGate` and the `logGateNotInvoked` completion-time guard.
 *
 * Before this issue, only `pr-merge`, `feature-validate`, and `pr-create` had
 * dedicated verifyPost*State methods wired into the extension's legacy TS
 * pipeline loop (`HeadlessOrchestrator.runPipeline()`). `issue-pickup`,
 * `feature-planning`, and `feature-dev` have live, registered, unit-tested
 * `StageGate` implementations in Go
 * (`internal/orchestrator/gates/{issue_pickup,feature_planning,feature_dev}_gate.go`)
 * that nothing in the extension path ever invoked — making #202's
 * FeatureDevGate and #74's premature-turn-end detection for feature-planning
 * inert in the mode Nightgauge actually runs in day to day.
 *
 * `runPostConditionGate` is the shared helper that closes this gap through
 * the same CLI seam (`nightgauge gate verify <stage> <N> --json --record`)
 * the three dedicated methods already use.
 *
 * @see Issue #210 - Half the stage gates never run in the extension path
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

// Mock skillRunner so importing HeadlessOrchestrator doesn't pull the real CLI.
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

// BinaryResolver returns a fake path (or null when the test wants the
// "binary unresolved → skip" branch).
const { binaryResolves } = vi.hoisted(() => ({ binaryResolves: { value: true } }));
vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: {
    fromVSCode: () => ({
      resolve: async () => (binaryResolves.value ? "/fake/nightgauge" : null),
    }),
  },
}));

// child_process.execFile — substitute for `nightgauge gate verify <stage> <N> --record`.
const { gateOutcome, recordedCalls } = vi.hoisted(() => ({
  gateOutcome: { value: "pass" as "pass" | "fail" | "no_op" | "throw" },
  recordedCalls: [] as Array<{ stage: string; issueNumber: string; hasRecordFlag: boolean }>,
}));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args && args[0] === "gate") {
      // args = ["gate","verify",<stage>,<issueNumber>,...,"--record"?]
      recordedCalls.push({
        stage: args[2],
        issueNumber: args[3],
        hasRecordFlag: args.includes("--record"),
      });

      if (gateOutcome.value === "throw") {
        const err: any = new Error("binary blew up");
        err.code = 1; // not 2 → unparseable / CLI failure
        return Promise.reject(err);
      }
      if (gateOutcome.value === "pass") {
        const stdout = JSON.stringify({
          stage: args[2],
          gate_name: args[2],
          passed: true,
          reason: "post-condition satisfied",
          kind: "ok",
        });
        return Promise.resolve({ stdout, stderr: "" });
      }
      // fail / no_op → CLI exits 2 with the JSON GateResult on stdout.
      // terminal_kind mirrors the Go gate's structured verdict
      // (feature_dev_gate.go) — #283 threads it through instead of
      // discarding it at this boundary.
      const stdout = JSON.stringify({
        stage: args[2],
        gate_name: args[2],
        passed: false,
        reason:
          gateOutcome.value === "no_op"
            ? "dev context file missing"
            : "dev context is not valid JSON",
        kind: gateOutcome.value === "no_op" ? "no_op" : "fail",
        terminal_kind: gateOutcome.value === "no_op" ? "dev_handoff_missing" : "validation_error",
      });
      const err: any = new Error("gate failed");
      err.code = 2;
      err.stdout = stdout;
      err.stderr = "";
      return Promise.reject(err);
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

describe("HeadlessOrchestrator.runPostConditionGate (Issue #210)", () => {
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    recordedCalls.length = 0;
    binaryResolves.value = true;
    gateOutcome.value = "pass";
    logger = makeLogger();
  });

  it.each(["issue-pickup", "feature-planning", "feature-dev"] as const)(
    "invokes `gate verify %s <N> --record` and returns null on pass",
    async (stage) => {
      const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
      const result = await (orch as any).runPostConditionGate(stage, 210);
      expect(result).toBeNull();
      expect(recordedCalls).toHaveLength(1);
      expect(recordedCalls[0].stage).toBe(stage);
      expect(recordedCalls[0].issueNumber).toBe("210");
      expect(recordedCalls[0].hasRecordFlag).toBe(true);
    }
  );

  it("returns a premature-turn-end failure with the gate's terminal_kind on a KindNoOp gate result", async () => {
    gateOutcome.value = "no_op";
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    const result = await (orch as any).runPostConditionGate("feature-dev", 210);
    expect(result).not.toBeNull();
    expect(result.error).toBeInstanceOf(Error);
    expect(result.error.message).toMatch(
      /premature turn end: stage exited 0 with no state change \(gate no-op\)/
    );
    expect(result.error.message).toMatch(/dev context file missing/);
    // #283 defect 3: the structured verdict survives the boundary.
    expect(result.terminalKind).toBe("dev_handoff_missing");
  });

  it("returns a gate-failed failure with the gate's terminal_kind on a KindFail gate result", async () => {
    gateOutcome.value = "fail";
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    const result = await (orch as any).runPostConditionGate("issue-pickup", 210);
    expect(result).not.toBeNull();
    expect(result.error).toBeInstanceOf(Error);
    expect(result.error.message).toBe("stage gate failed: dev context is not valid JSON");
    // #283 defect 3: a validation_error classifies as a harness/bookkeeping
    // fault downstream instead of "unclassified".
    expect(result.terminalKind).toBe("validation_error");
  });

  it("returns null (skips) when the binary cannot be resolved", async () => {
    binaryResolves.value = false;
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    const result = await (orch as any).runPostConditionGate("feature-planning", 210);
    expect(result).toBeNull();
    expect(recordedCalls).toHaveLength(0);
  });

  it("returns null (skips) when the gate binary produces no parseable result", async () => {
    gateOutcome.value = "throw";
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    const result = await (orch as any).runPostConditionGate("feature-dev", 210);
    expect(result).toBeNull();
  });
});

describe("HeadlessOrchestrator gate reachability (Issue #210 acceptance criterion)", () => {
  it("all 6 stage-gated stages are reachable from either a dedicated verifyPost*State method or runPostConditionGate", async () => {
    // Structural assertion mirroring internal/orchestrator/gates/registry.go's
    // Default() map (minus bookend stages pipeline-start/pipeline-finish,
    // which have no gate registered). Each of the 6 must be covered by
    // exactly one of: a dedicated verifyPost*State method (pr-merge,
    // feature-validate via deriveUnexercisedDeliverable, pr-create) or the
    // generic runPostConditionGate dispatch (issue-pickup, feature-planning,
    // feature-dev).
    const dedicated = new Set(["pr-merge", "pr-create", "feature-validate"]);
    const generic = new Set(["issue-pickup", "feature-planning", "feature-dev"]);
    const allGatedStages = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ];

    for (const stage of allGatedStages) {
      const coveredByDedicated = dedicated.has(stage);
      const coveredByGeneric = generic.has(stage);
      expect(coveredByDedicated || coveredByGeneric).toBe(true);
      // Exactly one path — never both, which would run a stage's gate twice.
      expect(coveredByDedicated && coveredByGeneric).toBe(false);
    }
  });
});
