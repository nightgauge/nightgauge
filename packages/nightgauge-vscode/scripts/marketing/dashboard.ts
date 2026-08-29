/**
 * Marketing capture — the dashboard webview, rendered by the real renderer.
 *
 * Everything below feeds `getDashboardHtml()` — the same function the
 * extension calls — with the #338 run and today's sibling runs from
 * `run-data.ts`. Nothing here is a hand-drawn mirror of the dashboard;
 * if the dashboard changes, the captured image changes with it.
 *
 * `vscode` is mocked with the same Module._load interception
 * `scripts/generate-dashboard-html.ts` uses, and must run BEFORE the
 * dashboard import — hence the order of the statements.
 */

import Module from "module";

const vscodeMock = {
  EventEmitter: class {
    on() {
      return this;
    }
    off() {
      return this;
    }
    fire() {}
    event = () => ({ dispose: () => {} });
  },
  window: {
    createOutputChannel: () => ({
      appendLine: () => {},
      show: () => {},
      clear: () => {},
      dispose: () => {},
    }),
  },
  Uri: {
    file: (p: string) => ({ fsPath: p, toString: () => `file://${p}` }),
    parse: (u: string) => ({ toString: () => u }),
  },
};
const _load = (Module as any)._load.bind(Module);
(Module as any)._load = (req: string, ...args: unknown[]) =>
  req === "vscode" ? vscodeMock : _load(req, ...args);

import { getDashboardHtml } from "../../src/views/dashboard/DashboardHtml.js";
import { RUN_338, RUN_338_DURATION_MS, SIBLING_RUNS, REPO_NAME } from "./run-data";

const now = new Date();
const STAGE_ORDER = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
] as const;

function stagesFor338() {
  let t = now.getTime() - RUN_338_DURATION_MS;
  return STAGE_ORDER.map((stage) => {
    const s = RUN_338.stages[stage];
    const startedAt = new Date(t);
    t += s.duration_ms;
    return {
      stage: stage as any,
      status: "complete" as const,
      startedAt,
      completedAt: new Date(t),
      durationMs: s.duration_ms,
      execution_path: s.execution_path,
      tokenUsage: {
        inputTokens: s.input ?? 0,
        outputTokens: s.output ?? 0,
        cacheReadTokens: s.cache_read ?? 0,
        cacheCreationTokens: s.cache_creation ?? 0,
        costUsd: s.cost_usd,
        model: s.model,
      },
    };
  });
}

const run338 = {
  runId: `run-338-${REPO_NAME}`,
  issueNumber: RUN_338.issue_number,
  title: RUN_338.title,
  branch: RUN_338.branch,
  startedAt: new Date(now.getTime() - RUN_338_DURATION_MS),
  completedAt: now,
  status: "complete" as const,
  stages: stagesFor338(),
  usage: {
    inputTokens: RUN_338.tokens.total_input,
    outputTokens: RUN_338.tokens.total_output,
    cacheReadTokens: RUN_338.tokens.total_cache_read,
    cacheCreationTokens: RUN_338.tokens.total_cache_creation,
    costUsd: RUN_338.tokens.estimated_cost_usd,
    durationMs: RUN_338_DURATION_MS,
  },
  toolCalls: [],
  timeSavedMs: 4 * 3_600_000,
  sizeLabel: RUN_338.complexity,
  issueType: "feature",
  performance_mode: RUN_338.performance_mode,
  repoName: REPO_NAME,
};

function siblingSummary(r: (typeof SIBLING_RUNS)[number]) {
  const startedAt = new Date(now.getTime() - r.startedAgoMs);
  const cur = STAGE_ORDER.indexOf((r.currentStage ?? "pr-merge") as any);
  const per = r.durationMs / Math.max(cur + 1, 1);
  const stages = STAGE_ORDER.map((stage, i) => {
    const status =
      r.status === "running"
        ? i < cur
          ? "complete"
          : i === cur
            ? "running"
            : "pending"
        : "complete";
    return {
      stage: stage as any,
      status: status as any,
      startedAt: i <= cur ? new Date(startedAt.getTime() + per * i) : undefined,
      completedAt:
        i < cur || r.status !== "running"
          ? new Date(startedAt.getTime() + per * (i + 1))
          : undefined,
      durationMs: i < cur || r.status !== "running" ? Math.round(per) : undefined,
    };
  });
  return {
    runId: `run-${r.issueNumber}-${r.repoName}`,
    issueNumber: r.issueNumber,
    title: r.title,
    branch: r.branch,
    startedAt,
    completedAt: r.status === "running" ? undefined : new Date(startedAt.getTime() + r.durationMs),
    status: r.status === "running" ? ("running" as const) : ("complete" as const),
    currentStage: r.status === "running" ? (r.currentStage as any) : undefined,
    stages,
    usage: {
      inputTokens: Math.round(r.costUsd * 2_400_000),
      outputTokens: Math.round(r.costUsd * 9_000),
      cacheReadTokens: Math.round(r.costUsd * 2_390_000),
      cacheCreationTokens: Math.round(r.costUsd * 55_000),
      costUsd: r.costUsd,
      durationMs: r.durationMs,
    },
    toolCalls: [],
    timeSavedMs: r.status === "running" ? undefined : Math.round(r.durationMs * 2.6),
    sizeLabel: r.sizeLabel,
    issueType: r.title.toLowerCase().includes("heading") ? "feature" : "bug",
    repoName: r.repoName,
  };
}

