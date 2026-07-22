/**
 * Post a detailed diagnostic comment on a GitHub issue when a pipeline fails.
 *
 * The comment includes stage timeline, cost breakdown, error details, backtrack
 * history, model escalations, and actionable recommendations so failures can be
 * diagnosed without digging through logs.
 *
 * @see Issue #2628 post-mortem — cross-repo failures were silent
 */

import { exec } from "child_process";
import { promisify } from "util";
import type { PipelineStage } from "@nightgauge/sdk";
import type { Logger } from "./logger";
import type { PipelineState, BacktrackRecord } from "../services/PipelineStateService";
import type { PipelineRunResult } from "../services/HeadlessOrchestrator";

const execAsync = promisify(exec);

/**
 * Sentinel embedded in the pipeline error message when feature-dev is halted by
 * the architecture-approval gate (Issue #4222). The orchestrator sets it; this
 * module keys off it to render an "awaiting approval" alert (an actionable pause,
 * not a failure) instead of a generic failure report. Keep in sync with the
 * message produced by `HeadlessOrchestrator.verifyArchitectureApproval`.
 */
export const ARCHITECTURE_APPROVAL_REQUIRED_MARKER = "ARCHITECTURE APPROVAL REQUIRED";

/**
 * Sentinel embedded when issue-pickup defers because the issue has an OPEN
 * native `blockedBy` dependency (Issue #231). This is belt-and-suspenders: the
 * primary fix makes the deferral a non-failing run (exit 0 + `signal=deferred`)
 * so `postFailureComment` is never called. If a blocked-dependency case ever
 * does reach this builder, render a "Deferred — Blocked by Dependency" notice
 * (an actionable hold, not a failure) instead of a generic failure report.
 * Mirrors the `[blocked-dependency]` failure-taxonomy marker.
 */
export const BLOCKED_DEPENDENCY_MARKER = "[blocked-dependency]";

/** Stage display order for the timeline */
const STAGE_ORDER: PipelineStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

interface FailureCommentOptions {
  issueNumber: number;
  result: PipelineRunResult;
  state: PipelineState | null;
  /** "owner/repo" for cross-repo items, undefined for same-repo */
  repoOverride?: string;
  /** Working directory for gh CLI (must have .nightgauge/config.yaml) */
  cwd: string;
  logger: Logger;
}

/**
 * Post a diagnostic failure comment on the GitHub issue.
 * Non-blocking: logs errors but never throws.
 */
