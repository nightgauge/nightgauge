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
export type SweepTrigger =
  "activation" | "view-refresh" | "timer" | "run-terminated" | "manual" | "visibility-regained";

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

// ─────────────────────────────────────────────────────────────────────────
// Shared idle-state polling gate (Issue #484)
// ─────────────────────────────────────────────────────────────────────────
//
// Mirrors the AutonomousActivityState pattern (#360 — "don't poll GitHub in
// the background when there is no work to serve"), but keyed on visibility
// and window focus rather than autonomous status. Two independent, EXPLICIT
// predicates — not one combined OR — so each consumer composes the
// semantics that actually fit it (#484 review round, DESIGN RULING):
//
//   - isWindowActive() — the window is focused, or was focused within the
//     last WINDOW_FOCUS_GRACE_MS (avoids flapping on a brief alt-tab).
//   - isViewVisible(key) — the tracked view registered under `key` is
//     currently visible, independent of every other key.
//
// Consumers compose their OWN predicate from these two:
//   - This file's periodic sweep timer: isWindowActive() ALONE. The
//     attention inbox (activity-bar badge) is a legitimate thing to keep
//     warm while the operator is actively working, even with every
//     Nightgauge tree view collapsed — so view visibility is irrelevant to
//     this consumer. Only a genuinely idle window (unfocused past grace)
//     suspends it.
//   - ProjectBoardTreeProvider's and RepositoriesTreeProvider's timer /
//     ipc.ready convenience polling: isViewVisible(own key) AND
//     isWindowActive(). A tree nobody can see is not worth a GitHub call
//     even while the operator is focused on some other pane — no reader, no
//     poll — and a tree left open overnight still suspends once the focus
//     grace lapses.
//
// Deliberately NOT consulted by active-run *monitoring* — the autonomous
// stall watchdog / liveness probe in autonomousCommands.ts — which must keep
// running whether or not any window is visible so a stall is never missed
// mid-run; see that file's `scheduleNextWatchdog` doc comment for the
// exemption's rationale. Also not consulted by the event-driven sweep
// triggers (activation, pipeline.complete/error below) — those model
// "something just happened", not ambient polling, so they always run.

/** Grace period after losing window focus during which polling remains
 * allowed — avoids flapping the gate closed/open on a brief alt-tab. */
export const WINDOW_FOCUS_GRACE_MS = 60_000;

/** One consumer's registration: its own composite predicate plus the
 * callback to fire on that predicate's false→true edge. */
interface GateSubscription {
  predicate: () => boolean;
  listener: () => void;
}

/**
 * PollingVisibilityGate — shared "who might be watching right now" signal.
 *
 * Tracks two independent inputs:
 *   - Zero or more tracked views' visibility, keyed by an arbitrary id so N
 *     views can register/unregister without one hidden view masking another
 *     that's still visible.
 *   - The VS Code window's live focus state, with a short grace window so a
 *     brief alt-tab doesn't suspend and immediately resume polling.
 *
 * Exposes each input as its OWN predicate (`isWindowActive()`,
 * `isViewVisible(key)`) rather than one combined "is polling allowed"
 * verdict — see the module doc comment above for why. Mirrors
 * AutonomousActivityState's singleton + `resetForTests()` shape.
 */
export class PollingVisibilityGate {
  private static _instance: PollingVisibilityGate | null = null;

  private readonly visibleKeys = new Set<string>();
  private windowFocused = true;
  private lastFocusedAt: number;
  private readonly subscriptions = new Set<GateSubscription>();

  /**
   * The window-focus subscription wiring, owned by `ensureWindowFocusTracking()`
   * below. Lives on the instance (not a module global) so `resetForTests()`
   * can tear down gate state AND focus-tracking wiring together as ONE
   * unified reset (#484 review round, SF-3) — a suite that only reset the
   * gate used to leave a fresh singleton primed from a stale subscription.
   * Public only so the sibling `ensureWindowFocusTracking()` function below
   * can read/write it; treat it as internal to this module.
   */
  focusTracking: { dispose(): void } | null = null;

