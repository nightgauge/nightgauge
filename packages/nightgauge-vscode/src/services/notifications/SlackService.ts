/**
 * SlackService — live-updating pipeline status posts for Slack.
 *
 * Posts a single attachment per pipeline run via `chat.postMessage`, then edits
 * that message in place via `chat.update` as stages progress — the same
 * live-updating single message Discord and Mattermost show, built from the same
 * shared renderer (`runAttachment.ts`) so all three say the same thing.
 *
 * **Why a bot token rather than an incoming webhook.** An incoming webhook
 * answers with the literal body `ok` and no message timestamp, so there is
 * nothing to edit with — a webhook-based notifier can only append messages, and
 * a per-stage append floods the channel. `chat.postMessage` returns the message
 * `ts`, which `chat.update` takes. Slack has also deprecated standalone
 * custom-integration webhooks: an incoming webhook is itself an app feature
 * now, so a workspace has to create an app either way. Given that, the bot
 * token is strictly more capable at the same setup cost, and it is the only
 * path to inbound slash commands later.
 *
 * Configuration (.nightgauge/config.yaml):
 *   notifications:
 *     slack:
 *       enabled: true
 *       bot_token_env: SLACK_BOT_TOKEN
 *       channel: "C0123456789"   # channel id, or "#pipeline"
 *
 * The bot token is preferred from VSCode SecretStorage
 * (SECRET_KEYS.slackBotToken); the env var is the CI fallback. Required scope
 * is `chat:write` (plus `chat:write.public` to post to a channel the bot has
 * not been invited to). The token IS the credential — it is never logged, and
 * every failure path is redacted.
 *
 * Slack reports API-level failures in a 200 response body (`{ok: false, error}`),
 * not in the HTTP status, so this service inspects the body rather than trusting
 * `res.ok` — a status-only check would silently treat every rejection as success.
 * When `chat.postMessage` succeeds but no `ts` comes back, the run degrades to
 * **post-only**: intermediate edits are suppressed and one terminal-state
 * message posts at the end, mirroring MattermostService's fallback.
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
import { CREDENTIAL_ENV_VAR, warnOnLegacyEnvKey } from "./credentials";
import type { Notifier, PipelineEventContext } from "./types";
import { NotifierStatusTracker } from "./NotifierStatusTracker";
import {
  DEBOUNCE_MS,
  DebouncedPatcher,
  FETCH_RETRY_DELAYS,
  redactSecrets,
  retryWithBackoff,
} from "./transport";
import {
  buildRunAttachment,
  type AttachmentLimits,
  type PipelineStateSnapshot,
  type RunAttachment,
} from "./runAttachment";

// ─── Slack API ──────────────────────────────────────────────────────────────

/** Slack Web API base. Only `chat.postMessage` and `chat.update` are used. */
export const SLACK_API_BASE = "https://slack.com/api";

/**
 * Slack truncates attachment `text` and per-field `value` at ~3000 chars (below
 * Mattermost's ~4000). Handed to the shared renderer as this provider's limits.
 */
export const SLACK_LIMITS: AttachmentLimits = {
  maxFieldValueLength: 3000,
  maxDescriptionLength: 3000,
  // 25, matching Discord and Mattermost. Slack's attachment reference states
  // no maximum for the `fields` array, so the previous 20 was an invented cap
  // that could silently drop a field Discord kept for the same run — the exact
  // class of divergence #1127 is about. Discord's 25 IS documented ("fields —
  // Up to 25 field objects"), so it is the binding limit and every provider
  // renders to it.
  maxFields: 25,
};

/**
 * Slack API errors that will never succeed on a retry: the token is wrong, the
 * scope is missing, or the channel is unreachable. Retrying these burns rate
 * limit and delays the honest log line, so they fail fast.
 */
const PERMANENT_SLACK_ERRORS = new Set([
  "invalid_auth",
  "account_inactive",
  "token_revoked",
  "not_authed",
  "missing_scope",
  "channel_not_found",
  "not_in_channel",
  "is_archived",
  "invalid_arguments",
]);

// ─── Interfaces ─────────────────────────────────────────────────────────────

interface SlackNotificationsConfig {
  enabled?: boolean;
  bot_token_env?: string;
  channel?: string;
}

