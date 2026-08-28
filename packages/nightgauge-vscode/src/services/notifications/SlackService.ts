/**
 * SlackService — pipeline status posts for a Slack channel.
 *
 * Posts one attachment per pipeline run via a Slack incoming webhook, using the
 * shared renderer (`runAttachment.ts`) so a Slack channel shows exactly what a
 * Discord or Mattermost channel shows for the same run.
 *
 * **Incoming webhooks cannot edit a message.** Discord and Mattermost both
 * patch a single post in place as stages progress; Slack's incoming-webhook API
 * has no equivalent (editing needs `chat.update` and a bot token, which is a
 * different auth model and a different feature). So this service does NOT
 * translate every stage transition into a POST — that would flood the channel
 * with a message per stage. Instead:
 *
 *   - `onPipelineStart` posts once, so the channel sees work begin.
 *   - Intermediate state is tracked in memory but not posted.
 *   - The terminal state posts the complete run summary — every stage, cost,
 *     outcome — which is the message an operator actually reads.
 *   - `onPipelineUpdate` posts only when the dispatcher's `NotificationRouter`
 *     routes an event to this notifier. An operator who wants per-stage
 *     granularity opts in with an `events` allowlist on the notifier instance;
 *     the default (no rules) stays at start + terminal.
 *
 * Configuration (.nightgauge/config.yaml):
 *   notifications:
 *     slack:
 *       enabled: true
 *       webhook_env: SLACK_WEBHOOK_URL
 *
 * The webhook URL is preferred from VSCode SecretStorage
 * (SECRET_KEYS.slackWebhookUrl); the env var is the CI fallback. The URL is the
 * credential — it is never logged, and every failure path is redacted.
 *
 * @see Issue #1071
 * @see runAttachment — the shared renderer this and MattermostService share
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import { PipelineStateService } from "../PipelineStateService";
import { ConfigBridge } from "../ConfigBridge";
import { Logger } from "../../utils/logger";
import { SecretStorageService, SECRET_KEYS } from "../SecretStorageService";
import type { Notifier, PipelineEventContext } from "./types";
import { NotifierStatusTracker } from "./NotifierStatusTracker";
import { FETCH_RETRY_DELAYS, redactSecrets, retryWithBackoff } from "./transport";
import {
  buildRunAttachment,
  type AttachmentLimits,
  type PipelineStateSnapshot,
  type RunAttachment,
} from "./runAttachment";

// ─── Slack attachment limits ────────────────────────────────────────────────

/**
 * Slack truncates attachment `text` and per-field `value` at ~3000 chars (below
 * Mattermost's ~4000) and renders at most 20 fields cleanly. Handed to the
 * shared renderer as this provider's limits.
 */
const SLACK_LIMITS: AttachmentLimits = {
  maxFieldValueLength: 3000,
  maxDescriptionLength: 3000,
  maxFields: 20,
};

/** Slack's incoming-webhook host. Anything else is not a Slack webhook. */
export const SLACK_WEBHOOK_HOST = "hooks.slack.com";

// ─── Interfaces ─────────────────────────────────────────────────────────────

interface SlackNotificationsConfig {
  enabled?: boolean;
  webhook_env?: string;
}

interface SlackPostBody {
  attachments: RunAttachment[];
}

interface ActiveRun {
  issueNumber: number;
  issueTitle: string;
  branch: string;
  repoName: string;
  repoSlug?: string;
  webhookUrl: string;
  startTime: number;
  costUsd: number;
  prUrl?: string;
  stageStartTimes: Map<string, number>;
  isFinal: boolean;
  stateService?: PipelineStateService;
  /** True once the terminal summary has been posted, so it posts exactly once. */
  finalPosted: boolean;
}

// ─── Helpers ────────────────────────────────────────────────────────────────

/**
 * True when `url` is a Slack incoming-webhook URL.
 *
 * Host-checked rather than prefix-matched so a look-alike host
 * (`hooks.slack.com.evil.test`) is rejected — a substring check would accept
 * it. A Discord or Mattermost webhook pasted into the Slack field is also
 * rejected here rather than producing a confusing 404 at POST time.
 */
export function isSlackWebhookUrl(url: string): boolean {
  try {
    const parsed = new URL(url.trim());
    return parsed.protocol === "https:" && parsed.hostname === SLACK_WEBHOOK_HOST;
  } catch {
    return false;
  }
}

