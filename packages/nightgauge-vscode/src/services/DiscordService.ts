/**
 * DiscordService - Live-updating pipeline status embeds for Discord
 *
 * Posts one embed message per pipeline run and edits it in-place as stages
 * progress, so the channel shows current status without flooding with
 * individual stage notifications.
 *
 * Embeds include:
 * - Stage progress with per-stage cost
 * - Error diagnostics and actionable insights on failure
 * - Gate results (build/test/lint) after validation
 * - Model escalation and retry information
 * - Cancellation context (which stage was interrupted)
 *
 * Requires a Discord webhook URL stored in an environment variable.
 *
 * Configuration (.nightgauge/config.yaml):
 *   notifications:
 *     discord:
 *       enabled: true
 *       webhook_env: DISCORD_WEBHOOK_URL
 *
 * @see docs/CONFIGURATION.md#discord-notifications
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import { PipelineStateService } from "./PipelineStateService";
import { ConfigBridge } from "./ConfigBridge";
import { Logger } from "../utils/logger";
import { SecretStorageService, SECRET_KEYS } from "./SecretStorageService";
import type { Notifier, PipelineEventContext } from "./notifications/types";
import { NotifierStatusTracker } from "./notifications/NotifierStatusTracker";
import {
  DEBOUNCE_MS,
  DebouncedPatcher,
  FETCH_RETRY_DELAYS,
  FINAL_PATCH_MAX_RETRIES,
  FINAL_PATCH_RETRY_DELAYS,
  formatBudgetFieldValue,
  formatCost,
  formatDuration,
  redactSecrets,
  retryWithBackoff,
  shortModel,
  truncate,
} from "./notifications/transport";

// Re-export so existing imports (tests/services/DiscordService.test.ts) still resolve.
export { redactSecrets };

// ─── Discord embed colors ─────────────────────────────────────────────────────

const COLOR_RUNNING = 0x5865f2; // Blurple  — in progress
const COLOR_COMPLETE = 0x57f287; // Green    — productive / verify-and-close / already-resolved
const COLOR_WARNING = 0xfee75c; // Yellow   — budget-ceiling
const COLOR_NEUTRAL = 0x95a5a6; // Grey     — cancelled
const COLOR_FAILED = 0xed4245; // Red      — stage error / unknown terminal state

// ─── Discord embed limits ─────────────────────────────────────────────────────

const MAX_DESCRIPTION_LENGTH = 4096;
const MAX_FIELD_VALUE_LENGTH = 1024;
const MAX_FIELDS = 25;

// ─── Pipeline stages in execution order ──────────────────────────────────────

const PIPELINE_STAGES: PipelineStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

const STAGE_LABEL: Record<string, string> = {
  "issue-pickup": "Issue Pickup",
  "feature-planning": "Feature Planning",
  "feature-dev": "Feature Dev",
  "feature-validate": "Feature Validate",
  "pr-create": "PR Create",
  "pr-merge": "PR Merge",
};

// ─── Interfaces ───────────────────────────────────────────────────────────────

interface DiscordNotificationsConfig {
  enabled?: boolean;
  /** Name of the environment variable that holds the Discord webhook URL */
  webhook_env?: string;
}

/** Fields we read from PipelineState for building Discord embeds */
interface PipelineStateSnapshot {
  issue_number: number;
  title: string;
  branch: string;
  base_branch?: string;
  stages?: Partial<
    Record<
      string,
      {
        status: string;
        duration_ms?: number;
        startTime?: number;
        error?: string;
        current_phase?: string;
        total_phases?: number;
      }
    >
  >;
  tokens?: {
    estimated_cost_usd?: number;
    total_cache_read?: number;
    total_cache_creation?: number;
    total_input?: number;
    total_output?: number;
    per_stage?: Record<string, { cost_usd?: number; model?: string }>;
  };
  outcome_type?: string;
  retry_count?: number;
  escalation_history?: Array<{
    stage: string;
    fromModel: string;
    toModel: string;
    reason: string;
  }>;
  ralph_iterations?: Record<string, number>;
  gate_results?: Array<{
    gate_name: string;
    result: string;
    error_summary?: string;
  }>;
  pr_url?: string;
  pipeline_meta?: {
    complexity?: string;
    file_count?: number;
    epic_number?: number;
    epic_total?: number;
    epic_position?: number;
    budget_estimate_usd?: number;
    budget_ceiling_usd?: number;
    route?: string;
    skip_stages?: string[];
    model?: string;
    pr_number?: number;
    health_score?: number;
    is_supercharge?: boolean;
    supercharge_model?: string;
    /** Active performance mode — Issue #3009. */
    performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
    /** Fable → Opus graceful downgrades applied this run after a usage/quota
     *  limit (Issue #26). Fable has a separate Max-plan usage bucket; a
     *  Fable-only exhaustion retries the stage on Opus rather than pausing the
     *  whole pipeline. Surfaced so operators see the downgrade in real time. */
    quota_fallbacks?: Array<{ stage: string; from: string; to: string }>;
  };
}

interface ActiveRun {
  issueNumber: number;
  issueTitle: string;
  branch: string;
  repoName: string;
  /** Full GitHub slug (e.g. "nightgauge/nightgauge") for issue links */
  repoSlug?: string;
  webhookId: string;
  webhookToken: string;
  messageId: string;
  startTime: number;
  costUsd: number;
  prUrl?: string;
  /** Per-stage start timestamps (epoch ms) for computing running stage elapsed time */
  stageStartTimes: Map<string, number>;
  isFinal: boolean;
  /** Cached when outcome_type is first seen — avoids re-reading state.json in
   *  batch mode where the file may be replaced by the next issue's pipeline
   *  before the debounced patchEmbed fires. */
  finalSnapshot?: PipelineStateSnapshot;
  /** Number of retry attempts for the final PATCH */
  finalPatchRetries: number;
  /** Per-slot state service for concurrent worktree pipelines.
   *  When set, patchEmbed reads state from the worktree path instead of the
   *  main singleton (which points to the main workspace root). */
  stateService?: PipelineStateService;
}

interface DiscordField {
  name: string;
  value: string;
  inline?: boolean;
}

