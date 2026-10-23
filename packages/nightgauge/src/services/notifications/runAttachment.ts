/**
 * runAttachment — the shared pipeline-run renderer for attachment-based
 * notifiers.
 *
 * Mattermost adopted Slack's legacy attachment format wholesale, so the exact
 * payload `MattermostService` has always built (`fallback` / `color` / `title` /
 * `title_link` / `text` / `fields` / `footer` / `ts`) is also what a Slack
 * incoming webhook accepts. Rather than duplicate ~350 lines of field-building
 * per provider — and let the two drift on what a run "says" — the renderer
 * lives here and both services call it.
 *
 * This module is pure: no network, no VSCode API, no per-provider branching.
 * Providers supply their own character limits via `RenderContext.limits`,
 * because that is the only thing that genuinely differs between them.
 *
 * @see Issue #1071 — Slack notifier
 * @see MattermostService — the original home of this code
 */

import type { PipelineStage } from "@nightgauge/sdk";
import type { Logger } from "../../utils/logger";
import {
  buildErrorDetailsBody,
  countFailedStages,
  outcomeDisplay,
  determineAction,
  modeDisplay,
} from "../DiscordService";
import { getBranchDisplayText } from "../../views/dashboard/DashboardComponents";
import {
  formatBudgetFieldValue,
  formatCostAccuracyValue,
  formatDuration,
  hexColor,
  reconcileRunTotalUsd,
  redactSecrets,
  shortModel,
  shouldRenderBudgetField,
  truncate,
} from "./transport";
import { formatCost } from "../../utils/formatCost";

// ─── Pipeline stages in execution order ─────────────────────────────────────

export const PIPELINE_STAGES: PipelineStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

export const STAGE_LABEL: Record<string, string> = {
  "issue-pickup": "Issue Pickup",
  "feature-planning": "Feature Planning",
  "feature-dev": "Feature Dev",
  "feature-validate": "Feature Validate",
  "pr-create": "PR Create",
  "pr-merge": "PR Merge",
};

// ─── Types ──────────────────────────────────────────────────────────────────

export interface PipelineStateSnapshot {
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
    budget_estimate_source?: string;
    budget_estimate_provider?: string;
    budget_ceiling_usd?: number;
    route?: string;
    skip_stages?: string[];
    model?: string;
    pr_number?: number;
    health_score?: number;
    is_supercharge?: boolean;
    supercharge_model?: string;
    /** Active performance mode — Issue 3009. */
    performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
    /** Fable → Opus graceful downgrades applied this run after a usage/quota
     *  limit (Issue #26). Surfaced so operators see the downgrade in real time. */
    quota_fallbacks?: Array<{ stage: string; from: string; to: string }>;
  };
}

/**
 * The subset of a provider's active-run record the renderer reads. Each service
 * keeps its own richer run type (post ids, edit mode, retry counters); those are
 * delivery concerns and never reach this module.
 */
export interface RunView {
  issueNumber: number;
  issueTitle: string;
  branch: string;
  repoName: string;
  repoSlug?: string;
  startTime: number;
  costUsd: number;
  prUrl?: string;
  stageStartTimes: Map<string, number>;
}

export interface AttachmentField {
  title: string;
  value: string;
  short?: boolean;
}

export interface RunAttachment {
  fallback?: string;
  color: string;
  title?: string;
  title_link?: string;
  text?: string;
  fields?: AttachmentField[];
  footer?: string;
  ts?: number;
}

/**
 * Per-provider character limits. These are the ONLY provider difference the
 * renderer knows about — a provider that truncates at a different length gets
 * correct output without a branch in the rendering code.
 */
export interface AttachmentLimits {
  maxFieldValueLength: number;
  maxDescriptionLength: number;
  maxFields: number;
}

export interface RenderContext {
  logger: Logger;
  limits: AttachmentLimits;
}

// ─── Helpers ────────────────────────────────────────────────────────────────

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

