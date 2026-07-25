/**
 * AttentionSweepService — the four moments the repo-scoped attention sweep runs
 * (issue #93, epic #88).
 *
 * `nightgauge attention sweep` has existed since #89 and nothing invoked it.
 * This is what invokes it. The sweep asks the registered repo-scoped producers
 * "is this repo blocked?" with no run in flight, and reconciles the answers
 * against the store — raising what is newly true, leaving untouched what is
 * still true, auto-resolving what is no longer true.
 *
 * NOT A DAEMON, by the sweep's own design commitment. There is no background
 * process here and no long-lived worker: a sweep is cheap, idempotent, and safe
 * to run redundantly, so it is triggered at the moments an operator is actually
 * looking rather than continuously:
 *
 *   1. Extension activation — the window opening is the operator arriving.
 *   2. Repository-view refresh — an explicit "tell me the current state".
 *   3. A conservative timer while the window is active.
 *   4. After any run terminates — a merge just changed the repo's shape.
 *
 * All four collapse into one throttled entry point. Redundancy is expected and
 * free: {@link minGapMs} coalesces bursts (a run terminating during activation),
 * and the daemon declines a second concurrent sweep outright.
 *
 * Failure is never surfaced to the operator. A sweep that cannot run — no forge
 * token, no attention store, a rate limit — is a logged no-op, because the one
 * thing worse than a missing card is a modal on every window open.
 *
 * @see internal/attention/sweep/sweep.go — the Sweeper and its producers
 * @see internal/ipc/attention_sweep.go — the IPC binding
 */

import * as vscode from "vscode";
import type { AttentionSweepResult } from "./IpcClientBase";
import type { Logger } from "../utils/logger";

/** What asked for a sweep. Echoed to the daemon log so a surprising burst of
 * forge traffic can be traced back to its trigger. */
export type SweepTrigger = "activation" | "view-refresh" | "timer" | "run-terminated" | "manual";

/** The IPC slice this service needs — narrow so tests need no real client. */
export interface AttentionSweepIpc {
  attentionSweep(repos?: string[], reason?: string): Promise<AttentionSweepResult>;
  on(event: string, handler: (data: unknown) => void): { dispose(): void };
}

export interface AttentionSweepDeps {
  ipc: AttentionSweepIpc;
  /** Resolves the workspace's repos as "owner/name". Called per sweep rather
   * than captured once, so a repo added to the workspace mid-session is swept
   * without a reload. */
  resolveRepos: () => Promise<string[]> | string[];
  logger: Logger;
  /** Invoked after a sweep that changed something, so the tree re-reads even if
   * the `attention.event` push is not wired in this window. */
  onChanged?: () => void;
  /** Overrides the config read (tests). */
  readConfig?: () => AttentionSweepConfig;
  /** Overrides the clock (tests). */
  now?: () => number;
}

export interface AttentionSweepConfig {
  enabled: boolean;
  /** Timer period. Zero or negative disables the timer; the event-driven
   * triggers still fire. */
  intervalMs: number;
  /** Minimum gap between sweeps. Triggers inside this window are dropped. */
  minGapMs: number;
}

/**
 * Default timer period: 15 minutes.
 *
 * Deliberately conservative. The conditions this catches are STANDING — a red
 * `main`, a PR waiting on a reviewer — and they persist for hours, so sweeping
 * more often buys almost nothing and spends forge quota the pipeline needs. The
 * event-driven triggers cover the moments where latency actually matters.
 */
export const DEFAULT_SWEEP_INTERVAL_MINUTES = 15;

/** Floor on the configured interval. A one-minute sweep across a multi-repo
 * workspace is a quota leak dressed as responsiveness. */
export const MIN_SWEEP_INTERVAL_MINUTES = 5;

/** Bursts inside this window collapse to one sweep. */
export const SWEEP_MIN_GAP_MS = 60_000;

/** The slice of WorkspaceManager the repo resolver needs. */
export interface SweepRepoSource {
  getAllRepositories(): Array<{ github?: { owner?: string; repo?: string } | null }>;
}

/**
 * Resolve the "owner/name" specs to sweep.
 *
 * The workspace manifest is authoritative — it maps a folder to its GitHub slug
 * explicitly, which folder-name inference gets wrong whenever the two differ
 * (#3766). `folderIdentities` is the fallback for a single-folder window with no
 * manifest, and is only consulted when the manifest yields nothing.
 */
export async function resolveSweepRepos(
  source: SweepRepoSource | null | undefined,
  folderIdentities: () => Promise<string[]>
): Promise<string[]> {
  const specs: string[] = [];
  for (const repo of source?.getAllRepositories() ?? []) {
    const gh = repo.github;
    if (gh?.owner && gh?.repo) specs.push(`${gh.owner}/${gh.repo}`);
  }
  if (specs.length > 0) return specs;
  try {
    return await folderIdentities();
  } catch {
    return [];
  }
}