interface DiscordEmbed {
  title: string;
  url?: string;
  description: string;
  color: number;
  fields: DiscordField[];
  footer: { text: string };
  timestamp: string;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function parseWebhookUrl(url: string): { id: string; token: string } | null {
  const match = url.match(/discord\.com\/api\/webhooks\/(\d+)\/([\w-]+)/);
  return match ? { id: match[1], token: match[2] } : null;
}

function statusIcon(status: string): string {
  switch (status) {
    case "running":
      return "🔄";
    case "complete":
      return "✅";
    case "failed":
      return "❌";
    case "skipped":
      return "⏭️";
    case "deferred":
      return "⏸️";
    default:
      return "⏳";
  }
}

// ─── Stream-JSON error formatting ───────────────────────────────────────────

/** Max length of an extracted human-readable error chunk (before embed-field
 *  truncation).  Discord field values cap at 1024 chars, but we clamp to 1500
 *  here so multi-line extractions don't blow out memory before the outer
 *  truncate() runs. */
const MAX_ERROR_EXTRACT_LENGTH = 1500;

/** Truncate a single JSON line for fallback display when extraction fails. */
const MAX_JSON_FALLBACK_LENGTH = 500;

/**
 * Extract human-readable content from a stream-JSON envelope that came out of
 * the Claude Agent SDK / CLI (`--output-format stream-json`).
 *
 * Returns `null` when the envelope carries no useful text (caller decides
 * whether to fall back to raw JSON).
 */
function extractTextFromEnvelope(envelope: unknown): string | null {
  if (envelope === null || typeof envelope !== "object") return null;
  const env = envelope as Record<string, unknown>;

  // user envelope → look for tool_result entries in message.content[]
  if (env.type === "user") {
    const message = env.message as { content?: unknown } | undefined;
    const content = message?.content;
    if (Array.isArray(content)) {
      // Take the LAST tool_result so a later error overrides an earlier one
      const toolResults = content.filter(
        (c): c is Record<string, unknown> =>
          c !== null &&
          typeof c === "object" &&
          (c as Record<string, unknown>).type === "tool_result"
      );
      const last = toolResults[toolResults.length - 1];
      if (last) {
        const inner = last.content;
        if (typeof inner === "string") return inner.trim() || null;
        if (Array.isArray(inner)) {
          const texts = inner
            .filter(
              (b): b is Record<string, unknown> =>
                b !== null &&
                typeof b === "object" &&
                (b as Record<string, unknown>).type === "text"
            )
            .map((b) => (typeof b.text === "string" ? b.text : ""))
            .filter((t) => t.length > 0);
          if (texts.length > 0) return texts.join("\n").trim() || null;
        }
      }
    }
    return null;
  }

  // assistant envelope → concat text blocks; summarise tool_use blocks
  if (env.type === "assistant") {
    const message = env.message as { content?: unknown } | undefined;
    const content = message?.content;
    if (Array.isArray(content)) {
      const parts: string[] = [];
      for (const block of content) {
        if (block === null || typeof block !== "object") continue;
        const b = block as Record<string, unknown>;
        if (b.type === "text" && typeof b.text === "string") {
          parts.push(b.text);
        } else if (b.type === "tool_use" && typeof b.name === "string") {
          parts.push(`Used tool: ${b.name}`);
        }
      }
      if (parts.length > 0) return parts.join("\n").trim() || null;
    }
    return null;
  }

  // system envelope with task_notification → summarise stop reason
  if (env.type === "system" && env.subtype === "task_notification") {
    const status = typeof env.status === "string" ? env.status : undefined;
    const summary = typeof env.summary === "string" ? env.summary : undefined;
    const exitCode =
      typeof env.exit_code === "number"
        ? env.exit_code
        : typeof env.exitCode === "number"
          ? env.exitCode
          : undefined;

    const bits: string[] = [];
    if (status) bits.push(status.charAt(0).toUpperCase() + status.slice(1));
    else bits.push("Task notification");
    if (summary) bits.push(summary);
    if (exitCode !== undefined) {
      if (exitCode === 137)
        bits.push(
          "exit code 137 (SIGKILL — process killed, likely stall timeout or user interrupt)"
        );
      else if (exitCode === 143) bits.push("exit code 143 (SIGTERM)");
      else bits.push(`exit code ${exitCode}`);
    }
    return bits.join(": ");
  }

  return null;
}

/**
 * Parse a raw error string that may contain stream-JSON envelopes (JSONL) and
 * return a human-readable summary safe to embed in a Discord field.
 *
 * Behavior:
 *   - Plain text that doesn't look like JSON passes through unchanged (trimmed).
 *   - JSONL — each non-empty line is JSON-parsed; lines that fail to parse are
 *     ignored.  Extracted text from every envelope is concatenated (deduped)
 *     so multi-envelope payloads surface the final tool_result / stop reason.
 *   - If nothing could be extracted, the first JSON line is returned truncated
 *     to MAX_JSON_FALLBACK_LENGTH so the user still sees *something* rather
 *     than an empty field.
 *   - The final string is clamped to MAX_ERROR_EXTRACT_LENGTH.
 *
 * Exported for unit testing.
 */
export function formatErrorForDiscord(raw: string | undefined | null): string {
  if (!raw) return "";
  const trimmed = raw.trim();
  if (!trimmed) return "";

  // Plain text fast path — doesn't look like JSON
  if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) {
    return redactSecrets(truncate(trimmed, MAX_ERROR_EXTRACT_LENGTH));
  }

  // Try JSONL parse — one envelope per line
  const lines = trimmed.split(/\r?\n/).filter((l) => l.trim().length > 0);
  const extracted: string[] = [];
  let anyParsed = false;
  let firstJsonLine: string | undefined;

  for (const line of lines) {
    const s = line.trim();
    if (!s.startsWith("{") && !s.startsWith("[")) {
      // Non-JSON line in a mostly-JSON blob — keep as-is
      extracted.push(s);
      continue;
    }
    try {
      const envelope = JSON.parse(s);
      anyParsed = true;
      if (firstJsonLine === undefined) firstJsonLine = s;
      const text = extractTextFromEnvelope(envelope);
      if (text) extracted.push(text);
    } catch {
      // Ignore — may be a truncated line or non-JSON noise
    }
  }

  if (extracted.length > 0) {
    // Dedupe consecutive identical chunks (assistant/tool-result often repeat)
    const deduped: string[] = [];
    for (const chunk of extracted) {
      if (deduped[deduped.length - 1] !== chunk) deduped.push(chunk);
    }
    return redactSecrets(truncate(deduped.join("\n").trim(), MAX_ERROR_EXTRACT_LENGTH));
  }

  // Nothing extracted — fall back to a truncated raw form so the user still
  // sees the shape of the failure rather than an empty field.
  if (anyParsed && firstJsonLine) {
    return redactSecrets(truncate(firstJsonLine, MAX_JSON_FALLBACK_LENGTH));
  }
  return redactSecrets(truncate(trimmed, MAX_JSON_FALLBACK_LENGTH));
}

/**
 * Maps a PipelineOutcomeType to the Discord embed color and status label.
 * Exported for unit testing.
 *
 * Outcome taxonomy:
 *   productive          — work done, PR merged          → Complete ✓  (green)
 *   verify-and-close    — no changes needed, closed     → Complete ✓  (green)
 *   already-resolved    — issue was already done        → Already Resolved (green)
 *   budget-ceiling      — stopped by budget limit       → Budget Ceiling  (yellow)
 *   skill-no-op         — skill exited 0 but did nothing → Skill No-op    (yellow)
 *   cancelled           — manually stopped              → Cancelled       (grey)
 *   undefined           — still running                 → Running…        (blurple)
 *   <unknown>           — fallback for future types     → Failed ✗        (red)
 */
