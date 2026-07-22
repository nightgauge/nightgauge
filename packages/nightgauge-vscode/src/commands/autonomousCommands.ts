/**
 * Autonomous mode commands
 *
 * Registers all user-facing commands for the autonomous cross-repo pipeline
 * scheduler: start, dry-run preview, pause, resume, stop, and status.
 *
 * Uses the IpcClient singleton to communicate with the Go binary's autonomous
 * scheduler via the autonomous.* IPC methods.
 *
 * @see internal/orchestrator/autonomous.go — Go autonomous scheduler
 * @see Issue #2373 — Autonomous mode UX
 */

import * as vscode from "vscode";
import { IpcClient } from "../services/IpcClient";
import { IpcClientBase } from "../services/IpcClient";
import type { AutonomousStatusResult } from "../services/IpcClientBase";
import type { Logger } from "../utils/logger";
import {
  formatCooldownLabel,
  formatCooldownRemaining,
  type StatusBarManager,
} from "../utils/statusBar";
import { getRepoIdentity } from "../utils/configPathResolver";
import type { WorkspaceManager } from "../services/WorkspaceManager";
import { entryMatchesRepo, type EnabledReposConfigService } from "../utils/enabledReposConfig";
import { getAutonomousStallConfig } from "../utils/incrediConfig";
import { getPRForIssue } from "../utils/prDetection";
import { detectAutonomousStall } from "../utils/autonomousStallDetector";
import {
  tripBreakerIfRateLimited,
  noteRateLimitOk,
  isBreakerTripped,
  getLastTripAt,
  getBreakerResetAt,
} from "../utils/rateLimitCircuitBreaker";
import { observeWatchdogResult } from "../utils/networkOutageCircuitBreaker";
import { autoResumeAfterRecovery } from "../utils/autonomousAutoResume";
import { setAutonomousContextKeys } from "../utils/autonomousContextKeys";
import {
  SSE_WIDENED_INTERVAL_MS,
  DISCONNECT_REVERT_THRESHOLD_MS,
} from "../services/ProjectEventSubscriber";
import type { ProjectEventSubscriber } from "../services/ProjectEventSubscriber";

/** Output channel for autonomous mode logs. Lazily created, shared across commands. */
let autonomousOutputChannel: vscode.OutputChannel | null = null;
let stallWatchdogTimer: ReturnType<typeof setTimeout> | null = null;

// ── Liveness probe — detects Go backend crash while autonomous is running ──
const LIVENESS_PROBE_INTERVAL_MS = 10_000;
const LIVENESS_FAILURE_THRESHOLD = 3;
const LIVENESS_PROBE_TIMEOUT_MS = 3_000;

let livenessProbeTimer: ReturnType<typeof setTimeout> | null = null;
let livenessConsecutiveFailures = 0;
let backendDisconnected = false;
let livenessStatusDisposable: { dispose(): void } | null = null;
/** StatusBarManager reference set by registerAutonomousCommands for liveness probe use. */
let _autonomousStatusBar: import("../utils/statusBar").StatusBarManager | null = null;

/** Project event subscriber — set externally via setProjectEventSubscriber(). */
let _projectEventSubscriber: ProjectEventSubscriber | null = null;

/** Called by extension.ts after creating the subscriber to wire cadence integration. */
export function setProjectEventSubscriber(subscriber: ProjectEventSubscriber | null): void {
  _projectEventSubscriber = subscriber;
}

/**
 * Safety-pause notifier function shape. The autonomous status-changed
 * subscriber invokes this when the Go scheduler reports paused /
 * safety_tripped with one of the wire-eligible triggers. Wired by
 * extension.ts at bootstrap to a DiscordService method so the operator
 * gets a Discord ping in addition to the VSCode toast. (Issue #3605 C)
 */
type AutonomousSafetyNotifier = (triggeredBy: string, reason: string) => Promise<void>;

let _autonomousSafetyNotifier: AutonomousSafetyNotifier | null = null;

/**
 * Register the function called whenever the Go autonomous scheduler
 * pauses for a wire-eligible safety trip (safety:rate-limit or
 * safety:cascading-failures — see CASCADE_PAUSE_TRIGGERS). Pass null to
 * unregister. The notifier is invoked off the status-change handler so
 * a hung webhook never blocks the UI. (Issue #3605 C)
 */
export function setAutonomousSafetyNotifier(notifier: AutonomousSafetyNotifier | null): void {
  _autonomousSafetyNotifier = notifier;
}

/**
 * The structured `pauseTriggeredBy` tags that should fan out to Discord
 * via setAutonomousSafetyNotifier. Budget-ceiling trips deliberately stay
 * in the toast/log path because they're per-issue events the operator is
 * already inside the IDE for.
 *
 * Tag conventions:
 *   - `rate-limit-circuit-breaker` — TS-side rateLimitCircuitBreaker.tripBreaker
 *     emits this when GitHub returns 429 mid-stage. Pre-#3605 it only showed
 *     a VSCode toast (gap noted in PR #3577).
 *   - `safety:cascading-failures` — Go-side cascadeTracker emits this when
 *     3 failures land inside the 30-minute sliding window. NOT in any auto-
 *     resume self-clear path — operator triage required.
 *   - `safety:lifetime-failure-cap` — Go-side per-issue chronic-failure cap.
 *     Halts ALL dispatching until triaged, exactly the event an unattended
 *     (dark-factory) operator must hear about remotely — a transient VSCode
 *     toast was the only signal when it stopped the bowlsheet factory for
 *     2h on 2026-07-11.
 *
 * Issue #3605 bullet C.
 */
export const CASCADE_PAUSE_TRIGGERS = new Set<string>([
  "rate-limit-circuit-breaker",
  "safety:cascading-failures",
  "safety:lifetime-failure-cap",
]);

/**
 * Module-scope reference to the runtime-tier `autonomous.enabled_repos` service.
 * Captured by `registerAutonomousCommands` so the stall watchdog (which runs on
 * a recursive timer outside any closure that holds the parameter) can read the
 * latest allowlist on every cycle without a Stop/Start. Issue #3427.
 *
 * Live read on each tick is intentional: the user's checkbox toggles update
 * the runtime store synchronously, and we want the next 30s watchdog tick to
 * pick up the new value without restarting autonomous.
 */
let _enabledReposConfigService: EnabledReposConfigService | null = null;
let stallWatchdogInFlight = false;
let stallWatchdogConsecutiveFailures = 0;
const alertedStalls = new Map<string, string>();
// #3509 — per-repo cache of when boardList last returned 0 "In Progress" items.
// Key: "<owner>/<repo>" (or "<owner>/proj:<projectNumber>" for org-level boards).
// Value: epoch ms when the empty result was observed.
// Skips the boardList call until EMPTY_BOARD_SKIP_MS has elapsed.
const emptyBoardLastSeenAt = new Map<string, number>();

// #3020 — when board.list times out (typically due to GitHub rate limiting),
// the original 30s polling cadence kept hammering the API every 30s for an
// hour. Each call took 30s to time out, then immediately fired again. We back
// off exponentially after consecutive failures, then snap back to fast cadence
// once a cycle succeeds.
// #3509 — raised from 30s to 2 min. The watchdog detects stalls that last
// >N minutes; checking every 30s vs every 2 min provides no benefit and burns
// 6× as many boardList GraphQL calls (360/hr vs 60/hr per window per repo).
const WATCHDOG_BASE_INTERVAL_MS = 2 * 60_000;
// Issue #3203: bumped from 5 min to 30 min so multi-hour DNS/network outages
// stop hammering api.github.com at floor cadence. Exponential growth (2^n)
// reaches 30 min at the 9th consecutive failure, which is the first watchdog
// tick past the ~12-min mark — long enough to indicate real outage, short
// enough to recover quickly when the network returns.
const WATCHDOG_MAX_INTERVAL_MS = 30 * 60_000;
const WATCHDOG_BACKOFF_AFTER_FAILURES = 3;
// #3509 — if boardList returns 0 "In Progress" items for a repo, skip that
// repo's boardList call for this many ms (5 min). Eliminates unnecessary
// GraphQL calls when there is nothing active to stall-detect.
const EMPTY_BOARD_SKIP_MS = 5 * 60_000;
// Minimum remaining GitHub API quota before the watchdog considers the rate
// limit recovered. Matches the same threshold used in preCheckAuth.
const RATE_LIMIT_PROBE_HEADROOM = 200;
// Suppress repeat "skipping — breaker open" log entries: emit at most once
// per trip (first occurrence always logged), then once per 5 min.
const BREAKER_SKIP_LOG_INTERVAL_MS = 5 * 60_000;
let lastBreakerSkipLoggedAt = 0;

function getOutputChannel(): vscode.OutputChannel {
  if (!autonomousOutputChannel) {
    autonomousOutputChannel = vscode.window.createOutputChannel("Nightgauge Autonomous");
  }
  return autonomousOutputChannel;
}

// Issue #3446 — last cooldown scan timestamp we logged, so we emit one
// "[cooldown] Dispatch suppressed …" line per *new* scan rather than every
// liveness tick. Reset on transition out of cooldown / autonomous stop.
let _lastCooldownLoggedScanAt: string | null = null;

/** Reset internal cooldown-log dedup state. Exported for unit tests. */
export function _resetCooldownLogTickStateForTests(): void {
  _lastCooldownLoggedScanAt = null;
}

/**
 * Emit "[cooldown] Dispatch suppressed for N candidates …" to the autonomous
 * output channel when a scan was blocked by an active quota cooldown. Dedupes
 * by `lastScanAt` so a single suppressed cycle produces exactly one log line
 * even though the liveness probe runs every 10s. Issue #3446.
 */