// ─── SlackService ───────────────────────────────────────────────────────────

export class SlackService implements Notifier, vscode.Disposable {
  private readonly runs = new Map<number, ActiveRun>();
  private readonly slotDisposables = new Map<number, vscode.Disposable[]>();
  private readonly pendingRepoSlugs = new Map<number, string>();

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
      this.pipelineStateService.onStateChanged((state) => {
        if (this.slotDisposables.size > 0) return;
        if (state) void this.handleStateChanged(state as unknown as PipelineStateSnapshot);
      })
    );
    this.logger.info("SlackService initialized");
  }

  // ─── Notifier interface ───────────────────────────────────────────────────

  onPipelineStart(ctx: PipelineEventContext): void {
    void this.handleStageStart(ctx.stage as PipelineStage, ctx.issueNumber);
  }

  /**
   * Router-gated update. Reaching this method means the dispatcher's routing
   * rules allowed the event through for this notifier, so it posts — that is
   * how an operator opts into per-stage granularity.
   */
  onPipelineUpdate(ctx: PipelineEventContext): void {
    if (ctx.state) {
      void this.handleStateChanged(ctx.state as unknown as PipelineStateSnapshot, {
        postIntermediate: true,
      });
      return;
    }
    void this.postCurrentState(ctx.issueNumber);
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
      slotStateService.onStateChanged((state) => {
        if (!state) return;
        const snap = state as unknown as PipelineStateSnapshot;
        if (snap.issue_number !== issueNumber) return;
        void this.handleStateChanged(snap);
      }),
    ];

    this.slotDisposables.set(issueNumber, subs);
    this.logger.info("SlackService: subscribed to worktree slot", { issueNumber });
  }

  unsubscribeFromSlot(issueNumber: number): void {
    const subs = this.slotDisposables.get(issueNumber);
    if (subs) {
      for (const s of subs) s.dispose();
      this.slotDisposables.delete(issueNumber);
    }
  }

  dispose(): void {
    for (const d of this.disposables) d.dispose();
    this.disposables = [];
    for (const subs of this.slotDisposables.values()) {
      for (const s of subs) s.dispose();
    }
    this.slotDisposables.clear();
    this.runs.clear();
    this.pendingRepoSlugs.clear();
  }

  // ─── Event handlers ───────────────────────────────────────────────────────

  private async handleStageStart(
    stage: PipelineStage,
    issueNumber: number,
    stateService?: PipelineStateService
  ): Promise<void> {
    const effectiveStateService = stateService ?? this.pipelineStateService;

    // A stage that is not the run's first only advances in-memory state; the
    // channel hears about it in the terminal summary (or via a routed update).
    const existing = this.runs.get(issueNumber);
    if (existing) {
      existing.stageStartTimes.set(stage, Date.now());
      if (stateService) existing.stateService = stateService;
      return;
    }
    if (stage !== "issue-pickup") {
      // Joined mid-run (e.g. after a restart): still track it so the terminal
      // summary is complete, but do not claim the run "started" now.
      await this.startRun(issueNumber, effectiveStateService, stage, { announce: false });
      return;
    }
    await this.startRun(issueNumber, effectiveStateService, stage, { announce: true });
  }

  private async startRun(
    issueNumber: number,
    stateService: PipelineStateService,
    stage: PipelineStage,
    opts: { announce: boolean }
  ): Promise<void> {
    const config = this.getSlackConfig();
    if (!config?.enabled) return;

    const webhookUrl = await this.resolveWebhookUrl(config);
    if (!webhookUrl) return;

    const state = (await stateService.getState()) as unknown as PipelineStateSnapshot | null;
    const repoSlug = this.pendingRepoSlugs.get(issueNumber);
    const run: ActiveRun = {
      issueNumber,
      issueTitle: state?.title ?? `Issue #${issueNumber}`,
      branch: state?.branch ?? "",
      repoName: repoSlug?.split("/")[1] ?? repoSlug ?? "",
      repoSlug,
      webhookUrl,
      startTime: Date.now(),
      costUsd: state?.tokens?.estimated_cost_usd ?? 0,
      stageStartTimes: new Map([[stage, Date.now()]]),
      isFinal: false,
      stateService,
      finalPosted: false,
    };
    this.runs.set(issueNumber, run);

    if (opts.announce && state) {
      await this.post(run, state);
    }
  }

  private async handleStateChanged(
    state: PipelineStateSnapshot,
    opts: { postIntermediate?: boolean } = {}
  ): Promise<void> {
    const run = this.runs.get(state.issue_number);
    if (!run) return;

    if (state.title) run.issueTitle = state.title;
    if (state.branch) run.branch = state.branch;
    const cost = state.tokens?.estimated_cost_usd;
    if (typeof cost === "number") run.costUsd = cost;
    if (state.pr_url) run.prUrl = state.pr_url;

    if (state.outcome_type) {
      run.isFinal = true;
      if (run.finalPosted) return;
      run.finalPosted = true;
      await this.post(run, state);
      this.runs.delete(state.issue_number);
      return;
    }

    if (opts.postIntermediate) {
      await this.post(run, state);
    }
  }

  private async postCurrentState(issueNumber: number): Promise<void> {
    const run = this.runs.get(issueNumber);
    if (!run) return;
    const service = run.stateService ?? this.pipelineStateService;
    const state = (await service.getState()) as unknown as PipelineStateSnapshot | null;
    if (!state) return;
    await this.handleStateChanged(state, { postIntermediate: true });
  }

  // ─── Delivery ─────────────────────────────────────────────────────────────

  /**
   * POST one attachment to the incoming webhook. Transient failures retry on
   * the shared backoff schedule; a permanent failure is logged (redacted) and
   * never thrown — a notifier must not be able to fail a pipeline run.
   */
  private async post(run: ActiveRun, state: PipelineStateSnapshot): Promise<void> {
    const attachment = buildRunAttachment(run, state, {
      logger: this.logger,
      limits: SLACK_LIMITS,
    });
    const body: SlackPostBody = { attachments: [attachment] };

    try {
      await retryWithBackoff(
        async () => {
          const res = await fetch(run.webhookUrl, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body),
          });
          if (!res.ok) throw new Error(`HTTP ${res.status}`);
          return res;
        },
        {
          delays: FETCH_RETRY_DELAYS,
          logger: this.logger,
          label: "SlackService",
          sanitizedUrl: `https://${SLACK_WEBHOOK_HOST}/services/[redacted]`,
        }
      );
      NotifierStatusTracker.getInstance()?.recordSuccess("slack");
    } catch (err) {
      const detail = err instanceof Error ? err.message : String(err);
      // redactSecrets strips webhook URLs and xox* tokens; the URL is never
      // interpolated into this message in the first place.
      this.logger.warn("SlackService: failed to post pipeline status", {
        issueNumber: run.issueNumber,
        detail: redactSecrets(detail),
      });
      NotifierStatusTracker.getInstance()?.recordError("slack", redactSecrets(detail));
    }
  }

  // ─── Config & secret resolution ───────────────────────────────────────────

  private getSlackConfig(): SlackNotificationsConfig | null {
    try {
      const result = this.configBridge.getEffectiveConfig();
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      return (result?.config as any)?.notifications?.slack ?? null;
    } catch {
      return null;
    }
  }

  /**
   * Resolve the webhook URL: SecretStorage first (the interactive path), env
   * var second (CI). A value that is not a Slack webhook URL is rejected with a
   * warning rather than POSTed to — pasting a Discord webhook here would
   * otherwise send pipeline status to the wrong provider.
   */
  private async resolveWebhookUrl(config: SlackNotificationsConfig): Promise<string | null> {
    const secretService = SecretStorageService.getInstance();
    if (secretService) {
      const stored = await secretService.getSecret(SECRET_KEYS.slackWebhookUrl);
      if (stored) return this.validated(stored, "SecretStorage");
    }
    const envName = config.webhook_env;
    if (envName) {
      const fromEnv = process.env[envName];
      if (fromEnv) return this.validated(fromEnv, `env ${envName}`);
    }
    return null;
  }

  private validated(url: string, source: string): string | null {
    if (!isSlackWebhookUrl(url)) {
      this.logger.warn("SlackService: configured webhook URL is not a Slack webhook — ignoring", {
        source,
        expectedHost: SLACK_WEBHOOK_HOST,
      });
      return null;
    }
    return url.trim();
  }
}