export function outcomeDisplay(outcomeType: string | undefined): {
  color: number;
  label: string;
} {
  switch (outcomeType) {
    case "productive":
    case "verify-and-close":
      return { color: COLOR_COMPLETE, label: "Complete ✓" };
    case "already-resolved":
      return { color: COLOR_COMPLETE, label: "Already Resolved" };
    case "budget-ceiling":
      return { color: COLOR_WARNING, label: "Budget Ceiling" };
    case "skill-no-op":
      // Yellow — same urgency as budget-ceiling. The skill said success but
      // the gate detected no state change. See #3267.
      return { color: COLOR_WARNING, label: "Skill No-op" };
    case "cancelled":
      return { color: COLOR_NEUTRAL, label: "Cancelled" };
    case undefined:
      return { color: COLOR_RUNNING, label: "Running…" };
    default:
      return { color: COLOR_FAILED, label: "Failed ✗" };
  }
}

/**
 * Determine an actionable recommendation based on the pipeline outcome.
 * Exported for unit testing.
 */
export function determineAction(state: PipelineStateSnapshot): string | null {
  switch (state.outcome_type) {
    case "productive":
    case "verify-and-close":
    case "already-resolved":
    case undefined: // still running
      return null;
    case "cancelled":
      return "Re-run when ready — issue and branch preserved";
    case "budget-ceiling":
      return "Increase budget limit or re-run with higher ceiling";
    case "skill-no-op":
      // The skill exited 0 but the gate detected nothing changed (#3267).
      // Operator should investigate the failed gate's reason in the run
      // record before re-running.
      return "Skill produced no state change — check stage gate reason and retry";
    default: {
      // Failed — analyze the error to give specific guidance
      const failedStage = Object.entries(state.stages ?? {}).find(
        ([, s]) => s?.status === "failed"
      );
      if (!failedStage) return "Check pipeline logs for details";

      const error = (failedStage[1]?.error ?? "").toLowerCase();
      const retries = state.retry_count ?? 0;

      if (error.includes("build") || error.includes("compile"))
        return "Manual fix needed — build errors require code changes";
      if (error.includes("test"))
        return retries > 0
          ? "Tests failed after retries — manual investigation needed"
          : "Re-run may resolve — could be a transient test failure";
      if (error.includes("rate limit") || error.includes("timeout"))
        return "Transient error — safe to re-run";
      if (retries >= 3) return "Max retries exhausted — manual intervention required";
      return "Review error details — re-run if transient";
    }
  }
}

/**
 * Derive the display label, icon, and model ceiling for the active performance
 * mode. Falls back to `is_supercharge` for pipeline runs predating Issue #3009.
 *
 * `ceiling` is the top model the mode's routing envelope can reach (Issue #19).
 * It lets notifiers show "up to Fable / Opus / Sonnet" so the message conveys
 * what a mode *means*, not just its name — and, crucially, distinguishes
 * Frontier (Fable-capable) from Elevated. Before this, Frontier had no case and
 * fell through to the Elevated default, so every Fable run mislabelled as
 * "Elevated".
 */
export function modeDisplay(meta: PipelineStateSnapshot["pipeline_meta"]): {
  label: string;
  icon: string;
  modelSuffix: string;
  ceiling: string;
} {
  const mode = meta?.performance_mode;
  if (mode === "maximum" || (!mode && meta?.is_supercharge)) {
    const modelSuffix = meta?.supercharge_model ? ` (${shortModel(meta.supercharge_model)})` : "";
    // Maximum pins Opus (not an envelope) — the modelSuffix already names it,
    // so notifiers suppress the "up to …" ceiling hint for this mode.
    return { label: "Maximum", icon: "⚡", modelSuffix, ceiling: "Opus" };
  }
  if (mode === "frontier")
    return { label: "Frontier", icon: "🚀", modelSuffix: "", ceiling: "Fable" };
  if (mode === "efficiency")
    return { label: "Efficiency", icon: "💡", modelSuffix: "", ceiling: "Sonnet" };
  // elevated is the default; also covers pre-#3009 runs without performance_mode
  return { label: "Elevated", icon: "", modelSuffix: "", ceiling: "Opus" };
}

// ─── DiscordService ───────────────────────────────────────────────────────────

export class DiscordService implements Notifier, vscode.Disposable {
  /** One entry per active pipeline run, keyed by issue number */
  private readonly runs = new Map<number, ActiveRun>();

  /** Debounce / final-PATCH retry timers (one per issue). Shared with
   *  Mattermost / future notifiers via `notifications/transport.ts`. */
  private readonly patcher = new DebouncedPatcher();

  /** Per-slot event subscriptions for concurrent worktree pipelines (Issue #1750) */
  private readonly slotDisposables = new Map<number, vscode.Disposable[]>();

  /** Repo slugs queued before embed creation (set via subscribeToSlot) */
  private readonly pendingRepoSlugs = new Map<number, string>();

  private disposables: vscode.Disposable[] = [];

  constructor(
    private readonly pipelineStateService: PipelineStateService,
    private readonly configBridge: ConfigBridge,
    private readonly logger: Logger
  ) {}

  async initialize(): Promise<void> {
    this.disposables.push(
      // stage:start — create message on issue-pickup, schedule update for others
      // Skip when concurrent slots are active — slot subscriptions handle their
      // own events with proper issue-number filtering.  Without this guard, the
      // shared IPC singleton causes every event to fire here AND in each slot
      // subscription, flooding Discord with duplicate webhook POSTs.
      this.pipelineStateService.onStageStart(({ stage, issueNumber }) => {
        if (this.slotDisposables.size > 0) return;
        void this.handleStageStart(stage as PipelineStage, issueNumber);
      }),

      // stage:error — schedule an immediate-ish update to show the failure
      this.pipelineStateService.onStageError(({ issueNumber }) => {
        if (this.slotDisposables.size > 0) return;
        this.scheduleUpdate(issueNumber);
      }),

      // state:changed — sync token cost + completion status, schedule update
      this.pipelineStateService.onStateChanged((state) => {
        if (this.slotDisposables.size > 0) return;
        if (state) void this.handleStateChanged(state as unknown as PipelineStateSnapshot);
      })
    );

    this.logger.info("DiscordService initialized");
  }

  // ─── Notifier interface delegations (Issue #3372) ───────────────────────────
  //
  // These exist on the Notifier contract so a future dispatcher can take over
  // event sourcing without re-shaping the interface. They are not invoked by
  // any current call site — DiscordService still drives its own embed lifecycle
  // through the internal PipelineStateService subscriptions installed in
  // initialize().

  onPipelineStart(ctx: PipelineEventContext): void {
    void this.handleStageStart(ctx.stage as PipelineStage, ctx.issueNumber);
  }

  onPipelineUpdate(ctx: PipelineEventContext): void {
    if (ctx.state) {
      void this.handleStateChanged(ctx.state as unknown as PipelineStateSnapshot);
    } else {
      this.scheduleUpdate(ctx.issueNumber);
    }
  }

  // ─── Concurrent worktree slot subscription (Issue #1750) ────────────────────

