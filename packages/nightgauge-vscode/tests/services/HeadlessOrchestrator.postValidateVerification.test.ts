/**
 * HeadlessOrchestrator.postValidateVerification.test.ts
 *
 * Tests for verifyPostValidateState() — the TS-path mirror of the deterministic
 * feature-validate post-condition gate (internal/orchestrator/gates/
 * feature_validate_gate.go).
 *
 * The feature-validate skill exits 0 even when validation FAILS: on a hard-gate
 * failure it writes validation_status:"failed" (+ an errorCategory) and leaves
 * the code uncommitted "on disk for retry" rather than exiting non-zero,
 * delegating the halt decision to the orchestrator. The Go scheduler runs
 * FeatureValidateGate inline; the legacy TS HeadlessOrchestrator path did not,
 * so a failed validation advanced into pr-create, which found no commit to push
 * and aborted at the no-commits gate — after burning the pr-create spend and
 * (once the worktree was pruned) destroying the uncommitted retry code.
 *
 * @see Issue #4220 - failed feature-validate advanced to pr-create
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
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

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

/** Build an orchestrator whose working directory is a scratch dir we control. */
function makeOrch(workdir: string, logger: Logger) {
  const orch = new HeadlessOrchestrator(null as any, logger, { contextFileWaitMs: 0 });
  vi.spyOn(orch as any, "getWorkingDirectory").mockReturnValue(workdir);
  return orch;
}

function writeValidateContext(workdir: string, issueNumber: number, body: object): void {
  const dir = path.join(workdir, ".nightgauge", "pipeline");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, `validate-${issueNumber}.json`), JSON.stringify(body));
}

describe("HeadlessOrchestrator.verifyPostValidateState (Issue #4220)", () => {
  let logger: Logger;
  let workdir: string;

  beforeEach(() => {
    vi.clearAllMocks();
    logger = makeLogger();
    workdir = fs.mkdtempSync(path.join(os.tmpdir(), "post-validate-"));
  });

  afterEach(() => {
    fs.rmSync(workdir, { recursive: true, force: true });
  });

  it("returns null when validation passed", () => {
    writeValidateContext(workdir, 42, { validation_status: "passed" });
    const result = (makeOrch(workdir, logger) as any).verifyPostValidateState(42);
    expect(result).toBeNull();
  });

  it("returns an Error when validation failed — halts before pr-create", () => {
    writeValidateContext(workdir, 42, {
      validation_status: "failed",
      errorCategory: "build-failed",
    });
    const result = (makeOrch(workdir, logger) as any).verifyPostValidateState(42);
    expect(result).toBeInstanceOf(Error);
    expect((result as Error).message).toMatch(/validation_status="failed"/i);
    expect((result as Error).message).toMatch(/build-failed/);
    expect((result as Error).message).toMatch(/no-commits-ahead/i);
  });

  // Issue #326: the failed-verdict message must carry the stable
  // `[validation-failed]` marker so ClassifyTerminalKind/classifyTerminalKind
  // record the honest organic quality-gate failure instead of falling through
  // to the generic subagent_crash fallback.
  it("stamps the [validation-failed] marker on a failed verdict (#326)", () => {
    writeValidateContext(workdir, 42, {
      validation_status: "failed",
      errorCategory: "tests-failed",
    });
    const result = (makeOrch(workdir, logger) as any).verifyPostValidateState(42);
    expect(result).toBeInstanceOf(Error);
    expect((result as Error).message).toContain("[validation-failed]");
  });

  // The exact production scenario: a Flutter/UI validation failure whose
  // errorCategory is out of the strict enum. The halt must NOT depend on the
  // errorCategory parsing — validation_status alone is the signal.
  it("halts on a failed verdict even when errorCategory is out of the enum", () => {
    writeValidateContext(workdir, 238, {
      validation_status: "failed",
      errorCategory: "mobile-apk-build-failed",
    });
    const result = (makeOrch(workdir, logger) as any).verifyPostValidateState(238);
    expect(result).toBeInstanceOf(Error);
    expect((result as Error).message).toMatch(/mobile-apk-build-failed/);
  });

  it.each(["partial", "skipped", "passed"])("returns null for non-failed verdict: %s", (status) => {
    writeValidateContext(workdir, 42, { validation_status: status });
    const result = (makeOrch(workdir, logger) as any).verifyPostValidateState(42);
    expect(result).toBeNull();
  });

  it("returns null (fail-open) when the context file is missing", () => {
    const result = (makeOrch(workdir, logger) as any).verifyPostValidateState(999);
    expect(result).toBeNull();
  });

  it("returns null (fail-open) when the context file is unreadable JSON", () => {
    const dir = path.join(workdir, ".nightgauge", "pipeline");
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, "validate-42.json"), "{ not valid json");
    const result = (makeOrch(workdir, logger) as any).verifyPostValidateState(42);
    expect(result).toBeNull();
  });

  it("returns null when validation_status is absent (no verdict to judge)", () => {
    writeValidateContext(workdir, 42, { issue_number: 42 });
    const result = (makeOrch(workdir, logger) as any).verifyPostValidateState(42);
    expect(result).toBeNull();
  });
});
