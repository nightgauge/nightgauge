/**
 * workflowTreeItems — the vscode.TreeItem rendering for the live workflow tree.
 *
 * One item class per node kind in the folded `run → phase → agent → judge`
 * hierarchy ({@link workflowTreeModel}). Rendering decisions (status dots,
 * per-agent token/cost description, green/red judge badges with rationale
 * tooltips, the fan-out counter and lanes-busy gauge) live here so the provider
 * stays a thin fold-then-render shell.
 *
 * Honesty rules (#3919): for `sdk-fanout` runs the lanes gauge reads the REAL
 * lower bound of busy lanes, costs are labelled estimates, and judges are
 * labelled "gate verification" rather than adversarial judgements; native runs
 * are labelled "research-preview".
 *
 * @see Issue #3919
 */

import * as vscode from "vscode";
import type { WorkflowNodeStatus, WorkflowAgentUsage, WorkflowJudgeVerdict } from "@nightgauge/sdk";
import {
  type FoldedRun,
  type FoldedPhase,
  type FoldedAgent,
  latestJudge,
} from "./workflowTreeModel";

/** Codicon + theme color for each node lifecycle status (the "status dot"). */
function statusIcon(status: WorkflowNodeStatus): vscode.ThemeIcon {
  switch (status) {
    case "running":
      return new vscode.ThemeIcon("loading~spin", new vscode.ThemeColor("charts.blue"));
    case "succeeded":
      return new vscode.ThemeIcon("pass-filled", new vscode.ThemeColor("testing.iconPassed"));
    case "failed":
      return new vscode.ThemeIcon("error", new vscode.ThemeColor("testing.iconFailed"));
    case "skipped":
      return new vscode.ThemeIcon("debug-step-over", new vscode.ThemeColor("testing.iconSkipped"));
    case "cancelled":
      return new vscode.ThemeIcon("circle-slash", new vscode.ThemeColor("testing.iconSkipped"));
    case "pending":
    default:
      return new vscode.ThemeIcon("circle-outline", new vscode.ThemeColor("testing.iconQueued"));
  }
}

/** Compact "$0.0123" / "—" cost label. `estimated` runs prefix a "~". */
function costLabel(usage: WorkflowAgentUsage): string {
  const cost = usage.costUsd > 0 ? `$${usage.costUsd.toFixed(4)}` : "$0";
  return usage.estimated ? `~${cost} est` : cost;
}

/** Compact "12.3k tok" token label from input + output tokens. */
function tokenLabel(usage: WorkflowAgentUsage): string {
  const total = usage.inputTokens + usage.outputTokens;
  if (total >= 1000) return `${(total / 1000).toFixed(1)}k tok`;
  return `${total} tok`;
}

/** Whether a run uses the portable fan-out floor (vs the native offload). */
function isFanout(run: FoldedRun): boolean {
  return run.node.backend === "sdk-fanout";
}

/** Base type for all workflow tree items — every node knows its children. */
export abstract class WorkflowTreeItem extends vscode.TreeItem {
  abstract getChildren(): WorkflowTreeItem[];
}

/**
 * Root run row. Renders the fan-out counter ("7/7 agents, 2 rejected"), the
 * lanes-busy gauge ("N of 16 lanes busy"), and the honesty banner (estimate /
 * research-preview labelling) in its description and tooltip.
 */
export class WorkflowRunTreeItem extends WorkflowTreeItem {
  private readonly children: WorkflowTreeItem[];

  constructor(
    private readonly run: FoldedRun,
    /** Concurrency ceiling for the lanes gauge (16 fan-out / 1 native floor). */
    private readonly concurrencyCeiling: number
  ) {
    const issue = run.node.issueNumber !== undefined ? ` #${run.node.issueNumber}` : "";
    super(run.node.label ?? `Workflow${issue}`, vscode.TreeItemCollapsibleState.Expanded);
    this.id = run.node.nodeId;
    this.contextValue = "workflow.run";
    this.iconPath = statusIcon(run.node.status);
    this.description = this.buildDescription();
    this.tooltip = this.buildTooltip();
    this.children = run.phases.map((p) => new WorkflowPhaseTreeItem(p, run));
  }

