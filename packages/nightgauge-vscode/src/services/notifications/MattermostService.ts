/**
 * MattermostService — live-updating pipeline status posts for Mattermost.
 *
 * Posts a single Slack-compatible attachment per pipeline run via an incoming
 * webhook. When the webhook response carries a parsable post id, edits the
 * post in place via `PUT /api/v4/posts/{id}` as stages progress (Discord
 * parity). When no id is returned (older / restricted Mattermost servers),
 * the run is downgraded to **post-only** mode: intermediate updates are
 * suppressed and a single terminal-state attachment posts at the end.
 *
 * Configuration (.nightgauge/config.yaml):
 *   notifications:
 *     mattermost:
 *       enabled: true
 *       webhook_env: MATTERMOST_WEBHOOK_URL
 *
 * Webhook URL is preferred from VSCode SecretStorage
 * (SECRET_KEYS.mattermostWebhookUrl) — env-var is fallback for CI.
 *
 * @see Issue #3373 — ADR-001 (shared transport), ADR-002 (ephemeral no-op),
 *      ADR-003 (post-only fallback), ADR-004 (hex color encoding).
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import { PipelineStateService } from "../PipelineStateService";
import { ConfigBridge } from "../ConfigBridge";
import { Logger } from "../../utils/logger";
import { SecretStorageService, SECRET_KEYS } from "../SecretStorageService";
import {
  formatErrorForDiscord,
  outcomeDisplay,
  determineAction,
  modeDisplay,
} from "../DiscordService";
import type { Notifier, PipelineEventContext } from "./types";
import { NotifierStatusTracker } from "./NotifierStatusTracker";
import {
  DEBOUNCE_MS,
  DebouncedPatcher,
  FETCH_RETRY_DELAYS,
  FINAL_PATCH_MAX_RETRIES,
  FINAL_PATCH_RETRY_DELAYS,
  formatBudgetFieldValue,
  formatCost,
  formatDuration,
  hexColor,
  redactSecrets,
  retryWithBackoff,
  shortModel,
  truncate,
} from "./transport";

// ─── Mattermost attachment limits ───────────────────────────────────────────

/** Mattermost truncates attachment.text and per-field value at ~4000 chars. */
const MAX_FIELD_VALUE_LENGTH = 4000;
const MAX_DESCRIPTION_LENGTH = 4000;
const MAX_FIELDS = 25;

// ─── Pipeline stages in execution order ─────────────────────────────────────

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

// ─── Interfaces ─────────────────────────────────────────────────────────────

interface MattermostNotificationsConfig {
  enabled?: boolean;
  webhook_env?: string;
}

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
    total_input?: number;
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
     *  limit (Issue #26). Surfaced so operators see the downgrade in real time. */
    quota_fallbacks?: Array<{ stage: string; from: string; to: string }>;
  };
}

/** Edit mode for the run.
 *   - "edit"      → live in-place edits via PUT /api/v4/posts/{id}.
 *   - "post-only" → fall back to a single terminal-state post.
 */
type EditMode = "edit" | "post-only";

interface ActiveRun {
  issueNumber: number;
  issueTitle: string;
  branch: string;
  repoName: string;
  repoSlug?: string;
  baseUrl: string;
  hookPath: string;
  postId: string;
  startTime: number;
  costUsd: number;
  prUrl?: string;
  stageStartTimes: Map<string, number>;
  isFinal: boolean;
  finalSnapshot?: PipelineStateSnapshot;
  finalPatchRetries: number;
  editMode: EditMode;
  /** True after we've logged the post-id-missing warning for this run. */
  fallbackWarned: boolean;
  stateService?: PipelineStateService;
}

interface MattermostField {
  title: string;
  value: string;
  short?: boolean;
}

interface MattermostAttachment {
  fallback?: string;
  color: string;
  title?: string;
  title_link?: string;
  text?: string;
  fields?: MattermostField[];
  footer?: string;
  ts?: number;
}