export async function postFailureComment(opts: FailureCommentOptions): Promise<void> {
  const { issueNumber, result, state, repoOverride, cwd, logger } = opts;

  try {
    const body = buildCommentBody(issueNumber, result, state);
    const repoArgs = repoOverride ? ["--repo", repoOverride] : [];

    // Use gh issue comment to post the diagnostic
    const escapedBody = body.replace(/'/g, "'\\''");
    const cmd = [
      "gh",
      "issue",
      "comment",
      String(issueNumber),
      ...repoArgs,
      "--body",
      `'${escapedBody}'`,
    ].join(" ");

    await execAsync(cmd, { cwd, timeout: 30_000 });

    logger.info("Posted pipeline failure comment on issue", {
      issueNumber,
      repo: repoOverride ?? "default",
    });
  } catch (err) {
    // Non-blocking — never prevent pipeline cleanup
    logger.warn("Failed to post failure comment on issue", {
      issueNumber,
      error: err instanceof Error ? err.message : String(err),
    });
  }
}

function buildCommentBody(
  issueNumber: number,
  result: PipelineRunResult,
  state: PipelineState | null
): string {
  const sections: string[] = [];

  const errMsg = result.error?.message ?? "";
  const isApprovalPause = errMsg.includes(ARCHITECTURE_APPROVAL_REQUIRED_MARKER);
  const isBlockedDependency =
    errMsg.includes(BLOCKED_DEPENDENCY_MARKER) ||
    errMsg.toLowerCase().includes("blocked by open dependency");

  // ── Header ──────────────────────────────────────────────────────────
  // An approval pause is not a failure — feature-dev halted before spending
  // anything, awaiting a human decision. Frame it as an actionable pause.
  if (isApprovalPause) {
    sections.push(
      "## ⏸️ Awaiting Architecture Approval\n",
      "This run **paused before implementation** — a human must approve this high-impact " +
        "decision before feature-dev proceeds. **No development or validation cost was incurred.** " +
        "See **Recommended Actions** below to approve and re-queue.\n"
    );
  } else if (isBlockedDependency) {
    // A blocked-dependency deferral is a controlled hold, not a failure —
    // issue-pickup stopped because the issue has an OPEN native `blockedBy`
    // dependency. It auto-resumes when the blockers close. (Issue #231)
    sections.push(
      "## ⏸️ Deferred — Blocked by Dependency\n",
      "This run **paused before pickup** — the issue has an open `blockedBy` dependency " +
        "(the blocker's PR is not merged). **No development or validation cost was incurred.** " +
        "The pipeline will automatically re-queue this issue when its blockers close " +
        "(`deps-gate promote` sweep, or the autonomous cascade).\n"
    );
  } else {
    sections.push("## \u{1F6A8} Pipeline Failure Report\n");
  }

  // ── Summary table ───────────────────────────────────────────────────
  const failedStage = result.failedStage ?? "unknown";
  const duration = formatDuration(result.totalDurationMs);
  const costUsd = state?.tokens?.estimated_cost_usd;
  const complexity = state?.pipeline_meta?.complexity ?? "unknown";
  const route = state?.pipeline_meta?.route ?? "unknown";
  const budgetEstimate = state?.pipeline_meta?.budget_estimate_usd;

  sections.push(
    "| Field | Value |",
    "|-------|-------|",
    `| **Failed Stage** | \`${failedStage}\` |`,
    `| **Duration** | ${duration} |`,
    `| **Cost** | ${costUsd != null ? `$${costUsd.toFixed(2)}` : "N/A"}${budgetEstimate != null ? ` / $${budgetEstimate.toFixed(2)} estimate` : ""} |`,
    `| **Complexity** | ${complexity} |`,
    `| **Route** | ${route} |`,
    `| **Outcome** | ${result.outcomeType ?? "failure"} |`,
    `| **Budget Exceeded** | ${result.budgetExceeded ? "Yes" : "No"} |`,
    ""
  );

  // ── Error details ───────────────────────────────────────────────────
  const errorMsg = result.error?.message ?? "No error message captured";
  sections.push("### Error", "```", truncate(errorMsg, 1000), "```", "");

  // ── Stage timeline ──────────────────────────────────────────────────
  sections.push("### Stage Timeline", "");
  const stageStates = state?.stages ?? {};
  const perStageTokens = state?.tokens?.per_stage ?? {};

  for (const stage of STAGE_ORDER) {
    const ss = stageStates[stage];
    const stageTokens = perStageTokens[stage];
    const cost = stageTokens?.cost_usd;
    const model = stageTokens?.model;

    let icon: string;
    let suffix = "";

    if (stage === failedStage) {
      icon = "\u274C"; // ❌
      const stageError = ss?.error;
      if (stageError) {
        suffix = ` — ${truncate(stageError, 200)}`;
      }
    } else if (result.completedStages.includes(stage)) {
      icon = "\u2705"; // ✅
    } else if (result.skippedStages.includes(stage)) {
      icon = "\u23ED\uFE0F"; // ⏭️
    } else {
      icon = "\u26AA"; // ⚪ not reached
    }

    const durationStr =
      ss?.startTime && ss?.endTime ? ` (${formatDuration(ss.endTime - ss.startTime)})` : "";
    const costStr = cost != null ? ` · $${cost.toFixed(2)}` : "";
    const modelStr = model ? ` · ${model}` : "";

    sections.push(`- ${icon} **${stage}**${durationStr}${costStr}${modelStr}${suffix}`);
  }
  sections.push("");

  // ── Backtrack history ───────────────────────────────────────────────
  const backtracks = state?.backtracks ?? [];
  if (backtracks.length > 0) {
    sections.push("### Backtrack History", `${backtracks.length} backtrack(s) attempted:`, "");
    for (const bt of backtracks) {
      sections.push(
        `- **${bt.from_stage}** \u2192 **${bt.to_stage}** (attempt ${bt.attempt_number})`,
        `  Signal: \`${bt.signal_type}\` — ${bt.rationale || bt.reason}`
      );
    }
    sections.push("");
  }

  // ── Model escalations ──────────────────────────────────────────────
  const escalations = state?.model_escalations ?? state?.modelEscalations ?? [];
  if (escalations.length > 0) {
    sections.push("### Model Escalations", "");
    for (const esc of escalations) {
      sections.push(`- **${esc.stage}**: ${esc.fromModel} \u2192 ${esc.toModel} — ${esc.reason}`);
    }
    sections.push("");
  }

  // ── Gate results ────────────────────────────────────────────────────
  const gates = state?.gate_results ?? [];
  const failedGates = gates.filter((g) => g.result !== "pass");
  if (failedGates.length > 0) {
    sections.push("### Failed Gates", "");
    for (const gate of failedGates) {
      sections.push(
        `- **${gate.gate_name}**: ${gate.result}${gate.error_summary ? ` — ${gate.error_summary}` : ""}`
      );
    }
    sections.push("");
  }

  // ── Recommended actions ─────────────────────────────────────────────
  sections.push(
    "### Recommended Actions",
    "",
    ...getRecommendations(issueNumber, result, state, backtracks),
    ""
  );

  // ── Footer ──────────────────────────────────────────────────────────
  sections.push(
    "<sub>",
    `Pipeline run at ${new Date().toISOString()} · Issue #${issueNumber}`,
    "This comment was auto-generated by the Nightgauge pipeline.",
    "</sub>"
  );

  return sections.join("\n");
}

/**
 * Generate actionable recommendations based on the failure pattern.
 */
function getRecommendations(
  issueNumber: number,
  result: PipelineRunResult,
  state: PipelineState | null,
  backtracks: BacktrackRecord[]
): string[] {
  const recs: string[] = [];
  const failedStage = result.failedStage;
  const errMessage = result.error?.message ?? "";

  // Blocked-dependency deferral (#231) — a controlled hold, not a failure.
  // Return the requeue guidance directly and skip the generic failure boilerplate.
  if (
    errMessage.includes(BLOCKED_DEPENDENCY_MARKER) ||
    errMessage.toLowerCase().includes("blocked by open dependency")
  ) {
    return [
      "- ⏳ **No action required** — this issue was deferred because it has an open " +
        "`blockedBy` dependency. It will be re-queued automatically when its blockers close.",
      "- 🔎 **Want to unblock sooner?** Land the blocking issue's PR, or run " +
        "`nightgauge deps-gate promote` to re-evaluate deferred items immediately.",
    ];
  }

  // Architecture-approval pause (#4222) — actionable, not a failure. Return the
  // approval instructions directly and skip the generic "development failed"
  // guidance, which would be misleading (feature-dev never ran).
  if (errMessage.includes(ARCHITECTURE_APPROVAL_REQUIRED_MARKER)) {
    return [
      "- ✅ **Approve the architecture** — add the `approved:architecture` label to this issue " +
        `(or write \`.nightgauge/pipeline/approval-${issueNumber}.json\` with ` +
        '`{"approved": true}`), then re-queue. feature-dev implements once approved.',
      "- \u{1F6AB} **Don't want this gate?** Set `pipeline.architecture_approval.enabled: false` in " +
        "`.nightgauge/config.yaml` so green CI + auto-merge decide everything.",
    ];
  }

  // Budget exceeded
  if (result.budgetExceeded) {
    recs.push(
      "- \u{1F4B0} **Budget exceeded** — consider increasing the budget ceiling, simplifying the issue scope, or breaking it into smaller issues."
    );
  }

  // Stage-specific recommendations
  switch (failedStage) {
    case "issue-pickup":
      recs.push(
        "- \u{1F4DD} **Issue pickup failed** — verify the issue exists, has clear acceptance criteria, and the pipeline has the correct repo access. Check for auth/session issues."
      );
      break;
    case "feature-planning":
      recs.push(
        "- \u{1F4CB} **Planning failed** — the issue description may lack sufficient detail for the agent to create a plan. Consider adding more context, technical notes, or file path hints."
      );
      break;
    case "feature-dev":
      recs.push(
        "- \u{1F6E0}\uFE0F **Development failed** — the agent could not implement the feature. Check if the issue scope is too broad, the codebase lacks documentation, or there are build/dependency issues."
      );
      break;
    case "feature-validate":
      recs.push(
        "- \u2705 **Validation failed** — the implementation did not pass tests or acceptance criteria. Review the test output above and consider whether the acceptance criteria are achievable in a single pass."
      );
      break;
    case "pr-create":
      recs.push(
        "- \u{1F4E4} **PR creation failed** — the agent could not push the branch or create the PR. Check for branch protection rules, auth permissions, or missing commits."
      );
      break;
    case "pr-merge":
      recs.push(
        "- \u{1F500} **PR merge failed** — this typically means CI failed or merge conflicts. Check the PR for review feedback or failing checks."
      );
      break;
  }

  // Backtrack patterns
  if (backtracks.length >= 3) {
    recs.push(
      "- \u{1F504} **Excessive backtracks** — the pipeline retried 3+ times, suggesting the issue may be too complex for autonomous completion. Consider manual intervention or breaking it down."
    );
  }

  // Oscillation detection (backtrack ping-pong between same stages)
  const btPairs = backtracks.map((bt) => `${bt.from_stage}->${bt.to_stage}`);
  const uniquePairs = new Set(btPairs);
  if (btPairs.length > uniquePairs.size) {
    recs.push(
      "- \u{1F3AF} **Backtrack oscillation detected** — the pipeline bounced between the same stages repeatedly. The issue likely needs human review to clarify ambiguous requirements."
    );
  }

  // Model escalation hints
  const escalations = state?.model_escalations ?? state?.modelEscalations ?? [];
  if (escalations.length > 0 && escalations.some((e) => e.toModel.includes("opus"))) {
    recs.push(
      "- \u{1F9E0} **Escalated to Opus** — the issue required the most capable model and still failed. This strongly suggests the issue scope needs human review."
    );
  }

  // Cross-repo detection from error
  const errorStr = result.error?.message ?? "";
  if (
    errorStr.includes("not found in workspace") ||
    errorStr.includes("Could not resolve to an Issue")
  ) {
    recs.push(
      "- \u{1F30D} **Cross-repo resolution failed** — the issue was dispatched to a workspace that doesn't contain the target repository. Ensure the correct repo is open in the workspace."
    );
  }

  // Fallback if no specific recommendations
  if (recs.length === 0) {
    recs.push(
      "- Review the error details and stage timeline above to diagnose the root cause.",
      "- If the issue description is unclear, add more context and re-queue.",
      "- If this is a recurring failure, consider filing a pipeline improvement issue."
    );
  }

  return recs;
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = seconds % 60;
  return `${minutes}m ${remainingSeconds}s`;
}

function truncate(str: string, maxLen: number): string {
  if (str.length <= maxLen) return str;
  return str.slice(0, maxLen) + "... (truncated)";
}