const runningSibling = SIBLING_RUNS.find((r) => r.status === "running")!;
const currentRun = siblingSummary(runningSibling);
const history = [
  run338,
  ...SIBLING_RUNS.filter((r) => r.status === "complete").map(siblingSummary),
];

const totalCost = history.reduce((a, r) => a + r.usage.costUsd, 0);
const totalTokens = history.reduce((a, r) => a + r.usage.inputTokens + r.usage.outputTokens, 0);
const totalSaved = history.reduce((a, r) => a + (r.timeSavedMs ?? 0), 0);

const aggregates = {
  totalRuns: 47,
  sessionRuns: history.length,
  totalTimeSavedMs: 47 * 2.9 * 3_600_000,
  sessionTimeSavedMs: totalSaved,
  totalCostUsd: 163.4,
  sessionCostUsd: totalCost,
  successRate: 0.915,
  avgCostPerRun: 163.4 / 47,
  avgTimeSavedPerRun: 2.9 * 3_600_000,
  totalTokens: 812_000_000,
  sessionTokens: totalTokens,
  epicEstimates: [],
  batchAssessments: new Map(),
  crossRepoEpicProgress: [],
  firewallAggregates: {
    totalBlocked: 3,
    totalWarned: 5,
    totalBypassed: 0,
    mostCommonCategory: "destructive" as any,
    mostRecentEvent: new Date(now.getTime() - 5_400_000),
    categoryBreakdown: {
      destructive: 3,
      exfiltration: 0,
      privilege_escalation: 0,
      prompt_injection: 1,
      path_traversal: 0,
      allowlist: 4,
      unknown: 0,
    },
    toolBreakdown: { Bash: 7, Write: 1 },
  },
  stageAverages: [
    {
      stage: "feature-planning",
      avgCostUsd: 0.39,
      avgInputTokens: 30,
      avgOutputTokens: 4_800,
      avgCacheReadTokens: 540_000,
      avgCacheCreationTokens: 60_000,
      avgDurationMs: 78_000,
      runCount: 47,
      primaryModel: "claude-sonnet-5",
    },
    {
      stage: "feature-dev",
      avgCostUsd: 1.52,
      avgInputTokens: 150,
      avgOutputTokens: 19_000,
      avgCacheReadTokens: 6_100_000,
      avgCacheCreationTokens: 110_000,
      avgDurationMs: 1_140_000,
      runCount: 47,
      primaryModel: "claude-fable-5",
    },
    {
      stage: "feature-validate",
      avgCostUsd: 1.18,
      avgInputTokens: 160,
      avgOutputTokens: 21_000,
      avgCacheReadTokens: 5_400_000,
      avgCacheCreationTokens: 98_000,
      avgDurationMs: 1_320_000,
      runCount: 45,
      primaryModel: "claude-fable-5",
    },
    {
      stage: "pr-merge",
      avgCostUsd: 0.48,
      avgInputTokens: 80,
      avgOutputTokens: 9_000,
      avgCacheReadTokens: 2_100_000,
      avgCacheCreationTokens: 70_000,
      avgDurationMs: 420_000,
      runCount: 43,
      primaryModel: "claude-fable-5",
    },
  ],
  costPerIssue: history.map((r) => ({
    issueNumber: r.issueNumber,
    totalCostUsd: r.usage.costUsd,
    runCount: 1,
    backtrackCount: 0,
    issueType: r.issueType ?? null,
    sizeLabel: r.sizeLabel ?? null,
  })),
};

const timeSavingsConfig = {
  pipelineStart: 0,
  issuePickup: 5,
  featurePlanning: 30,
  featureDev: 120,
  featureValidate: 15,
  prCreate: 10,
  prMerge: 5,
  pipelineFinish: 0,
};

