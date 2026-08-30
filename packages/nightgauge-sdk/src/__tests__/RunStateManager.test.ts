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
import { RUN_IDENTITY_PATTERN } from "../context/runIdentity.js";
import {
  ConcurrentRunRefused,
  ContextSchemaError,
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
      // Pinned against the AUTHORITY, not an inlined copy of it (#424): the
      // minter and the validator the Go side mirrors must agree, so this arm
      // goes red if uuidV7 ever produces something isRunIdentity refuses.
      expect(rs.run_id).toMatch(RUN_IDENTITY_PATTERN);
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

  /**
   * READ-BACK REFUSAL (#468) — strict, with no lenient branch.
   *
   * `run-state.json` used to validate `run_id` with `z.string().uuid()`, which
   * accepts every UUID version and uppercase hex: precisely the set Go's
   * run-identity authority refuses with `ErrNoRunIdentity`. An id only this
   * side accepted could be read back here and handed on as an IPC param and a
   * `runtime-{issue}-{runId}.json` filename component the Go scanner will not
   * parse — the F16 `run_id_invalid` family, where every progress call for the
   * run is silently discarded.
   *
   * The decision is refusal, not "lenient with telemetry": a compat branch for
   * on-disk files no customer has is out under `AGENTS.md` § Agent Operating
   * Rules. The resume path must fail LOUDLY — never silently drop the run and
   * never rewrite the id — so the refusal surfaces as the existing
   * `ContextSchemaError`, which already carries the file path, and the message
   * names the offending id so the operator can see which bytes are wrong.
   *
   * THE FILE UNDER TEST IS CAPTURED, NOT HAND-AUTHORED (#166): the fixture is
   * whatever `RunStateManager` really wrote — see
   * `scripts/capture-run-state-fixture.sh` and the `_capture` header inside the
   * fixture. Each arm edits ONLY `run_id`, exactly as that header states, so
   * what these arms exercise is the read-back path over a real file rather than
   * a schema over a hand-shaped object.
   */
  describe("read — run_id refusal on a captured run-state.json", () => {
    /**
     * The captured file, re-emitted into the manager's own directory with
     * `run_id` (both sites) replaced. Returns the path so the assertions can
     * check the error names it.
     */
    // `__dirname`, not `import.meta.dirname`: this package builds to CommonJS,
    // where the import.meta form is a compile error. The existing integration
    // tests resolve fixtures the same way.
    const FIXTURE = path.resolve(
      __dirname,
      "integration",
      "fixtures",
      "captured-run-state-paused.json"
    );

    async function loadCaptured(): Promise<Record<string, unknown>> {
      return JSON.parse(await fs.readFile(FIXTURE, "utf-8"));
    }

    /**
     * Re-emit the captured file into the manager's own directory, with `run_id`
     * (both the top-level site and the nested attempt site) replaced by
     * `runId`. Returns the path, so an assertion can check the error names it.
     */
    async function writeCapturedWithRunId(runId: string): Promise<string> {
      const fixture = (await loadCaptured()) as {
        run_id: string;
        attempts: Array<{ run_id: string }>;
      };
      fixture.run_id = runId;
      for (const attempt of fixture.attempts) attempt.run_id = runId;
      const file = path.join(dir, "run-state.json");
      await fs.writeFile(file, JSON.stringify(fixture, null, 2) + "\n", "utf-8");
      return file;
    }

    // The ACCEPTING arm, and it is not optional: without it every refusal below
    // would also pass against a schema that refuses everything, including one
    // that refuses what the product actually writes.
    it("reads the captured fixture back unedited", async () => {
      const captured = (await loadCaptured()) as { run_id: string };
      await writeCapturedWithRunId(captured.run_id);
      const rs = await mgr.read();
      expect(rs?.state).toBe("paused");
      expect(rs?.run_id).toBe(captured.run_id);
      expect(RUN_IDENTITY_PATTERN.test(rs!.run_id)).toBe(true);
    });

    it("refuses a run-state.json whose run_id is a v4 UUID", async () => {
      const bad = "3f2504e0-4f89-41d3-9a0c-0305e82c3301";
      const file = await writeCapturedWithRunId(bad);
      await expect(mgr.read()).rejects.toBeInstanceOf(ContextSchemaError);
      await expect(mgr.read()).rejects.toThrow(bad);
      await expect(mgr.read()).rejects.toThrow(file);
    });

    it("refuses a run-state.json whose run_id is uppercase", async () => {
      const bad = "019FE6F3-FCFE-7B6F-8A7C-BE0F444B6610";
      const file = await writeCapturedWithRunId(bad);
      await expect(mgr.read()).rejects.toBeInstanceOf(ContextSchemaError);
      await expect(mgr.read()).rejects.toThrow(bad);
      await expect(mgr.read()).rejects.toThrow(file);
    });

    // The resume path is the one that matters operationally: `resume()` goes
    // through `requireExisting()` → `read()`, so the refusal has to reach the
    // caller as the SAME loud error rather than being swallowed into a "no
    // state, start fresh" answer, which would silently orphan the run.
    it("resume() surfaces the refusal rather than starting fresh", async () => {
      await writeCapturedWithRunId("3f2504e0-4f89-41d3-9a0c-0305e82c3301");
      await expect(mgr.resume()).rejects.toBeInstanceOf(ContextSchemaError);
    });

    // `detectResume()` is the other read of the same file, and it decides
    // whether the UI offers resume/restart/discard at all. A refusal this path
    // swallowed would present a real paused run as absent.
    it("detectResume() surfaces the refusal rather than reporting a fresh run", async () => {
      await writeCapturedWithRunId("019FE6F3-FCFE-7B6F-8A7C-BE0F444B6610");
      await expect(
        mgr.detectResume({ branch: "feat/acme-platform-widget", hasContextFiles: true })
      ).rejects.toBeInstanceOf(ContextSchemaError);
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

    // Pins #654: the match must be anchored on the leading hyphen
    // ("-33.json"), not an unanchored numeric suffix ("33.json"). An
    // unanchored suffix also matches issue-633.json (and every other kind
    // of -N33.json file) because "633.json" ends with "33.json" — so
    // archiving issue 33 would silently relocate issue 633's live,
    // concurrently running context files into issue 33's archive dir.
    it("does not move an unrelated issue's files whose number ends with the same digits", async () => {
      const rs = await mgr.markRunning({ issue_number: 33, branch: "fix/33" });

      const own = [
        "issue-33.json",
        "planning-33.json",
        "dev-33.json",
        "validate-33.json",
        "pr-33.json",
        "feedback-33.json",
      ];
      const other = [
        "issue-633.json",
        "planning-633.json",
        "dev-633.json",
        "validate-633.json",
        "pr-633.json",
        "feedback-633.json",
      ];
      for (const name of [...own, ...other]) {
        await fs.writeFile(path.join(dir, name), "{}", "utf-8");
      }

      const archive = await mgr.archiveRun();
      expect(archive).toContain(rs.run_id);
      if (!archive) throw new Error("expected archive dir");

      for (const name of own) {
        await expect(
          fs.access(path.join(dir, name)),
          `${name} should have been moved out of the base dir`
        ).rejects.toThrow();
        await expect(
          fs.access(path.join(archive, name)),
          `${name} should be present in the archive dir`
        ).resolves.toBeUndefined();
      }

      for (const name of other) {
        await expect(
          fs.access(path.join(dir, name)),
          `${name} belongs to issue 633 and must stay in the base dir`
        ).resolves.toBeUndefined();
        await expect(
          fs.access(path.join(archive, name)),
          `${name} belongs to issue 633 and must NOT be in issue 33's archive dir`
        ).rejects.toThrow();
      }

      // run-state.json itself is never matched/moved by the suffix scan —
      // only the fresh snapshot written directly into the archive dir.
      await expect(fs.access(path.join(dir, "run-state.json"))).resolves.toBeUndefined();
    });
  });
});