/** Shape of a `chat.postMessage` / `chat.update` response (fields we read). */
interface SlackApiResponse {
  ok: boolean;
  ts?: string;
  error?: string;
}

/**
 * Edit mode for the run.
 *   - "edit"      → live in-place edits via `chat.update`.
 *   - "post-only" → a single terminal-state message, no intermediate edits.
 */
type EditMode = "edit" | "post-only";

interface ActiveRun {
  issueNumber: number;
  issueTitle: string;
  branch: string;
  repoName: string;
  repoSlug?: string;
  botToken: string;
  channel: string;
  /** Slack message timestamp — the edit handle. Empty until the first post. */
  ts: string;
  editMode: EditMode;
  startTime: number;
  costUsd: number;
  prUrl?: string;
  stageStartTimes: Map<string, number>;
  isFinal: boolean;
  /** Cached when `outcome_type` is first seen, so the terminal flush (#1127)
   *  has something to render even if the state source has moved on. */
  finalSnapshot?: PipelineStateSnapshot;
  /** Set once the terminal flush has rendered this run from its final state.
   *  The run entry is retained until then. */
  finalFlushed: boolean;
  stateService?: PipelineStateService;
  /** True after the post-id-missing warning has been logged for this run. */
  fallbackWarned: boolean;
}

// ─── Helpers ────────────────────────────────────────────────────────────────

/**
 * True when `token` looks like a Slack bot token.
 *
 * Slack bot tokens are `xoxb-` prefixed. Checking the prefix catches the most
 * common misconfiguration by far — pasting a webhook URL, a user token
 * (`xoxp-`), or an app-level token (`xapp-`) into the bot-token field — before
 * it becomes a confusing `invalid_auth` at the first pipeline run.
 */
export function isSlackBotToken(token: string): boolean {
  return /^xoxb-\S+$/.test(token.trim());
}

/**
 * Translate the shared renderer's Markdown into Slack's mrkdwn dialect.
 *
 * The renderer emits Discord/Mattermost-flavoured Markdown, which those two
 * providers parse natively. Slack does not: it uses `*bold*` (single asterisk)
 * and `<url|label>` links, so `**bold**` and `[label](url)` reach the channel as
 * literal punctuation. The payload is accepted either way — Slack returns
 * `ok: true` and nothing goes red — so this is a silent rendering defect that
 * only shows up by looking at the channel.
 *
 * Translating here rather than in the renderer keeps Discord and Mattermost
 * output byte-identical: the dialect is this provider's problem.
 *
 * Code spans and fenced blocks are passed through untouched — a `**` or a
 * bracket inside a command or a stack trace is literal text, not markup.
 */
