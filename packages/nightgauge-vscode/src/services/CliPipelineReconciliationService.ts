/**
 * Discovers pipeline runs started by `nightgauge run` outside the extension.
 *
 * IPC remains the primary live-state path. This service is deliberately a
 * bounded filesystem fallback: it only scans registered repository roots and
 * only accepts a runtime snapshot when the root's current-run sidecar agrees
 * on repository + issue and the owning process is still alive.
 */
import * as fs from "node:fs/promises";
import * as path from "node:path";
import type { Disposable } from "vscode";

export interface CliRuntimeSnapshot {
  repo: string;
  issueNumber: number;
  runId: string;
  title?: string;
  [key: string]: unknown;
}

interface CurrentRunSidecar {
  issue_number: number;
  repo: string;
  pid?: number;
}

export interface RegisteredPipelineRoot {
  path: string;
  repo: string;
}

export interface ReconciledCliRun {
  key: string;
  root: string;
  snapshot: CliRuntimeSnapshot;
}

export interface CliPipelineReconciliationCallbacks {
  onDiscovered(run: ReconciledCliRun): void;
  onUpdated(run: ReconciledCliRun): void;
  onSettled(run: ReconciledCliRun): void;
}

export interface CliPipelineReconciliationOptions {
  intervalMs?: number;
  isProcessAlive?: (pid: number) => boolean;
}

const DEFAULT_INTERVAL_MS = 1_000;

export class CliPipelineReconciliationService implements Disposable {
  private readonly active = new Map<string, ReconciledCliRun>();
  private timer: NodeJS.Timeout | undefined;
  private scanning = false;
  private readonly intervalMs: number;
  private readonly isProcessAlive: (pid: number) => boolean;

  constructor(
    private readonly roots: () => RegisteredPipelineRoot[],
    private readonly callbacks: CliPipelineReconciliationCallbacks,
    options: CliPipelineReconciliationOptions = {}
  ) {
    this.intervalMs = Math.max(250, options.intervalMs ?? DEFAULT_INTERVAL_MS);
    this.isProcessAlive = options.isProcessAlive ?? processIsAlive;
  }

  start(): void {
    if (this.timer) return;
    void this.scan(); // late activation: reconcile before the first interval
    this.timer = setInterval(() => void this.scan(), this.intervalMs);
  }

  async scan(): Promise<void> {
    if (this.scanning) return;
    this.scanning = true;
    try {
      const seen = new Set<string>();
      const dedupedRoots = new Map<string, RegisteredPipelineRoot>();
      for (const root of this.roots()) {
        const normalized = path.resolve(root.path);
        if (root.repo && !dedupedRoots.has(normalized)) {
          dedupedRoots.set(normalized, { path: normalized, repo: normalizeRepo(root.repo) });
        }
      }

      for (const root of dedupedRoots.values()) {
        const run = await readActiveRun(root, this.isProcessAlive);
        if (!run) continue;
        seen.add(run.key);
        if (this.active.has(run.key)) {
          this.active.set(run.key, run);
          this.callbacks.onUpdated(run);
        } else {
          this.active.set(run.key, run);
          this.callbacks.onDiscovered(run);
        }
      }

      for (const [key, run] of this.active) {
        if (!seen.has(key)) {
          this.active.delete(key);
          this.callbacks.onSettled(run);
        }
      }
    } finally {
      this.scanning = false;
    }
  }

  dispose(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
    this.active.clear();
  }
}

async function readActiveRun(
  root: RegisteredPipelineRoot,
  isProcessAlive: (pid: number) => boolean
): Promise<ReconciledCliRun | null> {
  const stateDir = path.join(root.path, ".nightgauge", "pipeline");
  try {
    const sidecar = JSON.parse(
      await fs.readFile(path.join(stateDir, "current-run.json"), "utf8")
    ) as CurrentRunSidecar;
    if (
      !Number.isInteger(sidecar.issue_number) ||
      sidecar.issue_number <= 0 ||
      normalizeRepo(sidecar.repo) !== root.repo ||
      !sidecar.pid ||
      !isProcessAlive(sidecar.pid)
    ) {
      return null;
    }

    const snapshot = JSON.parse(
      await fs.readFile(path.join(stateDir, `runtime-${sidecar.issue_number}.json`), "utf8")
    ) as CliRuntimeSnapshot;
    if (
      snapshot.issueNumber !== sidecar.issue_number ||
      normalizeRepo(snapshot.repo) !== root.repo ||
      typeof snapshot.runId !== "string" ||
      snapshot.runId.length === 0
    ) {
      return null;
    }
    const key = `${root.repo}:${snapshot.runId}:${snapshot.issueNumber}`;
    return { key, root: root.path, snapshot };
  } catch {
    // Missing files, atomic rename windows, malformed JSON, and permissions
    // are all transient/non-actionable. The next bounded poll retries.
    return null;
  }
}

function normalizeRepo(repo: string): string {
  return repo.trim().toLowerCase();
}

function processIsAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === "EPERM";
  }
}
