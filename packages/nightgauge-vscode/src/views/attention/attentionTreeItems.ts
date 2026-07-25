/**
 * attentionTreeItems — the vscode.TreeItem rendering for the Action Center
 * (ADR 015 §E, VSCode tree-section mockup).
 *
 * Root children are severity-band group headers ("Blocking" for
 * `blocking_fleet`/`blocking_run`, "Needs a human" for `fyi`), each containing
 * one card per open {@link AttentionRequestView}. Rendering decisions (icon
 * per `kind`, severity color, the one-line context string, relative age, and
 * the option-consequence text used by the quick-pick) live here so the
 * provider stays a thin fetch-then-render shell.
 *
 * @see docs/decisions/015-decision-requests.md — schema + mockup
 * @see Issue #325
 */

import * as vscode from "vscode";
import type {
  AttentionRequestView,
  AttentionOption,
  AttentionContext,
} from "../../services/IpcClientBase";

/** Base type for all Action Center tree items — every node knows its children. */
export abstract class AttentionTreeItem extends vscode.TreeItem {
  abstract getChildren(): AttentionTreeItem[];
}

/** Codicon per DecisionRequest `kind` (ADR 015 §A's closed kind set). */
const KIND_ICONS: Record<AttentionRequestView["kind"], string> = {
  unblock: "unlock",
  approve: "question",
  choose: "list-selection",
  provide_input: "key",
  handoff: "person",
  resume: "debug-continue",
};

/** Severity → theme color for the card icon (blocking states read as errors/warnings). */
function severityColor(severity: AttentionRequestView["severity"]): vscode.ThemeColor | undefined {
  switch (severity) {
    case "blocking_fleet":
      return new vscode.ThemeColor("errorForeground");
    case "blocking_run":
      return new vscode.ThemeColor("problemsWarningIcon.foreground");
    case "fyi":
    default:
      return undefined;
  }
}

/** Icon + severity color for a request's card. */
export function iconForRequest(request: AttentionRequestView): vscode.ThemeIcon {
  return new vscode.ThemeIcon(
    KIND_ICONS[request.kind] ?? "circle-outline",
    severityColor(request.severity)
  );
}

/** "repo#issue · stage · $cost" — only the parts the request actually carries.
 *
 * A repo-scoped card (issue #93) has no issue and no run, so this collapses to
 * the bare repo rather than emitting a placeholder for the fields it lacks:
 * "octocat/acme-web#undefined" is worse than saying nothing. `pr` fills the
 * `#n` slot when there is no issue — forge numbering is shared between issues
 * and PRs, so `owner/name#123` is the correct reference for either. */
export function formatContextLine(context: AttentionContext): string {
  const parts: string[] = [];
  const number = context.issue || context.pr;
  parts.push(number ? `${context.repo}#${number}` : context.repo);
  if (context.stage) parts.push(context.stage);
  if (context.cost_so_far_usd !== undefined && context.cost_so_far_usd > 0) {
    parts.push(`$${context.cost_so_far_usd.toFixed(2)}`);
  }
  return parts.join(" · ");
}

/** True for a card that describes a condition of the repository rather than of
 * a run — no run, no issue, no PR. The default-branch-health card is the
 * canonical case: it explains why EVERY PR in the repo is stuck. */
export function isRepoScoped(context: AttentionContext): boolean {
  return !context.run_id && !context.issue && !context.pr;
}

/** Relative age ("just now" / "4m ago" / "3h ago" / "2d ago") from an RFC3339 timestamp. */
export function formatRelativeAge(iso: string): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";
  const diffMs = Date.now() - then;
  const diffMinutes = Math.floor(diffMs / 60000);
  if (diffMinutes < 1) return "just now";
  if (diffMinutes < 60) return `${diffMinutes}m ago`;
  const diffHours = Math.floor(diffMinutes / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  return `${diffDays}d ago`;
}

/** Full one-line description: context line + relative age + suppression state.
 *
 * "muted" and "seen" are rendered rather than hidden. A muted card is silenced,
 * not resolved — it still belongs in the inbox at its severity, and an operator
 * scanning the list has to be able to tell which entries have already been
 * spoken for without opening each one. */
export function formatDescription(request: AttentionRequestView): string {
  const parts = [formatContextLine(request.context), formatRelativeAge(request.created_at)];
  if (request.lifecycle.muted) {
    parts.push("muted");
  } else if (request.lifecycle.state === "acknowledged") {
    parts.push("seen");
  }
  return parts.filter(Boolean).join(" · ");
}

/**
 * Tree-item `contextValue`, encoding the affordances a card actually has so
 * `view/item/context` when-clauses can be regex-matched against it:
 *
 * - `attention.request`      — the base; every card is resolvable
 * - `attention.request.link` — carries a `context.url` worth opening
 * - a trailing `.muted`      — currently silenced (offer unmute, not mute)
 *
 * Suffixes are appended in a fixed order so the four possible values are
 * enumerable, and the menu clauses need no negation (which VS Code's when-clause
 * grammar handles poorly around `=~`).
 */
export function contextValueFor(request: AttentionRequestView): string {
  let value = "attention.request";
  if (request.context.url) value += ".link";
  if (request.lifecycle.muted) value += ".muted";
  return value;
}

