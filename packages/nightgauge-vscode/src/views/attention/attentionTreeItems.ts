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

/** "repo#issue · stage · $cost" — only the parts the request actually carries. */
export function formatContextLine(context: AttentionContext): string {
  const parts: string[] = [];
  parts.push(context.issue ? `${context.repo}#${context.issue}` : context.repo);
  if (context.stage) parts.push(context.stage);
  if (context.cost_so_far_usd !== undefined && context.cost_so_far_usd > 0) {
    parts.push(`$${context.cost_so_far_usd.toFixed(2)}`);
  }
  return parts.join(" · ");
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

/** Full one-line description: context line + relative age. */
export function formatDescription(request: AttentionRequestView): string {
  const contextLine = formatContextLine(request.context);
  const age = formatRelativeAge(request.created_at);
  return [contextLine, age].filter(Boolean).join(" · ");
}

/**
 * Human-readable consequence text for a declared option, derived entirely
 * client-side from `verb` + `args` (the schema carries no free-text
 * description field — deriving it here avoids any Go/IPC protocol change).
 */
export function describeAttentionOption(option: AttentionOption): string {
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
      return "Takes no action.";
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
    this.contextValue = "attention.request";
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
    const md = new vscode.MarkdownString();
    md.appendMarkdown(`**${this.request.title}**\n\n`);
    if (this.request.body) md.appendMarkdown(`${this.request.body}\n\n`);
    md.appendMarkdown(`_Producer: ${this.request.producer}_`);
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
