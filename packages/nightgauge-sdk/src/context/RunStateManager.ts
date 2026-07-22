/**
 * RunStateManager — TypeScript-side manager for `.nightgauge/pipeline/run-state.json`.
 *
 * Backs the same on-disk file the Go binary's `internal/runstate` package
 * writes. Both sides use the atomic+fsync write contract (write-temp →
 * fsync(file) → rename → fsync(parent dir)) defined in `ContextManager`.
 *
 * This manager is the single source of truth for the pipeline lifecycle
 * (running / paused / completed / discarded / aborted). It does NOT hold
 * in-memory state — every transition reads, validates, mutates, and writes.
 *
 * @see docs/PIPELINE_STATE_SCHEMA.md
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as crypto from "node:crypto";
import * as os from "node:os";
import { atomicWriteJSON } from "./ContextManager.js";
import {
  RunStateSchema,
  newRunState,
  type RunState,
  type RunStateLifecycle,
  type RunStage,
} from "./schemas/run-state.js";
import {
  ConcurrentRunRefused,
  ContextSchemaError,
  isSchemaCompatible,
  SchemaVersionMismatch,
  WorktreeMissing,
} from "../errors/PipelineStateErrors.js";

const RUN_STATE_FILENAME = "run-state.json";
const CURRENT_SCHEMA_VERSION = "1.0";

/** Result of `detectResume()` — drives the orchestrator's start path. */
export type ResumeDetection =
  | { kind: "fresh" }
  | {
      kind: "paused";
      state: RunState;
      choices: ReadonlyArray<"resume" | "restart" | "discard">;
    }
  | {
      kind: "aborted";
      state: RunState;
      choices: ReadonlyArray<"restart" | "discard">;
    }
  | {
      kind: "running";
      state: RunState;
      reason: "concurrent_run" | "stale_writer";
    }
  | {
      kind: "orphaned";
      branch: string | null;
      hasContextFiles: boolean;
      choices: ReadonlyArray<"restart" | "manual-pickup">;
    };

/**
 * Generate a UUID v7 (time-ordered) using crypto.randomUUID() as a fallback
 * when v7 is not available. Node 22 has native crypto.randomUUID() but not
 * v7 yet — we synthesize it from a 48-bit timestamp and 74 bits of randomness.
 *
 * @internal — exported for tests.
 */
export function uuidV7(): string {
  // 48-bit timestamp (ms since epoch)
  const tsMs = Date.now();
  const tsHex = tsMs.toString(16).padStart(12, "0");
  // 74 bits of randomness
  const rand = crypto.randomBytes(10);
  // Set version (4 bits) to 7 in byte 0 high nibble of the random portion
  rand[0] = (rand[0] & 0x0f) | 0x70;
  // Set variant (2 bits) to 10 in byte 2 high bits
  rand[2] = (rand[2] & 0x3f) | 0x80;
  const randHex = rand.toString("hex");
  // Compose: tttttttt-tttt-7xxx-yxxx-xxxxxxxxxxxx
  return [
    tsHex.slice(0, 8),
    tsHex.slice(8, 12),
    randHex.slice(0, 4),
    randHex.slice(4, 8),
    randHex.slice(8, 20),
  ].join("-");
}

export class RunStateManager {
  private readonly filePath: string;

  constructor(public readonly basePath: string = ".nightgauge/pipeline") {
    this.filePath = path.join(basePath, RUN_STATE_FILENAME);
  }

  /**
   * Read the current RunState. Returns `null` if no file exists.
   *
   * Throws `SchemaVersionMismatch` for major-version skew.
   * Throws `ContextSchemaError` for any other validation failure.
   */
  async read(): Promise<RunState | null> {
    let raw: string;
    try {
      raw = await fs.readFile(this.filePath, "utf-8");
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") return null;
      throw error;
    }

    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch (e) {
      throw new ContextSchemaError(this.filePath, `invalid JSON: ${(e as Error).message}`);
    }

    const versionField = (parsed as { schema_version?: unknown })?.schema_version;
    const fileVersion = typeof versionField === "string" ? versionField : "1.0";
    if (!isSchemaCompatible(fileVersion, CURRENT_SCHEMA_VERSION)) {
      const [readerMajor] = CURRENT_SCHEMA_VERSION.split(".").map(Number);
      throw new SchemaVersionMismatch(this.filePath, fileVersion, readerMajor);
    }

    const result = RunStateSchema.safeParse(parsed);
    if (!result.success) {
      throw new ContextSchemaError(this.filePath, result.error.message);
    }
    return result.data;
  }