/** Read the sweep settings from VS Code configuration, clamped to sane bounds. */
export function readSweepConfig(): AttentionSweepConfig {
  const cfg = vscode.workspace.getConfiguration("nightgauge.attention");
  const enabled = cfg.get<boolean>("sweepEnabled", true);
  const raw = cfg.get<number>("sweepIntervalMinutes", DEFAULT_SWEEP_INTERVAL_MINUTES);
  const minutes = Number.isFinite(raw) ? Number(raw) : DEFAULT_SWEEP_INTERVAL_MINUTES;
  // A non-positive interval disables the timer without disabling the service —
  // "only sweep when I do something" is a legitimate preference.
  const intervalMs = minutes <= 0 ? 0 : Math.max(minutes, MIN_SWEEP_INTERVAL_MINUTES) * 60_000;
  return { enabled, intervalMs, minGapMs: SWEEP_MIN_GAP_MS };
}

export class AttentionSweepService implements vscode.Disposable {
  private readonly disposables: vscode.Disposable[] = [];
  private timer: ReturnType<typeof setInterval> | undefined;
  private inFlight: Promise<AttentionSweepResult | undefined> | null = null;
  private lastSweepAt = 0;
  private started = false;

  constructor(private readonly deps: AttentionSweepDeps) {}

  private get config(): AttentionSweepConfig {
    return (this.deps.readConfig ?? readSweepConfig)();
  }

  private now(): number {
    return (this.deps.now ?? Date.now)();
  }

  /**
   * Wire the event-driven triggers, start the timer, and sweep once for
   * activation. Idempotent — a second call is a no-op rather than a second
   * timer.
   */
  start(): void {
    if (this.started) return;
    this.started = true;

    const config = this.config;
    if (!config.enabled) {
      this.deps.logger.info("Attention sweep disabled by configuration");
      return;
    }

    // Trigger 4: a run terminating changed the repo's shape — a merge may have
    // cleared a standing blocker, or its own merge may have broken the default
    // branch. Both `pipeline.complete` and `pipeline.error` are terminal.
    for (const event of ["pipeline.complete", "pipeline.error"]) {
      this.disposables.push(this.deps.ipc.on(event, () => void this.sweep("run-terminated")));
    }

    // Trigger 3: the conservative timer, running only while this window lives.
    if (config.intervalMs > 0) {
      this.timer = setInterval(() => void this.sweep("timer"), config.intervalMs);
    }

    // Trigger 1: activation.
    void this.sweep("activation");
  }

  /**
   * Run a sweep, unless one is already running or the last one finished inside
   * the minimum gap. Returns the result, or undefined when the call was
   * throttled or the service is disabled.
   *
   * Never rejects: every failure mode is logged and swallowed, because three of
   * the four callers are ambient triggers the operator did not ask for.
   */
  async sweep(trigger: SweepTrigger): Promise<AttentionSweepResult | undefined> {
    const config = this.config;
    if (!config.enabled) return undefined;

    // A sweep already in flight covers this trigger — the whole point of an
    // idempotent evaluation is that the second caller can ride the first.
    if (this.inFlight) return this.inFlight;

    // A manual sweep is the operator asking directly; honour it even inside the
    // throttle window, or the button appears broken.
    if (trigger !== "manual" && this.now() - this.lastSweepAt < config.minGapMs) {
      this.deps.logger.debug("Attention sweep throttled", { trigger });
      return undefined;
    }

    this.inFlight = this.run(trigger);
    try {
      return await this.inFlight;
    } finally {
      this.inFlight = null;
      this.lastSweepAt = this.now();
    }
  }

  private async run(trigger: SweepTrigger): Promise<AttentionSweepResult | undefined> {
    let repos: string[];
    try {
      repos = await this.deps.resolveRepos();
    } catch (err) {
      this.deps.logger.warn("Attention sweep: could not resolve workspace repos", {
        trigger,
        error: err instanceof Error ? err.message : String(err),
      });
      return undefined;
    }
    if (repos.length === 0) return undefined;

    try {
      const result = await this.deps.ipc.attentionSweep(repos, trigger);
      this.report(trigger, repos, result);
      return result;
    } catch (err) {
      // A daemon that is down, restarting, or built without the method is not
      // an operator-facing problem. Log and wait for the next trigger.
      this.deps.logger.warn("Attention sweep failed", {
        trigger,
        repos: repos.length,
        error: err instanceof Error ? err.message : String(err),
      });
      return undefined;
    }
  }

  /** Log the outcome and nudge the tree when the store actually changed. */
  private report(trigger: SweepTrigger, repos: string[], result: AttentionSweepResult): void {
    if (result.unavailable || result.busy) {
      this.deps.logger.debug("Attention sweep skipped", {
        trigger,
        unavailable: result.unavailable,
        busy: result.busy,
      });
      return;
    }
    // Producers that could not observe a repo left their cards untouched
    // deliberately — worth a log line, never a notification.
    for (const repo of result.repos) {
      if (repo.skipped) {
        this.deps.logger.info("Attention sweep skipped a repo", {
          repo: repo.repo,
          reason: repo.skipReason,
        });
      } else if (repo.error) {
        this.deps.logger.warn("Attention sweep could not evaluate a repo", {
          repo: repo.repo,
          error: repo.error,
        });
      }
    }
    const changed = result.created + result.updated + result.autoResolved;
    if (changed > 0) {
      this.deps.logger.info("Attention sweep changed the inbox", {
        trigger,
        repos: repos.length,
        created: result.created,
        updated: result.updated,
        autoResolved: result.autoResolved,
      });
      this.deps.onChanged?.();
    }
  }

  dispose(): void {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = undefined;
    }
    for (const d of this.disposables) d.dispose();
    this.disposables.length = 0;
    this.started = false;
  }
}