export function buildRunAttachment(
  run: RunView,
  state: PipelineStateSnapshot,
  ctx: RenderContext
): RunAttachment {
  const elapsedMs = Date.now() - run.startTime;
  const { color: colorInt, label: statusLabel } = outcomeDisplay(state.outcome_type, {
    failedStageCount: countFailedStages(state),
    logger: ctx.logger,
  });
  const { icon: modeIcon } = modeDisplay(state.pipeline_meta);
  const modeBadge = modeIcon ? ` ${modeIcon}` : "";
  // Resolved once per render — see DiscordService.buildEmbed (#333 AC1).
  const runTotalUsd = resolveRunTotalUsd(run, state, ctx);

  const description = truncate(buildRunDescription(run, state), ctx.limits.maxDescriptionLength);
  const fields = buildRunFields(run, state, ctx, runTotalUsd).slice(0, ctx.limits.maxFields);
  const footer = buildRunFooter(runTotalUsd, elapsedMs);

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

export function buildRunDescription(run: RunView, state: PipelineStateSnapshot): string {
  const meta = state.pipeline_meta;

  const issueUrl = run.repoSlug
    ? `https://github.com/${run.repoSlug}/issues/${run.issueNumber}`
    : undefined;
  const titleText = issueUrl ? `[**${run.issueTitle}**](${issueUrl})` : `**${run.issueTitle}**`;
  // An undetermined branch (the honest "" sentinel, #448) must not collapse
  // to an empty segment: that leaves a dangling "→ `main`" with nothing on
  // its left once a non-default base branch is present. Render it as the
  // shared prose label instead of a code span — an empty/blank code span
  // reads worse than no backticks at all.
  const branchText = getBranchDisplayText(run.branch);
  const branchDisplay = run.branch.trim() ? `\`${branchText}\`` : branchText;
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
  // Mode is the title badge and nothing else (#333 decision I).
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

export function buildRunFields(
  run: RunView,
  state: PipelineStateSnapshot,
  ctx: RenderContext,
  runTotalUsd: number = resolveRunTotalUsd(run, state, ctx)
): AttachmentField[] {
  const fields: AttachmentField[] = [];
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
      value: truncate(redactSecrets(parts.join("\n")), ctx.limits.maxFieldValueLength),
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
      value: truncate(parts.join("\n"), ctx.limits.maxFieldValueLength),
      short: false,
    });
  }

  // Limits — the mode's name plus its consequences, stated exactly once
  // (#333 decision I). Discord parity: the value leads with the mode label
  // because the title badge is an icon, and Elevated has none.
  const liveMeta = state.pipeline_meta;
  const {
    label: liveModeLabel,
    modelSuffix: liveSuffix,
    ceiling: liveCeiling,
  } = modeDisplay(liveMeta);
  const limitParts: string[] = [
    liveModeLabel,
    liveModeLabel === "Maximum"
      ? `pinned${liveSuffix || ` ${liveCeiling}`}`
      : `up to ${liveCeiling}`,
  ];
  if (liveMeta?.route && liveMeta.route !== "standard") {
    limitParts.push(`route: ${liveMeta.route}`);
  }
  fields.push({
    title: "Limits",
    value: limitParts.join("  ·  "),
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
        ctx.limits.maxFieldValueLength
      ),
      short: false,
    });
  }

  if (!isTerminal) return fields;

  const failedStages = Object.entries(state.stages ?? {}).filter(([, s]) => s?.status === "failed");
  if (failedStages.length > 0) {
    // One shared renderer with Discord (#333 decision H / AC9) — the lead,
    // the collapsed path list, and the trimmed policy prose are the same on
    // both surfaces because they come from the same function.
    fields.push({
      title: "Error Details",
      value: truncate(
        redactSecrets(
          buildErrorDetailsBody(failedStages.map(([name, s]) => [name, s?.error] as const))
        ),
        ctx.limits.maxFieldValueLength
      ),
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
      value: `Spent ${formatCost(runTotalUsd)} before hitting limit\nIncrease budget or re-run with higher ceiling`,
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
      value: truncate(gateIcons.join("\n"), ctx.limits.maxFieldValueLength),
      short: false,
    });
  }

  // Budget only when it says something; estimate-vs-actual gets its own
  // field (#333 decisions F/G) — Discord parity, see DiscordService.
  const meta = state.pipeline_meta;
  const ceilingUsd = meta?.budget_ceiling_usd ?? 0;
  if (shouldRenderBudgetField(runTotalUsd, ceilingUsd, state.outcome_type)) {
    fields.push({
      title: "Budget",
      value: formatBudgetFieldValue(runTotalUsd, ceilingUsd),
      short: true,
    });
  }

  const estimateUsd = meta?.budget_estimate_usd;
  if (meta?.budget_estimate_source === "unpriced") {
    // An unpriced run carries no budget_estimate_usd, so the guard below would
    // simply drop the field — and an absent field reads as "no estimate was
    // made", not "this provider cannot be priced". Say which (#1213).
    fields.push({
      title: "Cost Accuracy",
      value: `unpriced (${meta.budget_estimate_provider ?? "provider"} has no registry rate)`,
      short: true,
    });
  } else if (estimateUsd != null && estimateUsd > 0) {
    fields.push({
      // Plain title — every other Mattermost field title here is plain, and
      // one emoji among them reads as an error rather than an accent.
      title: "Cost Accuracy",
      value: formatCostAccuracyValue(runTotalUsd, estimateUsd),
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

  // `total_input` is COMBINED (raw input + cache reads) by the Go scheduler
  // convention, so it already IS the "billed-as-input without caching"
  // denominator. Mattermost used to re-add cache_read, double-counting it and
  // pinning the display near 50% on every cache-dominated run — the #262
  // defect Discord already fixed; AC11 brings this path to parity.
  const cacheRead = state.tokens?.total_cache_read ?? 0;
  const totalInput = state.tokens?.total_input ?? 0;
  if (cacheRead > 0 && totalInput > 0) {
    const hitPct = (cacheRead / totalInput) * 100;
    if (hitPct > 100) {
      // Suppress, never clamp — see DiscordService (#333 decision C).
      ctx.logger.warn(
        "MattermostService: impossible cache hit rate — suppressing the Cache field",
        { cacheRead, totalInput, hitPct }
      );
    } else {
      fields.push({ title: "Cache", value: `${hitPct.toFixed(0)}% hit rate`, short: true });
    }
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

/**
 * The run total this attachment is allowed to assert (#333 AC1) —
 * `run.costUsd` unless the run's own per-stage costs contradict it.
 */
export function resolveRunTotalUsd(
  run: RunView,
  state: PipelineStateSnapshot,
  ctx: RenderContext
): number {
  return reconcileRunTotalUsd(run.costUsd, state.tokens?.per_stage, ctx.logger);
}

export function buildRunFooter(runTotalUsd: number, elapsedMs: number): string {
  const cost = runTotalUsd > 0 ? `💰 ${formatCost(runTotalUsd)}  ` : "";
  return `${cost}⏱ ${formatDuration(elapsedMs)}`;
}
