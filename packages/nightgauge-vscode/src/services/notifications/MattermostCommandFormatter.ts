/**
 * MattermostCommandFormatter — render slash-command results as
 * Mattermost-compatible markdown responses.
 *
 * Stateless: every helper takes structured input and returns a
 * MattermostResponse. Secrets are redacted via `redactSecrets` from
 * the shared transport module so no notification path can leak
 * sensitive values.
 *
 * @see Issue #3376 — MattermostCommandDispatcher consumes these.
 */

import type { IpcQueueState, PipelineStatus, HealthAnalysis } from "../IpcClientBase";
import type { MattermostCommandType } from "../IpcClientBase";
import { redactSecrets } from "./transport";

/** Response payload shape Mattermost accepts on `response_url`. */
export interface MattermostResponse {
  response_type: "ephemeral" | "in_channel";
  text: string;
}

/** Result of `queue list` rendered as a markdown table. */
export function formatQueueList(state: IpcQueueState | null | undefined): MattermostResponse {
  if (!state || state.items.length === 0) {
    return { response_type: "ephemeral", text: "Queue is empty." };
  }
  const header = "| # | Title | Priority | Status |\n| --- | --- | --- | --- |";
  const rows = state.items
    .map((item) => {
      const title = truncate(item.title ?? "", 60);
      const priority = item.priority != null ? String(item.priority) : "-";
      const status = item.status ?? "-";
      return `| #${item.issueNumber} | ${escapeCell(title)} | ${escapeCell(priority)} | ${escapeCell(status)} |`;
    })
    .join("\n");
  return {
    response_type: "ephemeral",
    text: redactSecrets(`${header}\n${rows}`),
  };
}

/** Result of `status`. */
export function formatStatus(
  status: PipelineStatus | null | undefined,
  issueNumber?: number
): MattermostResponse {
  if (!status) {
    return {
      response_type: "ephemeral",
      text: issueNumber ? `No active pipeline for #${issueNumber}.` : "No active pipeline.",
    };
  }
  const heading = issueNumber ? `**Pipeline #${issueNumber}**` : "**Pipeline**";
  const body = `\`\`\`\nStage: ${status.stage}\n\`\`\``;
  return {
    response_type: "ephemeral",
    text: redactSecrets(`${heading}\n${body}`),
  };
}

/** Result of `run` — immediate ack. */
export function formatRunAck(issueNumber?: number): MattermostResponse {
  const ref = issueNumber ? `#${issueNumber}` : "the next queued item";
  return {
    response_type: "in_channel",
    text: `▶ Starting ${ref}…`,
  };
}

/** Result of `pause`. */
export function formatPause(): MattermostResponse {
  return { response_type: "in_channel", text: "⏸ Pipeline paused." };
}

/** Result of `resume`. */
export function formatResume(): MattermostResponse {
  return { response_type: "in_channel", text: "▶ Pipeline resumed." };
}

/** Result of `stop`. */
export function formatStop(issueNumber?: number | null): MattermostResponse {
  if (!issueNumber) {
    return {
      response_type: "in_channel",
      text: "⏹ No active execution to stop.",
    };
  }
  return {
    response_type: "in_channel",
    text: `⏹ Pipeline stopped for #${issueNumber}.`,
  };
}

/** Result of `queue add`. */
export function formatQueueAdd(issueNumber?: number): MattermostResponse {
  return {
    response_type: "ephemeral",
    text: issueNumber
      ? `✅ Issue #${issueNumber} added to queue.`
      : "Usage: `queue add <issue-number>`",
  };
}

/** Result of `queue remove`. */
export function formatQueueRemove(issueNumber?: number): MattermostResponse {
  return {
    response_type: "ephemeral",
    text: issueNumber
      ? `✅ Issue #${issueNumber} removed from queue.`
      : "Usage: `queue remove <issue-number>`",
  };
}

/** Result of `health`. */
export function formatHealth(analysis: HealthAnalysis | null | undefined): MattermostResponse {
  if (!analysis) {
    return {
      response_type: "in_channel",
      text: "No health data available.",
    };
  }
  const score = analysis.overallScore;
  const badge = score >= 90 ? "🟢" : score >= 70 ? "🟡" : "🔴";
  const dimLines = Object.entries(analysis.dimensions ?? {})
    .map(([name, dim]) => `- **${name}**: ${dim.score}`)
    .join("\n");
  const recs =
    analysis.recommendations && analysis.recommendations.length > 0
      ? `\n\n**Recommendations:**\n${analysis.recommendations
          .slice(0, 5)
          .map((r) => `- ${r}`)
          .join("\n")}`
      : "";
  return {
    response_type: "in_channel",
    text: redactSecrets(`${badge} **Health: ${score}/100**\n${dimLines}${recs}`),
  };
}

/** Reference card for the `help` subcommand. */
export function formatHelp(): MattermostResponse {
  const usage = [
    "**Nightgauge slash commands**",
    "",
    "- `/nightgauge status` — show the active pipeline stage",
    "- `/nightgauge run <issue>` — enqueue an issue for the pipeline",
    "- `/nightgauge pause` — pause the active pipeline",
    "- `/nightgauge resume` — resume the paused pipeline",
    "- `/nightgauge stop [issue]` — stop an active execution",
    "- `/nightgauge queue add <issue>` — enqueue without starting",
    "- `/nightgauge queue remove <issue>` — remove from queue",
    "- `/nightgauge queue list` — list queued issues",
    "- `/nightgauge health` — run health analysis",
    "- `/nightgauge help` — show this reference",
  ].join("\n");
  return { response_type: "ephemeral", text: usage };
}

/** Response for unrecognized commands. */
export function formatUnknown(rawText?: string): MattermostResponse {
  const echo = rawText ? ` (received: \`${escapeCell(truncate(rawText, 100))}\`)` : "";
  return {
    response_type: "ephemeral",
    text: `Unknown command${echo}. Type \`/nightgauge help\` for usage.`,
  };
}

/** Response for errors raised during dispatch. Wraps with redactSecrets. */
export function formatError(err: unknown): MattermostResponse {
  const message = err instanceof Error ? err.message : String(err);
  return {
    response_type: "ephemeral",
    text: redactSecrets(`⚠️ Command failed: ${message}`),
  };
}

/** Response when user lacks write access for a specific command and repo. */
export function formatNotAuthorizedWithDetail(verb: string, repo: string): MattermostResponse {
  return {
    response_type: "ephemeral",
    text: `You are not authorized to ${verb} on ${repo}. Required: write access.`,
  };
}

/** Response when Mattermost user is not in the users: mapping. */
export function formatUnmapped(): MattermostResponse {
  return {
    response_type: "ephemeral",
    text: "Your Mattermost user is not mapped to a GitHub/GitLab identity. Ask an operator to add your mapping to `.nightgauge/config.yaml`. See `docs/MATTERMOST_INTEGRATION.md` for instructions.",
  };
}

// ─── Helpers ────────────────────────────────────────────────────────────────

function truncate(text: string, max: number): string {
  if (text.length <= max) return text;
  return text.slice(0, max - 1) + "…";
}

function escapeCell(text: string): string {
  // Pipes break Mattermost markdown tables; newlines collapse layout.
  return text.replace(/\|/g, "\\|").replace(/\r?\n/g, " ");
}
