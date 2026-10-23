/**
 * LocalTelemetryUsageProvider — adapter usage derived from telemetry
 * nightgauge already persists (Issue #658).
 *
 * The first `UsageProvider`, and the only one that needs no provider quota
 * API: it reads the per-stage token/cost records written to
 * `pipelineStateDir(root)/history/YYYY-MM-DD.jsonl` and buckets the dollars
 * attributed to one adapter into session / daily / monthly windows. For a model
 * that runs on the operator's own server it buckets tokens instead, under the
 * `local` plan (Issue #1665, ADR-022 § 4).
 *
 * ## Why this does not reuse `DashboardState.getAggregates()`
 *
 * #658's technical notes suggested reusing it as the reducer, and told us to
 * stop and document if the observed API differs. It does, in two ways that
 * matter:
 *
 * 1. **No adapter dimension.** `getAggregates()` reduces over
 *    `PipelineRunSummary`, which has no adapter field at all — adapter
 *    identity lives only on `tokens.per_stage[*].adapter` in the raw history
 *    record. An adapter-aware figure cannot be recovered from its output.
 * 2. **No calendar windows.** It exposes all-time and "session" totals only.
 *
 * So the dollar reduction happens here, over raw records. What we *do* reuse
 * is the dashboard's notion of where a session starts — injected as
 * `UsageSessionClock`, which `DashboardState` satisfies structurally via
 * `getSessionStartTime()` — so the two surfaces agree on the boundary instead
 * of inventing a second one. We also bucket on the run's `started_at`, the
 * same field `DashboardState.computeRecentActivityDelta` buckets on.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 * @see Issue #658 - Provider-neutral adapter usage model
 */

import {
  getModelDescriptor,
  isLocalModel,
  isLocalProvider,
  type LocalEndpoint,
  parseOpenCodeModel,
  providerFor,
} from "@nightgauge/sdk";
import type { ExecutionAdapter } from "../../config/schema";
import type {
  ExecutionHistoryRecord,
  HistoryStageDetail,
  HistoryStageTokenUsage,
} from "../../schemas/executionHistory";
import { ExecutionHistoryReader } from "../../utils/executionHistoryReader";
import { getOpenCodeEndpoints, getOpenCodeModel } from "../../utils/resolvers/modelResolver";
import { getLimitsSettings } from "../../config/limitsSettings";
import type {
  UsageConfidence,
  UsagePlanKind,
  UsageProvider,
  UsageSnapshot,
  UsageUnit,
  UsageWindow,
} from "./types";

const DAY_MS = 24 * 60 * 60 * 1000;

/**
 * Adapters this provider will describe.
 *
 * Deliberately not "all of them". Each adapter here has a meter local
 * telemetry can fill honestly:
 *
 * - `claude`, `codex`, `gemini`, `gemini-sdk`, `grok` bill per token, so their
 *   windows are dollars (`pay-per-token`).
 * - `opencode` is multi-provider (ADR-022): the provider of its configured
 *   model decides the plan. A local provider is `local` (tokens); a hosted
 *   one is `pay-per-token` (dollars) over that provider's stages only. See
 *   `LocalTelemetryUsageProvider.meteringFor`.
 *
 * `copilot` is not here: it is a flat seat subscription whose real meter is
 * premium requests per month, a number nothing in nightgauge's telemetry
 * records. The registry resolves no provider for it and the snapshot is
 * `plan.kind: "unknown"` with no windows, which is the honest answer.
 */
export const LOCAL_TELEMETRY_METERED_ADAPTERS: readonly ExecutionAdapter[] = [
  "claude",
  "codex",
  "gemini",
  "gemini-sdk",
  "grok",
  "opencode",
];

/**
 * Supplies the configured `opencode.model` (ADR-022 § 7), or `undefined` when
 * none is configured. `forWorkspace()` reads it from the workspace config.
 */
export type ConfiguredOpenCodeModel = () => string | undefined;

/**
 * Supplies the endpoints the machine-tier `opencode.endpoints[]` declares
 * (#1678). Locality follows them, not the provider-key brand (#2128).
 */
export type DeclaredOpenCodeEndpoints = () => readonly LocalEndpoint[];

/**
 * What one adapter's snapshot measures, and which of its stages count.
 *
 * `provider` narrows a `pay-per-token` snapshot to one provider's stages: an
 * `opencode` adapter can have run a local model and a hosted one in the same
 * month, and a dollar window that mixed them would describe neither.
 */
type Metering = { plan: "local" } | { plan: "pay-per-token"; provider?: string };

/**
 * Where history records come from.
 *
 * A port rather than a direct `ExecutionHistoryReader` call so the reduction
 * can be tested against exact records; `forWorkspace()` supplies the real
 * reader.
 */