interface PostBody {
  text?: string;
  attachments: MattermostAttachment[];
}

interface EditBody {
  id: string;
  message: string;
  props: { attachments: MattermostAttachment[] };
}

// ─── Helpers ────────────────────────────────────────────────────────────────

/**
 * Parse a Mattermost incoming-webhook URL into `{ baseUrl, token }`.
 * baseUrl is the scheme + host (no trailing slash) — used to build the
 * companion edit endpoint at `${baseUrl}/api/v4/posts/{id}`.
 *
 * Returns null for invalid input (including Discord webhook URLs).
 */
export function parseWebhookUrl(url: string): { baseUrl: string; token: string } | null {
  if (!url) return null;
  const match = url.match(/^(https?:\/\/[^/]+)\/hooks\/([A-Za-z0-9]+)\/?$/);
  if (!match) return null;
  return { baseUrl: match[1], token: match[2] };
}

function statusIcon(status: string): string {
  switch (status) {
    case "running":
      return ":arrows_counterclockwise:";
    case "complete":
      return ":white_check_mark:";
    case "failed":
      return ":x:";
    case "skipped":
      return ":fast_forward:";
    case "deferred":
      return ":pause_button:";
    default:
      return ":hourglass_flowing_sand:";
  }
}

// ─── MattermostService ──────────────────────────────────────────────────────

export class MattermostService implements Notifier, vscode.Disposable {
  private readonly runs = new Map<number, ActiveRun>();
  private readonly patcher = new DebouncedPatcher();
  private readonly slotDisposables = new Map<number, vscode.Disposable[]>();
  private readonly pendingRepoSlugs = new Map<number, string>();
  /** Per-issue ephemeral toggle exposed via setEphemeral(). MVP no-op. */
  private readonly ephemeralFlags = new Map<number, boolean>();

  private disposables: vscode.Disposable[] = [];

  constructor(
    private readonly pipelineStateService: PipelineStateService,
    private readonly configBridge: ConfigBridge,
    private readonly logger: Logger
  ) {}

  async initialize(): Promise<void> {
    this.disposables.push(
      this.pipelineStateService.onStageStart(({ stage, issueNumber }) => {
        if (this.slotDisposables.size > 0) return;
        void this.handleStageStart(stage as PipelineStage, issueNumber);
      }),
      this.pipelineStateService.onStageError(({ issueNumber }) => {
        if (this.slotDisposables.size > 0) return;
        this.scheduleUpdate(issueNumber);
      }),
      this.pipelineStateService.onStateChanged((state) => {
        if (this.slotDisposables.size > 0) return;
        if (state) void this.handleStateChanged(state as unknown as PipelineStateSnapshot);
      })
    );
    this.logger.info("MattermostService initialized");
  }

  // ─── Notifier interface delegations ───────────────────────────────────────

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

  // ─── Concurrent worktree slot subscription ────────────────────────────────