  /**
   * Subscribe to a worktree slot's PipelineStateService events.
   *
   * In concurrent worktree mode, each slot has its own PipelineStateService
   * that fires independent events. Call this from the onSlotStarted callback
   * so Discord embeds are created and updated for each concurrent pipeline.
   */
  subscribeToSlot(
    issueNumber: number,
    slotStateService: PipelineStateService,
    repoSlug?: string
  ): void {
    // Store repo slug for GitHub issue links — set on embed creation
    if (repoSlug) {
      this.pendingRepoSlugs.set(issueNumber, repoSlug);
    }
    // Clean up any existing subscription for this issue
    this.unsubscribeFromSlot(issueNumber);

    // IMPORTANT: IpcClient is a singleton — every PipelineStateService instance
    // receives ALL IPC events, not just events for its assigned issue.  Filter
    // by issueNumber here so each slot only processes its own events and we
    // don't flood Discord with duplicate/cross-contaminated webhook POSTs.
    const subs: vscode.Disposable[] = [
      slotStateService.onStageStart(({ stage, issueNumber: num }) => {
        if (num !== issueNumber) return; // filter: only this slot's events
        void this.handleStageStart(stage as PipelineStage, num, slotStateService);
      }),
      slotStateService.onStageError(({ issueNumber: num }) => {
        if (num !== issueNumber) return;
        this.scheduleUpdate(num);
      }),
      slotStateService.onStateChanged((state) => {
        if (!state) return;
        const snap = state as unknown as PipelineStateSnapshot;
        if (snap.issue_number !== issueNumber) return;
        void this.handleStateChanged(snap);
      }),
    ];

    this.slotDisposables.set(issueNumber, subs);
    this.logger.info("DiscordService: subscribed to worktree slot", {
      issueNumber,
    });
  }

  /**
   * Unsubscribe from a worktree slot's events.
   * Called when a slot completes or fails.
   */
  unsubscribeFromSlot(issueNumber: number): void {
    const subs = this.slotDisposables.get(issueNumber);
    if (subs) {
      for (const s of subs) s.dispose();
      this.slotDisposables.delete(issueNumber);
    }
  }

  // ─── Fetch helpers ──────────────────────────────────────────────────────────

  private sanitizeWebhookUrl(webhookUrl: string): string {
    const match = webhookUrl.match(/discord\.com\/api\/webhooks\/(\d+)/);
    return match ? `discord.com/api/webhooks/${match[1]}` : "discord.com/api/webhooks";
  }

  // ─── Event handlers ─────────────────────────────────────────────────────────