  private constructor() {
    this.lastFocusedAt = Date.now();
  }

  static get instance(): PollingVisibilityGate {
    if (!PollingVisibilityGate._instance) {
      PollingVisibilityGate._instance = new PollingVisibilityGate();
    }
    return PollingVisibilityGate._instance;
  }

  /** Test-only — drop the singleton AND dispose its window-focus
   * subscription, so each test starts from one genuinely clean state
   * instead of two independently-reset halves (#484 review round, SF-3). */
  static resetForTests(): void {
    PollingVisibilityGate._instance?.focusTracking?.dispose();
    PollingVisibilityGate._instance = null;
  }

  /**
   * Register (or update) whether a tracked view is currently visible, keyed
   * by an arbitrary id.
   */
  setViewVisible(key: string, visible: boolean): void {
    this.withEdgeDetection(() => {
      if (visible) {
        this.visibleKeys.add(key);
      } else {
        this.visibleKeys.delete(key);
      }
    });
  }

  /** Update the live window-focus signal. */
  setWindowFocused(focused: boolean): void {
    this.withEdgeDetection(() => {
      const wasFocused = this.windowFocused;
      this.windowFocused = focused;
      // `lastFocusedAt` means "the last instant the window WAS focused" —
      // stamp on gain AND on the focused→unfocused edge (guarded by
      // `wasFocused` so repeated blur notifications can't extend the grace),
      // so the grace window measures time since focus was LOST, not since
      // it was gained (#484 review round, MF-1).
      if (focused || wasFocused) {
        this.lastFocusedAt = Date.now();
      }
    });
  }

  /**
   * Snapshot every registered subscription's predicate BEFORE applying
   * `mutate`, then re-evaluate each AFTER and fire the ones that crossed
   * false→true. The snapshot is taken fresh against the live clock on every
   * call (not cached across calls) so a predicate that changed purely from
   * TIME passing — the focus grace silently expiring with no gate call in
   * between — is still detected correctly the next time anything touches
   * the gate, exactly as if the expiry itself were an event.
   */
  private withEdgeDetection(mutate: () => void): void {
    const before = new Map<GateSubscription, boolean>();
    for (const sub of this.subscriptions) {
      before.set(sub, this.safeEval(sub.predicate));
    }
    mutate();
    for (const sub of [...this.subscriptions]) {
      const wasAllowed = before.get(sub) ?? false;
      const isAllowed = this.safeEval(sub.predicate);
      if (isAllowed && !wasAllowed) {
        try {
          sub.listener();
        } catch {
          // A consumer that cannot service its own coalesced refresh must
          // not suppress the others' — this gate is shared state, not a
          // call chain (#484 review round, SF-1).
        }
      }
    }
  }

  private safeEval(predicate: () => boolean): boolean {
    try {
      return predicate();
    } catch {
      return false;
    }
  }

  /** True when the window is focused, or was focused within the grace
   * window. Says nothing about view visibility — see the module doc comment
   * for which consumers combine this with `isViewVisible()` and which don't. */
  isWindowActive(): boolean {
    return this.windowFocused || Date.now() - this.lastFocusedAt < WINDOW_FOCUS_GRACE_MS;
  }

  /** True when the view registered under `key` is currently visible. False
   * for a key that was never registered at all. */
  isViewVisible(key: string): boolean {
    return this.visibleKeys.has(key);
  }

  /**
   * Subscribe to a per-consumer "became allowed" edge: `predicate` is
   * re-evaluated on every gate mutation, and `listener` fires exactly once
   * per false→true transition of THAT predicate — not any other consumer's.
   * Different consumers pass different composite predicates (a tree
   * poller's `isViewVisible(key) && isWindowActive()` vs. the sweep timer's
   * `isWindowActive()` alone) and edge independently off the same
   * underlying state changes (#484 review round — DESIGN RULING). Returns a
   * disposable.
   */
  onDidBecomeAllowed(predicate: () => boolean, listener: () => void): { dispose(): void } {
    const sub: GateSubscription = { predicate, listener };
    this.subscriptions.add(sub);
    return {
      dispose: () => {
        this.subscriptions.delete(sub);
      },
    };
  }
}