  /** Write a RunState atomically with fsync. */
  async write(state: RunState): Promise<void> {
    const result = RunStateSchema.safeParse(state);
    if (!result.success) {
      throw new ContextSchemaError(this.filePath, result.error.message);
    }
    const stamped: RunState = { ...result.data, updated_at: new Date().toISOString() };
    await fs.mkdir(this.basePath, { recursive: true });
    await atomicWriteJSON(this.filePath, JSON.stringify(stamped, null, 2) + "\n");
  }

  /**
   * Start a new run. Throws `ConcurrentRunRefused` when an existing
   * `running` state has a live PID, unless `force` is true.
   *
   * Returns the freshly-written state.
   */
  async markRunning(args: {
    issue_number: number;
    branch: string;
    worktree_path?: string;
    force?: boolean;
  }): Promise<RunState> {
    const existing = await this.read();
    if (existing && existing.state === "running" && !args.force) {
      const alive = isProcessAlive(existing.attempts.at(-1)?.pid ?? null);
      if (alive) {
        throw new ConcurrentRunRefused(
          existing.issue_number,
          existing.attempts.at(-1)?.pid ?? null,
          existing.attempts.at(-1)?.host_id ?? null
        );
      }
      // Stale writer — proceed with a new attempt on the same record.
    }

    const fresh = newRunState({
      issue_number: args.issue_number,
      branch: args.branch,
      run_id: uuidV7(),
      pid: process.pid,
      host_id: stableHostId(),
      worktree_path: args.worktree_path,
    });
    await this.write(fresh);
    return fresh;
  }

  /**
   * Stop button → paused. Stop NEVER deletes branches, worktrees, or context
   * files. Discard is the only destructive transition.
   */
  async markPaused(reason: string, resumeFromStage?: RunStage): Promise<RunState> {
    const cur = await this.requireExisting();
    assertTransition(cur.state, "paused");
    const next: RunState = {
      ...cur,
      state: "paused",
      reason,
      resume_from_stage: resumeFromStage ?? cur.resume_from_stage ?? null,
      recoverable: true,
      recovery_actions: ["resume", "restart", "discard"],
    };
    await this.write(next);
    return next;
  }

  /** Successful pr-merge → completed. */
  async markCompleted(): Promise<RunState> {
    const cur = await this.requireExisting();
    assertTransition(cur.state, "completed");
    const next: RunState = {
      ...cur,
      state: "completed",
      reason: null,
      recoverable: false,
      recovery_actions: null,
    };
    await this.write(next);
    return next;
  }

  /** Abort button → aborted. Sets recoverable + reason. */
  async markAborted(reason: string, recoverable: boolean): Promise<RunState> {
    const cur = await this.requireExisting();
    assertTransition(cur.state, "aborted");
    const next: RunState = {
      ...cur,
      state: "aborted",
      reason,
      recoverable,
      recovery_actions: recoverable ? ["restart", "discard"] : ["discard"],
    };
    await this.write(next);
    return next;
  }

  /** Discard action — final terminal state, archives state. */
  async markDiscarded(reason: string): Promise<RunState> {
    const cur = await this.requireExisting();
    assertTransition(cur.state, "discarded");
    const next: RunState = {
      ...cur,
      state: "discarded",
      reason,
      recoverable: false,
      recovery_actions: null,
    };
    await this.write(next);
    return next;
  }

  /**
   * Inspect the run-state file and tell the caller what to do:
   * - fresh — no file, start a new run
   * - paused — show resume / restart / discard choice
   * - aborted — show restart / discard choice
   * - running — refuse with reason (concurrent or stale)
   * - orphaned — pre-Gap-1 / #3237 fixture: no run-state but branch + context
   *   files exist; surface restart / manual-pickup
   */
  async detectResume(args: {
    branch?: string | null;
    hasContextFiles?: boolean;
  }): Promise<ResumeDetection> {
    const state = await this.read();
    if (!state) {
      // #3237 fixture: branch present, no context file, no run-state.json
      // OR pre-Gap-1: context files present but no run-state.
      if (args.branch || args.hasContextFiles) {
        return {
          kind: "orphaned",
          branch: args.branch ?? null,
          hasContextFiles: !!args.hasContextFiles,
          choices: ["restart", "manual-pickup"],
        };
      }
      return { kind: "fresh" };
    }
    if (state.state === "running") {
      const last = state.attempts.at(-1);
      const alive = isProcessAlive(last?.pid ?? null);
      return {
        kind: "running",
        state,
        reason: alive ? "concurrent_run" : "stale_writer",
      };
    }
    if (state.state === "paused") {
      return { kind: "paused", state, choices: ["resume", "restart", "discard"] };
    }
    if (state.state === "aborted") {
      return { kind: "aborted", state, choices: ["restart", "discard"] };
    }
    // completed / discarded — caller may start fresh
    return { kind: "fresh" };
  }