/**
 * Human-readable consequence text for a declared option, derived entirely
 * client-side from `verb` + `args` (the schema carries no free-text
 * description field — deriving it here avoids any Go/IPC protocol change).
 *
 * `request` is optional context: a `noop` on a STANDING condition is not "takes
 * no action" — it clears the card and suppresses re-raising until the condition
 * itself changes, which is a materially different promise from the `noop` a
 * run-scoped card uses to mean "halt, do nothing".
 */
export function describeAttentionOption(
  option: AttentionOption,
  request?: AttentionRequestView
): string {
  const args = option.args ?? {};
  const str = (key: string): string | undefined =>
    typeof args[key] === "string" && (args[key] as string).length > 0
      ? (args[key] as string)
      : undefined;
  const num = (key: string): number | undefined =>
    typeof args[key] === "number" ? (args[key] as number) : undefined;

  switch (option.verb) {
    case "queue.add": {
      const title = str("title");
      return title
        ? `Adds this issue back to the queue — "${title}".`
        : "Adds this issue back to the pipeline queue.";
    }
    case "issue.removeBlockedBy":
      return "Removes the stale blockedBy edge so the issue can proceed.";
    case "autonomous.resume":
      return "Resumes the autonomous scheduler.";
    case "autonomous.rescan":
      return "Triggers an immediate rescan of the board.";
    case "autonomous.complete":
      return str("then") === "issue.close"
        ? "Marks the issue complete and closes it."
        : "Marks the issue complete.";
    case "autonomous.clearIssueFailures":
      return str("then") === "autonomous.rescan"
        ? "Clears the failure cooldown and triggers a rescan."
        : "Clears the failure cooldown for this issue.";
    case "budget.raiseCeiling": {
      const ceiling = num("ceilingUsd");
      return ceiling !== undefined
        ? `Raises the budget ceiling to $${ceiling.toFixed(2)} and retries.`
        : "Raises the budget ceiling and retries.";
    }
    case "run.retryWithEscalation": {
      const tier = str("tier") ?? "a stronger model";
      return `Retries with the model escalated to ${tier}.`;
    }
    case "issue.close":
      return "Closes the issue.";
    case "project.syncStatus": {
      const status = str("status") ?? "a new status";
      return `Moves the board status to "${status}".`;
    }
    case "noop":
      return request?.standing
        ? "Clears the card without changing anything. It returns if the condition changes."
        : "Takes no action.";
    default:
      return option.style === "danger"
        ? "Applies this action (not reversible)."
        : "Applies this action.";
  }
}

/** Rank used to order the "Blocking" band: fleet-wide stops surface first. */
function severityRank(severity: AttentionRequestView["severity"]): number {
  switch (severity) {
    case "blocking_fleet":
      return 0;
    case "blocking_run":
      return 1;
    case "fyi":
    default:
      return 2;
  }
}

/** Severity-desc, then newest-first — matches the store's own list ordering. */
export function compareRequests(a: AttentionRequestView, b: AttentionRequestView): number {
  const rankDiff = severityRank(a.severity) - severityRank(b.severity);
  if (rankDiff !== 0) return rankDiff;
  return new Date(b.created_at).getTime() - new Date(a.created_at).getTime();
}

/** One DecisionRequest card. Clicking it (or its inline icon) opens the resolve quick-pick. */
export class AttentionRequestTreeItem extends AttentionTreeItem {
  constructor(readonly request: AttentionRequestView) {
    super(request.title, vscode.TreeItemCollapsibleState.None);
    this.description = formatDescription(request);
    this.iconPath = iconForRequest(request);
    this.contextValue = contextValueFor(request);
    this.tooltip = this.buildTooltip();
    this.command = {
      command: "nightgauge.attentionResolve",
      title: "Resolve",
      arguments: [this],
    };
  }

  getChildren(): AttentionTreeItem[] {
    return [];
  }

  private buildTooltip(): vscode.MarkdownString {
    const { title, body, context, producer, lifecycle } = this.request;
    const md = new vscode.MarkdownString();
    // The URL is rendered as a real markdown link, not prose: for a card whose
    // only option is "dismiss", following it is the operator's actual next
    // action, and a link they have to retype is not an affordance.
    md.isTrusted = false;
    md.appendMarkdown(`**${title}**\n\n`);
    if (body) md.appendMarkdown(`${body}\n\n`);
    if (context.url) md.appendMarkdown(`[Open in browser](${context.url})\n\n`);
    if (lifecycle.muted) {
      md.appendMarkdown(
        `_Muted by ${lifecycle.muted.actor || "an operator"} — re-alerts if the condition changes._\n\n`
      );
    }
    md.appendMarkdown(`_Producer: ${producer}_`);
    return md;
  }
}

/** Severity-band group header ("Blocking" / "Needs a human") from the mockup. */
export class AttentionGroupTreeItem extends AttentionTreeItem {
  constructor(
    label: string,
    private readonly requests: AttentionRequestView[]
  ) {
    super(`${label} (${requests.length})`, vscode.TreeItemCollapsibleState.Expanded);
    this.contextValue = "attention.group";
  }

  getChildren(): AttentionTreeItem[] {
    return this.requests.map((r) => new AttentionRequestTreeItem(r));
  }
}