const day = 86_400_000;
const healthWidgetData = {
  summary: {
    score: RUN_338.health_score,
    status: "good" as const,
    components: [
      {
        name: "successRate",
        score: 92,
        weight: 0.3,
        trend: "improving" as const,
        label: "Success Rate",
      },
      { name: "costTrend", score: 84, weight: 0.3, trend: "stable" as const, label: "Cost Trend" },
      {
        name: "failureRate",
        score: 90,
        weight: 0.25,
        trend: "improving" as const,
        label: "Failure Rate",
      },
      {
        name: "cacheHitRate",
        score: 99,
        weight: 0.15,
        trend: "stable" as const,
        label: "Cache Hit Rate",
      },
    ],
  },
  sparklines: [
    {
      metric: "costUsd",
      label: "Cost per run",
      data: [2.14, 1.87, 8.41, 3.92, 6.72],
      trend: "stable" as const,
      polarity: "lower-is-better" as const,
      unit: "$",
    },
    {
      metric: "durationMs",
      label: "Duration",
      data: [33, 27, 91, 49, 84],
      trend: "stable" as const,
      polarity: "lower-is-better" as const,
      unit: "min",
    },
    {
      metric: "cacheHitRate",
      label: "Cache hit rate",
      data: [99.6, 99.8, 99.9, 99.9, 100],
      trend: "up" as const,
      polarity: "higher-is-better" as const,
      unit: "%",
    },
    {
      metric: "retries",
      label: "Retries",
      data: [0, 0, 1, 0, 0],
      trend: "stable" as const,
      polarity: "lower-is-better" as const,
      unit: "",
      treatZeroAsMissing: false,
    },
  ],
  alerts: [
    {
      level: "info" as const,
      stage: "feature-validate",
      metric: "costTrend",
      message:
        "#338 came in 1.5× over its $4.46 estimate — validate stage ran the full app_e2e sweep",
      timestamp: now.toISOString(),
    },
  ],
  recommendations: [
    {
      title: "Route feature-planning to Sonnet by default",
      description:
        "Planning succeeded on Sonnet in 14 of the last 15 M-sized runs; pinning it saves the Fable premium on every run.",
      estimatedSavingsUsd: 4.5,
      category: "model-routing",
      severity: "medium",
      action: {
        type: "config-patch" as const,
        configPath: "stages.featurePlanning.model",
        suggestedValue: "claude-sonnet-5",
        label: "Pin planning to Sonnet",
      },
    },
  ],
  predictionAccuracy: {
    totalObservations: 47,
    avgEstimated: 4.1,
    avgActual: 4.7,
    accuracyPercent: 87.2,
    trend: "improving" as const,
  },
  lastUpdated: now.toISOString(),
  isEmpty: false,
  trendChart: [81, 83, 84, 86, 85, 88, RUN_338.health_score].map((avgScore, i) => ({
    date: new Date(now.getTime() - (6 - i) * day).toISOString().slice(0, 10),
    avgScore,
    count: 4 + (i % 3),
  })),
  trendAnalysis: {
    direction: "improving" as const,
    message: "Health score improved 9.9% over the last 7 days",
    periodDays: 7,
    percentChange: 9.9,
  },
  trendRange: "7d" as const,
  dimensionSparklines: [],
};

const modelRoutingMetrics = {
  totalAutoSelectedRuns: 47,
  overallSuccessRate: 0.915,
  totalCostUsd: 163.4,
  perStage: [
    { stage: "feature-planning", totalRuns: 47, successRate: 0.98, totalCostUsd: 18.2 },
    { stage: "feature-dev", totalRuns: 47, successRate: 0.91, totalCostUsd: 71.6 },
    { stage: "feature-validate", totalRuns: 45, successRate: 0.93, totalCostUsd: 52.9 },
    { stage: "pr-merge", totalRuns: 43, successRate: 0.95, totalCostUsd: 20.7 },
  ],
  confidenceDistribution: { low: 3, medium: 12, high: 32 },
  modelUsage: { "claude-sonnet-5": 61, "claude-fable-5": 104, "claude-haiku-4-5": 17 },
};