export function maybeLogCooldownTick(
  result: {
    status?: string;
    lastScanAt?: string;
    quotaCooldownUntil?: string;
    quotaCooldownReason?: string;
    lastRejectionReasons?: Record<string, number>;
    remaining?: number;
  },
  logger: Logger,
  channel: vscode.OutputChannel = getOutputChannel(),
  now: Date = new Date()
): boolean {
  // Only emit when cooldown is actively suppressing the cycle. We check the
  // rejection-reason marker (set by the Go scheduler) — not just the
  // cooldown deadline — so we don't fire a log while autonomous is paused
  // or otherwise idle with a stale-but-future cooldown deadline.
  const blocked = result.lastRejectionReasons?.["quota-cooldown"] ?? 0;
  if (blocked < 1) return false;
  const deadline = parseFutureCooldown(result.quotaCooldownUntil, now);
  if (!deadline) return false;
  const scanAt = result.lastScanAt ?? "";
  if (scanAt && scanAt === _lastCooldownLoggedScanAt) return false;
  _lastCooldownLoggedScanAt = scanAt || _lastCooldownLoggedScanAt;
  const remaining = formatCooldownRemaining(deadline, now);
  const candidates = typeof result.remaining === "number" ? result.remaining : 0;
  const reason = result.quotaCooldownReason ? `: ${result.quotaCooldownReason}` : "";
  const line =
    `[${now.toISOString()}] [cooldown] Dispatch suppressed for ${candidates} candidate${candidates === 1 ? "" : "s"} ` +
    `— quota cooldown active until ${deadline.toISOString()} (${remaining} remaining)${reason}`;
  channel.appendLine(line);
  logger.info("Autonomous cooldown tick logged", {
    lastScanAt: scanAt,
    until: deadline.toISOString(),
    remaining,
  });
  return true;
}

interface WatchdogRepoContext {
  workspaceRoot: string;
  owner: string;
  repo: string;
  projectNumber: number;
  ownerType?: string;
}

interface WatchdogPRView {
  state?: string;
  checkStatus?: string;
  mergeable?: string;
}

function nextWatchdogIntervalMs(): number {
  // When SSE is connected, widen polling to 5 min — events drive rescans instead.
  // Revert to base cadence after subscriber has been disconnected for >2 min.
  if (_projectEventSubscriber?.isConnected()) {
    return SSE_WIDENED_INTERVAL_MS;
  }
  if (
    _projectEventSubscriber &&
    !_projectEventSubscriber.isConnected() &&
    _projectEventSubscriber.getDisconnectedDurationMs() <= DISCONNECT_REVERT_THRESHOLD_MS
  ) {
    // Still within grace period after disconnect — keep widened interval briefly.
    return SSE_WIDENED_INTERVAL_MS;
  }

  if (stallWatchdogConsecutiveFailures < WATCHDOG_BACKOFF_AFTER_FAILURES) {
    return WATCHDOG_BASE_INTERVAL_MS;
  }
  // Exponential after threshold: 30s × 2^(failures - 3), capped at 5 min.
  // 3rd failure → 60s, 4th → 120s, 5th → 240s, 6th+ → 300s.
  const exp = stallWatchdogConsecutiveFailures - WATCHDOG_BACKOFF_AFTER_FAILURES + 1;
  const ms = WATCHDOG_BASE_INTERVAL_MS * Math.pow(2, exp);
  return Math.min(ms, WATCHDOG_MAX_INTERVAL_MS);
}

function scheduleNextWatchdog(logger: Logger): void {
  if (stallWatchdogTimer === null && stallWatchdogConsecutiveFailures < 0) return; // stopped
  const interval = nextWatchdogIntervalMs();
  stallWatchdogTimer = setTimeout(async () => {
    await runAutonomousStallWatchdog(logger);
    // Re-arm only if still active (stop sets timer to null).
    if (stallWatchdogTimer !== null || stallWatchdogConsecutiveFailures >= 0) {
      scheduleNextWatchdog(logger);
    }
  }, interval);
}

function startAutonomousStallWatchdog(logger: Logger): void {
  if (stallWatchdogTimer) return;
  stallWatchdogConsecutiveFailures = 0;
  // Fire once immediately, then schedule the next via the recursive timer so
  // the cadence can adapt to consecutive failures.
  void runAutonomousStallWatchdog(logger).then(() => scheduleNextWatchdog(logger));
  startLivenessProbe(logger);
}

function stopAutonomousStallWatchdog(): void {
  if (stallWatchdogTimer) {
    clearTimeout(stallWatchdogTimer);
    stallWatchdogTimer = null;
  }
  stallWatchdogInFlight = false;
  stallWatchdogConsecutiveFailures = -1; // sentinel: "stopped"
  alertedStalls.clear();
  emptyBoardLastSeenAt.clear();
  stopLivenessProbe();
  // #3296 — clear the network-outage breaker too so a fresh autonomous
  // start begins with a clean counter (no carryover from a prior outage).
  // Pass `null` (success-shaped observation) to reset the connectivity
  // counter and breaker state.
  // We don't have a logger here; use the output channel-backed logger via
  // a lightweight no-op shim — observeWatchdogResult only logs on
  // recovery (counter > 0), so on a fresh stop this is a silent reset.
  void observeWatchdogResult(null, {
    info: () => {},
    warn: () => {},
    error: () => {},
    debug: () => {},
  } as unknown as Logger);
}

// ── Liveness probe functions ─────────────────────────────────────────────────

function startLivenessProbe(logger: Logger): void {
  if (livenessProbeTimer) return;
  livenessConsecutiveFailures = 0;
  backendDisconnected = false;

  // Subscribe to socket disconnect events — backend crash is detected immediately
  // without waiting for the polling cycle.
  const ipc = IpcClient.getInstance();
  livenessStatusDisposable = ipc.onDidChangeStatus((connected) => {
    if (!connected && !backendDisconnected) {
      livenessConsecutiveFailures = LIVENESS_FAILURE_THRESHOLD;
      void handleBackendDisconnected(logger);
    }
  });

  scheduleLivenessProbe(logger);
}

function scheduleLivenessProbe(logger: Logger): void {
  if (backendDisconnected) return;
  livenessProbeTimer = setTimeout(() => {
    void runLivenessProbe(logger).then(() => {
      if (!backendDisconnected) {
        scheduleLivenessProbe(logger);
      }
    });
  }, LIVENESS_PROBE_INTERVAL_MS);
}

async function runLivenessProbe(logger: Logger): Promise<void> {
  if (backendDisconnected) return;
  try {
    const ipc = IpcClient.getInstance();
    const probePromise = ipc.autonomousStatus();
    const timeoutPromise = new Promise<never>((_, reject) =>
      setTimeout(() => reject(new Error("liveness probe timed out")), LIVENESS_PROBE_TIMEOUT_MS)
    );
    const result = await Promise.race([probePromise, timeoutPromise]);
    // Success — reset failure counter
    if (livenessConsecutiveFailures > 0) {
      logger.info("Autonomous liveness probe recovered", {
        failuresBeforeRecovery: livenessConsecutiveFailures,
      });
    }
    livenessConsecutiveFailures = 0;
    // Reconcile the status-bar badge against the authoritative live state
    // (Issue #3251). Even with the autonomous.statusChanged event in place,
    // the probe is a belt-and-suspenders backstop: if a transition event
    // were ever dropped (extension reload, IPC reconnect mid-transition),
    // the next probe self-heals the badge.
    if (_autonomousStatusBar && result && typeof result.status === "string") {
      reconcileAutonomousBadge(result, logger);
    }
    // Issue #3446 — emit a cooldown notice to the autonomous output channel
    // when a scan was suppressed by the global Anthropic-quota cooldown.
    // The scheduler records `LastRejectionReasons["quota-cooldown"] = 1` on
    // each blocked cycle and refreshes `LastScanAt`. We emit one log line
    // per *new* scan timestamp so the user sees activity rather than a
    // silent idle, without spamming the channel inside a single scan.
    maybeLogCooldownTick(result, logger);
  } catch (error) {
    livenessConsecutiveFailures += 1;
    logger.warn("Autonomous liveness probe failed", {
      error: error instanceof Error ? error.message : String(error),
      consecutiveFailures: livenessConsecutiveFailures,
    });
    if (livenessConsecutiveFailures >= LIVENESS_FAILURE_THRESHOLD) {
      await handleBackendDisconnected(logger);
    }
  }
}

/**
 * Reconcile the status-bar badge against the authoritative live status from
 * the Go scheduler (Issue #3251). Used by the liveness probe as a backstop
 * for the `autonomous.statusChanged` event subscription so the badge
 * self-heals if a transition event is ever missed.
 */
function reconcileAutonomousBadge(
  result: {
    status: string;
    running?: { length?: number } | unknown[];
    remaining?: number;
    quotaCooldownUntil?: string;
  },
  logger: Logger
): void {
  const sb = _autonomousStatusBar;
  if (!sb) return;
  const runningCount = Array.isArray(result.running) ? result.running.length : 0;
  const remaining = typeof result.remaining === "number" ? result.remaining : 0;
  switch (result.status) {
    case "running": {
      // Issue #3446 — when a global quota cooldown is active, show the
      // "cooldown until …" badge instead of the misleading "running" label.
      // The scheduler's logical Status is "running" because dispatch will
      // resume automatically when the deadline expires; the UI distinguishes
      // the two states.
      const cooldownDeadline = parseFutureCooldown(result.quotaCooldownUntil);
      if (cooldownDeadline) {
        sb.showAutonomousCooldown(cooldownDeadline);
      } else {
        sb.showAutonomousRunning(runningCount, remaining);
      }
      break;
    }
    case "paused":
    case "safety_tripped":
      sb.showAutonomousPaused();
      break;
    case "stopped":
    case "complete":
    case "budget_exhausted":
    case "crashed":
      // Status bar is updated by the originating command; don't override here.
      break;
    default:
      logger.debug("reconcileAutonomousBadge: unknown status", { status: result.status });
      return;
  }
  setAutonomousContextKeys(result.status);
}