  /**
   * Resume path: paused → running. Reuses runId, increments attempt_number
   * if the previous attempt's ended_at is set.
   */
  async resume(): Promise<RunState> {
    const cur = await this.requireExisting();
    if (cur.state !== "paused") {
      throw new Error(`cannot resume from ${cur.state}; only paused → running is allowed`);
    }
    const lastAttempt = cur.attempts.at(-1);
    const newAttempt = (lastAttempt?.attempt_number ?? 0) + 1;
    const now = new Date().toISOString();
    const next: RunState = {
      ...cur,
      state: "running",
      attempt_number: newAttempt,
      reason: null,
      attempts: [
        ...cur.attempts,
        {
          run_id: cur.run_id,
          attempt_number: newAttempt,
          started_at: now,
          ended_at: null,
          pid: process.pid,
          host_id: stableHostId(),
          last_stage: cur.resume_from_stage ?? null,
        },
      ],
    };
    await this.write(next);
    return next;
  }

  /**
   * Validate the worktree path recorded in run-state still exists on disk.
   * Throws WorktreeMissing if the user manually deleted it.
   */
  async validateWorktree(): Promise<void> {
    const cur = await this.read();
    if (!cur || !cur.worktree_path) return;
    try {
      const stat = await fs.stat(cur.worktree_path);
      if (!stat.isDirectory()) {
        throw new WorktreeMissing(cur.worktree_path, cur.branch);
      }
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code === "ENOENT") {
        throw new WorktreeMissing(cur.worktree_path, cur.branch);
      }
      throw e;
    }
  }

  /** Mark a stage complete. Called from the orchestrator after each stage's
   *  context-file rename succeeds. */
  async markStageComplete(stage: RunStage): Promise<RunState> {
    const cur = await this.requireExisting();
    if (cur.completed_stages.includes(stage)) return cur;
    const next: RunState = {
      ...cur,
      completed_stages: [...cur.completed_stages, stage],
      resume_from_stage: nextStage(stage) ?? cur.resume_from_stage ?? null,
    };
    await this.write(next);
    return next;
  }

  /**
   * Move every live context file for this issue into history/<runId>/. Used
   * on completion and on restart (archive-then-new-run).
   */
  async archiveRun(): Promise<string | null> {
    const cur = await this.read();
    if (!cur) return null;
    const archiveDir = path.join(this.basePath, "history", cur.run_id);
    await fs.mkdir(archiveDir, { recursive: true });

    const issuePrefix = `${cur.issue_number}.json`;
    let entries: string[];
    try {
      entries = await fs.readdir(this.basePath);
    } catch {
      return archiveDir;
    }
    for (const name of entries) {
      // Match issue-<N>.json, planning-<N>.json, dev-<N>.json, validate-<N>.json,
      // pr-<N>.json, feedback-<N>.json — never run-state.json itself.
      if (name === RUN_STATE_FILENAME) continue;
      if (!name.endsWith(issuePrefix)) continue;
      const src = path.join(this.basePath, name);
      const dst = path.join(archiveDir, name);
      try {
        await fs.rename(src, dst);
      } catch {
        // Cross-device rename — fall back to copy + unlink.
        const data = await fs.readFile(src);
        await fs.writeFile(dst, data);
        await fs.unlink(src).catch(() => {});
      }
    }
    // Snapshot final run-state inside the archive for forensic clarity.
    await atomicWriteJSON(
      path.join(archiveDir, RUN_STATE_FILENAME),
      JSON.stringify(cur, null, 2) + "\n"
    );
    return archiveDir;
  }

  private async requireExisting(): Promise<RunState> {
    const cur = await this.read();
    if (!cur) {
      throw new Error(`${this.filePath} not found — call markRunning() before any transition`);
    }
    return cur;
  }
}

const STAGE_ORDER: readonly RunStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

function nextStage(stage: RunStage): RunStage | null {
  const idx = STAGE_ORDER.indexOf(stage);
  if (idx === -1 || idx === STAGE_ORDER.length - 1) return null;
  return STAGE_ORDER[idx + 1];
}

const ALLOWED_TRANSITIONS: Record<RunStateLifecycle, ReadonlyArray<RunStateLifecycle>> = {
  running: ["paused", "completed", "aborted"],
  paused: ["running", "discarded"],
  aborted: ["discarded"],
  completed: [],
  discarded: [],
};

function assertTransition(from: RunStateLifecycle, to: RunStateLifecycle): void {
  if (!ALLOWED_TRANSITIONS[from].includes(to)) {
    throw new Error(`illegal lifecycle transition: ${from} → ${to}`);
  }
}

function isProcessAlive(pid: number | null | undefined): boolean {
  if (!pid || pid <= 0) return false;
  try {
    // signal 0 = liveness probe, no actual signal delivered
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

function stableHostId(): string {
  // hostname is good enough for our concurrent-run check; we deliberately
  // avoid pulling machine UUIDs since they need platform-specific code.
  return os.hostname();
}
