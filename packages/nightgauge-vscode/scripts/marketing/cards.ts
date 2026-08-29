/**
 * Marketing capture — Discord and Slack notification cards.
 *
 * Both payloads come from the PRODUCTION builders, not a hand-written mirror:
 *   - Discord: `DiscordService.buildEmbed()` — the exact embed the extension
 *     POSTs to the webhook.
 *   - Slack:   `buildRunAttachment()` + the same mrkdwn translation
 *     `SlackService` applies before `chat.postMessage`.
 *
 * The HTML around each payload reproduces the client chrome (avatar, APP
 * badge, colour bar, field grid, footer) closely enough to compare against
 * a real screenshot of the same run. If the builders change, re-running
 * the capture changes the images — that is the point.
 */

import type {
  PipelineStateSnapshot,
  RunAttachment,
} from "../../src/services/notifications/runAttachment";
import { buildRunAttachment } from "../../src/services/notifications/runAttachment";
import { DiscordService } from "../../src/services/DiscordService";
import { toSlackMrkdwn } from "../../src/services/notifications/SlackService";
import { RUN_338, RUN_338_DURATION_MS, REPO_NAME, REPO_SLUG } from "./run-data";

const silentLogger = {
  info: () => {},
  warn: () => {},
  error: () => {},
  debug: () => {},
  trace: () => {},
} as any;

export function snapshotFor338(): PipelineStateSnapshot {
  const per_stage: Record<string, { cost_usd?: number; model?: string }> = {};
  const stages: NonNullable<PipelineStateSnapshot["stages"]> = {};
  for (const [name, s] of Object.entries(RUN_338.stages)) {
    stages[name] = { status: s.status, duration_ms: s.duration_ms };
    per_stage[name] = { cost_usd: s.cost_usd, model: s.model };
  }
  return {
    issue_number: RUN_338.issue_number,
    title: RUN_338.title,
    branch: RUN_338.branch,
    base_branch: RUN_338.base_branch,
    stages,
    tokens: {
      estimated_cost_usd: RUN_338.tokens.estimated_cost_usd,
      total_cache_read: RUN_338.tokens.total_cache_read,
      total_input: RUN_338.tokens.total_input,
      per_stage,
    },
    outcome_type: RUN_338.outcome_type,
    pr_url: RUN_338.pr_url,
    pipeline_meta: {
      complexity: RUN_338.complexity,
      file_count: RUN_338.file_count,
      budget_estimate_usd: RUN_338.budget_estimate_usd,
      model: RUN_338.model,
      pr_number: RUN_338.pr_number,
      health_score: RUN_338.health_score,
      performance_mode: RUN_338.performance_mode,
    },
  };
}

function runView() {
  return {
    issueNumber: RUN_338.issue_number,
    issueTitle: RUN_338.title,
    branch: RUN_338.branch,
    repoName: REPO_NAME,
    repoSlug: REPO_SLUG,
    startTime: Date.now() - RUN_338_DURATION_MS,
    costUsd: RUN_338.tokens.estimated_cost_usd,
    prUrl: RUN_338.pr_url,
    stageStartTimes: new Map<string, number>(),
  };
}

export interface DiscordEmbedJson {
  title: string;
  url?: string;
  description: string;
  color: number;
  fields: Array<{ name: string; value: string; inline?: boolean }>;
  footer: { text: string };
  timestamp: string;
}

/** The embed exactly as `DiscordService` would POST it for this run. */
export function buildDiscordEmbed(): DiscordEmbedJson {
  const svc = new DiscordService({} as any, {} as any, silentLogger);
  const run = {
    ...runView(),
    webhookId: "0",
    webhookToken: "",
    messageId: "0",
    isFinal: true,
    finalPatchRetries: 0,
  };
  // buildEmbed is private by design (delivery concern); the capture reaches
  // it the same way a unit test would, so the image tracks production.
  return (svc as any).buildEmbed(run, snapshotFor338());
}

/** The legacy attachment exactly as `SlackService` would send it. */
export function buildSlackAttachment(): RunAttachment & { mrkdwn_in: string[] } {
  const att = buildRunAttachment(runView(), snapshotFor338(), {
    logger: silentLogger,
    limits: { maxFieldValueLength: 2000, maxDescriptionLength: 3000, maxFields: 20 },
  });
  return {
    ...att,
    text: att.text ? toSlackMrkdwn(att.text) : att.text,
    fields: att.fields?.map((f) => ({ ...f, value: toSlackMrkdwn(f.value) })),
    mrkdwn_in: ["text", "fields"],
  };
}