  subscribeToSlot(
    issueNumber: number,
    slotStateService: PipelineStateService,
    repoSlug?: string
  ): void {
    if (repoSlug) this.pendingRepoSlugs.set(issueNumber, repoSlug);
    this.unsubscribeFromSlot(issueNumber);

    const subs: vscode.Disposable[] = [
      slotStateService.onStageStart(({ stage, issueNumber: num }) => {
        if (num !== issueNumber) return;
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
    this.logger.info("MattermostService: subscribed to worktree slot", { issueNumber });
  }

  unsubscribeFromSlot(issueNumber: number): void {
    const subs = this.slotDisposables.get(issueNumber);
    if (subs) {
      for (const s of subs) s.dispose();
      this.slotDisposables.delete(issueNumber);
    }
  }

  /**
   * Mark posts for an issue as ephemeral. Forward-compat surface — for MVP
   * this is a no-op against incoming webhooks (Mattermost ignores
   * `response_type: "ephemeral"` outside slash-command responses). When a
   * future PR adds bot-token auth, this flag will route those posts via
   * `/api/v4/posts/ephemeral`.
   *
   * @see ADR-002
   */
  setEphemeral(issueNumber: number, ephemeral: boolean): void {
    this.ephemeralFlags.set(issueNumber, ephemeral);
    this.logger.debug("MattermostService: ephemeral flag set (no-op for incoming webhooks)", {
      issueNumber,
      ephemeral,
    });
  }

  // ─── Event handlers ───────────────────────────────────────────────────────

  private async handleStageStart(
    stage: PipelineStage,
    issueNumber: number,
    stateService?: PipelineStateService
  ): Promise<void> {
    const effectiveStateService = stateService ?? this.pipelineStateService;

    if (stage !== "issue-pickup") {
      this.scheduleUpdate(issueNumber);
      return;
    }

    this.flushStaleRuns(issueNumber);

    const config = this.getMattermostConfig();
    if (!config?.enabled) return;

    const webhookUrl = await this.resolveWebhookUrl(config);
    if (!webhookUrl) return;

    const parsed = parseWebhookUrl(webhookUrl);
    if (!parsed) {
      this.logger.warn("MattermostService: invalid Mattermost webhook URL format");
      return;
    }

    const state = await effectiveStateService.getState();
    if (!state || state.issue_number !== issueNumber) return;

    const statePath = effectiveStateService.getStatePath();
    let repoRoot = statePath.split("/.nightgauge/")[0];
    repoRoot = repoRoot.replace(/\/\.worktrees\/[^/]+$/, "");
    const repoName = repoRoot.split("/").pop() ?? repoRoot;

    const repoSlug = this.pendingRepoSlugs.get(issueNumber);
    this.pendingRepoSlugs.delete(issueNumber);

    const run: ActiveRun = {
      issueNumber,
      issueTitle: (state as unknown as PipelineStateSnapshot).title ?? `Issue #${issueNumber}`,
      branch: (state as unknown as PipelineStateSnapshot).branch ?? "",
      repoName,
      repoSlug,
      baseUrl: parsed.baseUrl,
      hookPath: `/hooks/${parsed.token}`,
      postId: "",
      startTime: Date.now(),
      costUsd: 0,
      stageStartTimes: new Map(),
      isFinal: false,
      finalPatchRetries: 0,
      editMode: "edit",
      fallbackWarned: false,
      stateService,
    };

    const attachment = this.buildAttachment(run, state as unknown as PipelineStateSnapshot);
    const sanitizedUrl = this.sanitizeWebhookUrl(parsed.baseUrl);

    try {
      const res = await retryWithBackoff(
        () =>
          fetch(`${parsed.baseUrl}/hooks/${parsed.token}`, {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              Accept: "application/json",
            },
            body: JSON.stringify({ attachments: [attachment] } satisfies PostBody),
          }),
        {
          delays: FETCH_RETRY_DELAYS,
          logger: this.logger,
          label: "MattermostService",
          sanitizedUrl,
        }
      );

      const postId = await this.extractPostId(res);
      if (postId) {
        run.postId = postId;
      } else {
        run.editMode = "post-only";
        run.fallbackWarned = true;
        this.logger.warn(
          "MattermostService: webhook response missing post id — falling back to post-only mode",
          { issueNumber, sanitizedUrl }
        );
      }
      this.runs.set(issueNumber, run);
      NotifierStatusTracker.getInstance()?.recordSuccess("mattermost");
      this.logger.info("MattermostService: pipeline post created", {
        issueNumber,
        editMode: run.editMode,
      });
    } catch (err) {
      NotifierStatusTracker.getInstance()?.recordError(
        "mattermost",
        redactSecrets(err instanceof Error ? err.message : String(err))
      );
      this.logger.error("MattermostService: failed to create post after retries", {
        issueNumber,
        sanitizedUrl,
        err,
      });
    }
  }

  private async handleStateChanged(state: PipelineStateSnapshot): Promise<void> {
    const run = this.runs.get(state.issue_number);
    if (!run) return;

    if (state.title) run.issueTitle = state.title;
    if (state.branch) run.branch = state.branch;
    const cost = state.tokens?.estimated_cost_usd;
    if (typeof cost === "number") run.costUsd = cost;
    if (state.pr_url) run.prUrl = state.pr_url;

    if (state.stages) {
      for (const [name, info] of Object.entries(state.stages)) {
        if (info?.status === "running" && !run.stageStartTimes.has(name)) {
          run.stageStartTimes.set(name, info.startTime ?? Date.now());
        }
      }
    }

    if (state.outcome_type) {
      run.finalSnapshot = state;
      if (!run.isFinal) {
        run.isFinal = true;
        this.patcher.cancel(state.issue_number);
        await this.patchPost(state.issue_number);
      }
      return;
    }

    // Suppress intermediate updates in post-only mode — only post on terminal state.
    if (run.editMode === "post-only") return;

    this.scheduleUpdate(state.issue_number);
  }

  // ─── Debounced update / retry ─────────────────────────────────────────────

  private scheduleUpdate(issueNumber: number): void {
    this.patcher.schedule(issueNumber, () => this.patchPost(issueNumber), DEBOUNCE_MS);
  }

  private scheduleRetry(issueNumber: number): void {
    const run = this.runs.get(issueNumber);
    if (!run) return;

    if (run.finalPatchRetries >= FINAL_PATCH_MAX_RETRIES) {
      this.logger.error(
        "MattermostService: final patch failed after all retries — post may be stuck",
        { issueNumber, retries: run.finalPatchRetries }
      );
      this.runs.delete(issueNumber);
      return;
    }

    const delay = FINAL_PATCH_RETRY_DELAYS[run.finalPatchRetries] ?? 6000;
    run.finalPatchRetries += 1;

    this.logger.info("MattermostService: scheduling final patch retry", {
      issueNumber,
      attempt: run.finalPatchRetries,
      delayMs: delay,
    });

    this.patcher.schedule(issueNumber, () => this.patchPost(issueNumber), delay);
  }

  private flushStaleRuns(excludeIssue?: number): void {
    for (const [issueNumber, run] of this.runs) {
      if (issueNumber === excludeIssue) continue;
      if (run.isFinal && run.finalSnapshot) {
        this.patcher.cancel(issueNumber);
        void this.patchPost(issueNumber);
      }
    }
  }

  /**
   * Edit the in-flight post in place, or — in post-only mode — post a fresh
   * terminal-state attachment.
   */
  private async patchPost(issueNumber: number): Promise<void> {
    const run = this.runs.get(issueNumber);
    if (!run) return;

    let snapshot: PipelineStateSnapshot;
    if (run.isFinal && run.finalSnapshot) {
      snapshot = run.finalSnapshot;
    } else {
      const effectiveService = run.stateService ?? this.pipelineStateService;
      const state = await effectiveService.getState();
      if (!state) return;
      snapshot = state as unknown as PipelineStateSnapshot;
    }

    if (snapshot.title) run.issueTitle = snapshot.title;
    if (snapshot.branch) run.branch = snapshot.branch;
    const cost = snapshot.tokens?.estimated_cost_usd;
    if (typeof cost === "number") run.costUsd = cost;
    if (snapshot.pr_url) run.prUrl = snapshot.pr_url;
    if (snapshot.outcome_type) run.isFinal = true;

    const attachment = this.buildAttachment(run, snapshot);
    const sanitizedUrl = this.sanitizeWebhookUrl(run.baseUrl);

    // post-only mode → fresh POST at terminal state, no in-place edit.
    if (run.editMode === "post-only") {
      if (!run.isFinal) return;
      try {
        const res = await fetch(`${run.baseUrl}${run.hookPath}`, {
          method: "POST",
          headers: { "Content-Type": "application/json", Accept: "application/json" },
          body: JSON.stringify({ attachments: [attachment] } satisfies PostBody),
        });
        if (!res.ok) {
          this.handlePatchFailure(run, issueNumber, sanitizedUrl, `HTTP ${res.status}`);
          return;
        }
      } catch (err) {
        this.handlePatchFailure(run, issueNumber, sanitizedUrl, err);
        return;
      }
      this.runs.delete(issueNumber);
      return;
    }

    // Edit mode → PUT /api/v4/posts/{id}.
    if (!run.postId) return;

    const editBody: EditBody = {
      id: run.postId,
      message: "",
      props: { attachments: [attachment] },
    };

    let res: Response;
    try {
      res = await fetch(`${run.baseUrl}/api/v4/posts/${run.postId}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify(editBody),
      });
    } catch (err) {
      this.handlePatchFailure(run, issueNumber, sanitizedUrl, err);
      return;
    }

    if (res.status === 401 || res.status === 403) {
      // Server rejects unauthenticated edit — degrade to post-only.
      if (!run.fallbackWarned) {
        this.logger.warn(
          "MattermostService: edit endpoint rejected webhook auth — downgrading to post-only mode",
          { issueNumber, status: res.status, sanitizedUrl }
        );
        run.fallbackWarned = true;
      }
      run.editMode = "post-only";
      // For final state, immediately retry in post-only mode.
      if (run.isFinal) {
        await this.patchPost(issueNumber);
      }
      return;
    }

    if (!res.ok) {
      this.handlePatchFailure(run, issueNumber, sanitizedUrl, `HTTP ${res.status}`);
      return;
    }

    NotifierStatusTracker.getInstance()?.recordSuccess("mattermost");
    if (run.isFinal) this.runs.delete(issueNumber);
  }

  private handlePatchFailure(
    run: ActiveRun,
    issueNumber: number,
    sanitizedUrl: string,
    err: unknown
  ): void {
    const detail = err instanceof Error ? err.message : String(err);
    if (run.isFinal) {
      this.logger.warn("MattermostService: failed to patch post", {
        issueNumber,
        detail,
      });
      if (run.finalPatchRetries >= FINAL_PATCH_MAX_RETRIES) {
        this.logger.error(
          "MattermostService: final patch failed after all retries — post may be stuck",
          { issueNumber, retries: run.finalPatchRetries, sanitizedUrl }
        );
        this.runs.delete(issueNumber);
      } else {
        this.scheduleRetry(issueNumber);
      }
    } else {
      this.logger.warn("MattermostService: failed to patch post", { issueNumber, detail });
    }
  }

  /**
   * Pull a Mattermost post id out of a webhook response. Mattermost servers
   * vary — modern v9.x/v10.x return the created post; older deployments
   * return an empty body. Returns null when no id is found.
   */
  private async extractPostId(res: Response): Promise<string | null> {
    try {
      const ct = res.headers.get("content-type") ?? "";
      if (!ct.includes("json")) return null;
      const data = (await res.json()) as Record<string, unknown> | null;
      if (!data || typeof data !== "object") return null;
      if (typeof data.id === "string" && data.id.length > 0) return data.id;
      const post = (data as { post?: { id?: unknown } }).post;
      if (post && typeof post.id === "string" && post.id.length > 0) return post.id;
      return null;
    } catch {
      return null;
    }
  }

  private sanitizeWebhookUrl(baseUrl: string): string {
    try {
      const u = new URL(baseUrl);
      return `${u.protocol}//${u.host}/hooks`;
    } catch {
      return "/hooks";
    }
  }

  // ─── Attachment builder ───────────────────────────────────────────────────

  buildAttachment(run: ActiveRun, state: PipelineStateSnapshot): MattermostAttachment {
    const elapsedMs = Date.now() - run.startTime;
    const { color: colorInt, label: statusLabel } = outcomeDisplay(state.outcome_type);
    const { icon: modeIcon } = modeDisplay(state.pipeline_meta);
    const modeBadge = modeIcon ? ` ${modeIcon}` : "";

    const description = truncate(this.buildDescription(run, state), MAX_DESCRIPTION_LENGTH);
    const fields = this.buildFields(run, state).slice(0, MAX_FIELDS);
    const footer = this.buildFooter(run, elapsedMs);

    return {
      fallback: redactSecrets(`Pipeline #${run.issueNumber}: ${statusLabel}`),
      color: hexColor(colorInt),
      title: `Pipeline #${run.issueNumber}${modeBadge} — ${statusLabel}`,
      title_link: run.prUrl,
      text: description,
      fields,
      footer,
      ts: Math.floor(Date.now() / 1000),
    };
  }

  private buildDescription(run: ActiveRun, state: PipelineStateSnapshot): string {
    const meta = state.pipeline_meta;

    const issueUrl = run.repoSlug
      ? `https://github.com/${run.repoSlug}/issues/${run.issueNumber}`
      : undefined;
    const titleText = issueUrl ? `[**${run.issueTitle}**](${issueUrl})` : `**${run.issueTitle}**`;
    const branchDisplay = run.branch ? `\`${run.branch}\`` : "";
    const baseBranch = state.base_branch;
    const branchLine =
      baseBranch && baseBranch !== "main" ? `${branchDisplay} → \`${baseBranch}\`` : branchDisplay;
    const header = `${titleText}\n\`${run.repoName}\` · ${branchLine}`;

    const contextParts: string[] = [];
    if (meta?.complexity) contextParts.push(`**${meta.complexity}**`);
    if (meta?.file_count) contextParts.push(`${meta.file_count} files`);
    if (meta?.epic_number) {
      const pos = meta.epic_position ?? "?";
      const total = meta.epic_total ?? "?";
      contextParts.push(`Epic #${meta.epic_number} (${pos}/${total})`);
    }
    if (meta?.route && meta.route !== "standard") contextParts.push(`${meta.route} route`);
    const { label: modeLabel, icon: modeContextIcon, modelSuffix } = modeDisplay(meta);
    if (modeLabel !== "Elevated") {
      const prefix = modeContextIcon ? `${modeContextIcon} ` : "";
      contextParts.push(`${prefix}**${modeLabel}**${modelSuffix}`);
    }
    if (meta?.skip_stages && meta.skip_stages.length > 0) {
      const skipped = meta.skip_stages.map((s) => STAGE_LABEL[s] ?? s).join(", ");
      contextParts.push(`Skipped: ${skipped}`);
    }
    const contextLine = contextParts.length > 0 ? `\n${contextParts.join("  ·  ")}` : "";

    const perStageCost = state.tokens?.per_stage;
    const stageLines = PIPELINE_STAGES.map((stage) => {
      const info = state.stages?.[stage];
      const status = info?.status ?? "pending";
      const icon = statusIcon(status);
      const label = STAGE_LABEL[stage] ?? stage;

      const parts = [`${icon}  **${label}**`];

      if (status === "running" && info?.current_phase && info?.total_phases) {
        const phaseLabel = info.current_phase.replace(/-/g, " ");
        parts.push(`— ${phaseLabel} (${info.total_phases} phases)`);
      }
      if (status === "running") {
        const stageStart = run.stageStartTimes.get(stage) ?? info?.startTime;
        if (stageStart) parts.push(`— ${formatDuration(Date.now() - stageStart)}`);
      }
      if (status !== "running" && info?.duration_ms != null) {
        parts.push(`— ${formatDuration(info.duration_ms)}`);
      }

      const stageCost = perStageCost?.[stage]?.cost_usd;
      if (stageCost != null && stageCost > 0) parts.push(`(${formatCost(stageCost)})`);

      return parts.join("  ");
    });

    return redactSecrets(`${header}${contextLine}\n\n${stageLines.join("\n")}`);
  }

  private buildFields(run: ActiveRun, state: PipelineStateSnapshot): MattermostField[] {
    const fields: MattermostField[] = [];
    const isTerminal = !!state.outcome_type;

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
        title: "Retries & Escalations",
        value: truncate(redactSecrets(parts.join("\n")), MAX_FIELD_VALUE_LENGTH),
        short: false,
      });
    }

    const ralph = state.ralph_iterations;
    if (ralph && Object.keys(ralph).length > 0) {
      const parts = Object.entries(ralph).map(([stage, count]) => {
        const label = STAGE_LABEL[stage] ?? stage;
        return `${label}: ${count} iteration${count > 1 ? "s" : ""}`;
      });
      fields.push({
        title: "RALPH Self-Healing",
        value: truncate(parts.join("\n"), MAX_FIELD_VALUE_LENGTH),
        short: false,
      });
    }

    const liveMeta = state.pipeline_meta;
    const {
      label: liveModeLabel,
      icon: liveModeIcon,
      modelSuffix: liveSuffix,
      ceiling: liveCeiling,
    } = modeDisplay(liveMeta);
    let modeValue = `${liveModeIcon ? liveModeIcon + " " : ""}${liveModeLabel}${liveSuffix}`;
    // Show the routing envelope's model ceiling ("up to Fable/Opus/Sonnet").
    // Maximum pins Opus and names it in the suffix, so suppress the hint there.
    if (liveModeLabel !== "Maximum") modeValue += `  ·  up to ${liveCeiling}`;
    const modeParts: string[] = [modeValue];
    if (liveMeta?.route && liveMeta.route !== "standard") {
      modeParts.push(`route: ${liveMeta.route}`);
    }
    fields.push({
      title: "Mode",
      value: modeParts.join("  ·  "),
      short: true,
    });

    // Usage-limit fallback — Fable → Opus graceful downgrades (Issue #26).
    const quotaFallbacks = liveMeta?.quota_fallbacks ?? [];
    if (quotaFallbacks.length > 0) {
      const lines = quotaFallbacks.map((f) => {
        const label = STAGE_LABEL[f.stage] ?? f.stage;
        return `${label}: ${shortModel(f.from)} → ${shortModel(f.to)}`;
      });
      fields.push({
        title: "⚠️ Usage-Limit Fallback",
        value: truncate(
          `${lines.join("\n")}\nFable quota hit — retried on Opus (separate Max-plan bucket)`,
          MAX_FIELD_VALUE_LENGTH
        ),
        short: false,
      });
    }

    if (!isTerminal) return fields;

    const failedStages = Object.entries(state.stages ?? {}).filter(
      ([, s]) => s?.status === "failed"
    );
    if (failedStages.length > 0) {
      const errorLines = failedStages.map(([name, s]) => {
        const label = STAGE_LABEL[name] ?? name;
        const extracted = formatErrorForDiscord(s?.error);
        const err = extracted ? `: ${extracted}` : "";
        return `❌ **${label}**${err}`;
      });
      fields.push({
        title: "Error Details",
        value: truncate(redactSecrets(errorLines.join("\n")), MAX_FIELD_VALUE_LENGTH),
        short: false,
      });
    }

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

      fields.push({ title: "Cancelled", value: parts.join("\n"), short: false });
    }

