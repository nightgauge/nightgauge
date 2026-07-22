/**
 * Tests for StaleSlotRecoveryService
 *
 * Verifies stale concurrent slot detection and recovery:
 * - Empty result when worktree directory doesn't exist
 * - Empty result when no stages are running
 * - Recovery of stale slots with no PID past threshold
 * - Skipping of running stages within threshold
 * - NOT recovering alive processes past threshold (#3840 — never kill live work)
 * - Marking dead processes as failed (marked-failed)
 * - Only recovering the first stale stage per worktree
 * - Graceful handling when failStage throws
 *
 * @see StaleSlotRecoveryService.ts
 * @see Issue #1643 — Stale concurrent slot recovery on extension reload
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// Module mocks (hoisted before imports)
// ---------------------------------------------------------------------------

vi.mock("node:fs/promises", () => ({
  readdir: vi.fn(),
  readFile: vi.fn(),
  stat: vi.fn(),
}));

vi.mock("../../src/services/PipelineStateService", () => ({
  PipelineStateService: {
    createForWorktree: vi.fn(),
  },
}));

// ---------------------------------------------------------------------------
// Imports (after mocks)
// ---------------------------------------------------------------------------

import * as fsp from "node:fs/promises";
import { StaleSlotRecoveryService } from "../../src/services/StaleSlotRecoveryService";
import { PipelineStateService } from "../../src/services/PipelineStateService";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const REPO_ROOT = "/test-repo";
const WORKTREE_BASE = ".worktrees";
const WORKTREE_DIR = `${REPO_ROOT}/${WORKTREE_BASE}`;

/** Default threshold: 10 minutes in ms */
const DEFAULT_THRESHOLD_MS = 10 * 60 * 1000;

/** Stale threshold used in tests for easier control */
const TEST_THRESHOLD_MS = 60_000;

const mockLogger = {
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
  debug: vi.fn(),
};

function makeMockDirent(name: string, isDir = true) {
  return {
    name,
    isDirectory: () => isDir,
  };
}

function makeStateJson(overrides: Record<string, unknown> = {}): string {
  const base = {
    title: "Test Issue",
    branch: "feat/123-test",
    stages: {},
    ...overrides,
  };
  return JSON.stringify(base);
}

function makeRunningStageState(startedAt: Date, pid: number | null = null) {
  return {
    "feature-dev": {
      status: "running",
      started_at: startedAt.toISOString(),
      process_pid: pid,
    },
  };
}