  private async handleStageStart(
    stage: PipelineStage,
    issueNumber: number,
    stateService?: PipelineStateService
  ): Promise<void> {
    const effectiveStateService = stateService ?? this.pipelineStateService;

    if (stage !== "issue-pickup") {
      // Non-first stage: update the existing embed
      this.scheduleUpdate(issueNumber);
      return;
    }

    // Flush any completed runs from previous queued issues so their embeds
    // transition from "Running…" to their final state before we create
    // the new embed for this issue.
    this.flushStaleRuns(issueNumber);

    const config = this.getDiscordConfig();
    if (!config?.enabled) return;

    const webhookUrl = await this.resolveWebhookUrl(config);
    if (!webhookUrl) return;

    const parsed = parseWebhookUrl(webhookUrl);
    if (!parsed) {
      this.logger.warn("DiscordService: invalid Discord webhook URL format");
      return;
    }

    const state = await effectiveStateService.getState();
    if (!state || state.issue_number !== issueNumber) return;

    // Derive repo name from statePath (context-aware) rather than workspaceRoot
    // (static initial value). statePath is updated by RepositoryContextLoader
    // when the active repo changes, so it correctly reflects the platform repo
    // during batch runs on acme-platform.
    // e.g. ".../acme-platform/.nightgauge/pipeline/state.json"
    //   → split by "/.nightgauge/" → ".../acme-platform"
    //   → pop last segment → "acme-platform"
    // Worktree paths: ".../repo/.worktrees/issue-63/.nightgauge/..."
    //   → split by "/.nightgauge/" → ".../repo/.worktrees/issue-63"
    //   → strip ".worktrees/..." → ".../repo"
    //   → pop last segment → "repo"
    const statePath = effectiveStateService.getStatePath();
    let repoRoot = statePath.split("/.nightgauge/")[0];
    // Strip worktree suffix so we get the actual repo name, not "issue-NNN"
    repoRoot = repoRoot.replace(/\/\.worktrees\/[^/]+$/, "");
    const repoName = repoRoot.split("/").pop() ?? repoRoot;

    // Consume pending repo slug (set via subscribeToSlot before embed creation)
    const repoSlug = this.pendingRepoSlugs.get(issueNumber);
    this.pendingRepoSlugs.delete(issueNumber);

    const run: ActiveRun = {
      issueNumber,
      issueTitle: (state as unknown as PipelineStateSnapshot).title ?? `Issue #${issueNumber}`,
      branch: (state as unknown as PipelineStateSnapshot).branch ?? "",
      repoName,
      repoSlug,
      webhookId: parsed.id,
      webhookToken: parsed.token,
      messageId: "", // filled in after POST
      startTime: Date.now(),
      costUsd: 0,
      stageStartTimes: new Map(),
      isFinal: false,
      finalPatchRetries: 0,
      stateService, // per-slot service for worktree pipelines
    };

    const embed = this.buildEmbed(run, state as unknown as PipelineStateSnapshot);

    const baseUrl = `discord.com/api/webhooks/${parsed.id}`;
    try {
      const res = await retryWithBackoff(
        () =>
          fetch(`https://discord.com/api/webhooks/${parsed.id}/${parsed.token}?wait=true`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ embeds: [embed] }),
          }),
        {
          delays: FETCH_RETRY_DELAYS,
          logger: this.logger,
          label: "DiscordService",
          sanitizedUrl: baseUrl,
        }
      );

      const data = (await res.json()) as { id: string };
      run.messageId = data.id;
      this.runs.set(issueNumber, run);

      NotifierStatusTracker.getInstance()?.recordSuccess("discord");
      this.logger.info("DiscordService: pipeline embed created", {
        issueNumber,
        messageId: data.id,
      });
    } catch (err) {
      NotifierStatusTracker.getInstance()?.recordError(
        "discord",
        redactSecrets(err instanceof Error ? err.message : String(err))
      );
      this.logger.error("DiscordService: failed to create embed after retries", {
        issueNumber,
        baseUrl,
        err,
      });
    }
  }

  private async handleStateChanged(state: PipelineStateSnapshot): Promise<void> {
    const run = this.runs.get(state.issue_number);
    if (!run) return;

    // Sync metadata from latest state — title/branch arrive after issue-pickup
    // resolves the real GitHub issue title, replacing the placeholder.
    if (state.title) run.issueTitle = state.title;
    if (state.branch) run.branch = state.branch;

    // Sync token cost
    const cost = state.tokens?.estimated_cost_usd;
    if (typeof cost === "number") run.costUsd = cost;

    // Sync PR URL when it becomes available
    if (state.pr_url) run.prUrl = state.pr_url;

    // Track stage start times for computing running stage elapsed time.
    // Record the first time we see a stage in "running" status.
    if (state.stages) {
      for (const [name, info] of Object.entries(state.stages)) {
        if (info?.status === "running" && !run.stageStartTimes.has(name)) {
          run.stageStartTimes.set(name, info.startTime ?? Date.now());
        }
      }
    }

    // Mark as final when pipeline has an outcome; cache the snapshot so
    // patchEmbed doesn't have to re-read from disk (in batch mode, state.json
    // is replaced by the next issue's pipeline before the 1.5 s debounce fires)
    if (state.outcome_type) {
      // Always update the cached snapshot with the latest data
      run.finalSnapshot = state;

      if (!run.isFinal) {
        // First time seeing outcome — dispatch final PATCH immediately.
        // The 1.5 s debounce is fine for intermediate updates but the final
        // PATCH is the only one that carries the outcome status.  If anything
        // (queue advance, timer cleanup, rate-limit) prevents a debounced PATCH
        // from firing, the embed is stuck at "Running…" forever.
        run.isFinal = true;
        this.patcher.cancel(state.issue_number);
        await this.patchEmbed(state.issue_number);
      }
      // Subsequent state changes with outcome_type: don't re-dispatch.
      // The retry mechanism (scheduleRetry) handles failures from the
      // first dispatch. This prevents flooding Discord with concurrent
      // PATCH calls when multiple onStateChanged events fire in rapid
      // succession during pipeline-finish.
      return;
    }

    this.scheduleUpdate(state.issue_number);
  }

  // ─── Debounced update ────────────────────────────────────────────────────────

  private scheduleUpdate(issueNumber: number): void {
    this.patcher.schedule(issueNumber, () => this.patchEmbed(issueNumber), DEBOUNCE_MS);
  }

  /**
   * Schedule a retry for a failed final PATCH with exponential backoff.
   *
   * After FINAL_PATCH_MAX_RETRIES exhausted, gives up and deletes the run.
   * Reuses the shared patcher timer slot so dispose() cleans up retry timers
   * automatically.
   */
  private scheduleRetry(issueNumber: number): void {
    const run = this.runs.get(issueNumber);
    if (!run) return;

    if (run.finalPatchRetries >= FINAL_PATCH_MAX_RETRIES) {
      this.logger.error(
        "DiscordService: final patch failed after all retries — embed may be stuck",
        { issueNumber, retries: run.finalPatchRetries }
      );
      this.runs.delete(issueNumber);
      return;
    }

    const delay = FINAL_PATCH_RETRY_DELAYS[run.finalPatchRetries] ?? 6000;
    run.finalPatchRetries += 1;

    this.logger.info("DiscordService: scheduling final patch retry", {
      issueNumber,
      attempt: run.finalPatchRetries,
      delayMs: delay,
    });

    this.patcher.schedule(issueNumber, () => this.patchEmbed(issueNumber), delay);
  }

  /**
   * Immediately patch any completed runs that haven't been flushed yet.
   *
   * Called before creating a new embed so stale "Running…" embeds from
   * the previous queued pipeline run are updated to their final state.
   */
  private flushStaleRuns(excludeIssue?: number): void {
    for (const [issueNumber, run] of this.runs) {
      if (issueNumber === excludeIssue) continue;
      if (run.isFinal && run.finalSnapshot) {
        this.patcher.cancel(issueNumber);
        void this.patchEmbed(issueNumber);
      }
    }
  }

  private async patchEmbed(issueNumber: number): Promise<void> {
    const run = this.runs.get(issueNumber);
    if (!run?.messageId) return;

    // Use the cached final snapshot when available — in batch mode, state.json
    // may already have been replaced by the next issue's pipeline by the time
    // this 1.5 s debounce fires, so re-reading from disk would return a
    // different issue's state (no outcome_type) and show "Running…" forever.
    let snapshot: PipelineStateSnapshot;
    if (run.isFinal && run.finalSnapshot) {
      snapshot = run.finalSnapshot;
    } else {
      // Use the per-slot state service when available (concurrent worktree mode),
      // otherwise fall back to the main singleton (single-issue / batch mode).
      const effectiveService = run.stateService ?? this.pipelineStateService;
      const state = await effectiveService.getState();
      if (!state) return;
      snapshot = state as unknown as PipelineStateSnapshot;
    }

    // Refresh metadata from latest state
    if (snapshot.title) run.issueTitle = snapshot.title;
    if (snapshot.branch) run.branch = snapshot.branch;
    const cost = snapshot.tokens?.estimated_cost_usd;
    if (typeof cost === "number") run.costUsd = cost;
    if (snapshot.pr_url) run.prUrl = snapshot.pr_url;
    if (snapshot.outcome_type) run.isFinal = true;

    const embed = this.buildEmbed(run, snapshot);

    const baseUrl = this.sanitizeWebhookUrl(
      `https://discord.com/api/webhooks/${run.webhookId}/${run.webhookToken}`
    );

    let res: Response;
    try {
      res = await fetch(
        `https://discord.com/api/webhooks/${run.webhookId}/${run.webhookToken}/messages/${run.messageId}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ embeds: [embed] }),
        }
      );
    } catch (err) {
      if (run.isFinal) {
        this.logger.warn("DiscordService: network error patching embed", { issueNumber, err });
        if (run.finalPatchRetries >= FINAL_PATCH_MAX_RETRIES) {
          this.logger.error(
            "DiscordService: final patch failed after all retries — embed may be stuck",
            { issueNumber, retries: run.finalPatchRetries, baseUrl }
          );
          this.runs.delete(issueNumber);
        } else {
          this.scheduleRetry(issueNumber);
        }
      } else {
        this.logger.warn("DiscordService: network error patching embed", { issueNumber, err });
      }
      return;
    }

    if (!res.ok) {
      if (run.isFinal) {
        this.logger.warn("DiscordService: failed to patch embed", {
          issueNumber,
          status: res.status,
        });
        if (run.finalPatchRetries >= FINAL_PATCH_MAX_RETRIES) {
          this.logger.error(
            "DiscordService: final patch failed after all retries — embed may be stuck",
            { issueNumber, retries: run.finalPatchRetries, baseUrl }
          );
          this.runs.delete(issueNumber);
        } else {
          this.scheduleRetry(issueNumber);
        }
      } else {
        this.logger.warn("DiscordService: failed to patch embed", {
          issueNumber,
          status: res.status,
        });
      }
      return;
    }

    // Remove completed runs only after a successful final patch
    NotifierStatusTracker.getInstance()?.recordSuccess("discord");
    if (run.isFinal) {
      this.runs.delete(issueNumber);
    }
  }

  // ─── Embed builder ───────────────────────────────────────────────────────────

  private buildEmbed(run: ActiveRun, state: PipelineStateSnapshot): DiscordEmbed {
    const elapsedMs = Date.now() - run.startTime;
    const { color, label: statusLabel } = outcomeDisplay(state.outcome_type);
    const { icon: modeIcon } = modeDisplay(state.pipeline_meta);
    const modeBadge = modeIcon ? ` ${modeIcon}` : "";

    return {
      title: `🔨 Pipeline #${run.issueNumber}${modeBadge} — ${statusLabel}`,
      url: run.prUrl,
      description: truncate(this.buildDescription(run, state), MAX_DESCRIPTION_LENGTH),
      color,
      fields: this.buildFields(run, state).slice(0, MAX_FIELDS),
      footer: { text: this.buildFooter(run, elapsedMs) },
      timestamp: new Date().toISOString(),
    };
  }

  private buildDescription(run: ActiveRun, state: PipelineStateSnapshot): string {
    const meta = state.pipeline_meta;

    // Header: issue title (linked to GitHub issue) + repo/branch
    const issueUrl = run.repoSlug
      ? `https://github.com/${run.repoSlug}/issues/${run.issueNumber}`
      : undefined;
    const titleText = issueUrl ? `[**${run.issueTitle}**](${issueUrl})` : `**${run.issueTitle}**`;
    const branchDisplay = run.branch ? `\`${run.branch}\`` : "";
    const baseBranch = state.base_branch;
    const branchLine =
      baseBranch && baseBranch !== "main" ? `${branchDisplay} → \`${baseBranch}\`` : branchDisplay;
    const header = `${titleText}\n\`${run.repoName}\` · ${branchLine}`;

    // Context line: complexity, file count, epic progress, routing, mode
    const contextParts: string[] = [];
    if (meta?.complexity) contextParts.push(`**${meta.complexity}**`);
    if (meta?.file_count) contextParts.push(`${meta.file_count} files`);
    if (meta?.epic_number) {
      const pos = meta.epic_position ?? "?";
      const total = meta.epic_total ?? "?";
      contextParts.push(`Epic #${meta.epic_number} (${pos}/${total})`);
    }
    if (meta?.route && meta.route !== "standard") {
      contextParts.push(`${meta.route} route`);
    }
    const { label: modeLabel, icon: modeContextIcon, modelSuffix } = modeDisplay(meta);
    if (modeLabel !== "Elevated") {
      const prefix = modeContextIcon ? `${modeContextIcon} ` : "";
      contextParts.push(`${prefix}**${modeLabel}**${modelSuffix}`);
    }
    if (meta?.skip_stages && meta.skip_stages.length > 0) {
      const skipped = meta.skip_stages.map((s) => STAGE_LABEL[s] ?? s).join(", ");
      contextParts.push(`⏭️ Skipped: ${skipped}`);
    }
    const contextLine = contextParts.length > 0 ? `\n${contextParts.join("  ·  ")}` : "";

    // Stage progress lines with optional per-stage cost and phase progress
    const perStageCost = state.tokens?.per_stage;
    const stageLines = PIPELINE_STAGES.map((stage) => {
      const info = state.stages?.[stage];
      const status = info?.status ?? "pending";
      const icon = statusIcon(status);
      const label = STAGE_LABEL[stage] ?? stage;

      const parts = [`${icon}  **${label}**`];

      // Show phase progress for running stages
      if (status === "running" && info?.current_phase && info?.total_phases) {
        const phaseLabel = info.current_phase.replace(/-/g, " ");
        parts.push(`— ${phaseLabel} (${info.total_phases} phases)`);
      }

      // Show elapsed time for running stages so you can monitor long-running stages
      if (status === "running") {
        const stageStart = run.stageStartTimes.get(stage) ?? info?.startTime;
        if (stageStart) {
          parts.push(`—  ${formatDuration(Date.now() - stageStart)}`);
        }
      }

      if (status !== "running" && info?.duration_ms != null) {
        parts.push(`—  ${formatDuration(info.duration_ms)}`);
      }

      // Show per-stage cost and model for completed/failed stages
      const stageTokens = perStageCost?.[stage];
      const stageCost = stageTokens?.cost_usd;
      const stageModel = stageTokens?.model;
      if ((stageCost != null && stageCost > 0) || stageModel) {
        const costStr = stageCost != null && stageCost > 0 ? formatCost(stageCost) : null;
        const modelStr = stageModel ? shortModel(stageModel) : null;
        const annotation = [costStr, modelStr].filter(Boolean).join("  ·  ");
        if (annotation) parts.push(`(${annotation})`);
      }

      return parts.join("  ");
    });

    return `${header}${contextLine}\n\n${stageLines.join("\n")}`;
  }

  private buildFields(run: ActiveRun, state: PipelineStateSnapshot): DiscordField[] {
    const fields: DiscordField[] = [];
    const isTerminal = !!state.outcome_type;

    // --- Live fields (shown during pipeline progress AND on completion) ---

    // Retry / model escalation — surface in real-time so you see escalations
    // happening, not just after the pipeline finishes
    const retryCount = state.retry_count ?? 0;
    const escalations = state.escalation_history ?? [];
    if (retryCount > 0 || escalations.length > 0) {
      const parts: string[] = [];
      if (retryCount > 0) {
        parts.push(`${retryCount} retry attempt${retryCount > 1 ? "s" : ""}`);
      }
      for (const esc of escalations) {
        const stage = STAGE_LABEL[esc.stage] ?? esc.stage;
        parts.push(`${stage}: ${shortModel(esc.fromModel)} → ${shortModel(esc.toModel)}`);
      }
      fields.push({
        name: "🔄 Retries & Escalations",
        value: truncate(parts.join("\n"), MAX_FIELD_VALUE_LENGTH),
      });
    }

    // RALPH loop iterations — show in real-time
    const ralph = state.ralph_iterations;
    if (ralph && Object.keys(ralph).length > 0) {
      const parts = Object.entries(ralph).map(([stage, count]) => {
        const label = STAGE_LABEL[stage] ?? stage;
        return `${label}: ${count} iteration${count > 1 ? "s" : ""}`;
      });
      fields.push({
        name: "🔁 RALPH Self-Healing",
        value: truncate(parts.join("\n"), MAX_FIELD_VALUE_LENGTH),
      });
    }

    // Mode field — surface performance mode + envelope ceiling + routing in
    // real-time so users can see which configuration is driving the run
    // (high-cost runs, Fable-capable Frontier, fast-track skips, etc.)
    const liveMeta = state.pipeline_meta;
    const {
      label: liveModeLabel,
      icon: liveModeIcon,
      modelSuffix: liveSuffix,
      ceiling: liveCeiling,
    } = modeDisplay(liveMeta);
    let modeValue = `${liveModeIcon ? liveModeIcon + " " : ""}${liveModeLabel}${liveSuffix}`;
    // Show the routing envelope's model ceiling ("up to Fable/Opus/Sonnet") so
    // the mode reads as a capability, not just a name. Maximum pins Opus and
    // already names it in the suffix, so suppress the hint there.
    if (liveModeLabel !== "Maximum") modeValue += `  ·  up to ${liveCeiling}`;
    const modeParts: string[] = [modeValue];
    if (liveMeta?.route && liveMeta.route !== "standard") {
      modeParts.push(`route: ${liveMeta.route}`);
    }
    fields.push({
      name: "⚙️ Mode",
      value: modeParts.join("  ·  "),
      inline: true,
    });

    // Usage-limit fallback — surface Fable → Opus graceful downgrades (Issue #26)
    // in real-time. Fable's separate Max-plan usage bucket can exhaust while Opus
    // still has capacity; the orchestrator retries the stage on Opus rather than
    // pausing the whole pipeline for the global cooldown. Operators want to see
    // this the moment it happens.
    const quotaFallbacks = liveMeta?.quota_fallbacks ?? [];
    if (quotaFallbacks.length > 0) {
      const lines = quotaFallbacks.map((f) => {
        const label = STAGE_LABEL[f.stage] ?? f.stage;
        return `${label}: ${shortModel(f.from)} → ${shortModel(f.to)}`;
      });
      fields.push({
        name: "⚠️ Usage-Limit Fallback",
        value: truncate(
          `${lines.join("\n")}\nFable quota hit — retried on Opus (separate Max-plan bucket)`,
          MAX_FIELD_VALUE_LENGTH
        ),
      });
    }

    // --- Terminal-only fields (shown only on completion/failure/cancellation) ---

    if (!isTerminal) return fields;

    // Error details for failed pipelines
    const failedStages = Object.entries(state.stages ?? {}).filter(
      ([, s]) => s?.status === "failed"
    );
    if (failedStages.length > 0) {
      const errorLines = failedStages.map(([name, s]) => {
        const label = STAGE_LABEL[name] ?? name;
        // Raw errors often contain stream-JSON envelopes from the Claude Agent
        // SDK (tool_result, assistant text, task_notification).  Run them
        // through formatErrorForDiscord so the user sees the actual error
        // message instead of a JSON blob.
        const extracted = formatErrorForDiscord(s?.error);
        const err = extracted ? `: ${extracted}` : "";
        return `❌ **${label}**${err}`;
      });
      fields.push({
        name: "🔍 Error Details",
        value: truncate(errorLines.join("\n"), MAX_FIELD_VALUE_LENGTH),
      });
    }

    // Cancellation context
    if (state.outcome_type === "cancelled") {
      const runningStage = Object.entries(state.stages ?? {}).find(
        ([, s]) => s?.status === "running"
      );
      const completedCount = Object.values(state.stages ?? {}).filter(
        (s) => s?.status === "complete" || s?.status === "skipped"
      ).length;

      const parts: string[] = [];
      if (runningStage) {
        const label = STAGE_LABEL[runningStage[0]] ?? runningStage[0];
        parts.push(`Stopped during **${label}**`);
      }
      parts.push(`${completedCount}/${PIPELINE_STAGES.length} stages complete`);
      parts.push("Issue open · Branch preserved");

      fields.push({ name: "⏹️ Cancelled", value: parts.join("\n") });
    }

    // Budget ceiling context
    if (state.outcome_type === "budget-ceiling") {
      fields.push({
        name: "💰 Budget Ceiling",
        value: `Spent ${formatCost(run.costUsd)} before hitting limit\nIncrease budget or re-run with higher ceiling`,
      });
    }

    // Gate results (build/test/lint)
    if (state.gate_results && state.gate_results.length > 0) {
      const gateIcons = state.gate_results.map((g) => {
        const icon = g.result === "pass" ? "✅" : "❌";
        const err = g.error_summary ? ` — ${g.error_summary}` : "";
        return `${icon} ${g.gate_name}${err}`;
      });
      fields.push({
        name: "🧪 Gate Results",
        value: truncate(gateIcons.join("\n"), MAX_FIELD_VALUE_LENGTH),
      });
    }

    // Budget usage (for successful and failed runs). See formatBudgetFieldValue
    // (transport.ts) for why the pre-flight estimate is labeled "Pre-run est."
    // with an accuracy ratio rather than a bare "Est:" figure (#267).
    const meta = state.pipeline_meta;
    if (meta?.budget_ceiling_usd && meta.budget_ceiling_usd > 0) {
      fields.push({
        name: "💰 Budget",
        value: formatBudgetFieldValue(
          run.costUsd,
          meta.budget_ceiling_usd,
          meta.budget_estimate_usd
        ),
        inline: true,
      });
    }

    // Health score on completion
    if (meta?.health_score != null) {
      const score = meta.health_score;
      const healthIcon = score >= 90 ? "🟢" : score >= 70 ? "🟡" : "🔴";
      const healthLabel = score >= 90 ? "Excellent" : score >= 70 ? "Good" : "Needs Attention";
      fields.push({
        name: "🏥 Pipeline Health",
        value: `${healthIcon} ${score}/100 — ${healthLabel}`,
        inline: true,
      });
    }

    // Cache efficiency (shows how well prompt caching is working)
    // `total_input` follows the Go scheduler convention: it is COMBINED
    // (raw input + cache reads — see CompleteStageWithCost and the
    // PipelineStateService.completeStage comment), so it already IS the
    // "billed-as-input without caching" denominator. Re-adding cache_read
    // double-counted it and pinned the display at ~50% on every
    // cache-dominated run (#262).
    const cacheRead = state.tokens?.total_cache_read ?? 0;
    const totalInput = state.tokens?.total_input ?? 0;
    if (cacheRead > 0 && totalInput > 0) {
      const hitPct = ((cacheRead / totalInput) * 100).toFixed(0);
      fields.push({
        name: "📦 Cache",
        value: `${hitPct}% hit rate`,
        inline: true,
      });
    }

    // PR number (explicit — complement to the embed URL link)
    if (meta?.pr_number) {
      fields.push({
        name: "📋 Pull Request",
        value: `#${meta.pr_number}`,
        inline: true,
      });
    }

    // Model used (derive from per-stage data if not in meta)
    const models = new Set<string>();
    if (meta?.model) models.add(meta.model);
    const perStage = state.tokens?.per_stage;
    if (perStage) {
      for (const s of Object.values(perStage)) {
        if (s.model) models.add(shortModel(s.model));
      }
    }
    if (models.size > 0) {
      fields.push({
        name: "🤖 Model",
        value: Array.from(models).map(shortModel).join(", "),
        inline: true,
      });
    }

    // Actionable recommendation
    const action = determineAction(state);
    if (action) {
      fields.push({ name: "⚡ Recommended Action", value: action });
    }

    return fields;
  }

  private buildFooter(run: ActiveRun, elapsedMs: number): string {
    const cost = run.costUsd > 0 ? `💰 ${formatCost(run.costUsd)}  ` : "";
    return `${cost}⏱ ${formatDuration(elapsedMs)}`;
  }

  // ─── Config & secret resolution ──────────────────────────────────────────────

  private getDiscordConfig(): DiscordNotificationsConfig | null {
    try {
      const result = this.configBridge.getEffectiveConfig();
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      return (result?.config as any)?.notifications?.discord ?? null;
    } catch {
      return null;
    }
  }

  /**
   * Resolve the webhook URL using a priority chain:
   *   1. VSCode SecretStorage (OS keychain) — set via the
   *      "Nightgauge: Configure Discord Notifications" command
   *   2. Environment variable named by `config.webhook_env` — for CI/headless
   *
   * Returns null (and logs a warning) if no URL is available.
   */
  private async resolveWebhookUrl(config: DiscordNotificationsConfig): Promise<string | null> {
    // 1. SecretStorage (primary — interactive users)
    const secretService = SecretStorageService.getInstance();
    if (secretService) {
      const stored = await secretService.getSecret(SECRET_KEYS.discordWebhookUrl);
      if (stored) return stored;
    }

    // 2. Env var fallback (CI / power users)
    if (config.webhook_env) {
      const envUrl = process.env[config.webhook_env];
      if (envUrl) return envUrl;
      this.logger.warn(
        `DiscordService: no webhook URL found in SecretStorage or env var "${config.webhook_env}" — ` +
          'run "Nightgauge: Configure Discord Notifications" to set it up.'
      );
    } else {
      this.logger.warn(
        "DiscordService: no webhook URL configured — " +
          'run "Nightgauge: Configure Discord Notifications" to set it up.'
      );
    }

    return null;
  }

  // ─── Safety-pause notifications (Issue #3605 bullet C) ────────────────

  /**
   * Send a one-off Discord webhook message announcing that the autonomous
   * scheduler has paused itself due to a safety trip. Distinct from the
   * per-pipeline embed lifecycle in {@link initialize} / {@link handleStageStart}:
   * this is a single POST with no follow-up PATCH, so safety pauses don't
   * interleave with pipeline-stage updates on Discord.
   *
   * Called by the `autonomous.statusChanged` IPC subscriber when the new
   * status is `paused` or `safety_tripped` and the trigger is one of:
   *
   *   - `rate-limit-circuit-breaker` — GitHub GraphQL rate-limit bucket
   *     exhausted (#3577 originally only fired a VSCode toast)
   *   - `safety:cascading-failures` — 3 failures in 30m window (#3605 C)
   *
   * Returns silently when no webhook URL is configured so this is safe
   * to call unconditionally from the status-change handler.
   *
   * @param triggeredBy The structured pauseTriggeredBy tag from Go.
   * @param reason      The human-readable pauseReason from Go (carried
   *                    verbatim into the embed description).
   */
  async notifySafetyPause(triggeredBy: string, reason: string): Promise<void> {
    const config = this.getDiscordConfig();
    if (!config) {
      this.logger.debug(
        "DiscordService.notifySafetyPause: Discord notifications disabled — skipping safety pause notification"
      );
      return;
    }
    const webhookUrl = await this.resolveWebhookUrl(config);
    if (!webhookUrl) {
      this.logger.debug(
        "DiscordService.notifySafetyPause: no webhook URL configured — skipping safety pause notification"
      );
      return;
    }
    const parsed = parseWebhookUrl(webhookUrl);
    if (!parsed) {
      this.logger.warn("DiscordService.notifySafetyPause: malformed webhook URL — skipping");
      return;
    }

    // Embed shape: red bar (failure-ish), bold title, reason verbatim in
    // description, footer carries the structured tag so an operator
    // grepping Discord can locate the originating Go code path.
    const titleByTag: Record<string, string> = {
      "rate-limit-circuit-breaker": "Autonomous paused: GitHub rate-limit exhausted",
      "safety:cascading-failures": "Autonomous paused: cascading pipeline failures",
    };
    const title = titleByTag[triggeredBy] ?? `Autonomous paused: ${triggeredBy || "safety trip"}`;

    const embed = {
      title,
      description:
        reason && reason.length > 0
          ? reason
          : "No further details were provided by the Go scheduler.",
      color: 0xed4245, // Discord red — same hue used for stage:error
      footer: {
        text: `${triggeredBy || "unknown"} · manual triage required (nightgauge autonomous resume)`,
      },
      timestamp: new Date().toISOString(),
    };

    const baseUrl = `discord.com/api/webhooks/${parsed.id}`;
    try {
      await retryWithBackoff(
        () =>
          fetch(`https://discord.com/api/webhooks/${parsed.id}/${parsed.token}`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ embeds: [embed] }),
          }),
        {
          delays: FETCH_RETRY_DELAYS,
          logger: this.logger,
          label: "DiscordService.notifySafetyPause",
          sanitizedUrl: baseUrl,
        }
      );
      NotifierStatusTracker.getInstance()?.recordSuccess("discord");
      this.logger.info("DiscordService: safety-pause notification sent", { triggeredBy });
    } catch (err) {
      NotifierStatusTracker.getInstance()?.recordError(
        "discord",
        redactSecrets(err instanceof Error ? err.message : String(err))
      );
      this.logger.error("DiscordService: safety-pause notification failed after retries", {
        triggeredBy,
        baseUrl,
        err,
      });
    }
  }

  /**
   * Send a one-off Discord webhook message announcing a model-unavailable
   * fallback (#42): the API rejected the run's selected model (not offered on
   * the current plan, unknown ID, or a model-specific usage cap) and the
   * pipeline substituted the next-best tier for the rest of the run. Same
   * single-POST shape as {@link notifySafetyPause} — this is informational
   * (the run CONTINUES on the substituted model), hence the amber bar rather
   * than the failure red.
   *
   * @param issueNumber The issue whose run substituted models (0 = unknown).
   * @param summary     Human-readable text naming the original model, the
   *                    rejection reason, and the substituted model — built by
   *                    the pipeline.modelFallback subscriber from the Go
   *                    scheduler's event payload.
   */
  async notifyModelFallback(issueNumber: number, summary: string): Promise<void> {
    const config = this.getDiscordConfig();
    if (!config) {
      this.logger.debug(
        "DiscordService.notifyModelFallback: Discord notifications disabled — skipping"
      );
      return;
    }
    const webhookUrl = await this.resolveWebhookUrl(config);
    if (!webhookUrl) {
      this.logger.debug("DiscordService.notifyModelFallback: no webhook URL configured — skipping");
      return;
    }
    const parsed = parseWebhookUrl(webhookUrl);
    if (!parsed) {
      this.logger.warn("DiscordService.notifyModelFallback: malformed webhook URL — skipping");
      return;
    }

    const embed = {
      title: issueNumber > 0 ? `Model fallback on #${issueNumber}` : "Model fallback",
      description: summary,
      color: 0xfee75c, // Discord amber — informational, the run continues
      footer: {
        text: "model_unavailable · sticky for this run · resets next run",
      },
      timestamp: new Date().toISOString(),
    };

    const baseUrl = `discord.com/api/webhooks/${parsed.id}`;
    try {
      await retryWithBackoff(
        () =>
          fetch(`https://discord.com/api/webhooks/${parsed.id}/${parsed.token}`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ embeds: [embed] }),
          }),
        {
          delays: FETCH_RETRY_DELAYS,
          logger: this.logger,
          label: "DiscordService.notifyModelFallback",
          sanitizedUrl: baseUrl,
        }
      );
      NotifierStatusTracker.getInstance()?.recordSuccess("discord");
      this.logger.info("DiscordService: model-fallback notification sent", { issueNumber });
    } catch (err) {
      NotifierStatusTracker.getInstance()?.recordError(
        "discord",
        redactSecrets(err instanceof Error ? err.message : String(err))
      );
      this.logger.error("DiscordService: model-fallback notification failed after retries", {
        issueNumber,
        baseUrl,
        err,
      });
    }
  }

  // ─── Disposal ────────────────────────────────────────────────────────────────

  dispose(): void {
    this.patcher.dispose();
    for (const subs of this.slotDisposables.values()) {
      for (const s of subs) s.dispose();
    }
    this.slotDisposables.clear();
    this.pendingRepoSlugs.clear();
    for (const d of this.disposables) d.dispose();
    this.disposables = [];
    this.runs.clear();
  }
}
