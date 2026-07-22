/**
 * HeadlessOrchestrator.mergeBlockerClassification.test.ts
 *
 * Tests for the #185 repo-config retry gate:
 * - classifyMergeBlockerRetryability(): non-retryable on a failing
 *   required-check config mismatch or unresolved ruleset blockers
 *   (via `pr ruleset-precheck --json`), retryable on clean output,
 *   and fail-open (retryable) when the binary is unavailable.
 * - writePrMergeRetryFeedback(): merges a PR_MERGE_RETRY signal into
 *   feedback-{N}.json (severity "warning" so the Go RetryEngine's
 *   blocking-only backtrack evaluation ignores it), preserving any
 *   existing signals.
 *
 * @see Issue #185 - CI-blocked merge misclassified as retryable
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

// Mutable stub state shared with the hoisted child_process factory.
const { precheckResponse, binaryPath } = vi.hoisted(() => ({
  precheckResponse: { value: "" as string },
  binaryPath: { value: "/fake/nightgauge" as string | null },
}));

vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: {
    fromVSCode: () => ({ resolve: async () => binaryPath.value }),
  },
}));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args?.[1] === "ruleset-precheck") {
      if (precheckResponse.value === "ERROR") {
        return Promise.reject(new Error("precheck exploded"));
      }
      return Promise.resolve({ stdout: precheckResponse.value, stderr: "" });
    }
    // resolveRunRepoSlug's `gh repo view` probe.
    return Promise.resolve({ stdout: "test-owner/test-repo", stderr: "" });
  };

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: "", stderr: "" });

  return { ...actual, exec: execMock, execFile: execFileMock };
});

function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

describe("HeadlessOrchestrator #185 merge-blocker retry gate", () => {
  let tmpDir: string;
  let orchestrator: HeadlessOrchestrator;

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ng-185-"));
    fs.mkdirSync(path.join(tmpDir, ".nightgauge", "pipeline"), { recursive: true });
    orchestrator = new HeadlessOrchestrator(null, createMockLogger());
    orchestrator.setWorktreeOverride(tmpDir);
    binaryPath.value = "/fake/nightgauge";
    precheckResponse.value = "";
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
    vi.clearAllMocks();
  });

  function writePrContext(issueNumber: number, prNumber: number | null): void {
    fs.writeFileSync(
      path.join(tmpDir, ".nightgauge", "pipeline", `pr-${issueNumber}.json`),
      JSON.stringify(prNumber ? { pr_number: prNumber } : {})
    );
  }

  async function classify(issueNumber: number) {
    return (orchestrator as any).classifyMergeBlockerRetryability(issueNumber, tmpDir) as Promise<{
      retryable: boolean;
      reason?: string;
    }>;
  }

  describe("classifyMergeBlockerRetryability", () => {
    it("is non-retryable when a required check is continue-on-error and failing", async () => {
      writePrContext(233, 276);
      precheckResponse.value = JSON.stringify({
        blockers: [],
        allowed_to_merge: true,
        config_mismatches: [
          {
            check: "Sentry Smoke (integration)",
            failing: true,
            remediation: "remove Sentry Smoke from required checks or drop continue-on-error",
          },
        ],
      });

      const result = await classify(233);
      expect(result.retryable).toBe(false);
      expect(result.reason).toContain("Sentry Smoke");
    });

    it("is non-retryable when unresolved ruleset blockers remain", async () => {
      writePrContext(233, 276);
      precheckResponse.value = JSON.stringify({
        blockers: ["required-check-config-mismatch:Sentry Smoke (integration)"],
        allowed_to_merge: false,
      });

      const result = await classify(233);
      expect(result.retryable).toBe(false);
      expect(result.reason).toContain("required-check-config-mismatch");
    });

    it("is retryable when the precheck reports no blockers", async () => {
      writePrContext(233, 276);
      precheckResponse.value = JSON.stringify({
        blockers: [],
        allowed_to_merge: true,
        config_mismatches: [],
      });

      const result = await classify(233);
      expect(result.retryable).toBe(true);
    });

    it("ignores non-failing config mismatches (hazard, not blocker)", async () => {
      writePrContext(233, 276);
      precheckResponse.value = JSON.stringify({
        blockers: [],
        allowed_to_merge: true,
        config_mismatches: [{ check: "Flaky Job", failing: false }],
      });

      const result = await classify(233);
      expect(result.retryable).toBe(true);
    });

    it("fails open (retryable) when the binary is unavailable", async () => {
      writePrContext(233, 276);
      binaryPath.value = null;

      const result = await classify(233);
      expect(result.retryable).toBe(true);
    });

    it("fails open (retryable) when the precheck errors", async () => {
      writePrContext(233, 276);
      precheckResponse.value = "ERROR";

      const result = await classify(233);
      expect(result.retryable).toBe(true);
    });

    it("fails open (retryable) when pr context is missing or has no PR number", async () => {
      const noFile = await classify(999);
      expect(noFile.retryable).toBe(true);

      writePrContext(233, null);
      const noNumber = await classify(233);
      expect(noNumber.retryable).toBe(true);
    });
  });

  describe("readPrBlockerRecord (#190)", () => {
    it("returns the structured blocker record when present", () => {
      fs.writeFileSync(
        path.join(tmpDir, ".nightgauge", "pipeline", "pr-233.json"),
        JSON.stringify({
          pr_number: 276,
          blocker: {
            classification: "required-check-config-mismatch:Sentry Smoke (integration)",
            remediation: "remove Sentry Smoke from required checks or drop continue-on-error",
            non_retryable: true,
          },
        })
      );

      const record = (orchestrator as any).readPrBlockerRecord(233);
      expect(record).toMatchObject({
        classification: "required-check-config-mismatch:Sentry Smoke (integration)",
        remediation: "remove Sentry Smoke from required checks or drop continue-on-error",
        prNumber: 276,
      });
    });

    it("returns null when the context has no blocker record", () => {
      writePrContext(233, 276);
      expect((orchestrator as any).readPrBlockerRecord(233)).toBeNull();
    });

    it("returns null when the context file is missing or malformed", () => {
      expect((orchestrator as any).readPrBlockerRecord(999)).toBeNull();
      fs.writeFileSync(path.join(tmpDir, ".nightgauge", "pipeline", "pr-233.json"), "{not json");
      expect((orchestrator as any).readPrBlockerRecord(233)).toBeNull();
    });
  });

  describe("writePrMergeRetryFeedback", () => {
    it("creates feedback-{N}.json with a warning-severity PR_MERGE_RETRY signal", () => {
      (orchestrator as any).writePrMergeRetryFeedback(233, "attempt 1 blocked", ["state: OPEN"]);

      const feedbackPath = path.join(tmpDir, ".nightgauge", "pipeline", "feedback-233.json");
      const ctx = JSON.parse(fs.readFileSync(feedbackPath, "utf-8"));
      expect(ctx.issue_number).toBe(233);
      expect(ctx.signals).toHaveLength(1);
      expect(ctx.signals[0]).toMatchObject({
        signal_type: "PR_MERGE_RETRY",
        emitted_by_stage: "pr-merge",
        backtrack_target_stage: "pr-merge",
        rationale: "attempt 1 blocked",
        severity: "warning",
      });
    });

    it("preserves existing signals when merging", () => {
      const feedbackPath = path.join(tmpDir, ".nightgauge", "pipeline", "feedback-233.json");
      fs.writeFileSync(
        feedbackPath,
        JSON.stringify({
          schema_version: "1.1",
          issue_number: 233,
          signals: [{ signal_type: "CONFLICT_RESOLUTION_NEEDED", severity: "blocking" }],
        })
      );

      (orchestrator as any).writePrMergeRetryFeedback(233, "attempt 1 blocked", []);

      const ctx = JSON.parse(fs.readFileSync(feedbackPath, "utf-8"));
      expect(ctx.signals).toHaveLength(2);
      expect(ctx.signals[0].signal_type).toBe("CONFLICT_RESOLUTION_NEEDED");
      expect(ctx.signals[1].signal_type).toBe("PR_MERGE_RETRY");
    });
  });
});