  private buildDescription(): string {
    const agg = this.run.aggregate;
    const fanout = `${agg.succeededAgents}/${agg.totalAgents} agents`;
    const rejected = agg.rejectedByJudge > 0 ? `, ${agg.rejectedByJudge} rejected` : "";
    const lanes = `${agg.runningAgents} of ${this.concurrencyCeiling} lanes busy`;
    return `${fanout}${rejected} · ${lanes}`;
  }

  private buildTooltip(): vscode.MarkdownString {
    const md = new vscode.MarkdownString(undefined, true);
    const fanout = isFanout(this.run);
    md.appendMarkdown(`**Backend:** ${this.run.node.backend}`);
    md.appendMarkdown(
      fanout
        ? " (portable fan-out floor — concurrency is the real lower bound)\n\n"
        : " (Claude native dynamic workflows — research-preview)\n\n"
    );
    const agg = this.run.aggregate;
    md.appendMarkdown(`**Agents:** ${agg.succeededAgents} ok / ${agg.failedAgents} failed `);
    md.appendMarkdown(`of ${agg.totalAgents} (${agg.rejectedByJudge} judge-rejected)\n\n`);
    const cost = agg.estimatedCost
      ? `~$${agg.totalCostUsd.toFixed(4)} (estimated)`
      : `$${agg.totalCostUsd.toFixed(4)}`;
    md.appendMarkdown(`**Cost:** ${cost}`);
    return md;
  }

  getChildren(): WorkflowTreeItem[] {
    return this.children;
  }
}

/** A phase row — groups the fanned-out agents under a stage/phase. */
export class WorkflowPhaseTreeItem extends WorkflowTreeItem {
  private readonly children: WorkflowTreeItem[];

  constructor(phase: FoldedPhase, run: FoldedRun) {
    super(phase.node.label ?? phase.node.name, vscode.TreeItemCollapsibleState.Expanded);
    this.id = phase.node.nodeId;
    this.contextValue = "workflow.phase";
    this.iconPath = statusIcon(phase.node.status);
    this.description = `phase ${phase.node.index + 1}/${phase.node.total} · ${phase.agents.length} agent${phase.agents.length === 1 ? "" : "s"}`;
    this.children = phase.agents.map((a) => new WorkflowAgentTreeItem(a, run));
  }

  getChildren(): WorkflowTreeItem[] {
    return this.children;
  }
}

/**
 * An agent row — shows per-agent status dot, token/cost in the description, and
 * (when judged) a green/red judge badge with the rationale in the tooltip.
 */
export class WorkflowAgentTreeItem extends WorkflowTreeItem {
  private readonly children: WorkflowTreeItem[];

  constructor(agent: FoldedAgent, run: FoldedRun) {
    const role = agent.node.role ? `${agent.node.role}: ` : "";
    super(
      `${role}${agent.node.label ?? agent.node.agentId}`,
      agent.judges.length > 0
        ? vscode.TreeItemCollapsibleState.Collapsed
        : vscode.TreeItemCollapsibleState.None
    );
    this.id = agent.node.nodeId;
    this.contextValue = "workflow.agent";
    this.iconPath = statusIcon(agent.node.status);

    const usage = agent.node.usage;
    const verdict = latestJudge(agent)?.verdict;
    const badge = verdict ? ` ${verdictBadge(verdict, isFanout(run))}` : "";
    this.description = `${agent.node.provider} · ${tokenLabel(usage)} · ${costLabel(usage)}${badge}`;
    this.tooltip = this.buildTooltip(agent, run);

    this.children = agent.judges.map((_, i) => new WorkflowJudgeTreeItem(agent, i, run));
  }