export interface UsageHistorySource {
  readDateRange(startDate: Date, endDate: Date): Promise<ExecutionHistoryRecord[]>;
}

/**
 * Where the "session" window starts. `DashboardState` satisfies this.
 *
 * Required, not optional: falling back to local midnight would make the
 * session window a duplicate of the daily one, which is a window that looks
 * like it measures something it does not.
 */
export interface UsageSessionClock {
  getSessionStartTime(): Date;
}

/** One stage attributed to the adapter under inspection. */
interface StageEvent {
  at: Date;
  costUsd: number;
  /** Every token the stage's model processed: input, output, cache read and cache write. */
  tokens: number;
  /** How far `costUsd` can be trusted. Token counts are always `measured`. */
  confidence: UsageConfidence;
  /** The provider that served the stage (`lm-studio`, `anthropic`, `other`, ...). */
  provider: string;
  /** Whether a server the operator runs served the stage (#2128). */
  local: boolean;
}

/**
 * The provider of a recorded `opencode` model when the stage carries no
 * recorded `model_provider`. A record holds the model in one of three forms
 * (ADR-022 § 1, § 2): the `-m` value (`lmstudio/qwen/qwen3.8-27b`), the wire
 * form of an unregistered model (`lm-studio/qwen/qwen3.8-27b`), or the bare id
 * of a registry model (`claude-sonnet-5`). An unrecognized form is `other`,
 * which is never local and never matches a hosted provider.
 */
function recordedOpenCodeProvider(model: string): string {
  const parsed = parseOpenCodeModel(model);
  if (parsed.provider !== "other") {
    return parsed.provider;
  }
  const slash = model.indexOf("/");
  if (slash > 0) {
    const key = model.slice(0, slash);
    return isLocalProvider(key) ? key : "other";
  }
  return getModelDescriptor(model)?.provider ?? "other";
}

/**
 * The provider that served one stage. For `opencode` the recorded
 * `model_provider` (ADR-022 § 2) wins, because it names the model that
 * actually served the stage; the model string is the fallback. Every other
 * adapter serves one provider.
 */
function stageProvider(
  adapter: ExecutionAdapter,
  usage: HistoryStageTokenUsage,
  detail: HistoryStageDetail | undefined
): string {
  if (adapter !== "opencode") {
    return providerFor(adapter, usage.model ?? "");
  }
  const recorded = detail?.model_selection?.model_provider;
  if (recorded) {
    return recorded;
  }
  const model = usage.model ?? detail?.model_selection?.model;
  return model ? recordedOpenCodeProvider(model) : "other";
}

/**
 * How much a single stage's `cost_usd` can be trusted.
 *
 * `cost_unstamped` is checked first because it is the field the Go history
 * writer actually maintains for this purpose: true means `cost_usd` is a
 * placeholder zero because the (provider, model) pair missed the pricing
 * registry. It is never set for a legitimately free local-provider run, so
 * its absence on a zero-cost stage means the zero is real.
 *
 * `cost_source` is honoured when present: `"native"` is a vendor/CLI-reported
 * measurement, `"computed"` is a rate-card estimate, `"unknown"` is an
 * explicit "could not price this at all". Absent (Issue #682) means one of
 * two things: a pre-#682 record, or a Go write path that has not been taught
 * to set the field yet (RecordTerminatingStageTokens's failure-path
 * synthesis — see its doc comment in internal/state/runtime_state.go) — and
 * this function cannot tell those apart. Until #682 the READER backfilled
 * `"native"` for any absent field with `cost_usd > 0`; that manufactured a
 * confident answer from silence, which is exactly the bug #682 fixes. Absent
 * now honestly reports `"unknown"`, the same as the writer's own explicit
 * `"unknown"` label — silence and an explicit "I don't know" carry the same
 * weight here.
 */
export function stageCostConfidence(usage: HistoryStageTokenUsage): UsageConfidence {
  if (usage.cost_unstamped === true) {
    return "unknown";
  }
  switch (usage.cost_source) {
    case "native":
      return "measured";
    // A stage that dispatched no model (Issue #890) cost exactly $0 — that is
    // a measurement, not an absence of one. Folding it as "unknown" (the
    // default arm it used to fall into) would have dragged every window
    // containing a bookend stage down to unknown confidence.
    case "deterministic":
      return "measured";
    case "computed":
      return "estimated";
    case "unknown":
    default:
      return "unknown";
  }
}

/**
 * Fold per-event confidence into a window's confidence: the weakest input
 * decides. A window with no contributing events is `"measured"` — a measured
 * zero is a real answer ("you have not run this adapter today"), distinct
 * from having nothing to say at all, which is signalled by returning no
 * snapshot.
 */