/**
 * Parse a possibly-empty ISO-8601 cooldown deadline. Returns a Date when the
 * deadline is parseable and still in the future, otherwise null (which the
 * caller treats as "no cooldown active"). Centralised so the status bar,
 * prompt, and output-channel logger all use the same parse rules. Issue #3446.
 */
export function parseFutureCooldown(
  value: string | undefined,
  now: Date = new Date()
): Date | null {
  if (!value) return null;
  const d = new Date(value);
  if (isNaN(d.getTime())) return null;
  if (d.getTime() <= now.getTime()) return null;
  return d;
}

async function handleBackendDisconnected(logger: Logger): Promise<void> {
  if (backendDisconnected) return; // idempotent
  backendDisconnected = true;
  stopLivenessProbe();

  logger.warn("Autonomous backend disconnected — Go backend process may have crashed");
  _autonomousStatusBar?.showAutonomousDisconnected();
  setAutonomousContextKeys("crashed");

  const channel = getOutputChannel();
  channel.appendLine(
    `[${new Date().toISOString()}] Backend disconnected — autonomous mode stopped unexpectedly. ` +
      `Check .nightgauge/logs/autonomous-exits.jsonl for crash details.`
  );

  const action = await vscode.window.showWarningMessage(
    "Autonomous mode: backend lost connection. The Go backend may have crashed.",
    "Restart",
    "Dismiss"
  );
  if (action === "Restart") {
    await vscode.commands.executeCommand("nightgauge.autonomousRun");
  }
}

function stopLivenessProbe(): void {
  if (livenessProbeTimer) {
    clearTimeout(livenessProbeTimer);
    livenessProbeTimer = null;
  }
  livenessStatusDisposable?.dispose();
  livenessStatusDisposable = null;
}

/**
 * Pure helper: drop watchdog repo contexts that are not in the user's
 * `autonomous.enabled_repos` allowlist. Empty/absent allowlist = "scan all"
 * (matches the Go-side `resolveAutonomousAllowlist` and tree-view semantics).
 *
 * Matching is case-insensitive on the short repo name via `entryMatchesRepo`,
 * so allowlist entries written as either "nightgauge" or
 * "nightgauge/nightgauge" both work.
 *
 * Exported for unit testing — Issue #3427.
 */
export function filterRepoContextsByEnabledRepos<T extends { repo: string }>(
  contexts: T[],
  enabledRepos: string[]
): T[] {
  if (!enabledRepos || enabledRepos.length === 0) return contexts;
  return contexts.filter((ctx) => enabledRepos.some((entry) => entryMatchesRepo(entry, ctx.repo)));
}

async function collectWatchdogRepoContexts(): Promise<WatchdogRepoContext[]> {
  const ipc = IpcClient.getInstance();
  const repos: WatchdogRepoContext[] = [];
  for (const folder of vscode.workspace.workspaceFolders ?? []) {
    const workspaceRoot = folder.uri.fsPath;
    const identity = await getRepoIdentity(workspaceRoot);
    if (!identity) continue;
    try {
      const project = await ipc.configGetProjectConfig(workspaceRoot);
      if (!project.projectNumber) continue;
      repos.push({
        workspaceRoot,
        owner: identity.owner,
        repo: identity.repo,
        projectNumber: project.projectNumber,
        ownerType: project.ownerType,
      });
    } catch {
      // Skip folders without project config.
    }
  }

  // Issue #3427: respect the user's `autonomous.enabled_repos` allowlist on
  // every watchdog cycle. Without this filter the watchdog polled every
  // workspace folder every 30s — even ones the user explicitly deselected —
  // burning GitHub quota and tripping `SharedRateLimitTracker` (floor=100).
  // Live read so checkbox toggles take effect on the next tick (no restart).
  const enabledRepos = _enabledReposConfigService?.readEnabledRepos() ?? [];
  return filterRepoContextsByEnabledRepos(repos, enabledRepos);
}

async function rerunPrMerge(
  workspaceRoot: string,
  prNumber: number,
  logger: Logger,
  automatic: boolean
): Promise<void> {
  const terminal = vscode.window.createTerminal({
    name: `Nightgauge PR Merge #${prNumber}`,
    cwd: workspaceRoot,
  });
  terminal.show(true);
  terminal.sendText(`nightgauge pr merge ${prNumber}`);
  logger.info("Triggered stalled pr-merge recovery", { prNumber, workspaceRoot, automatic });
}

async function runAutonomousStallWatchdog(logger: Logger): Promise<void> {
  if (stallWatchdogInFlight) return;
  stallWatchdogInFlight = true;
  IpcClientBase.activeCallSource = "stall-watchdog";
  try {
    // When the rate-limit circuit breaker is open, probe the quota before
    // skipping. Without this probe the system deadlocks: paused autonomous
    // never calls preCheckAuth (the normal re-arm path) and the watchdog
    // always skips, so noteRateLimitOk() never fires and autonomous stays
    // paused indefinitely even after GitHub's quota resets.
    if (isBreakerTripped()) {
      // #3509 — skip the probe entirely when GitHub's own resetAt tells us
      // the quota window hasn't closed yet. This eliminates githubRateLimit()
      // calls every 2 min while the rate limit is provably still exhausted.
      const resetAt = getBreakerResetAt();
      const resetElapsed = resetAt > 0 && Date.now() >= resetAt * 1000 + 5_000;
      if (!resetElapsed && resetAt > 0) {
        const now = Date.now();
        if (
          lastBreakerSkipLoggedAt < getLastTripAt() ||
          now - lastBreakerSkipLoggedAt > BREAKER_SKIP_LOG_INTERVAL_MS
        ) {
          lastBreakerSkipLoggedAt = now;
          const secsLeft = Math.ceil((resetAt * 1000 + 5_000 - now) / 1000);
          logger.debug(
            `runAutonomousStallWatchdog: skipping — rate-limit circuit breaker open, probe suppressed for ${secsLeft}s`
          );
        }
        return;
      }

      let probeCleared = false;
      try {
        const ipc = IpcClient.getInstance();
        const info = await ipc.githubRateLimit();
        if (info.remaining >= RATE_LIMIT_PROBE_HEADROOM) {
          noteRateLimitOk();
          logger.info(
            "runAutonomousStallWatchdog: rate-limit quota recovered — re-arming breaker and resuming sweep"
          );
          probeCleared = true;
        }
      } catch {
        // IPC probe failed — can't confirm recovery; treat as still exhausted.
      }
      if (!probeCleared) {
        // Suppress log spam: always log the first skip per trip, then at most
        // once per BREAKER_SKIP_LOG_INTERVAL_MS.
        const now = Date.now();
        if (
          lastBreakerSkipLoggedAt < getLastTripAt() ||
          now - lastBreakerSkipLoggedAt > BREAKER_SKIP_LOG_INTERVAL_MS
        ) {
          lastBreakerSkipLoggedAt = now;
          logger.debug("runAutonomousStallWatchdog: skipping — rate-limit circuit breaker is open");
        }
        return;
      }
    }

    const repos = await collectWatchdogRepoContexts();
    const ipc = IpcClient.getInstance();
    const seen = new Set<string>();

    for (const repo of repos) {
      const stallCfg = getAutonomousStallConfig(repo.workspaceRoot);

      // #3509 — skip boardList if this repo's board was empty on the last check
      // and the skip window hasn't expired. Eliminates redundant GraphQL calls
      // when nothing is actively In Progress.
      const emptyKey = `${repo.owner}/proj:${repo.projectNumber}`;
      const lastEmptyAt = emptyBoardLastSeenAt.get(emptyKey) ?? 0;
      if (lastEmptyAt > 0 && Date.now() - lastEmptyAt < EMPTY_BOARD_SKIP_MS) {
        continue;
      }

      const items = await ipc.boardList(
        repo.owner,
        repo.projectNumber,
        "In Progress",
        repo.ownerType
      );

      if (items.length === 0) {
        emptyBoardLastSeenAt.set(emptyKey, Date.now());
        continue;
      }
      // Board has items — clear the empty cache so the next tick always checks.
      emptyBoardLastSeenAt.delete(emptyKey);

      for (const item of items) {
        if (item.isPR || item.isEpic) continue;
        const prInfo = await getPRForIssue(item.number, repo.workspaceRoot);
        if (!prInfo) continue;

        const pr = (await ipc.prView(repo.owner, repo.repo, prInfo.number)) as WatchdogPRView;
        const stall = detectAutonomousStall({
          boardStatus: item.status,
          updatedAt: item.updatedAt,
          prState: pr.state,
          prCheckStatus: pr.checkStatus,
          prMergeable: pr.mergeable,
          thresholdMinutes: stallCfg.stallDetectionMinutes,
        });
        if (!stall.stalled) continue;

        const issueKey = `${repo.owner}/${repo.repo}#${item.number}`;
        const fingerprint = `${item.updatedAt ?? ""}|${prInfo.number}|${pr.state ?? ""}|${pr.checkStatus ?? ""}|${pr.mergeable ?? ""}`;
        seen.add(issueKey);
        if (alertedStalls.get(issueKey) === fingerprint) {
          continue;
        }
        alertedStalls.set(issueKey, fingerprint);

        const recoveryCommand = `nightgauge pr merge ${prInfo.number}`;
        const message =
          `Stalled issue detected: ${repo.owner}/${repo.repo}#${item.number} has been In Progress ` +
          `for ${stall.stalledMinutes}m with green PR #${prInfo.number}. Recovery: ${recoveryCommand}`;

        getOutputChannel().appendLine(`[${new Date().toISOString()}] ${message}`);

        if (stallCfg.autoRedispatchStalled) {
          void vscode.window.showWarningMessage(
            `${message}. Auto re-running pr-merge now.`,
            "Dismiss"
          );
          await rerunPrMerge(repo.workspaceRoot, prInfo.number, logger, true);
          continue;
        }

        const action = await vscode.window.showWarningMessage(
          message,
          "Re-run pr-merge",
          "Dismiss"
        );
        if (action === "Re-run pr-merge") {
          await rerunPrMerge(repo.workspaceRoot, prInfo.number, logger, false);
        }
      }
    }

    for (const key of Array.from(alertedStalls.keys())) {
      if (!seen.has(key)) {
        alertedStalls.delete(key);
      }
    }

    // Successful sweep — reset back-off so cadence snaps back to base.
    if (stallWatchdogConsecutiveFailures > 0) {
      logger.info("Autonomous stall watchdog recovered", {
        failuresBeforeRecovery: stallWatchdogConsecutiveFailures,
      });
    }
    stallWatchdogConsecutiveFailures = 0;
    // #3296 — also clear the network-outage breaker on a successful sweep.
    // observeWatchdogResult(null, ...) resets the consecutive-connectivity
    // counter and re-arms the breaker for the next outage.
    void observeWatchdogResult(null, logger);
    // #3307 — re-arm the rate-limit breaker on a successful sweep so the next
    // 429 can trip it again, and auto-resume autonomous when the pause was
    // caused by a self-clearing condition (rate-limit / network-outage).
    // Without this, a rate-limit pause is a deadlock: paused autonomous never
    // calls preCheckAuth, which is the only path that re-arms the breaker.
    noteRateLimitOk();
    void autoResumeAfterRecovery(logger);
  } catch (error) {
    stallWatchdogConsecutiveFailures += 1;
    const nextMs = nextWatchdogIntervalMs();
    logger.warn("Autonomous stall watchdog failed", {
      error: error instanceof Error ? error.message : String(error),
      consecutiveFailures: stallWatchdogConsecutiveFailures,
      nextIntervalSeconds: Math.round(nextMs / 1000),
    });
    // #3020 — if the watchdog failure looks like a GitHub rate-limit, trip
    // the autonomous circuit breaker. The watchdog itself is a leading
    // indicator: it polls every 30s, so it'll observe the 429 well before
    // the next pipeline-level call. Don't await — we don't want a hung IPC
    // to delay the next watchdog cycle.
    void tripBreakerIfRateLimited(error, logger, { source: "autonomous stall watchdog" });
    // #3296 — connectivity outage circuit breaker. Distinct from the
    // rate-limit breaker above: this one detects DNS / ECONNREFUSED / network
    // unreachable / IPC timeouts (api.github.com unreachable, gh CLI
    // can't connect) and aborts active LLM stage subprocesses once
    // consecutive failures cross the threshold (default 3 ≈ 1.5–2 min).
    // Without this, an outage like 2026-05-07 burns LLM cost for hours
    // until Anthropic's stream-idle-timeout fires (#3216 cost $20.87,
    // #3230 cost $29.43 in one ~2.5h DNS outage).
    void observeWatchdogResult(error, logger, {
      source: "autonomous stall watchdog",
    });
  } finally {
    stallWatchdogInFlight = false;
    IpcClientBase.activeCallSource = undefined;
  }
}

