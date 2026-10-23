/**
 * HeadlessOrchestrator.postCreateVerification.test.ts
 *
 * Tests for verifyPostCreateState() — the TS-path mirror of the deterministic
 * pr-create post-condition gate (internal/orchestrator/gates/pr_create_gate.go).
 *
 * The pr-create skill can exit 0 having written a context/assessment file but
 * never actually opened a PR (push blocked, prompt dismissed in autonomous
 * mode). The Go scheduler runs PrCreateGate inline; the legacy TS
 * HeadlessOrchestrator path did not, so a false success slipped through
 * (AcmeApp #42 / #3867). verifyPostCreateState closes that gap.
 *
 * @see Issue #3869 - TS HeadlessOrchestrator ran no pr-create post-condition gate
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

// child_process.execFile — substitute for `nightgauge gate verify pr-create`.
const { gatePasses, gateThrows } = vi.hoisted(() => ({
  gatePasses: { value: true },
  gateThrows: { value: false },
}));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args && args[0] === "gate") {
      // args = ["gate","verify","pr-create",N,...]
      if (gateThrows.value) {
        const err: any = new Error("binary blew up");
        err.code = 1; // not 2 → unparseable / CLI failure
        return Promise.reject(err);
      }
      if (gatePasses.value) {
        const stdout = JSON.stringify({
          stage: "pr-create",
          gate_name: "pr-create",
          passed: true,
          reason: "PR is OPEN with the recorded number",
          evidence: ["pr=123"],
        });
        return Promise.resolve({ stdout, stderr: "" });
      }
      // passed=false → CLI exits 2 with the JSON GateResult on stdout.
      const stdout = JSON.stringify({
        stage: "pr-create",
        gate_name: "pr-create",
        passed: false,
        reason: "pr context file missing",
        evidence: ["expected .nightgauge/pipeline/pr-42.json"],
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

describe("HeadlessOrchestrator.verifyPostCreateState (Issue #3869)", () => {
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    binaryResolves.value = true;
    gatePasses.value = true;
    gateThrows.value = false;
    logger = makeLogger();
  });

  it("returns null when the gate confirms an OPEN PR", async () => {
    gatePasses.value = true;
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    const result = await (orch as any).verifyPostCreateState(42);
    expect(result).toBeNull();
  });

  it("returns an Error when the gate fails (no PR / missing context) — closes the false-success hole", async () => {
    gatePasses.value = false;
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    const result = await (orch as any).verifyPostCreateState(42);
    expect(result).toBeInstanceOf(Error);
    expect((result as Error).message).toMatch(/no open PR exists/i);
    expect((result as Error).message).toMatch(/pr context file missing/i);
  });

  it("returns null (skips) when the binary cannot be resolved", async () => {
    binaryResolves.value = false;
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    const result = await (orch as any).verifyPostCreateState(42);
    expect(result).toBeNull();
  });

  it("returns null (skips) when the gate binary produces no parseable result", async () => {
    gateThrows.value = true;
    const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
    const result = await (orch as any).verifyPostCreateState(42);
    expect(result).toBeNull();
  });
});
