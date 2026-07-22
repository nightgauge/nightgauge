/**
 * RunStateManager tests (Issue #3238)
 *
 * Cover the lifecycle state machine, atomic-write contract, concurrent-run
 * detection, schema-version enforcement, and the #3237 orphaned-state fixture.
 */
import { describe, it, expect, beforeEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import { RunStateManager, uuidV7 } from "../context/RunStateManager.js";
import {
  ConcurrentRunRefused,
  SchemaVersionMismatch,
  WorktreeMissing,
} from "../errors/PipelineStateErrors.js";

async function tmpDir(): Promise<string> {
  return await fs.mkdtemp(path.join(os.tmpdir(), "runstate-"));
}

describe("uuidV7", () => {
  it("returns a 36-char canonical UUID with version=7", () => {
    const id = uuidV7();
    expect(id).toHaveLength(36);
    const parts = id.split("-");
    expect(parts).toHaveLength(5);
    expect(parts[2][0]).toBe("7");
    expect(["8", "9", "a", "b"]).toContain(parts[3][0]);
  });

  it("is monotonically time-ordered (sortable by ms)", () => {
    const a = uuidV7();
    // small forced delay so ms tick advances
    const start = Date.now();
    while (Date.now() === start) {
      /* spin one ms */
    }
    const b = uuidV7();
    expect(a < b).toBe(true);
  });
});

describe("RunStateManager", () => {
  let dir: string;
  let mgr: RunStateManager;

  beforeEach(async () => {
    dir = await tmpDir();
    mgr = new RunStateManager(dir);
  });

  describe("markRunning", () => {
    it("creates a fresh run-state.json on empty dir", async () => {
      const rs = await mgr.markRunning({ issue_number: 42, branch: "feat/x" });
      expect(rs.state).toBe("running");
      expect(rs.run_id).toMatch(
        /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
      );
      expect(rs.attempt_number).toBe(1);
      expect(rs.completed_stages).toEqual([]);
      expect(rs.resume_from_stage).toBe("issue-pickup");
      const onDisk = await mgr.read();
      expect(onDisk?.run_id).toBe(rs.run_id);
    });

    it("refuses concurrent runs unless force=true", async () => {
      await mgr.markRunning({ issue_number: 1, branch: "b" });
      await expect(mgr.markRunning({ issue_number: 1, branch: "b" })).rejects.toBeInstanceOf(
        ConcurrentRunRefused
      );
      // force overrides
      const forced = await mgr.markRunning({ issue_number: 1, branch: "b", force: true });
      expect(forced.state).toBe("running");
    });
  });

  describe("markPaused", () => {
    it("preserves branch + worktree across stop (ADR-001)", async () => {
      const rs = await mgr.markRunning({
        issue_number: 7,
        branch: "feat/preserve",
        worktree_path: "/tmp/wt-7",
      });
      const paused = await mgr.markPaused("user clicked stop", "feature-dev");
      expect(paused.state).toBe("paused");
      expect(paused.run_id).toBe(rs.run_id);
      expect(paused.branch).toBe("feat/preserve");
      expect(paused.worktree_path).toBe("/tmp/wt-7");
      expect(paused.reason).toBe("user clicked stop");
      expect(paused.recovery_actions).toEqual(["resume", "restart", "discard"]);
    });

    it("rejects illegal transitions", async () => {
      await mgr.markRunning({ issue_number: 1, branch: "b" });
      await mgr.markCompleted();
      await expect(mgr.markPaused("late stop")).rejects.toThrow(/illegal lifecycle transition/);
    });
  });

  describe("resume", () => {
    it("paused → running adds a new attempt", async () => {
      await mgr.markRunning({ issue_number: 1, branch: "b" });
      await mgr.markPaused("stop");
      const resumed = await mgr.resume();
      expect(resumed.state).toBe("running");
      expect(resumed.attempt_number).toBe(2);
      expect(resumed.attempts).toHaveLength(2);
    });

    it("refuses to resume from non-paused states", async () => {
      await mgr.markRunning({ issue_number: 1, branch: "b" });
      await expect(mgr.resume()).rejects.toThrow(/cannot resume/);
    });
  });

  describe("markStageComplete", () => {
    it("advances resume_from_stage to the next pipeline stage", async () => {
      await mgr.markRunning({ issue_number: 1, branch: "b" });
      const after = await mgr.markStageComplete("issue-pickup");
      expect(after.completed_stages).toEqual(["issue-pickup"]);
      expect(after.resume_from_stage).toBe("feature-planning");
    });

    it("is idempotent", async () => {
      await mgr.markRunning({ issue_number: 1, branch: "b" });
      await mgr.markStageComplete("issue-pickup");
      const again = await mgr.markStageComplete("issue-pickup");
      expect(again.completed_stages).toEqual(["issue-pickup"]);
    });
  });

  describe("detectResume", () => {
    it("returns kind=fresh when no state and no branch/context", async () => {
      const det = await mgr.detectResume({});
      expect(det.kind).toBe("fresh");
    });

    it("returns kind=orphaned for the #3237 fixture (branch present, no context, no run-state)", async () => {
      const det = await mgr.detectResume({ branch: "feat/orphan", hasContextFiles: false });
      expect(det.kind).toBe("orphaned");
      if (det.kind === "orphaned") {
        expect(det.choices).toEqual(["restart", "manual-pickup"]);
        expect(det.branch).toBe("feat/orphan");
      }
    });

    it("returns kind=paused with resume/restart/discard choices", async () => {
      await mgr.markRunning({ issue_number: 1, branch: "b" });
      await mgr.markPaused("stop");
      const det = await mgr.detectResume({ branch: "b", hasContextFiles: true });
      expect(det.kind).toBe("paused");
      if (det.kind === "paused") {
        expect(det.choices).toEqual(["resume", "restart", "discard"]);
      }
    });

    it("returns kind=aborted with restart/discard choices", async () => {
      await mgr.markRunning({ issue_number: 1, branch: "b" });
      await mgr.markAborted("crashed", true);
      const det = await mgr.detectResume({ branch: "b", hasContextFiles: false });
      expect(det.kind).toBe("aborted");
      if (det.kind === "aborted") {
        expect(det.choices).toEqual(["restart", "discard"]);
      }
    });
  });

  describe("schema_version gating", () => {
    it("rejects a major-version skew with SchemaVersionMismatch", async () => {
      const file = path.join(dir, "run-state.json");
      await fs.writeFile(
        file,
        JSON.stringify({
          schema_version: "2.0",
          issue_number: 1,
          state: "running",
          run_id: "00000000-0000-7000-8000-000000000000",
          attempt_number: 1,
          completed_stages: [],
          branch: "b",
          created_at: "2026-05-06T00:00:00Z",
          updated_at: "2026-05-06T00:00:00Z",
          attempts: [
            {
              run_id: "00000000-0000-7000-8000-000000000000",
              attempt_number: 1,
              started_at: "2026-05-06T00:00:00Z",
            },
          ],
        }),
        "utf-8"
      );
      await expect(mgr.read()).rejects.toBeInstanceOf(SchemaVersionMismatch);
    });

    it("accepts a current 1.0 file", async () => {
      await mgr.markRunning({ issue_number: 1, branch: "b" });
      const rs = await mgr.read();
      expect(rs?.schema_version).toBe("1.0");
    });
  });

  describe("validateWorktree", () => {
    it("throws WorktreeMissing when path no longer exists", async () => {
      await mgr.markRunning({
        issue_number: 1,
        branch: "b",
        worktree_path: path.join(dir, "nonexistent-worktree"),
      });
      await expect(mgr.validateWorktree()).rejects.toBeInstanceOf(WorktreeMissing);
    });

    it("succeeds when path exists", async () => {
      const wt = path.join(dir, "wt");
      await fs.mkdir(wt, { recursive: true });
      await mgr.markRunning({ issue_number: 1, branch: "b", worktree_path: wt });
      await expect(mgr.validateWorktree()).resolves.toBeUndefined();
    });
  });

  describe("archiveRun", () => {
    it("moves issue context files into history/<runId>/", async () => {
      const rs = await mgr.markRunning({ issue_number: 11, branch: "b" });
      // Drop a fake context file
      await fs.writeFile(path.join(dir, "issue-11.json"), "{}", "utf-8");
      const archive = await mgr.archiveRun();
      expect(archive).toContain(rs.run_id);
      // Live file gone
      await expect(fs.access(path.join(dir, "issue-11.json"))).rejects.toThrow();
      // Archive present
      expect(archive).toBeTruthy();
      if (archive) {
        await expect(fs.access(path.join(archive, "issue-11.json"))).resolves.toBeUndefined();
        await expect(fs.access(path.join(archive, "run-state.json"))).resolves.toBeUndefined();
      }
    });
  });
});