/**
 * Format an AutonomousStatusResult into a human-readable string for the
 * output channel or quick-pick display.
 *
 * The `completed` and `failed` arrays in the status result are LIFETIME
 * state — they persist across Start/Stop cycles in `.nightgauge/
 * autonomous/state.json`. Previously the formatter printed them verbatim
 * under a header saying "Started: 1s ago", which made every session start
 * look like 8 failures had just happened. This formatter separates:
 *
 *   • "This session" — items with timestamps after `status.startedAt`.
 *   • "History" — counts-only summary of everything prior.
 *   • "Repeat failures" — deduplicated ×N list of issues that failed more
 *     than once across all sessions (the real actionable signal).
 *
 * Passing `now` is for deterministic testing; production callers omit it.
 */
function formatStatus(status: AutonomousStatusResult, now: Date = new Date()): string {
  const lines: string[] = [];

  if (status.status === "backend_disconnected") {
    lines.push("Autonomous Mode: BACKEND DISCONNECTED");
    lines.push("The Go backend process stopped unexpectedly.");
    lines.push(
      'Check .nightgauge/logs/autonomous-exits.jsonl for crash details, or click "Start Autonomous" to restart.'
    );
    return lines.join("\n");
  }

  lines.push(`Autonomous Mode: ${status.status.toUpperCase()}`);

  if (status.status === "safety_tripped" && status.safety?.tripReason) {
    lines.push(`Safety trip reason: ${status.safety.tripReason}`);
    lines.push(`Consecutive failures: ${status.safety.consecutiveFailures ?? "unknown"}`);
    lines.push('Use "Resume Autonomous" or click Start to reset and continue.');
  }

  // ─── Header: session age + lifetime cycles ─────────────────────────
  if (status.startedAt) {
    const elapsed = formatElapsedFrom(status.startedAt, now);
    const cyc = `${status.cyclesRun.toLocaleString()} total cycles`;
    lines.push(`Session started ${elapsed ? `${elapsed} ago` : "just now"} · ${cyc}`);
  } else {
    lines.push(`Cycles: ${status.cyclesRun.toLocaleString()}`);
  }

  // ─── Budget ────────────────────────────────────────────────────────
  if (status.tokensCeiling > 0) {
    const pct = Math.round((status.tokensSpent / status.tokensCeiling) * 100);
    lines.push(
      `Budget: ${status.tokensSpent.toLocaleString()} / ${status.tokensCeiling.toLocaleString()} tokens (${pct}%)`
    );
  } else if (status.tokensSpent > 0) {
    lines.push(`Tokens spent: ${status.tokensSpent.toLocaleString()} (no ceiling)`);
  }

  // ─── Running (in-flight NOW) ───────────────────────────────────────
  if (status.running.length > 0) {
    lines.push("");
    lines.push(`Running (${status.running.length}):`);
    for (const r of status.running) {
      const elapsed = formatElapsedFrom(r.startedAt, now);
      lines.push(`  ${formatIssueRef(r.repo, r.number, r.title)}${elapsed ? ` (${elapsed})` : ""}`);
    }
  }

  // ─── Remaining (queue depth) ───────────────────────────────────────
  if (status.remaining > 0) {
    lines.push("");
    lines.push(`Remaining: ${status.remaining} issues`);
  }

  // ─── Split completed/failed into "this session" vs "history" ───────
  const sessionStart = parseIsoOrNull(status.startedAt);
  const sessionCompleted = status.completed.filter(
    (c) => sessionStart !== null && parseIsoOrNull(c.completedAt)! >= sessionStart
  );
  const sessionFailed = status.failed.filter(
    (f) => sessionStart !== null && parseIsoOrNull(f.failedAt)! >= sessionStart
  );
  const historicalCompletedCount = status.completed.length - sessionCompleted.length;
  const historicalFailed = status.failed.filter((f) => !sessionFailed.includes(f));

  // ─── This session ──────────────────────────────────────────────────
  lines.push("");
  if (sessionCompleted.length === 0 && sessionFailed.length === 0) {
    lines.push("This session — nothing completed or failed yet");
  } else {
    lines.push(
      `This session (${sessionCompleted.length} completed, ${sessionFailed.length} failed):`
    );
    for (const c of sessionCompleted) {
      const elapsed = formatElapsedFrom(c.completedAt, now);
      lines.push(
        `  ✓ ${formatIssueRef(c.repo, c.number, c.title)}${elapsed ? ` (${elapsed})` : ""}`
      );
    }
    for (const f of sessionFailed) {
      const reason = f.reason ? ` — ${f.reason}` : "";
      const elapsed = formatElapsedFrom(f.failedAt, now);
      lines.push(
        `  ✗ ${formatIssueRef(f.repo, f.number, f.title)}${reason}${elapsed ? ` (${elapsed})` : ""}`
      );
    }
  }

  // ─── History (compact summary of prior sessions) ───────────────────
  if (historicalCompletedCount > 0 || historicalFailed.length > 0) {
    const uniqueFailed = groupFailuresByIssue(historicalFailed);
    const mostRecentFail = historicalFailed.reduce<Date | null>((latest, f) => {
      const t = parseIsoOrNull(f.failedAt);
      return t && (!latest || t > latest) ? t : latest;
    }, null);
    const mostRecent = mostRecentFail
      ? ` (most recent ${formatElapsedFrom(mostRecentFail.toISOString(), now)} ago)`
      : "";
    const failureSummary =
      historicalFailed.length > 0
        ? ` · ${historicalFailed.length} failure${historicalFailed.length === 1 ? "" : "s"} on ${uniqueFailed.size} unique issue${uniqueFailed.size === 1 ? "" : "s"}${mostRecent}`
        : "";

    lines.push("");
    lines.push("History (previous sessions):");
    lines.push(`  ${historicalCompletedCount} completed${failureSummary}`);

    // Repeat failures — only surface issues that have failed 2+ times.
    // A single historical failure is noise; repeated failures are a signal.
    const recurring = Array.from(uniqueFailed.entries())
      .filter(([, info]) => info.count >= 2)
      .sort((a, b) => b[1].count - a[1].count || b[1].lastAt.getTime() - a[1].lastAt.getTime())
      .slice(0, 5);

    if (recurring.length > 0) {
      lines.push("");
      lines.push("Repeat failures — may need manual attention:");
      for (const [key, info] of recurring) {
        const reason = info.reason ? ` — ${info.reason}` : "";
        const when = formatElapsedFrom(info.lastAt.toISOString(), now);
        lines.push(
          `  ${key} × ${info.count} failures${when ? ` (last ${when} ago)` : ""}${reason}`
        );
      }
    }
  }

  return lines.join("\n");
}