    if (state.outcome_type === "budget-ceiling") {
      fields.push({
        title: "Budget Ceiling",
        value: `Spent ${formatCost(run.costUsd)} before hitting limit\nIncrease budget or re-run with higher ceiling`,
        short: false,
      });
    }

    if (state.gate_results && state.gate_results.length > 0) {
      const gateIcons = state.gate_results.map((g) => {
        const icon = g.result === "pass" ? "✅" : "❌";
        const errSummary = g.error_summary ? ` — ${redactSecrets(g.error_summary)}` : "";
        return `${icon} ${g.gate_name}${errSummary}`;
      });
      fields.push({
        title: "Gate Results",
        value: truncate(gateIcons.join("\n"), MAX_FIELD_VALUE_LENGTH),
        short: false,
      });
    }

    // See formatBudgetFieldValue (transport.ts) for why the pre-flight estimate
    // is labeled "Pre-run est." with an accuracy ratio rather than a bare
    // "Est:" figure (#267).
    const meta = state.pipeline_meta;
    if (meta?.budget_ceiling_usd && meta.budget_ceiling_usd > 0) {
      fields.push({
        title: "Budget",
        value: formatBudgetFieldValue(
          run.costUsd,
          meta.budget_ceiling_usd,
          meta.budget_estimate_usd
        ),
        short: true,
      });
    }