  private buildTooltip(agent: FoldedAgent, run: FoldedRun): vscode.MarkdownString {
    const md = new vscode.MarkdownString(undefined, true);
    const u = agent.node.usage;
    md.appendMarkdown(`**${agent.node.agentId}** (${agent.node.provider}`);
    md.appendMarkdown(agent.node.model ? ` · ${agent.node.model})\n\n` : ")\n\n");
    md.appendMarkdown(`Status: ${agent.node.status}`);
    if (agent.node.terminalKind) md.appendMarkdown(` (${agent.node.terminalKind})`);
    md.appendMarkdown("\n\n");
    md.appendMarkdown(`Tokens: in ${u.inputTokens} / out ${u.outputTokens} · `);
    md.appendMarkdown(`cache r ${u.cacheReadTokens} / c ${u.cacheCreationTokens}\n\n`);
    const cost = u.estimated ? `~$${u.costUsd.toFixed(4)} (estimated)` : `$${u.costUsd.toFixed(4)}`;
    md.appendMarkdown(`Cost: ${cost}`);
    const judge = latestJudge(agent);
    if (judge) {
      const kind = isFanout(run) ? "Gate verification" : "Judge";
      md.appendMarkdown(`\n\n---\n\n**${kind}: ${judge.verdict.toUpperCase()}**`);
      if (judge.rationale) md.appendMarkdown(`\n\n${judge.rationale}`);
    }
    return md;
  }

  getChildren(): WorkflowTreeItem[] {
    return this.children;
  }
}

/** A judge verdict leaf — green pass / red fail badge with rationale tooltip. */
export class WorkflowJudgeTreeItem extends WorkflowTreeItem {
  constructor(agent: FoldedAgent, judgeIndex: number, run: FoldedRun) {
    const judge = agent.judges[judgeIndex];
    const kind = isFanout(run) ? "gate verification" : "judge";
    super(`${kind}: ${judge.label ?? judge.judgeId}`, vscode.TreeItemCollapsibleState.None);
    this.id = judge.nodeId;
    this.contextValue = "workflow.judge";
    this.iconPath = verdictIcon(judge.verdict);
    const conf = judge.confidence !== undefined ? ` · ${(judge.confidence * 100).toFixed(0)}%` : "";
    this.description = `${judge.verdict}${conf} · ${judge.provider}`;
    const md = new vscode.MarkdownString(undefined, true);
    md.appendMarkdown(`**${kind}: ${judge.verdict.toUpperCase()}**`);
    if (judge.confidence !== undefined) {
      md.appendMarkdown(` (confidence ${(judge.confidence * 100).toFixed(0)}%)`);
    }
    if (judge.rationale) md.appendMarkdown(`\n\n${judge.rationale}`);
    this.tooltip = md;
  }

  getChildren(): WorkflowTreeItem[] {
    return [];
  }
}

/** Inline text badge for a verdict in an item description. */
function verdictBadge(verdict: WorkflowJudgeVerdict, fanout: boolean): string {
  const label = fanout ? "gate" : "judge";
  switch (verdict) {
    case "pass":
      return `✓ ${label}`;
    case "fail":
      return `✗ ${label}`;
    case "uncertain":
    default:
      return `? ${label}`;
  }
}

/** Green / red / yellow icon for a judge verdict leaf. */
function verdictIcon(verdict: WorkflowJudgeVerdict): vscode.ThemeIcon {
  switch (verdict) {
    case "pass":
      return new vscode.ThemeIcon("pass", new vscode.ThemeColor("testing.iconPassed"));
    case "fail":
      return new vscode.ThemeIcon("error", new vscode.ThemeColor("testing.iconFailed"));
    case "uncertain":
    default:
      return new vscode.ThemeIcon("question", new vscode.ThemeColor("testing.iconQueued"));
  }
}