/**
 * Format `{repo}#{number}: {title}` with graceful handling of empty titles
 * (which happen when a failure record was written without a title — e.g.
 * early crashes or refined issues). Avoids the trailing `:  — reason` that
 * made the old display look broken.
 */
function formatIssueRef(repo: string, number: number, title: string | undefined): string {
  const base = `${repo}#${number}`;
  return title && title.trim() ? `${base}: ${title}` : base;
}

interface FailureGroup {
  count: number;
  lastAt: Date;
  reason: string | undefined;
}

/**
 * Deduplicate a failure list by `{repo}#{number}`, keeping the count and the
 * most-recent failure timestamp + reason per issue.
 *
 * Newer state files (post Go-side dedup) have at most one FailedItem per
 * issue with an `attemptCount` field. Legacy state files may still contain
 * one row per attempt with no `attemptCount`. This function handles both:
 * when `attemptCount` is present it is trusted; when absent each row counts
 * as 1. Either way the display sums to the correct per-issue total.
 */
function groupFailuresByIssue(failed: AutonomousStatusResult["failed"]): Map<string, FailureGroup> {
  const byIssue = new Map<string, FailureGroup>();
  for (const f of failed) {
    const key = `${f.repo}#${f.number}`;
    const at = parseIsoOrNull(f.failedAt) ?? new Date(0);
    const attempts = f.attemptCount && f.attemptCount > 0 ? f.attemptCount : 1;
    const existing = byIssue.get(key);
    if (!existing) {
      byIssue.set(key, { count: attempts, lastAt: at, reason: f.reason });
    } else {
      existing.count += attempts;
      if (at > existing.lastAt) {
        existing.lastAt = at;
        existing.reason = f.reason ?? existing.reason;
      }
    }
  }
  return byIssue;
}

function parseIsoOrNull(iso: string | undefined): Date | null {
  if (!iso) return null;
  const t = new Date(iso);
  return Number.isNaN(t.getTime()) ? null : t;
}

/**
 * Format elapsed time since an ISO timestamp as a short human-readable string.
 *
 * Extends units up to days so that historical state (e.g. a failure from 3
 * days ago persisted in `autonomous/state.json`) reads naturally instead of
 * showing a confusing "4320m" that hides the age.
 *
 * `now` is injected for deterministic tests; production callers default to
 * `new Date()`.
 */
function formatElapsedFrom(isoTimestamp: string, now: Date = new Date()): string {
  if (!isoTimestamp) return "";
  const start = new Date(isoTimestamp).getTime();
  const diffMs = now.getTime() - start;
  if (diffMs < 0 || Number.isNaN(diffMs)) return "";

  const totalSeconds = Math.floor(diffMs / 1000);
  if (totalSeconds < 60) return `${totalSeconds}s`;

  const totalMinutes = Math.floor(totalSeconds / 60);
  if (totalMinutes < 60) return `${totalMinutes}m`;

  const totalHours = Math.floor(totalMinutes / 60);
  if (totalHours < 24) {
    const minutes = totalMinutes % 60;
    return minutes > 0 ? `${totalHours}h ${minutes}m` : `${totalHours}h`;
  }

  const days = Math.floor(totalHours / 24);
  const hours = totalHours % 24;
  return hours > 0 ? `${days}d ${hours}h` : `${days}d`;
}

/**
 * Register all autonomous mode commands.
 *
 * Returns an array of Disposable subscriptions that should be pushed
 * into the extension context.
 */