    if (meta?.health_score != null) {
      const score = meta.health_score;
      const healthIcon = score >= 90 ? "🟢" : score >= 70 ? "🟡" : "🔴";
      const healthLabel = score >= 90 ? "Excellent" : score >= 70 ? "Good" : "Needs Attention";
      fields.push({
        title: "Pipeline Health",
        value: `${healthIcon} ${score}/100 — ${healthLabel}`,
        short: true,
      });
    }

    const cacheRead = state.tokens?.total_cache_read ?? 0;
    const totalInput = state.tokens?.total_input ?? 0;
    const totalTokens = cacheRead + totalInput;
    if (cacheRead > 0 && totalTokens > 0) {
      const hitPct = ((cacheRead / totalTokens) * 100).toFixed(0);
      fields.push({ title: "Cache", value: `${hitPct}% hit rate`, short: true });
    }

    if (meta?.pr_number) {
      fields.push({ title: "Pull Request", value: `#${meta.pr_number}`, short: true });
    }

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
        title: "Model",
        value: Array.from(models).map(shortModel).join(", "),
        short: true,
      });
    }

    const action = determineAction(state);
    if (action) {
      fields.push({ title: "Recommended Action", value: action, short: false });
    }

    return fields;
  }

  private buildFooter(run: ActiveRun, elapsedMs: number): string {
    const cost = run.costUsd > 0 ? `💰 ${formatCost(run.costUsd)}  ` : "";
    return `${cost}⏱ ${formatDuration(elapsedMs)}`;
  }

  // ─── Config & secret resolution ───────────────────────────────────────────

  private getMattermostConfig(): MattermostNotificationsConfig | null {
    try {
      const result = this.configBridge.getEffectiveConfig();
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      return (result?.config as any)?.notifications?.mattermost ?? null;
    } catch {
      return null;
    }
  }

  private async resolveWebhookUrl(config: MattermostNotificationsConfig): Promise<string | null> {
    const secretService = SecretStorageService.getInstance();
    if (secretService) {
      const stored = await secretService.getSecret(SECRET_KEYS.mattermostWebhookUrl);
      if (stored) return stored;
    }

    if (config.webhook_env) {
      const envUrl = process.env[config.webhook_env];
      if (envUrl) return envUrl;
      this.logger.warn(
        `MattermostService: no webhook URL found in SecretStorage or env var "${config.webhook_env}" — ` +
          'run "Nightgauge: Configure Mattermost Notifications" to set it up.'
      );
    } else {
      this.logger.warn(
        "MattermostService: no webhook URL configured — " +
          'run "Nightgauge: Configure Mattermost Notifications" to set it up.'
      );
    }

    return null;
  }

  // ─── Disposal ─────────────────────────────────────────────────────────────

  dispose(): void {
    this.patcher.dispose();
    for (const subs of this.slotDisposables.values()) {
      for (const s of subs) s.dispose();
    }
    this.slotDisposables.clear();
    this.pendingRepoSlugs.clear();
    this.ephemeralFlags.clear();
    for (const d of this.disposables) d.dispose();
    this.disposables = [];
    this.runs.clear();
  }
}