function foldConfidence(events: readonly StageEvent[]): UsageConfidence {
  let seenEstimated = false;
  for (const event of events) {
    if (event.confidence === "unknown") {
      return "unknown";
    }
    if (event.confidence === "estimated") {
      seenEstimated = true;
    }
  }
  return seenEstimated ? "estimated" : "measured";
}

function startOfLocalDay(now: Date): Date {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate());
}

function startOfNextLocalDay(now: Date): Date {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1);
}

function startOfLocalMonth(now: Date): Date {
  return new Date(now.getFullYear(), now.getMonth(), 1);
}

function startOfNextLocalMonth(now: Date): Date {
  return new Date(now.getFullYear(), now.getMonth() + 1, 1);
}

/**
 * Pull every stage attributed to `adapter` out of the records.
 *
 * Stages whose `adapter` field is absent are skipped, never defaulted: the
 * history schema is explicit that absence means adapter-unknown, and guessing
 * would credit one adapter with another's spend.
 */
function collectStageEvents(
  records: readonly ExecutionHistoryRecord[],
  adapter: ExecutionAdapter,
  endpoints: readonly LocalEndpoint[] = []
): StageEvent[] {
  const events: StageEvent[] = [];
  for (const record of records) {
    if (record.record_type !== "run") {
      continue;
    }
    const perStage = record.tokens.per_stage;
    if (!perStage) {
      continue;
    }
    const at = new Date(record.started_at);
    if (Number.isNaN(at.getTime())) {
      continue;
    }
    for (const [stage, usage] of Object.entries(perStage)) {
      if (!usage || usage.adapter !== adapter) {
        continue;
      }
      const provider = stageProvider(adapter, usage, record.stages[stage]);
      const model = usage.model ?? record.stages[stage]?.model_selection?.model ?? "";
      events.push({
        at,
        costUsd: usage.cost_usd,
        tokens: usage.input + usage.output + usage.cache_read + usage.cache_creation,
        confidence: stageCostConfidence(usage),
        provider,
        local: isLocalProvider(provider) || isLocalModel(adapter, model, endpoints),
      });
    }
  }
  return events;
}

/**
 * Whether a stage belongs in the snapshot `metering` describes. A `local`
 * snapshot counts only stages a local provider served; a `pay-per-token`
 * snapshot scoped to a provider counts only that provider's stages.
 */
function isMetered(event: StageEvent, metering: Metering): boolean {
  if (metering.plan === "local") {
    return event.local;
  }
  return metering.provider === undefined || event.provider === metering.provider;
}

function buildWindow(
  id: string,
  label: string,
  scope: UsageWindow["scope"],
  unit: Extract<UsageUnit, "usd" | "tokens">,
  events: readonly StageEvent[],
  since: Date,
  limit: number | null,
  resetsAt: Date | null
): UsageWindow {
  const inWindow = events.filter((event) => event.at.getTime() >= since.getTime());
  if (unit === "tokens") {
    // A token count is what the model server reported, not a price derived
    // from it, so an unpriced stage does not weaken it.
    return {
      id,
      label,
      scope,
      used: inWindow.reduce((sum, event) => sum + event.tokens, 0),
      limit,
      unit,
      resetsAt,
      confidence: "measured",
    };
  }
  return {
    id,
    label,
    scope,
    used: inWindow.reduce((sum, event) => sum + event.costUsd, 0),
    limit,
    unit,
    resetsAt,
    confidence: foldConfidence(inWindow),
  };
}

export class LocalTelemetryUsageProvider implements UsageProvider {
  readonly id = "local-telemetry";

  constructor(
    private readonly source: UsageHistorySource,
    private readonly sessionClock: UsageSessionClock,
    private readonly configuredOpenCodeModel: ConfiguredOpenCodeModel = () => undefined,
    private readonly declaredOpenCodeEndpoints: DeclaredOpenCodeEndpoints = () => []
  ) {}

  /** Wire the provider to a workspace's on-disk history and config. */
  static forWorkspace(
    workspaceRoot: string,
    sessionClock: UsageSessionClock
  ): LocalTelemetryUsageProvider {
    return new LocalTelemetryUsageProvider(
      {
        readDateRange: (startDate, endDate) =>
          ExecutionHistoryReader.readDateRange(workspaceRoot, startDate, endDate),
      },
      sessionClock,
      () => getOpenCodeModel(workspaceRoot),
      () => getOpenCodeEndpoints(workspaceRoot)
    );
  }

  supports(adapter: ExecutionAdapter): boolean {
    return LOCAL_TELEMETRY_METERED_ADAPTERS.includes(adapter);
  }