export function registerAutonomousCommands(
  logger: Logger,
  statusBar: StatusBarManager,
  queueService?: {
    enqueue(
      issueNumber: number,
      title: string,
      labels?: string[],
      blockedBy?: unknown,
      options?: { repoOverride?: { owner: string; repo: string } }
    ): Promise<{ position: number } | null>;
  } | null,
  /**
   * Phase 3 of #3313 (#3336) — runtime-tier service for `autonomous.enabled_repos`.
   * Used to (a) pre-intersect workspaceRepos with the runtime allowlist before
   * calling autonomousStart/Resume so the Go scheduler keeps filtering without
   * seeing the YAML key, and (b) write the user's selection from the
   * `autonomousSelectRepos` QuickPick.
   *
   * Optional so the registration still compiles when called outside the
   * normal bootstrap flow (e.g. minimal test harness). When absent, the
   * pre-intersection is skipped (workspaceRepos passes through unchanged)
   * and the QuickPick command logs a warning instead of writing.
   */
  enabledReposConfigService?: EnabledReposConfigService | null,
  /** Called after autonomous successfully starts — used to drain pre-existing queue items (#3532). */
  onAutonomousStart?: () => Promise<void>,
  /**
   * WorkspaceManager instance for deriving workspace-scoped repos from the
   * workspace manifest instead of folder-identity detection (#3766).
   */
  workspaceManager?: WorkspaceManager | null
): vscode.Disposable[] {
  _autonomousStatusBar = statusBar;
  // Capture for the stall watchdog so it can filter on every tick (#3427).
  _enabledReposConfigService = enabledReposConfigService ?? null;
  const disposables: vscode.Disposable[] = [];

  /**
   * Phase 3 of #3313 (#3336) — pre-intersect the workspace's repos with the
   * runtime-tier `autonomous.enabled_repos` allowlist before sending to the
   * Go scheduler. Mirrors the Go-side `resolveAutonomousAllowlist` rule:
   * "explicit user intent wins" — when the intersection is empty, fall back
   * to the explicit allowlist so the user's "scan only X" intent is honored
   * even if X isn't currently open in the workspace.
   *
   * Returns the list to send as the IPC `WorkspaceRepos` payload, or
   * undefined when the workspace is empty AND no allowlist is set (callers
   * pass undefined → Go scheduler's default scan-all behavior).
   */
  function intersectWithEnabledRepos(workspaceRepos: string[]): string[] | undefined {
    const enabled = enabledReposConfigService?.readEnabledRepos() ?? [];
    if (enabled.length === 0) {
      return workspaceRepos.length > 0 ? workspaceRepos : undefined;
    }
    if (workspaceRepos.length === 0) {
      return enabled;
    }
    // Case-insensitive intersection via the existing helper.
    const intersected = workspaceRepos.filter((wsRepo) => {
      const shortName = wsRepo.includes("/") ? wsRepo.split("/")[1] : wsRepo;
      return enabled.some((e) => entryMatchesRepo(e, shortName));
    });
    if (intersected.length === 0) {
      // Empty intersection — fall back to the explicit allowlist (Go-side
      // "explicit user intent wins" rule). Tested in
      // `runtimeWritesCleanTree.test.ts`.
      return enabled;
    }
    return intersected;
  }

  // ── Listen for autonomous.statusChanged events from Go scheduler ───
  // Every Go-side Status transition (Pause, Resume, safety_tripped,
  // complete, init) emits this event. The status-bar badge subscribes
  // here so it stays in sync without polling — even when the transition
  // came from a path the TS extension didn't initiate (e.g. Go-side
  // safety trip, or a Pause IPC call from a third party). Issue #3251.
  //
  // Safety-pause Discord notifications (Issue #3605 bullet C): when the
  // status is paused/safety_tripped AND the pauseTriggeredBy tag is one
  // of the wire-eligible safety triggers (rate-limit / cascading-failures),
  // fan the event out to the registered safety notifier so the operator
  // gets a Discord ping. Without this, #3577's rate-limit pause only fired
  // a VSCode toast — invisible if the editor wasn't focused.
  const ipc = IpcClient.getInstance();
  disposables.push(
    ipc.on("autonomous.statusChanged", (raw: unknown) => {
      const data = raw as {
        status?: string;
        pauseReason?: string;
        pauseTriggeredBy?: string;
        runningCount?: number;
        remaining?: number;
      };
      if (!data || typeof data.status !== "string") return;
      const channel = getOutputChannel();
      const why = data.pauseReason
        ? ` (${data.pauseTriggeredBy ?? "unknown"}: ${data.pauseReason})`
        : "";
      channel.appendLine(`[${new Date().toISOString()}] Autonomous status → ${data.status}${why}`);
      switch (data.status) {
        case "running":
          // Issue #3446 — the statusChanged payload doesn't carry cooldown
          // info, so eagerly fetch the full status snapshot. If a global
          // quota cooldown is in effect, render the cooldown badge instead
          // of the misleading "running" label. The liveness probe is a
          // 10s fallback; this avoids the brief window where the user sees
          // a green "running" badge that's actually idle.
          statusBar.showAutonomousRunning(data.runningCount ?? 0, data.remaining ?? 0);
          startAutonomousStallWatchdog(logger);
          void ipc
            .autonomousStatus()
            .then((snapshot) => {
              const deadline = parseFutureCooldown(snapshot.quotaCooldownUntil);
              if (deadline) {
                statusBar.showAutonomousCooldown(deadline);
              }
            })
            .catch(() => {
              // Liveness probe will reconcile if this fetch fails.
            });
          break;
        case "paused":
        case "safety_tripped":
          statusBar.showAutonomousPaused();
          // Keep the stall watchdog running while paused so it can detect
          // recovery (#3307 — auto-resume on rate-limit/outage clearance).
          // For safety_tripped, the watchdog is harmless when no candidates
          // are eligible.
          //
          // Issue #3605 bullet C: when the trigger tag is in the cascade-
          // notify allowlist (rate-limit / cascading-failures), fan the
          // event out to the registered safety notifier so the operator
          // gets a Discord ping. Wrapped in void(...) so the async work
          // doesn't block this synchronous handler — a hung webhook never
          // delays the status-bar refresh.
          if (
            _autonomousSafetyNotifier &&
            data.pauseTriggeredBy &&
            CASCADE_PAUSE_TRIGGERS.has(data.pauseTriggeredBy)
          ) {
            const notifier = _autonomousSafetyNotifier;
            void notifier(data.pauseTriggeredBy, data.pauseReason ?? "").catch((err) => {
              logger.error("autonomousCommands: safety-pause notifier threw", err);
            });
          }
          break;
        case "stopped":
        case "complete":
        case "budget_exhausted":
        case "crashed":
          stopAutonomousStallWatchdog();
          break;
      }
      setAutonomousContextKeys(data.status);
    })
  );

  // ── Listen for autonomous.dispatch events from Go scheduler ────────
  // When the Go autonomous scheduler finds a candidate, it emits this event
  // instead of using its own queue. We route it through the extension's
  // IssueQueueService → ConcurrentPipelineManager → HeadlessOrchestrator
  // so runs appear in the pipeline tree view.
  disposables.push(
    ipc.on("autonomous.dispatch", (raw: unknown) => {
      const data = raw as {
        owner: string;
        repo: string;
        issueNumber: number;
        title: string;
      };
      const channel = getOutputChannel();
      channel.appendLine(
        `[${new Date().toISOString()}] Dispatching ${data.owner}/${data.repo}#${data.issueNumber}: ${data.title}`
      );

      if (queueService) {
        queueService
          .enqueue(data.issueNumber, data.title, [], undefined, {
            repoOverride: { owner: data.owner, repo: data.repo },
          })
          .then((result) => {
            if (result) {
              logger.info("Autonomous dispatch enqueued", {
                issueNumber: data.issueNumber,
                repo: `${data.owner}/${data.repo}`,
                position: result.position,
              });
            }
          })
          .catch((err) => {
            logger.error("Autonomous dispatch failed", {
              issueNumber: data.issueNumber,
              error: String(err),
            });
          });
      } else {
        logger.warn("Autonomous dispatch received but no queue service available");
      }
    })
  );

  // ── Autonomous: Run ────────────────────────────────────────────────
  disposables.push(
    vscode.commands.registerCommand("nightgauge.autonomousRun", async () => {
      const confirm = await vscode.window.showWarningMessage(
        "Start autonomous mode? The pipeline will independently claim, plan, implement, and merge issues across all repos.",
        { modal: true },
        "Start",
        "Dry Run First"
      );

      if (!confirm) return;

      if (confirm === "Dry Run First") {
        // Redirect to dry run command
        await vscode.commands.executeCommand("nightgauge.autonomousDryRun");
        return;
      }

      try {
        const ipc = IpcClient.getInstance();

        // Check current status first. If safety_tripped, the goroutine is
        // still alive — autonomous.start will call Resume() to reset rails.
        // Show the user what tripped so they can make an informed decision.
        let currentStatus: AutonomousStatusResult | null = null;
        try {
          currentStatus = await ipc.autonomousStatus();
        } catch {
          // If status check fails, proceed with normal start
        }

        if (currentStatus?.status === "safety_tripped") {
          const tripReason =
            currentStatus.safety?.tripReason ?? "safety rail tripped (reason lost after reload)";
          const resume = await vscode.window.showWarningMessage(
            `Autonomous mode stopped: ${tripReason}. Resume and reset safety rails?`,
            { modal: true },
            "Resume"
          );
          if (resume !== "Resume") return;
        }

        // Issue #3446 — surface the global Anthropic-quota cooldown before
        // start. Without this prompt the user clicks "Run", autonomous
        // technically starts, and then nothing dispatches for the next 5
        // hours with no visible reason. Three explicit outcomes:
        //   - Wait: start anyway, scheduler idles until cooldown expires
        //   - Override: clear the cooldown via IPC, then start
        //   - Cancel: do nothing
        const cooldownDeadline = parseFutureCooldown(currentStatus?.quotaCooldownUntil);
        if (cooldownDeadline) {
          const label = formatCooldownLabel(cooldownDeadline);
          const remaining = formatCooldownRemaining(cooldownDeadline);
          const choice = await vscode.window.showWarningMessage(
            "Autonomous quota cooldown active",
            {
              modal: true,
              detail:
                `Cooldown active until ${label} (${remaining} remaining). ` +
                `Autonomous will not dispatch any issue until then.`,
            },
            "Wait (start anyway)",
            "Override cooldown and start"
          );
          if (!choice) return; // Cancel / dismiss
          if (choice === "Override cooldown and start") {
            try {
              const cleared = await ipc.autonomousClearQuotaCooldown();
              const ch = getOutputChannel();
              ch.appendLine(
                `[${new Date().toISOString()}] Quota cooldown cleared by user (previous deadline ${cleared.previousUntil ?? "?"}).`
              );
              logger.info("Quota cooldown cleared at start", {
                previousUntil: cleared.previousUntil,
                cleared: cleared.cleared,
              });
            } catch (err) {
              const message = err instanceof Error ? err.message : String(err);
              vscode.window.showErrorMessage(`Failed to clear quota cooldown: ${message}`);
              return;
            }
          }
        }

        // Resolve workspace repos so the scheduler only scans repos open
        // in this VS Code workspace — not all sibling directories.
        // Use WorkspaceManager (reads .vscode/nightgauge-workspace.yaml) as
        // the authoritative source to avoid folder-name/GitHub-slug mismatches
        // (#3766). Fall back to folder-identity detection when no manifest exists.
        const workspaceRepos: string[] = [];
        if (workspaceManager) {
          for (const r of workspaceManager.getAllRepositories()) {
            const gh = r.github;
            if (gh?.owner && gh?.repo) {
              workspaceRepos.push(`${gh.owner}/${gh.repo}`);
            }
          }
        }
        if (workspaceRepos.length === 0) {
          for (const folder of vscode.workspace.workspaceFolders ?? []) {
            try {
              const identity = await getRepoIdentity(folder.uri.fsPath);
              if (identity) {
                workspaceRepos.push(`${identity.owner}/${identity.repo}`);
              }
            } catch {
              // Skip folders without .nightgauge config
            }
          }
        }

        const result = await ipc.autonomousStart(intersectWithEnabledRepos(workspaceRepos));

        // Update status bar
        statusBar.showAutonomousRunning(result.running.length, result.remaining);
        startAutonomousStallWatchdog(logger);

        // Drain any queue items that survived the reload — they won't get an
        // onItemAdded event since they weren't newly added (#3532).
        if (onAutonomousStart) {
          void onAutonomousStart();
        }

        // Set context for UI visibility — drives Run/Pause/Resume/Stop button visibility.
        setAutonomousContextKeys("running");

        // Log to output channel
        const channel = getOutputChannel();
        const action = currentStatus?.status === "safety_tripped" ? "resumed" : "started";
        channel.appendLine(`[${new Date().toISOString()}] Autonomous mode ${action}`);
        channel.appendLine(formatStatus(result));
        channel.show(true);

        vscode.window.showInformationMessage(`Autonomous mode ${action}.`);
        logger.info(`Autonomous mode ${action}`, {
          running: result.running.length,
          remaining: result.remaining,
        });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error";
        logger.error("Failed to start autonomous mode", { error: message });
        vscode.window.showErrorMessage(`Failed to start autonomous mode: ${message}`);
      }
    })
  );

  // ── Autonomous: Dry Run ────────────────────────────────────────────
  disposables.push(
    vscode.commands.registerCommand("nightgauge.autonomousDryRun", async () => {
      try {
        const ipc = IpcClient.getInstance();
        const result = await ipc.autonomousStatus();

        const channel = getOutputChannel();
        channel.clear();
        channel.appendLine("=== Autonomous Mode: Dry Run Preview ===");
        channel.appendLine(`Status: ${result.status}`);
        channel.appendLine("");

        if (result.running.length > 0) {
          channel.appendLine(`Would process (${result.running.length} running):`);
          for (const r of result.running) {
            channel.appendLine(`  ${r.repo}#${r.number}: ${r.title}`);
          }
        }

        if (result.remaining > 0) {
          channel.appendLine(`Remaining candidates: ${result.remaining} issues`);
        } else {
          channel.appendLine("No candidates found. All issues may be complete or blocked.");
        }

        if (result.completed.length > 0) {
          channel.appendLine("");
          channel.appendLine(`Previously completed (${result.completed.length}):`);
          for (const c of result.completed) {
            channel.appendLine(`  ${c.repo}#${c.number}: ${c.title}`);
          }
        }

        if (result.failed.length > 0) {
          channel.appendLine("");
          channel.appendLine(`Previously failed (${result.failed.length}):`);
          for (const f of result.failed) {
            const reason = f.reason ? ` — ${f.reason}` : "";
            channel.appendLine(`  ${f.repo}#${f.number}: ${f.title}${reason}`);
          }
        }

        if (result.tokensCeiling > 0) {
          channel.appendLine("");
          const pct = Math.round((result.tokensSpent / result.tokensCeiling) * 100);
          channel.appendLine(
            `Budget: ${result.tokensSpent.toLocaleString()} / ${result.tokensCeiling.toLocaleString()} tokens (${pct}%)`
          );
        }

        channel.appendLine("");
        channel.appendLine("=== End Dry Run Preview ===");
        channel.show(true);

        logger.info("Autonomous dry run preview displayed", {
          remaining: result.remaining,
          completed: result.completed.length,
          failed: result.failed.length,
        });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error";
        logger.error("Failed to get autonomous status for dry run", {
          error: message,
        });
        vscode.window.showErrorMessage(`Failed to preview autonomous mode: ${message}`);
      }
    })
  );

  // ── Autonomous: Pause ──────────────────────────────────────────────
  disposables.push(
    vscode.commands.registerCommand("nightgauge.autonomousPause", async () => {
      // Mark this as a user-initiated pause so investigations distinguish
      // manual pauses from automatic safety trips / halt-on-failure events.
      try {
        const ipc = IpcClient.getInstance();
        const result = await ipc.autonomousPause(
          "user requested via UI (autonomousPause command)",
          "user"
        );

        statusBar.showAutonomousPaused();
        // Watchdog stays running so #3307 auto-resume can detect recovery
        // when the pause was driven by the rate-limit/outage breakers. For a
        // user-initiated pause the watchdog is harmless polling.

        // Toolbar swaps to Resume + Stop buttons.
        setAutonomousContextKeys("paused");

        const channel = getOutputChannel();
        channel.appendLine(`[${new Date().toISOString()}] Autonomous mode paused`);

        const selection = await vscode.window.showInformationMessage(
          "Autonomous mode paused. No new issues will be dispatched.",
          "Resume"
        );

        if (selection === "Resume") {
          await vscode.commands.executeCommand("nightgauge.autonomousResume");
        }

        logger.info("Autonomous mode paused", { status: result.status });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error";
        logger.error("Failed to pause autonomous mode", { error: message });
        vscode.window.showErrorMessage(`Failed to pause autonomous mode: ${message}`);
      }
    })
  );

  // ── Autonomous: Resume ─────────────────────────────────────────────
  disposables.push(
    vscode.commands.registerCommand("nightgauge.autonomousResume", async () => {
      try {
        const ipc = IpcClient.getInstance();

        // Resolve workspace repos — same WorkspaceManager-first logic as autonomousRun (#3766).
        const workspaceRepos: string[] = [];
        if (workspaceManager) {
          for (const r of workspaceManager.getAllRepositories()) {
            const gh = r.github;
            if (gh?.owner && gh?.repo) {
              workspaceRepos.push(`${gh.owner}/${gh.repo}`);
            }
          }
        }
        if (workspaceRepos.length === 0) {
          for (const folder of vscode.workspace.workspaceFolders ?? []) {
            try {
              const identity = await getRepoIdentity(folder.uri.fsPath);
              if (identity) {
                workspaceRepos.push(`${identity.owner}/${identity.repo}`);
              }
            } catch {
              // Skip folders without .nightgauge config
            }
          }
        }

        const result = await ipc.autonomousResume(intersectWithEnabledRepos(workspaceRepos));

        statusBar.showAutonomousRunning(result.running.length, result.remaining);
        startAutonomousStallWatchdog(logger);

        setAutonomousContextKeys("running");

        const channel = getOutputChannel();
        channel.appendLine(`[${new Date().toISOString()}] Autonomous mode resumed`);

        vscode.window.showInformationMessage("Autonomous mode resumed.");
        logger.info("Autonomous mode resumed", {
          running: result.running.length,
          remaining: result.remaining,
        });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error";
        logger.error("Failed to resume autonomous mode", { error: message });
        vscode.window.showErrorMessage(`Failed to resume autonomous mode: ${message}`);
      }
    })
  );

  // ── Autonomous: Stop ───────────────────────────────────────────────
  disposables.push(
    vscode.commands.registerCommand("nightgauge.autonomousStop", async () => {
      const confirm = await vscode.window.showWarningMessage(
        "Stop autonomous mode? Running pipelines will complete but no new issues will be dispatched.",
        { modal: true },
        "Stop"
      );

      if (confirm !== "Stop") return;

      try {
        const ipc = IpcClient.getInstance();
        const result = await ipc.autonomousStop();

        statusBar.showAutonomousComplete(result.completed.length);
        stopAutonomousStallWatchdog();

        setAutonomousContextKeys("stopped");

        // Hide the "Stop After Current Issue" batch button — autonomous stop already
        // prevents new dispatches, so it's redundant while the current slot drains.
        void vscode.commands.executeCommand("setContext", "nightgauge.stopAfterCurrentBatch", true);

        const channel = getOutputChannel();
        channel.appendLine(`[${new Date().toISOString()}] Autonomous mode stopped`);
        channel.appendLine(formatStatus(result));

        vscode.window.showInformationMessage(
          `Autonomous mode stopped. ${result.completed.length} issues completed, ${result.failed.length} failed.`
        );

        logger.info("Autonomous mode stopped", {
          completed: result.completed.length,
          failed: result.failed.length,
        });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error";
        logger.error("Failed to stop autonomous mode", { error: message });
        vscode.window.showErrorMessage(`Failed to stop autonomous mode: ${message}`);
      }
    })
  );

  // ── Autonomous: Status ─────────────────────────────────────────────
  disposables.push(
    vscode.commands.registerCommand("nightgauge.autonomousStatus", async () => {
      try {
        const ipc = IpcClient.getInstance();
        const result = await ipc.autonomousStatus();

        const channel = getOutputChannel();
        channel.clear();
        channel.appendLine(`=== Autonomous Mode Status (${new Date().toISOString()}) ===`);
        channel.appendLine("");
        channel.appendLine(formatStatus(result));
        channel.appendLine("");
        channel.appendLine("=== End Status ===");
        channel.show(true);

        // Update status bar to match current state.
        switch (result.status) {
          case "running":
            statusBar.showAutonomousRunning(result.running.length, result.remaining);
            startAutonomousStallWatchdog(logger);
            break;
          case "paused":
            statusBar.showAutonomousPaused();
            // Watchdog stays running; see #3307 auto-resume rationale.
            break;
          case "safety_tripped": {
            statusBar.showAutonomousPaused();
            const tripReason = result.safety?.tripReason ?? "safety rail tripped";
            const action = await vscode.window.showWarningMessage(
              `Autonomous mode stopped: ${tripReason}`,
              "Resume",
              "Dismiss"
            );
            if (action === "Resume") {
              await vscode.commands.executeCommand("nightgauge.autonomousResume");
            }
            break;
          }
          case "complete":
            statusBar.showAutonomousComplete(result.completed.length);
            stopAutonomousStallWatchdog();
            break;
          default:
            // stopped, budget_exhausted, etc.
            stopAutonomousStallWatchdog();
            break;
        }
        setAutonomousContextKeys(result.status);

        logger.info("Autonomous status displayed", { status: result.status });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error";
        logger.error("Failed to get autonomous status", { error: message });
        vscode.window.showErrorMessage(`Failed to get autonomous status: ${message}`);
      }
    })
  );

  // ── Autonomous: Select Repos ───────────────────────────────────────
  // Lets the user restrict autonomous scanning to a subset of workspace
  // repos (e.g. "only watch platform"). Writes to autonomous.enabled_repos
  // in the root-level .nightgauge/config.yaml. Takes effect on the
  // next Start/Resume. Primary motivation: cut GraphQL rate-limit pressure
  // when the user is focused on one repo.
  disposables.push(
    vscode.commands.registerCommand("nightgauge.autonomousSelectRepos", async () => {
      try {
        const selected = await pickAutonomousRepos(logger, enabledReposConfigService ?? null);
        if (selected === undefined) return; // user cancelled

        if (!enabledReposConfigService) {
          logger.warn(
            "autonomousSelectRepos: EnabledReposConfigService not wired — selection not persisted"
          );
        } else {
          await enabledReposConfigService.writeEnabledRepos(selected);
          logger.info("Wrote autonomous.enabled_repos to runtime tier", {
            count: selected.length,
          });
        }

        const channel = getOutputChannel();
        const rendered = selected.length === 0 ? "all workspace repos" : selected.join(", ");
        channel.appendLine(
          `[${new Date().toISOString()}] Autonomous enabled_repos set to: ${rendered}`
        );

        const ipc = IpcClient.getInstance();
        let isRunning = false;
        try {
          const status = await ipc.autonomousStatus();
          isRunning = status.status === "running" || status.status === "paused";
        } catch {
          /* ignore — status fetch best-effort */
        }

        const msg =
          selected.length === 0
            ? "Autonomous will scan all workspace repos."
            : `Autonomous scoped to: ${rendered}.`;

        if (isRunning) {
          const action = await vscode.window.showInformationMessage(
            `${msg} Restart autonomous mode now to apply?`,
            "Restart Autonomous",
            "Later"
          );
          if (action === "Restart Autonomous") {
            await vscode.commands.executeCommand("nightgauge.autonomousStop");
            await vscode.commands.executeCommand("nightgauge.autonomousRun");
          }
        } else {
          vscode.window.showInformationMessage(`${msg} Takes effect on next Start.`);
        }

        logger.info("Autonomous enabled_repos updated", { selected });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error";
        logger.error("Failed to update autonomous.enabled_repos", { error: message });
        vscode.window.showErrorMessage(`Failed to update enabled repos: ${message}`);
      }
    })
  );

  // #3446 — manual escape hatch for the global Anthropic-quota cooldown
  // (#3431). When the user knows the quota has recovered (or the recorded
  // deadline is a known false-positive) they can clear the cooldown so the
  // next runCycle dispatches immediately without waiting out the recorded
  // wall-clock time. Confirms via a warning modal because clearing an
  // accurate cooldown risks immediately re-burning $2-14 of front-loaded
  // cache_creation tokens on a still-exhausted bucket.
  disposables.push(
    vscode.commands.registerCommand("nightgauge.autonomousClearQuotaCooldown", async () => {
      const ipc = IpcClient.getInstance();
      let snapshot: AutonomousStatusResult | null = null;
      try {
        snapshot = await ipc.autonomousStatus();
      } catch (err) {
        // Continue — the IPC call below will surface the real error if any.
        logger.warn("Pre-check autonomousStatus failed before clearQuotaCooldown", {
          error: err instanceof Error ? err.message : String(err),
        });
      }
      const deadline = parseFutureCooldown(snapshot?.quotaCooldownUntil);
      const detail = deadline
        ? `Active cooldown ends ${deadline.toISOString()} (${formatCooldownRemaining(deadline)} remaining). Clearing it will let autonomous dispatch immediately.`
        : "No cooldown is currently active. Confirming will be a no-op.";
      const confirm = await vscode.window.showWarningMessage(
        "Clear global Anthropic-quota cooldown?",
        { modal: true, detail },
        "Clear Cooldown"
      );
      if (confirm !== "Clear Cooldown") return;
      try {
        const result = await ipc.autonomousClearQuotaCooldown();
        const channel = getOutputChannel();
        if (result.cleared) {
          channel.appendLine(
            `[${new Date().toISOString()}] Quota cooldown cleared by user (was until ${result.previousUntil ?? "?"}).`
          );
          vscode.window.showInformationMessage(
            `Quota cooldown cleared. Autonomous will dispatch on the next scan.`
          );
        } else {
          channel.appendLine(`[${new Date().toISOString()}] No active quota cooldown to clear.`);
          vscode.window.showInformationMessage("No active quota cooldown to clear.");
        }
        logger.info("Autonomous quota cooldown cleared", {
          cleared: result.cleared,
          previousUntil: result.previousUntil,
        });
        // Refresh badge against authoritative live state so the UI
        // transitions out of the cooldown view immediately.
        try {
          const refreshed = await ipc.autonomousStatus();
          reconcileAutonomousBadge(refreshed, logger);
        } catch {
          // Liveness probe will reconcile within 10s.
        }
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error";
        logger.error("Failed to clear quota cooldown", { error: message });
        vscode.window.showErrorMessage(`Failed to clear quota cooldown: ${message}`);
      }
    })
  );

  // #3020 — manual escape hatch for the per-issue lifetime failure cap. After
  // an issue hits MaxLifetimeFailuresPerIssue (default 2) the dispatch loop
  // refuses to retry it, tripping safety mode with a reason naming the issue.
  // Once the user has triaged (fixed the underlying problem, edited the
  // ACs, reset the branch, etc.) they invoke this command to clear the
  // counter. Empty input clears every issue at once.
  disposables.push(
    vscode.commands.registerCommand("nightgauge.autonomousClearIssueFailures", async () => {
      const ipc = IpcClient.getInstance();
      const input = await vscode.window.showInputBox({
        title: "Clear Lifetime Failure Cap",
        prompt: "Enter repo#number (e.g. acme/dashboard#283), or leave blank to clear all",
        placeHolder: "acme/dashboard#283",
        ignoreFocusOut: true,
      });
      if (input === undefined) return; // cancelled

      const key = input.trim();
      try {
        const result = await ipc.autonomousClearIssueFailures(key);
        const target = key === "" ? "all issues" : key;
        const msg = `Cleared lifetime failure cap for ${target} (${result.cleared} entr${result.cleared === 1 ? "y" : "ies"} removed)`;
        logger.info("Autonomous lifetime failure cap cleared", { key, cleared: result.cleared });
        vscode.window.showInformationMessage(msg);
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error";
        logger.error("Failed to clear lifetime failure cap", { key, error: message });
        vscode.window.showErrorMessage(`Failed to clear failure cap: ${message}`);
      }
    })
  );

  return disposables;
}

