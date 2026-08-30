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
import type { Notifier, PipelineEventContext } from "./types";
import { NotifierStatusTracker } from "./NotifierStatusTracker";
import { CREDENTIAL_ENV_VAR, warnOnLegacyEnvKey } from "./credentials";
import {
  DEBOUNCE_MS,
  DebouncedPatcher,
  FETCH_RETRY_DELAYS,
  FINAL_PATCH_MAX_RETRIES,
  FINAL_PATCH_RETRY_DELAYS,
  redactSecrets,
  retryWithBackoff,
} from "./transport";
import {
  buildRunAttachment,
  resolveRunTotalUsd,
  type AttachmentLimits,
  type PipelineStateSnapshot,
  type RunAttachment,
} from "./runAttachment";

// ─── Mattermost attachment limits ───────────────────────────────────────────

/**
 * Mattermost truncates attachment.text and per-field value at ~4000 chars.
 * Handed to the shared renderer as this provider's limits — the rendering
 * itself is identical across providers (#1071).
 */
export const MATTERMOST_LIMITS: AttachmentLimits = {
  maxFieldValueLength: 4000,
  maxDescriptionLength: 4000,
  maxFields: 25,
};

// ─── Interfaces ─────────────────────────────────────────────────────────────

interface MattermostNotificationsConfig {
  enabled?: boolean;
  webhook_env?: string;
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
  /** Set once the terminal flush (#1127) has rendered this run from its final
   *  state. The run entry is retained until then. */
  finalFlushed: boolean;
  finalPatchRetries: number;
  editMode: EditMode;
  /** True after we've logged the post-id-missing warning for this run. */
  fallbackWarned: boolean;
  stateService?: PipelineStateService;
}

/**
 * Mattermost's attachment shape is the shared renderer's output verbatim —
 * Mattermost adopted Slack's legacy attachment format, which is why one
 * renderer serves both providers (#1071).
 */
type MattermostAttachment = RunAttachment;

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
      }),
      // The run is terminal AND its final metadata is written (#1127).
      this.pipelineStateService.onRunFinalized((state) => {
        if (this.slotDisposables.size > 0) return;
        if (state) void this.handleRunFinalized(state as unknown as PipelineStateSnapshot);
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

  onPipelineFinal(ctx: PipelineEventContext): void {
    if (!ctx.state) return;
    void this.handleRunFinalized(ctx.state as unknown as PipelineStateSnapshot);
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
      slotStateService.onRunFinalized((state) => {
        if (!state) return;
        const snap = state as unknown as PipelineStateSnapshot;
        if (snap.issue_number !== issueNumber) return;
        void this.handleRunFinalized(snap);
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
    // No further event can reach this run once the slot is gone — flush it
    // rather than strand a terminal card mid-state (#1127).
    const run = this.runs.get(issueNumber);
    if (run?.isFinal && !run.finalFlushed && run.finalSnapshot) {
      void this.handleRunFinalized(run.finalSnapshot);
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

    // #471: was a state-file path getter split back to its directory; same value.
    let repoRoot = effectiveStateService.getRepoRoot();
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
      finalFlushed: false,
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

  /**
   * Terminal flush (#1127) — render the run once more from the state as it
   * finally stands. The orchestrator writes the health score after
   * `outcome_type` is already on the state, so the patch dispatched on the
   * first outcome sighting is not the run's last word.
   *
   * Idempotent, and a no-op in post-only mode: without an editable post a
   * second render would append a duplicate card to the channel rather than
   * correct the first one.
   */
  private async handleRunFinalized(state: PipelineStateSnapshot): Promise<void> {
    const run = this.runs.get(state.issue_number);
    if (!run || run.finalFlushed) return;
    if (run.editMode === "post-only") return;

    run.isFinal = true;
    run.finalFlushed = true;
    run.finalSnapshot = state;
    this.patcher.cancel(state.issue_number);
    await this.patchPost(state.issue_number);
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
        // No terminal flush is coming for a run the queue has moved past.
        run.finalFlushed = true;
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
    // Released only after the terminal flush — see handleRunFinalized (#1127).
    if (run.isFinal && run.finalFlushed) this.runs.delete(issueNumber);
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

  /**
   * Render this run's attachment. Delegates to the shared renderer (#1071) —
   * the payload is identical to what this method built inline before the
   * extraction, and this service's format tests are the proof.
   */
  buildAttachment(run: ActiveRun, state: PipelineStateSnapshot): MattermostAttachment {
    return buildRunAttachment(run, state, this.renderContext());
  }

  private renderContext() {
    return { logger: this.logger, limits: MATTERMOST_LIMITS };
  }

  /**
   * The run total this attachment is allowed to assert (#333 AC1) —
   * `run.costUsd` unless the run's own per-stage costs contradict it.
   */
  private resolveRunTotalUsd(run: ActiveRun, state: PipelineStateSnapshot): number {
    return resolveRunTotalUsd(run, state, this.renderContext());
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

    const envName = CREDENTIAL_ENV_VAR.mattermost;
    const envUrl = process.env[envName];
    if (envUrl) return envUrl;

    const hadLegacy = warnOnLegacyEnvKey(
      this.logger,
      "mattermost",
      config as unknown as Record<string, unknown>,
      "Nightgauge: Configure Mattermost Notifications"
    );
    if (!hadLegacy) {
      this.logger.warn(
        `MattermostService: no webhook URL in SecretStorage or ${envName} — ` +
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