/** Returns a Date that is `ms` milliseconds in the past */
function msAgo(ms: number): Date {
  return new Date(Date.now() - ms);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("StaleSlotRecoveryService", () => {
  let mockFailStage: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.clearAllMocks();

    mockFailStage = vi.fn().mockResolvedValue(undefined);
    vi.mocked(PipelineStateService.createForWorktree).mockReturnValue({
      failStage: mockFailStage,
    } as unknown as InstanceType<typeof PipelineStateService>);
  });

  // ---------------------------------------------------------------------------
  // Worktree directory absent
  // ---------------------------------------------------------------------------

  describe("when worktree directory does not exist", () => {
    it("returns empty array", async () => {
      vi.mocked(fsp.readdir).mockRejectedValue(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      const service = new StaleSlotRecoveryService(
        REPO_ROOT,
        WORKTREE_BASE,
        mockLogger,
        TEST_THRESHOLD_MS
      );

      const result = await service.recoverStaleSlots();

      expect(result).toEqual([]);
    });
  });

  // ---------------------------------------------------------------------------
  // No running stages
  // ---------------------------------------------------------------------------

  describe("when no worktrees have running stages", () => {
    it("returns empty array when all stages are complete", async () => {
      vi.mocked(fsp.readdir).mockResolvedValue([makeMockDirent("issue-42")] as unknown as Awaited<
        ReturnType<typeof fsp.readdir>
      >);

      vi.mocked(fsp.readFile).mockResolvedValue(
        makeStateJson({
          stages: {
            "feature-dev": {
              status: "complete",
              started_at: msAgo(TEST_THRESHOLD_MS + 1000).toISOString(),
              process_pid: null,
            },
          },
        }) as unknown as Buffer
      );

      const service = new StaleSlotRecoveryService(
        REPO_ROOT,
        WORKTREE_BASE,
        mockLogger,
        TEST_THRESHOLD_MS
      );

      const result = await service.recoverStaleSlots();

      expect(result).toEqual([]);
      expect(mockFailStage).not.toHaveBeenCalled();
    });

    it("returns empty array when worktree directory has no issue-NNN subdirectories", async () => {
      vi.mocked(fsp.readdir).mockResolvedValue([
        makeMockDirent("some-other-dir"),
        makeMockDirent("notissue-42"),
      ] as unknown as Awaited<ReturnType<typeof fsp.readdir>>);

      const service = new StaleSlotRecoveryService(
        REPO_ROOT,
        WORKTREE_BASE,
        mockLogger,
        TEST_THRESHOLD_MS
      );

      const result = await service.recoverStaleSlots();

      expect(result).toEqual([]);
    });
  });

  // ---------------------------------------------------------------------------
  // Stale slot: no PID, past threshold
  // ---------------------------------------------------------------------------

  describe("when a running stage has no PID and elapsed > threshold", () => {
    it("marks the stage as failed and returns stale slot info", async () => {
      const startedAt = msAgo(TEST_THRESHOLD_MS + 5000);

      vi.mocked(fsp.readdir).mockResolvedValue([makeMockDirent("issue-42")] as unknown as Awaited<
        ReturnType<typeof fsp.readdir>
      >);

      vi.mocked(fsp.readFile).mockResolvedValue(
        makeStateJson({
          title: "My Feature Issue",
          branch: "feat/42-my-feature",
          stages: makeRunningStageState(startedAt, null),
        }) as unknown as Buffer
      );

      const service = new StaleSlotRecoveryService(
        REPO_ROOT,
        WORKTREE_BASE,
        mockLogger,
        TEST_THRESHOLD_MS
      );

      const result = await service.recoverStaleSlots();

      expect(result).toHaveLength(1);
      expect(result[0]).toMatchObject({
        issueNumber: 42,
        title: "My Feature Issue",
        branch: "feat/42-my-feature",
        staleStage: "feature-dev",
        processAlive: false,
        action: "marked-failed",
      });
      expect(result[0].staleSinceMs).toBeGreaterThanOrEqual(TEST_THRESHOLD_MS);
      expect(mockFailStage).toHaveBeenCalledWith(
        "feature-dev",
        expect.stringContaining("[stale-slot-orphan]")
      );
    });
  });

  // ---------------------------------------------------------------------------
  // Running stage within threshold — should be skipped
  // ---------------------------------------------------------------------------

  describe("when a running stage elapsed < threshold and has no PID", () => {
    it("skips the stage (still genuinely running)", async () => {
      const startedAt = msAgo(TEST_THRESHOLD_MS - 5000);

      vi.mocked(fsp.readdir).mockResolvedValue([makeMockDirent("issue-55")] as unknown as Awaited<
        ReturnType<typeof fsp.readdir>
      >);

      vi.mocked(fsp.readFile).mockResolvedValue(
        makeStateJson({
          stages: makeRunningStageState(startedAt, null),
        }) as unknown as Buffer
      );

      const service = new StaleSlotRecoveryService(
        REPO_ROOT,
        WORKTREE_BASE,
        mockLogger,
        TEST_THRESHOLD_MS
      );

      const result = await service.recoverStaleSlots();

      expect(result).toEqual([]);
      expect(mockFailStage).not.toHaveBeenCalled();
    });
  });

  // ---------------------------------------------------------------------------
  // Alive process past threshold → NOT recovered (#3840 regression guard)
  // ---------------------------------------------------------------------------

  describe("when process is alive and past threshold (#3840)", () => {
    it("does NOT kill or fail a live, actively-running stage", async () => {
      // Pre-#3840 this SIGTERM'd a healthy 105-turn feature-dev at the 10-min
      // mark on extension reload and falsely recorded it failed. A live process
      // is doing real work — never touch it here regardless of elapsed time.
      const startedAt = msAgo(TEST_THRESHOLD_MS + 5_000);
      const pid = 99999;

      vi.mocked(fsp.readdir).mockResolvedValue([makeMockDirent("issue-77")] as unknown as Awaited<
        ReturnType<typeof fsp.readdir>
      >);

      vi.mocked(fsp.readFile).mockResolvedValue(
        makeStateJson({
          stages: makeRunningStageState(startedAt, pid),
        }) as unknown as Buffer
      );

      // Simulate an alive process: kill(pid, 0) succeeds (no throw).
      const killSpy = vi.spyOn(process, "kill").mockImplementation((_pid, _sig) => true);

      const service = new StaleSlotRecoveryService(
        REPO_ROOT,
        WORKTREE_BASE,
        mockLogger,
        TEST_THRESHOLD_MS
      );

      const result = await service.recoverStaleSlots();

      // No recovery, no failStage, and crucially NO termination signal.
      expect(result).toEqual([]);
      expect(mockFailStage).not.toHaveBeenCalled();
      expect(killSpy).not.toHaveBeenCalledWith(pid, "SIGTERM");
      expect(killSpy).not.toHaveBeenCalledWith(pid, "SIGKILL");

      killSpy.mockRestore();
    });
  });

  // ---------------------------------------------------------------------------
  // Dead process (PID present, isProcessAlive returns false) → marked-failed
  // ---------------------------------------------------------------------------

  describe("when process PID is present but process is dead", () => {
    it("marks stage as failed without killing", async () => {
      const startedAt = msAgo(TEST_THRESHOLD_MS + 5000);
      const pid = 88888;

      vi.mocked(fsp.readdir).mockResolvedValue([makeMockDirent("issue-88")] as unknown as Awaited<
        ReturnType<typeof fsp.readdir>
      >);

      vi.mocked(fsp.readFile).mockResolvedValue(
        makeStateJson({
          stages: makeRunningStageState(startedAt, pid),
        }) as unknown as Buffer
      );

      // Simulate dead process: kill(pid, 0) throws ESRCH
      const killSpy = vi.spyOn(process, "kill").mockImplementation(() => {
        throw Object.assign(new Error("ESRCH"), { code: "ESRCH" });
      });

      const service = new StaleSlotRecoveryService(
        REPO_ROOT,
        WORKTREE_BASE,
        mockLogger,
        TEST_THRESHOLD_MS
      );

      const result = await service.recoverStaleSlots();

      expect(result).toHaveLength(1);
      expect(result[0]).toMatchObject({
        issueNumber: 88,
        staleStage: "feature-dev",
        processAlive: false,
        action: "marked-failed",
      });
      expect(mockFailStage).toHaveBeenCalledWith(
        "feature-dev",
        expect.stringContaining(`PID ${pid}`)
      );
      // SIGTERM should NOT have been called (process already dead)
      expect(killSpy).not.toHaveBeenCalledWith(pid, "SIGTERM");

      killSpy.mockRestore();
    });
  });

  // ---------------------------------------------------------------------------
  // Only first stale stage per worktree is recovered (break after first match)
  // ---------------------------------------------------------------------------

  describe("when multiple stages are stale in one worktree", () => {
    it("recovers only the first stale stage and breaks", async () => {
      const startedAt = msAgo(TEST_THRESHOLD_MS + 5000);

      vi.mocked(fsp.readdir).mockResolvedValue([makeMockDirent("issue-100")] as unknown as Awaited<
        ReturnType<typeof fsp.readdir>
      >);

      vi.mocked(fsp.readFile).mockResolvedValue(
        makeStateJson({
          stages: {
            "feature-planning": {
              status: "running",
              started_at: startedAt.toISOString(),
              process_pid: null,
            },
            "feature-dev": {
              status: "running",
              started_at: startedAt.toISOString(),
              process_pid: null,
            },
          },
        }) as unknown as Buffer
      );

      const service = new StaleSlotRecoveryService(
        REPO_ROOT,
        WORKTREE_BASE,
        mockLogger,
        TEST_THRESHOLD_MS
      );

      const result = await service.recoverStaleSlots();

      // Only one stage recovered per worktree
      expect(result).toHaveLength(1);
      expect(mockFailStage).toHaveBeenCalledTimes(1);
    });
  });

  // ---------------------------------------------------------------------------
  // failStage throws — should log warning and continue
  // ---------------------------------------------------------------------------

  describe("when failStage throws", () => {
    it("logs a warning and continues without adding to recovered list", async () => {
      const startedAt = msAgo(TEST_THRESHOLD_MS + 5000);

      vi.mocked(fsp.readdir).mockResolvedValue([
        makeMockDirent("issue-200"),
        makeMockDirent("issue-201"),
      ] as unknown as Awaited<ReturnType<typeof fsp.readdir>>);

      vi.mocked(fsp.readFile)
        .mockResolvedValueOnce(
          makeStateJson({
            stages: makeRunningStageState(startedAt, null),
          }) as unknown as Buffer
        )
        .mockResolvedValueOnce(
          makeStateJson({
            title: "Second Issue",
            branch: "feat/201",
            stages: makeRunningStageState(startedAt, null),
          }) as unknown as Buffer
        );

      // First worktree: failStage throws; second: succeeds
      const secondFailStage = vi.fn().mockResolvedValue(undefined);
      vi.mocked(PipelineStateService.createForWorktree)
        .mockReturnValueOnce({
          failStage: vi.fn().mockRejectedValue(new Error("IPC error")),
        } as unknown as InstanceType<typeof PipelineStateService>)
        .mockReturnValueOnce({
          failStage: secondFailStage,
        } as unknown as InstanceType<typeof PipelineStateService>);

      const service = new StaleSlotRecoveryService(
        REPO_ROOT,
        WORKTREE_BASE,
        mockLogger,
        TEST_THRESHOLD_MS
      );

      const result = await service.recoverStaleSlots();

      // First worktree failed → not in result; second succeeded → in result
      expect(result).toHaveLength(1);
      expect(result[0].issueNumber).toBe(201);
      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Failed to mark stale stage as failed",
        expect.objectContaining({ issueNumber: 200 })
      );
    });
  });

  // ---------------------------------------------------------------------------
  // State file missing or invalid JSON
  // ---------------------------------------------------------------------------

  describe("when state file is missing or contains invalid JSON", () => {
    it("skips the worktree and returns empty array", async () => {
      vi.mocked(fsp.readdir).mockResolvedValue([makeMockDirent("issue-300")] as unknown as Awaited<
        ReturnType<typeof fsp.readdir>
      >);

      vi.mocked(fsp.readFile).mockRejectedValue(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      const service = new StaleSlotRecoveryService(
        REPO_ROOT,
        WORKTREE_BASE,
        mockLogger,
        TEST_THRESHOLD_MS
      );

      const result = await service.recoverStaleSlots();

      expect(result).toEqual([]);
      expect(mockFailStage).not.toHaveBeenCalled();
    });
  });

  // ---------------------------------------------------------------------------
  // Worktree path construction
  // ---------------------------------------------------------------------------

  describe("worktree path construction", () => {
    it("reads state.json from the correct path inside the worktree", async () => {
      const startedAt = msAgo(TEST_THRESHOLD_MS + 5000);

      vi.mocked(fsp.readdir).mockResolvedValue([makeMockDirent("issue-42")] as unknown as Awaited<
        ReturnType<typeof fsp.readdir>
      >);

      vi.mocked(fsp.readFile).mockResolvedValue(
        makeStateJson({
          stages: makeRunningStageState(startedAt, null),
        }) as unknown as Buffer
      );

      const service = new StaleSlotRecoveryService(
        REPO_ROOT,
        WORKTREE_BASE,
        mockLogger,
        TEST_THRESHOLD_MS
      );

      await service.recoverStaleSlots();

      expect(fsp.readFile).toHaveBeenCalledWith(
        `${WORKTREE_DIR}/issue-42/.nightgauge/pipeline/state.json`,
        "utf-8"
      );

      expect(PipelineStateService.createForWorktree).toHaveBeenCalledWith(
        `${WORKTREE_DIR}/issue-42`
      );
    });
  });
});