const costSummary = {
  totalCostUsd: RUN_338.tokens.estimated_cost_usd,
  stages: STAGE_ORDER.filter((s) => RUN_338.stages[s].cost_usd > 0).map((stage) => {
    const s = RUN_338.stages[stage];
    return {
      stage: stage as any,
      model: s.model ?? "",
      effortLevel: stage === "feature-planning" ? "medium" : "high",
      inputTokens: s.input ?? 0,
      outputTokens: s.output ?? 0,
      cacheReadTokens: s.cache_read ?? 0,
      cacheCreationTokens: s.cache_creation ?? 0,
      costUsd: s.cost_usd,
      percentOfTotal: (s.cost_usd / RUN_338.tokens.estimated_cost_usd) * 100,
    };
  }),
  hypotheticalDefaultCostUsd: 9.88,
  defaultModel: "claude-fable-5",
  savingsUsd: 9.88 - RUN_338.tokens.estimated_cost_usd,
  savingsPercent: ((9.88 - RUN_338.tokens.estimated_cost_usd) / 9.88) * 100,
  routingMode: "adaptive",
};

const costHistory = history.map((r) => ({
  issueNumber: r.issueNumber,
  costUsd: r.usage.costUsd,
  stageCount: 6,
  timestamp: r.startedAt,
}));

const pipelineSlotsView = {
  maxConcurrent: 2,
  queueStatus: "processing" as const,
  slots: [
    {
      slotIndex: 0,
      issueNumber: runningSibling.issueNumber,
      title: runningSibling.title,
      branch: runningSibling.branch,
      repoName: runningSibling.repoName,
      status: "running" as const,
      startedAt: currentRun.startedAt.toISOString(),
      currentStage: "feature-dev" as any,
      currentPhase: { name: "Implementation", index: 2, total: 4 },
      stages: currentRun.stages.map((s) => ({
        stage: s.stage,
        status: s.status as any,
        durationMs: s.durationMs,
      })),
      completedStageCount: 2,
      totalStageCount: 6,
      inputTokens: currentRun.usage.inputTokens,
      outputTokens: currentRun.usage.outputTokens,
      cacheReadTokens: currentRun.usage.cacheReadTokens,
      costUsd: currentRun.usage.costUsd,
    },
    {
      slotIndex: 1,
      issueNumber: RUN_338.issue_number,
      title: RUN_338.title,
      branch: RUN_338.branch,
      repoName: REPO_NAME,
      status: "complete" as const,
      startedAt: run338.startedAt.toISOString(),
      stages: run338.stages.map((s) => ({
        stage: s.stage,
        status: "complete" as const,
        durationMs: s.durationMs,
        costUsd: s.tokenUsage.costUsd,
      })),
      completedStageCount: 6,
      totalStageCount: 6,
      inputTokens: run338.usage.inputTokens,
      outputTokens: run338.usage.outputTokens,
      cacheReadTokens: run338.usage.cacheReadTokens,
      costUsd: run338.usage.costUsd,
    },
  ],
  queued: [
    {
      issueNumber: 343,
      title: "Device identity per (device, user): fix the shared-device 403",
      position: 1,
      status: "ready" as const,
      isBlocked: false,
      blockerCount: 0,
      blockerNumbers: [],
      labels: ["type:bug", "priority:p1", "size:s"],
      priority: "P1" as const,
      repoName: REPO_NAME,
    },
    {
      issueNumber: 339,
      title: "Guest-safe substitutions and lineup corrections in scoring",
      position: 2,
      status: "ready" as const,
      isBlocked: false,
      blockerCount: 0,
      blockerNumbers: [],
      labels: ["type:feature", "priority:p1", "size:s"],
      priority: "P1" as const,
      repoName: REPO_NAME,
    },
    {
      issueNumber: 340,
      title: "First-run experience and contextual sign-in prompts",
      position: 3,
      status: "pending" as const,
      isBlocked: true,
      blockerCount: 1,
      blockerNumbers: [339],
      labels: ["type:feature", "priority:p1", "size:m"],
      priority: "P1" as const,
      repoName: REPO_NAME,
    },
  ],
};

const usageLimitsData = { costUsd: totalCost, budgetUsd: 50, usagePct: (totalCost / 50) * 100 };

const historyPagination = {
  page: 0,
  pageSize: 20,
  totalCount: history.length,
  hasMore: false,
};

const mockWebview = { cspSource: "https://mock.vscode-cdn.net" } as any;

/** Render one dashboard tab with the real renderer and the real run data. */
export function renderDashboardTab(activeTab: string): string {
  return getDashboardHtml(
    mockWebview,
    currentRun as any,
    history as any,
    aggregates as any,
    timeSavingsConfig,
    "all",
    undefined,
    null,
    healthWidgetData as any,
    modelRoutingMetrics as any,
    [],
    costSummary as any,
    costHistory,
    null,
    historyPagination as any,
    null,
    null,
    null,
    [],
    [],
    usageLimitsData,
    activeTab,
    null,
    null,
    null,
    pipelineSlotsView as any,
    "all",
    null,
    [],
    null,
    [],
    [],
    null,
    "7d",
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null
  );
}
