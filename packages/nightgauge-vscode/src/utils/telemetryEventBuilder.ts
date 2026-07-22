/**
 * telemetryEventBuilder — Pure function that maps pipeline execution data
 * to an AnalyticsEvent payload for the platform analytics API
 * (POST /v1/analytics/events, via IpcClient.platformSubmitAnalytics →
 * platform.submitAnalytics → AnalyticsService.Ingest).
 *
 * No PII, source code filenames, or issue content in the payload — only
 * numerical metrics and categorical labels.
 *
 * The wire body's `data` field name (not `metadata`) is dictated by the
 * OpenAPI `AnalyticsIngestRequest.events[].data` schema
 * (api/generated/ts/platform-api.ts) — this builder's `payload` field maps
 * 1:1 onto it via PlatformSubmitAnalyticsParams.Payload → AnalyticsEvent.Data,
 * so no local field is ever literally named `data`/`metadata` here; verified
 * as part of the platform contract alignment review (no drift found on this
 * hop). `schema_version` is embedded inside `payload` (the endpoint's schema
 * has no top-level envelope field for it) so the platform can evolve how it
 * parses this specific event's shape without an unversioned payload change.
 *
 * @see Issue #1480 - Send pipeline execution telemetry to analytics API
 * @see ../../../../docs/TELEMETRY_PRIVACY.md - Public telemetry contract
 */

import type { AnalyticsEvent } from "../platform/types.js";
import type { PipelineState, PipelineOutcomeType } from "../services/PipelineStateService.js";

/**
 * Schema version for the `pipeline_execution_completed` event payload.
 * Bump when the payload's field set changes in a way readers must branch on.
 */
export const PIPELINE_EXECUTION_EVENT_SCHEMA_VERSION = 1;

/**
 * Input data for building a pipeline execution telemetry event.
 */
export interface PipelineExecutionInput {
  state: PipelineState;
  issueMetadata: {
    issueNumber: number;
    sizeLabel: string | null;
    typeLabel: string | null;
  };
  startedAt: Date;
  completedAt: Date;
}

/**
 * Outcome types that map to "success".
 *
 * Note: `skill-no-op` (Issue #3267) is intentionally NOT in this list. The
 * skill exited 0 but produced no state change — analytics should treat it
 * as a non-success so reliability and pipeline-effectiveness dashboards
 * surface it. The exhaustive switch in `outcome` resolution below relies
 * on this list being authoritative; adding a new "success" outcome means
 * appending here.
 */
const SUCCESS_OUTCOMES: PipelineOutcomeType[] = [
  "success",
  "productive",
  "verify-and-close",
  "already-resolved",
  // The PR shipped despite the budget kill — we treat this as success so
  // analytics, queue auto-start, and failure-rate dashboards stay accurate.
  // See #3108.
  "shipped-but-overbudget",
];

/**
 * Build an AnalyticsEvent from pipeline execution data.
 *
 * Maps PipelineState token/stage/outcome data into a flat payload
 * suitable for POST /v1/analytics/events.
 */
export function buildPipelineExecutionEvent(input: PipelineExecutionInput): AnalyticsEvent {
  const { state, issueMetadata, startedAt, completedAt } = input;

  const durationMs = completedAt.getTime() - startedAt.getTime();

  // Token totals — defensive against missing tokens
  const tokens = state.tokens;
  const totalInput = tokens?.total_input ?? tokens?.input ?? 0;
  const totalOutput = tokens?.total_output ?? tokens?.output ?? 0;
  const totalCacheRead = tokens?.total_cache_read ?? tokens?.cacheRead ?? 0;
  const totalCacheCreation = tokens?.total_cache_creation ?? tokens?.cacheCreation ?? 0;

  // Per-stage token breakdown
  const perStageTokens: Record<string, { input: number; output: number }> = {};
  if (tokens?.per_stage) {
    for (const [stage, usage] of Object.entries(tokens.per_stage)) {
      if (usage) {
        perStageTokens[stage] = {
          input: usage.input ?? 0,
          output: usage.output ?? 0,
        };
      }
    }
  }

  // Count completed stages only
  const stageCount = Object.values(state.stages).filter(
    (s) => s.status === "complete" || s.status === "skipped"
  ).length;

  // Map outcome_type to success/failure
  const outcomeType = state.outcome_type;
  const outcome = outcomeType && SUCCESS_OUTCOMES.includes(outcomeType) ? "success" : "failure";

  // Extract primary model from first completed stage with a model_selection
  const firstModel = Object.values(state.stages).find((s) => s.model_selection)?.model_selection;
  const modelUsed = firstModel?.model ?? null;

  // Backtrack and escalation counts
  const backtracks = state.backtracks?.length ?? state.backtrack_count ?? 0;
  const modelEscalations = state.modelEscalations?.length ?? state.model_escalations?.length ?? 0;

  return {
    eventType: "pipeline_execution_completed",
    payload: {
      schema_version: PIPELINE_EXECUTION_EVENT_SCHEMA_VERSION,
      pipeline_duration_ms: Math.max(0, durationMs),
      total_input_tokens: totalInput,
      total_output_tokens: totalOutput,
      total_cache_read_tokens: totalCacheRead,
      total_cache_creation_tokens: totalCacheCreation,
      per_stage_tokens: perStageTokens,
      outcome,
      stage_count: stageCount,
      issue_complexity: issueMetadata.sizeLabel,
      model_used: modelUsed,
      backtracks,
      model_escalations: modelEscalations,
    },
    timestamp: completedAt.toISOString(),
  };
}