/**
 * Show a multi-select QuickPick listing workspace repos (plus any repos
 * already present in `autonomous.enabled_repos` that aren't in the current
 * workspace), with current selections pre-checked.
 *
 * Returns:
 *   - undefined when the user cancels (nothing should change).
 *   - [] when the user clears the selection (meaning "scan all workspace
 *     repos" — enabled_repos key is removed).
 *   - a non-empty array of short repo names ("acme-platform")
 *     for the selected subset.
 */
async function pickAutonomousRepos(
  logger: Logger,
  enabledReposConfigService: EnabledReposConfigService | null
): Promise<string[] | undefined> {
  const workspaceRepos: Array<{ owner: string; repo: string; fullName: string }> = [];
  for (const folder of vscode.workspace.workspaceFolders ?? []) {
    try {
      const identity = await getRepoIdentity(folder.uri.fsPath);
      if (identity) {
        workspaceRepos.push({
          owner: identity.owner,
          repo: identity.repo,
          fullName: `${identity.owner}/${identity.repo}`,
        });
      }
    } catch (err) {
      logger.warn("Select Repos: skipping folder without .nightgauge", {
        folder: folder.uri.fsPath,
        error: String(err),
      });
    }
  }

  const existing = enabledReposConfigService?.readEnabledRepos() ?? [];

  // Build the candidate set: union of workspace repos and anything already
  // configured. Short names ("platform") are kept as-is; full names are kept
  // as-is so the QuickPick accurately reflects what's on disk.
  const candidates = new Map<string, { label: string; detail?: string; short: string }>();
  for (const r of workspaceRepos) {
    candidates.set(r.fullName.toLowerCase(), {
      label: r.repo,
      detail: r.fullName,
      short: r.repo,
    });
  }
  for (const e of existing) {
    const key = e.toLowerCase();
    if (!candidates.has(key)) {
      candidates.set(key, { label: e, detail: "(not open in workspace)", short: e });
    }
  }

  if (candidates.size === 0) {
    vscode.window.showWarningMessage(
      "No workspace repos detected. Open at least one folder with .nightgauge/config.yaml."
    );
    return undefined;
  }

  const existingLower = new Set(existing.map((e) => e.toLowerCase()));
  const existingShortLower = new Set(
    existing.map((e) => (e.includes("/") ? e.split("/")[1] : e).toLowerCase())
  );
  const hasFilter = existing.length > 0;

  const items: vscode.QuickPickItem[] = [];
  for (const [key, entry] of candidates) {
    const shortLower = entry.short.toLowerCase();
    // Pre-check items when a filter is active AND this repo matches, or when
    // no filter is active (all repos scanned = all checked).
    const picked = hasFilter ? existingLower.has(key) || existingShortLower.has(shortLower) : true;
    items.push({
      label: entry.label,
      description: picked ? "$(check)" : undefined,
      detail: entry.detail,
      picked,
    });
  }

  const picked = await vscode.window.showQuickPick(items, {
    canPickMany: true,
    title: "Autonomous: Select Repos to Scan",
    placeHolder:
      "Uncheck repos to exclude from autonomous scanning. Unchecking all = scan all (no filter).",
  });
  if (picked === undefined) return undefined;

  // If user picked all workspace repos, clear the filter (enabled_repos empty
  // means "no scoping"). Match against workspace repos by short name so an
  // "include everything" intent reliably removes the filter.
  const pickedShort = picked.map((i) => i.label);
  const workspaceShort = new Set(workspaceRepos.map((r) => r.repo));
  const allWorkspaceSelected =
    pickedShort.length === workspaceShort.size && pickedShort.every((s) => workspaceShort.has(s));
  if (allWorkspaceSelected) return [];
  return pickedShort;
}