// ─── Rendering ──────────────────────────────────────────────────────────────

const SHORTCODES: Record<string, string> = {
  white_check_mark: "✅",
  x: "❌",
  arrows_counterclockwise: "🔄",
  fast_forward: "⏩",
  pause_button: "⏸️",
  hourglass_flowing_sand: "⏳",
};

function esc(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function emoji(s: string): string {
  return s.replace(/:([a-z_]+):/g, (m, code) => SHORTCODES[code] ?? m);
}

/** Discord-flavoured markdown → HTML (the subset the builders emit). */
function discordMd(src: string): string {
  let s = esc(emoji(src));
  s = s.replace(/`([^`]+)`/g, '<code class="inline">$1</code>');
  s = s.replace(/\[([^\]]+)\]\((https?:[^)]+)\)/g, '<a href="$2">$1</a>');
  s = s.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  return s.replace(/\n/g, "<br>");
}

/** Slack mrkdwn → HTML (the subset `toSlackMrkdwn` produces). */
function slackMd(src: string): string {
  let s = esc(emoji(src));
  s = s.replace(/`([^`]+)`/g, '<code class="inline">$1</code>');
  s = s.replace(/&lt;(https?:[^|&]+)\|([^&]+)&gt;/g, '<a href="$1">$2</a>');
  s = s.replace(/(^|[\s(>])\*([^*\n]+)\*/g, "$1<strong>$2</strong>");
  return s.replace(/\n/g, "<br>");
}

function hex(n: number): string {
  return `#${n.toString(16).padStart(6, "0")}`;
}

const TIME_LABEL = "Today at 8:43 AM";

export function renderDiscordHtml(embed: DiscordEmbedJson, avatarDataUri: string): string {
  const fields = embed.fields
    .map(
      (f) => `<div class="field ${f.inline ? "inline" : "wide"}">
        <div class="fname">${discordMd(f.name)}</div>
        <div class="fvalue">${discordMd(f.value)}</div></div>`
    )
    .join("");
  return `<!doctype html><html><head><meta charset="utf-8"><style>
  html,body{margin:0;background:#313338;color:#dbdee1;font-family:"gg sans","Noto Sans",-apple-system,"Helvetica Neue",Helvetica,Arial,sans-serif;font-size:16px;line-height:1.375;-webkit-font-smoothing:antialiased}
  .msg{display:flex;gap:16px;padding:20px 24px 24px 20px;width:1180px;box-sizing:border-box}
  .avatar{width:44px;height:44px;border-radius:50%;flex:none;margin-top:2px}
  .body{flex:1;min-width:0}
  .hdr{display:flex;align-items:center;gap:6px;margin-bottom:4px}
  .hdr .name{font-weight:600;color:#f2f3f5;font-size:16px}
  .badge{background:#5865f2;color:#fff;font-size:10px;font-weight:600;border-radius:3px;padding:1px 5px;line-height:15px;text-transform:uppercase;letter-spacing:.02em;display:inline-flex;align-items:center;gap:3px}
  .hdr .time{color:#949ba4;font-size:12px;margin-left:2px}
  .embed{display:grid;grid-template-columns:auto;max-width:1040px;background:#2b2d31;border-radius:4px;border-left:4px solid ${hex(embed.color)};padding:10px 16px 14px 12px;box-sizing:border-box}
  .title{font-weight:600;color:#f2f3f5;font-size:16px;margin:2px 0 8px}
  .title a{color:#f2f3f5;text-decoration:none}
  .desc{font-size:14px;color:#dbdee1;white-space:normal;margin-bottom:10px}
  .desc a{color:#00a8fc;text-decoration:none;font-weight:600}
  .fields{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:8px 12px}
  .field.wide{grid-column:1 / -1}
  .fname{font-weight:600;font-size:14px;color:#f2f3f5;margin-bottom:2px}
  .fvalue{font-size:14px;color:#dbdee1}
  code.inline{background:#1e1f22;border-radius:4px;padding:2px 5px;font-family:"SF Mono",Menlo,Consolas,monospace;font-size:13px;color:#dbdee1}
  .footer{margin-top:10px;font-size:12px;color:#dbdee1;display:flex;align-items:center;gap:4px}
  .edited{color:#949ba4;font-size:11px;margin:6px 0 0 2px}
</style></head><body><div class="msg">
  <img class="avatar" src="${avatarDataUri}" alt="">
  <div class="body">
    <div class="hdr"><span class="name">Nightgauge</span><span class="badge">✓ App</span><span class="time">${TIME_LABEL.replace("Today at ", "")}</span></div>
    <div class="embed">
      <div class="title">${embed.url ? `<a href="${embed.url}">${discordMd(embed.title)}</a>` : discordMd(embed.title)}</div>
      <div class="desc">${discordMd(embed.description)}</div>
      <div class="fields">${fields}</div>
      <div class="footer">${esc(embed.footer.text)} • ${TIME_LABEL}</div>
    </div>
    <div class="edited">(edited)</div>
  </div></div></body></html>`;
}

export function renderSlackHtml(att: RunAttachment, avatarDataUri: string): string {
  const fields = (att.fields ?? [])
    .map(
      (f) => `<div class="field ${f.short ? "short" : "wide"}">
        <div class="fname">${slackMd(f.title)}</div>
        <div class="fvalue">${slackMd(f.value)}</div></div>`
    )
    .join("");
  return `<!doctype html><html><head><meta charset="utf-8"><style>
  html,body{margin:0;background:#1a1d21;color:#d1d2d3;font-family:"Lato","Slack-Lato",-apple-system,"Helvetica Neue",Helvetica,Arial,sans-serif;font-size:15px;line-height:1.46668;-webkit-font-smoothing:antialiased}
  .msg{display:flex;gap:12px;padding:16px 24px 20px 20px;width:1180px;box-sizing:border-box}
  .avatar{width:40px;height:40px;border-radius:8px;flex:none;margin-top:2px}
  .body{flex:1;min-width:0}
  .hdr{display:flex;align-items:baseline;gap:6px}
  .hdr .name{font-weight:900;color:#f8f8f8;font-size:15px}
  .badge{background:#2f3136;color:#b9bbbe;font-size:10px;font-weight:700;border-radius:2px;padding:1px 4px;line-height:14px}
  .hdr .time{color:#ababad;font-size:12px}
  .fallback{color:#d1d2d3;margin:2px 0 8px}
  .att{display:flex;max-width:1080px}
  .bar{width:4px;border-radius:8px;background:${att.color};flex:none}
  .inner{padding:2px 12px 0 12px;flex:1;min-width:0}
  .title{font-weight:900;color:#f8f8f8;margin:0 0 4px}
  .title a{color:#f8f8f8;text-decoration:none}
  .text{margin-bottom:6px}
  .text a{color:#1d9bd1;text-decoration:none;font-weight:700}
  .fields{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:6px 24px;margin-top:6px}
  .field.wide{grid-column:1 / -1}
  .fname{font-weight:700;color:#f8f8f8}
  .fvalue{color:#d1d2d3}
  code.inline{background:#222529;border:1px solid #3b3f44;border-radius:3px;padding:1px 4px;font-family:"SF Mono",Menlo,Consolas,monospace;font-size:13px;color:#e8912d}
  .footer{margin-top:10px;font-size:12px;color:#ababad}
</style></head><body><div class="msg">
  <img class="avatar" src="${avatarDataUri}" alt="">
  <div class="body">
    <div class="hdr"><span class="name">Nightgauge</span><span class="badge">APP</span><span class="time">${TIME_LABEL.replace("Today at ", "")}</span></div>
    <div class="fallback">${esc(att.fallback ?? "")}</div>
    <div class="att"><div class="bar"></div><div class="inner">
      <div class="title">${att.title_link ? `<a href="${att.title_link}">${slackMd(att.title ?? "")}</a>` : slackMd(att.title ?? "")}</div>
      <div class="text">${slackMd(att.text ?? "")}</div>
      <div class="fields">${fields}</div>
      <div class="footer">${esc(att.footer ?? "")} | ${TIME_LABEL} | Added by Nightgauge</div>
    </div></div>
  </div></div></body></html>`;
}