/**
 * Wire vscode.window's live focus state into the shared gate. Idempotent —
 * safe to call from every consumer's constructor/start(); only the first
 * call actually subscribes, so N provider instances cost one listener.
 * Deliberately never disposed in production — the subscription lives for
 * the extension host's lifetime, matching the gate singleton's own lifetime
 * (mirrors AutonomousActivityState). Only `resetForTests()`'s unified reset
 * tears it down, for test isolation (#484 review round, SF-6).
 *
 * Defensive against older/minimal `vscode` mocks in tests that don't stub
 * `window.state` / `window.onDidChangeWindowState` — falls back to treating
 * the window as focused (the safe, never-over-suppress default) rather than
 * throwing.
 */
export function ensureWindowFocusTracking(): void {
  const gate = PollingVisibilityGate.instance;
  if (gate.focusTracking) return;
  gate.focusTracking = { dispose: () => {} };
  // Minimal `vscode` test mocks (this file's own included) sometimes stub
  // only the specific named exports a suite needs, and vitest's mock proxy
  // throws on an unlisted property rather than returning undefined — so
  // `vscode.window` itself can throw, not just its members. try/catch keeps
  // that a no-op (falls back to "focused", the never-over-suppress default)
  // instead of an unrelated test suite failing on an internal implementation
  // detail of this gate.
  try {
    const w = vscode.window as unknown as {
      state?: { focused: boolean };
      onDidChangeWindowState?: (listener: (e: { focused: boolean }) => void) => { dispose(): void };
    };
    gate.setWindowFocused(w.state?.focused ?? true);
    if (typeof w.onDidChangeWindowState === "function") {
      gate.focusTracking = w.onDidChangeWindowState((e) => {
        gate.setWindowFocused(e.focused);
      });
    }
  } catch {
    gate.setWindowFocused(true);
  }
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

    // #484 — start (or join) the shared idle-state polling gate so the
    // timer below can skip ticks with nobody watching.
    ensureWindowFocusTracking();

    // Trigger 3: the conservative timer, running only while this window lives.
    if (config.intervalMs > 0) {
      this.timer = setInterval(() => {
        // #484 — this consumer's own predicate is isWindowActive() ALONE
        // (no isViewVisible check): the attention inbox stays warm while
        // the operator is working even with every tree view collapsed. See
        // the gate's module doc comment for the per-consumer rationale.
        if (!PollingVisibilityGate.instance.isWindowActive()) {
          this.deps.logger.debug("Attention sweep timer skipped — window inactive", {});
          return;
        }
        void this.sweep("timer");
      }, config.intervalMs);

      // #484 AC2 — when this consumer's own predicate (isWindowActive())
      // transitions closed→open — the window regains focus after being
      // idle past the grace — fire one coalesced sweep instead of waiting
      // out the rest of the interval. `sweep()`'s own minGap throttle still
      // applies, so a burst of near-simultaneous transitions collapses to
      // at most one call.
      this.disposables.push(
        PollingVisibilityGate.instance.onDidBecomeAllowed(
          () => PollingVisibilityGate.instance.isWindowActive(),
          () => void this.sweep("visibility-regained")
        )
      );
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
    if (result.unavailable || result.busy || result.throttled) {
      this.deps.logger.debug("Attention sweep skipped", {
        trigger,
        unavailable: result.unavailable,
        busy: result.busy,
        // #848 — the daemon declined this one because a sweep finished inside
        // SweepMinGap. Logged distinctly from `busy` so a trigger path that is
        // firing too often is visible as throttling rather than as contention.
        throttled: result.throttled,
        throttledForMs: result.throttledForMs,
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