/**
 * Dispose the shared autonomous output channel.
 * Called during extension deactivation.
 */
export function disposeAutonomousOutputChannel(): void {
  if (autonomousOutputChannel) {
    autonomousOutputChannel.dispose();
    autonomousOutputChannel = null;
  }
}

/**
 * Reset all watchdog and liveness probe module-level state.
 * Exported for use in tests only — not part of the public extension API.
 */
export function resetWatchdogStateForTest(): void {
  if (stallWatchdogTimer) {
    clearTimeout(stallWatchdogTimer);
    stallWatchdogTimer = null;
  }
  if (livenessProbeTimer) {
    clearTimeout(livenessProbeTimer);
    livenessProbeTimer = null;
  }
  stallWatchdogConsecutiveFailures = 0;
  stallWatchdogInFlight = false;
  livenessConsecutiveFailures = 0;
  backendDisconnected = false;
  livenessStatusDisposable?.dispose();
  livenessStatusDisposable = null;
  alertedStalls.clear();
  _enabledReposConfigService = null;
}

/**
 * Test-only setter for the module-scope enabled-repos service. Lets unit
 * tests exercise the watchdog allowlist filter without standing up the full
 * `registerAutonomousCommands` wiring. Issue #3427.
 */
export function setEnabledReposConfigServiceForTest(
  service: EnabledReposConfigService | null
): void {
  _enabledReposConfigService = service;
}