  /**
   * What `adapter`'s snapshot measures, or `null` when nothing can be said.
   *
   * A single-provider adapter's provider decides: every one is
   * `pay-per-token`. For `opencode` the provider of the
   * configured model decides (ADR-022 § 1): a local provider is `local`, a
   * hosted one is `pay-per-token` over that provider's stages. With no
   * configured model, or one whose provider is `other`, there is no provider
   * to describe, and the answer is `unknown`.
   */
  private meteringFor(adapter: ExecutionAdapter): Metering | null {
    if (adapter !== "opencode") {
      return isLocalProvider(providerFor(adapter, ""))
        ? { plan: "local" }
        : { plan: "pay-per-token" };
    }
    const configured = this.configuredOpenCodeModel();
    if (!configured) {
      return null;
    }
    const provider = providerFor(adapter, configured);
    if (isLocalModel(adapter, configured, this.declaredOpenCodeEndpoints())) {
      return { plan: "local" };
    }
    return provider === "other" ? null : { plan: "pay-per-token", provider };
  }

  /**
   * Derive session / daily / monthly windows for `adapter`: dollars for a
   * `pay-per-token` snapshot, tokens with no limit for a `local` one.
   *
   * Returns `null` — leaving the caller to emit the unknown snapshot — when
   * the adapter is not metered here, when its metering cannot be resolved,
   * or when no record in the read horizon attributes a single metered stage
   * to it. The second case is not the same
   * as "$0 spent": with no attributed record we cannot tell a fresh install
   * from a quiet month, and drawing a 0% bar for either would be a claim we
   * cannot support.
   */
  async getSnapshot(adapter: ExecutionAdapter): Promise<UsageSnapshot | null> {
    if (!this.supports(adapter)) {
      return null;
    }
    const metering = this.meteringFor(adapter);
    if (metering === null) {
      return null;
    }

    const now = new Date();
    const sessionStart = this.sessionClock.getSessionStartTime();
    const dayStart = startOfLocalDay(now);
    const monthStart = startOfLocalMonth(now);

    // Read from the earliest window boundary we will need. No 7/30-day
    // constant is introduced: every boundary here is calendar-derived from
    // the windows #658 asks for. The one day of padding on each end absorbs
    // the skew between the writer's local-calendar filenames and the reader's
    // UTC day iteration; records outside a window are filtered by timestamp
    // regardless.
    const horizonStart = new Date(Math.min(sessionStart.getTime(), monthStart.getTime()) - DAY_MS);
    const horizonEnd = new Date(now.getTime() + DAY_MS);

    const records = await this.source.readDateRange(horizonStart, horizonEnd);
    const attributed = collectStageEvents(records, adapter, this.declaredOpenCodeEndpoints());
    const events = attributed.filter((event) => isMetered(event, metering));
    if (events.length === 0) {
      return null;
    }
    // A local snapshot counts tokens, so a hosted stage's dollars have no
    // window to go in. Carry them instead of dropping them silently. The
    // mirror case, a hosted snapshot leaving out local stages, drops only
    // stages no provider bills, and needs no note.
    const excludedHosted =
      metering.plan === "local"
        ? attributed.filter((event) => !event.local && event.at.getTime() >= monthStart.getTime())
        : [];

    const plan: UsagePlanKind = metering.plan;
    const unit = plan === "local" ? "tokens" : "usd";
    // The configured budget is a dollar ceiling, so it bounds only dollar
    // windows. No provider grants a local model a token allowance, and
    // inventing one would be a fabricated limit.
    const monthlyBudgetUsd = unit === "usd" ? getLimitsSettings().monthlyBudgetUsd : 0;

    return {
      adapter,
      plan: { kind: plan },
      capturedAt: now,
      windows: [
        buildWindow(
          `${this.id}:session`,
          "This session",
          "session",
          unit,
          events,
          sessionStart,
          null,
          // A session ends when the user's session ends; there is no clock
          // that resets it, so claiming a reset time would be an invention.
          null
        ),
        buildWindow(
          `${this.id}:daily`,
          "Today",
          "daily",
          unit,
          events,
          dayStart,
          null,
          startOfNextLocalDay(now)
        ),
        buildWindow(
          `${this.id}:monthly`,
          "This month",
          "monthly",
          unit,
          events,
          monthStart,
          // The configured budget is the only ceiling that exists — a local
          // policy, not a provider-reported limit. 0 means "not configured",
          // which is `null`, not a limit of zero.
          monthlyBudgetUsd > 0 ? monthlyBudgetUsd : null,
          startOfNextLocalMonth(now)
        ),
      ],
      ...(excludedHosted.length > 0
        ? {
            hostedSpendExcludedUsd: excludedHosted.reduce((sum, event) => sum + event.costUsd, 0),
          }
        : {}),
    };
  }
}