export function toSlackMrkdwn(text: string): string {
  // Split on fenced blocks and inline code spans, translating only the parts
  // outside them. The capture group keeps the delimiters in the output.
  return text
    .split(/(```[\s\S]*?```|`[^`\n]*`)/g)
    .map((chunk, i) => {
      // Odd indices are the captured code spans/fences — leave them alone.
      if (i % 2 === 1) return chunk;
      return (
        chunk
          // [label](url) → <url|label>. Runs before the bold rule so a label
          // like **Title** is unwrapped by the bold rule afterwards.
          .replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, "<$2|$1>")
          // **bold** → *bold*. Non-greedy so two runs on one line stay separate.
          .replace(/\*\*(.+?)\*\*/g, "*$1*")
          // ~~strike~~ → ~strike~
          .replace(/~~(.+?)~~/g, "~$1~")
      );
    })
    .join("");
}

/**
 * Apply the mrkdwn translation across an attachment's human-readable text, and
 * declare `mrkdwn_in` so Slack actually formats those fields — a legacy
 * attachment renders them as plain text without it, which would defeat the
 * translation above.
 */
function toSlackAttachment(att: RunAttachment): RunAttachment & { mrkdwn_in: string[] } {
  return {
    ...att,
    text: att.text ? toSlackMrkdwn(att.text) : att.text,
    fields: att.fields?.map((f) => ({ ...f, value: toSlackMrkdwn(f.value) })),
    mrkdwn_in: ["text", "fields"],
  };
}

// ─── SlackService ───────────────────────────────────────────────────────────

export class SlackService implements Notifier, vscode.Disposable {
  private readonly runs = new Map<number, ActiveRun>();
  private readonly patcher = new DebouncedPatcher();
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
    this.logger.info("SlackService initialized");
  }

  // ─── Notifier interface ───────────────────────────────────────────────────

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
    this.logger.info("SlackService: subscribed to worktree slot", { issueNumber });
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

  dispose(): void {
    for (const d of this.disposables) d.dispose();
    this.disposables = [];
    for (const subs of this.slotDisposables.values()) {
      for (const s of subs) s.dispose();
    }
    this.slotDisposables.clear();
    this.patcher.dispose();
    this.runs.clear();
    this.pendingRepoSlugs.clear();
  }

  // ─── Event handlers ───────────────────────────────────────────────────────

  private async handleStageStart(
    stage: PipelineStage,
    issueNumber: number,
    stateService?: PipelineStateService
  ): Promise<void> {
    const existing = this.runs.get(issueNumber);
    if (existing) {
      existing.stageStartTimes.set(stage, Date.now());
      if (stateService) existing.stateService = stateService;
      this.scheduleUpdate(issueNumber);
      return;
    }
    await this.startRun(issueNumber, stateService ?? this.pipelineStateService, stage);
  }

  private async startRun(
    issueNumber: number,
    stateService: PipelineStateService,
    stage: PipelineStage
  ): Promise<void> {
    const config = this.getSlackConfig();
    if (!config?.enabled) {
      // Not an error, but it is the state an operator who just switched Slack
      // "on" in one place and not the other lands in. Silence here reads as
      // working (#1106), because subscribeToSlot already logged success.
      this.logger.debug("SlackService: notifications.slack.enabled is not true — not posting");
      return;
    }

    const botToken = await this.resolveBotToken(config);
    if (!botToken) return; // resolveBotToken has already explained why.
    const channel = config.channel?.trim();
    if (!channel) {
      this.logger.warn("SlackService: notifications.slack.channel is not set — cannot post");
      return;
    }

    const state = (await stateService.getState()) as unknown as PipelineStateSnapshot | null;
    const repoSlug = this.pendingRepoSlugs.get(issueNumber);
    const run: ActiveRun = {
      issueNumber,
      issueTitle: state?.title ?? `Issue #${issueNumber}`,
      branch: state?.branch ?? "",
      repoName: repoSlug?.split("/")[1] ?? repoSlug ?? "",
      repoSlug,
      botToken,
      channel,
      ts: "",
      editMode: "edit",
      startTime: Date.now(),
      costUsd: state?.tokens?.estimated_cost_usd ?? 0,
      stageStartTimes: new Map([[stage, Date.now()]]),
      isFinal: false,
      finalFlushed: false,
      stateService,
      fallbackWarned: false,
    };
    this.runs.set(issueNumber, run);

    if (!state) return;
    const attachment = toSlackAttachment(buildRunAttachment(run, state, this.renderContext()));
    const res = await this.call("chat.postMessage", run, {
      channel: run.channel,
      text: attachment.fallback ?? `Pipeline #${issueNumber}`,
      attachments: [attachment],
    });

    if (!res?.ok) {
      this.runs.delete(issueNumber);
      return;
    }
    if (res.ts) {
      run.ts = res.ts;
      // Say so. Discord logs "pipeline embed created" with its message id, and
      // Slack logged nothing at all on success — so a working notifier and one
      // that never started were byte-identical in the log, and confirming a run
      // had posted meant going and looking at the channel (#1126). The inert
      // reasons all report themselves (#1106); the working case must too, or
      // silence still means nothing.
      this.logger.info("SlackService: pipeline message posted", {
        issueNumber,
        channel: run.channel,
        ts: res.ts,
      });
    } else {
      // No timestamp came back, so there is nothing to edit. Degrade rather
      // than append a message per stage.
      run.editMode = "post-only";
      run.fallbackWarned = true;
      this.logger.warn(
        "SlackService: chat.postMessage returned no ts — downgrading to post-only mode",
        { issueNumber }
      );
    }
    NotifierStatusTracker.getInstance()?.recordSuccess("slack");
  }

  private async handleStateChanged(state: PipelineStateSnapshot): Promise<void> {
    const run = this.runs.get(state.issue_number);
    if (!run) return;

    if (state.title) run.issueTitle = state.title;
    if (state.branch) run.branch = state.branch;
    const cost = state.tokens?.estimated_cost_usd;
    if (typeof cost === "number") run.costUsd = cost;
    if (state.pr_url) run.prUrl = state.pr_url;

    if (state.outcome_type) {
      // Keep the freshest terminal state for the flush below.
      run.finalSnapshot = state;
      if (!run.isFinal) {
        run.isFinal = true;
        // Terminal state must not sit behind the debounce — cancel any pending
        // edit and edit now, or the run's last word can be lost to dispose().
        this.patcher.cancel(state.issue_number);
        await this.patchMessage(state.issue_number, state);
      }
      // Later terminal writes do not each earn a chat.update — the run's
      // last render is the terminal flush below (#1127).
      return;
    }
    this.scheduleUpdate(state.issue_number);
  }

  /**
   * Terminal flush (#1127) — render the run once more from the state as it
   * finally stands. `outcome_type` is written before the orchestrator's
   * post-run enrichment (the health score), so the edit dispatched on the
   * first outcome sighting is not the run's last word.
   *
   * Idempotent, and a no-op in post-only mode: with no editable message a
   * second render would append a duplicate card rather than correct the first.
   */
  private async handleRunFinalized(state: PipelineStateSnapshot): Promise<void> {
    const run = this.runs.get(state.issue_number);
    if (!run || run.finalFlushed) return;
    if (run.editMode === "post-only") return;

    run.isFinal = true;
    run.finalFlushed = true;
    run.finalSnapshot = state;
    this.patcher.cancel(state.issue_number);
    await this.patchMessage(state.issue_number, state);
  }

  /** Coalesce bursts of stage events into one edit per DEBOUNCE_MS. */
  private scheduleUpdate(issueNumber: number): void {
    const run = this.runs.get(issueNumber);
    if (!run || run.editMode === "post-only") return;
    this.patcher.schedule(issueNumber, () => this.patchMessage(issueNumber), DEBOUNCE_MS);
  }

  // ─── Delivery ─────────────────────────────────────────────────────────────

  /**
   * Edit the run's message in place (or, in post-only mode, post the terminal
   * summary once). Never throws — a notifier must not be able to fail a run.
   */
  private async patchMessage(
    issueNumber: number,
    finalState?: PipelineStateSnapshot
  ): Promise<void> {
    const run = this.runs.get(issueNumber);
    if (!run) return;

    let snapshot = finalState;
    if (!snapshot) {
      const service = run.stateService ?? this.pipelineStateService;
      const state = (await service.getState()) as unknown as PipelineStateSnapshot | null;
      if (!state) return;
      snapshot = state;
    }

    const attachment = toSlackAttachment(buildRunAttachment(run, snapshot, this.renderContext()));
    const text = attachment.fallback ?? `Pipeline #${issueNumber}`;

    // post-only: nothing to edit, so only the terminal summary is worth sending.
    if (run.editMode === "post-only") {
      if (!run.isFinal) return;
      await this.call("chat.postMessage", run, {
        channel: run.channel,
        text,
        attachments: [attachment],
      });
      this.runs.delete(issueNumber);
      return;
    }

    if (!run.ts) return;
    const res = await this.call("chat.update", run, {
      channel: run.channel,
      ts: run.ts,
      text,
      attachments: [attachment],
    });
    if (res?.ok) NotifierStatusTracker.getInstance()?.recordSuccess("slack");
    // Released only after the terminal flush — see handleRunFinalized (#1127).
    if (run.isFinal && run.finalFlushed) this.runs.delete(issueNumber);
  }

  /**
   * Call a Slack Web API method with the run's bot token.
   *
   * Slack signals API failures in a 200 body (`{ok: false, error: "..."}`), so
   * the body is inspected rather than the HTTP status — checking only `res.ok`
   * would report every rejection as a success. Transport and 5xx/429 failures
   * retry on the shared backoff; a permanent API error (bad token, missing
   * scope, unknown channel) fails fast. Returns null on failure.
   */
  private async call(
    method: "chat.postMessage" | "chat.update",
    run: ActiveRun,
    payload: Record<string, unknown>
  ): Promise<SlackApiResponse | null> {
    try {
      const res = await retryWithBackoff(
        async () => {
          const r = await fetch(`${SLACK_API_BASE}/${method}`, {
            method: "POST",
            headers: {
              "Content-Type": "application/json; charset=utf-8",
              Authorization: `Bearer ${run.botToken}`,
            },
            body: JSON.stringify(payload),
          });
          if (!r.ok) throw new Error(`HTTP ${r.status}`);
          return r;
        },
        {
          delays: FETCH_RETRY_DELAYS,
          logger: this.logger,
          label: "SlackService",
          sanitizedUrl: `${SLACK_API_BASE}/${method}`,
        }
      );

      const body = (await res.json()) as SlackApiResponse;
      if (body.ok) return body;

      const error = body.error ?? "unknown_error";
      const permanent = PERMANENT_SLACK_ERRORS.has(error);
      this.logger.warn("SlackService: Slack API rejected the request", {
        issueNumber: run.issueNumber,
        method,
        error,
        permanent,
        hint: permanent ? this.hintFor(error) : undefined,
      });
      NotifierStatusTracker.getInstance()?.recordError("slack", error);
      return null;
    } catch (err) {
      const detail = err instanceof Error ? err.message : String(err);
      this.logger.warn("SlackService: failed to reach the Slack API", {
        issueNumber: run.issueNumber,
        method,
        detail: redactSecrets(detail),
      });
      NotifierStatusTracker.getInstance()?.recordError("slack", redactSecrets(detail));
      return null;
    }
  }

  /** Turn an opaque Slack error code into the action that actually fixes it. */
  private hintFor(error: string): string {
    switch (error) {
      case "missing_scope":
        return "the bot token needs the chat:write scope (chat:write.public to post without being invited)";
      case "not_in_channel":
        return "invite the bot to the channel, or grant chat:write.public";
      case "channel_not_found":
        return "check notifications.slack.channel — use the channel id, and confirm the bot can see it";
      case "invalid_auth":
      case "not_authed":
      case "token_revoked":
      case "account_inactive":
        return "the bot token is invalid or revoked — reconfigure it";
      case "is_archived":
        return "the target channel is archived";
      default:
        return "see Slack's chat.postMessage error reference";
    }
  }

  private renderContext() {
    return { logger: this.logger, limits: SLACK_LIMITS };
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
   * Resolve the bot token: SecretStorage first (the interactive path), env var
   * second (CI). A value that is not a bot token is rejected with a warning
   * rather than sent — pasting a webhook URL or a user token here would
   * otherwise surface as an opaque `invalid_auth` at the first pipeline run.
   */
  private async resolveBotToken(config: SlackNotificationsConfig): Promise<string | null> {
    const secretService = SecretStorageService.getInstance();
    if (secretService) {
      const stored = await secretService.getSecret(SECRET_KEYS.slackBotToken);
      if (stored) return this.validated(stored, "SecretStorage");
    }

    const envName = CREDENTIAL_ENV_VAR.slack;
    const fromEnv = process.env[envName];
    if (fromEnv) return this.validated(fromEnv, `env ${envName}`);

    // Nothing resolved. Say so, and attribute it to a legacy key when there is
    // one — a token pasted into bot_token_env used to fail here in total
    // silence (#1106), which is how a live token sat in plaintext unnoticed.
    const hadLegacy = warnOnLegacyEnvKey(
      this.logger,
      "slack",
      config as unknown as Record<string, unknown>,
      "Nightgauge: Configure Slack Notifications"
    );
    if (!hadLegacy) {
      this.logger.warn(
        `SlackService: no bot token in SecretStorage or ${envName} — ` +
          'run "Nightgauge: Configure Slack Notifications" to set it up. Slack will not post.'
      );
    }
    return null;
  }

  private validated(token: string, source: string): string | null {
    if (!isSlackBotToken(token)) {
      this.logger.warn(
        "SlackService: configured credential is not a Slack bot token (expected an xoxb- token) — ignoring",
        { source }
      );
      return null;
    }
    return token.trim();
  }
}
