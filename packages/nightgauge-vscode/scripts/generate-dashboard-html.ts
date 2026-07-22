/**
 * Generates real dashboard HTML for Playwright testing (Issue #1757).
 *
 * Must mock the 'vscode' module BEFORE any dashboard imports execute.
 * Uses Node.js Module._load interception (CJS) — tsx compiles to CJS on-the-fly.
 *
 * Usage:
 *   npx tsx scripts/generate-dashboard-html.ts
 *
 * Output: /tmp/dashboard-test.html
 */

// 1. Intercept Node's module loader to mock 'vscode' before any imports
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

// 2. Now safe to import dashboard modules
import { getDashboardHtml } from "../src/views/dashboard/DashboardHtml.js";
import { writeFileSync } from "fs";

// 3. Build minimal mock webview (only cspSource used; CSP meta stripped below)
const mockWebview = { cspSource: "https://mock.vscode-cdn.net" } as any;

// 4. Rich mock data — ensures all interactive sections render with clickable elements

const now = new Date();

const mockCurrentRun = {
  issueNumber: 42,
  title: "Add Playwright-based interactive testing",
  branch: "feat/42-playwright-testing",
  startedAt: new Date(now.getTime() - 300_000),
  status: "running" as const,
  currentStage: "feature-dev" as any,
  stages: [
    {
      stage: "issue-pickup" as any,
      status: "complete" as const,
      startedAt: new Date(now.getTime() - 300_000),
      completedAt: new Date(now.getTime() - 280_000),
      durationMs: 20_000,
    },
    {
      stage: "feature-planning" as any,
      status: "complete" as const,
      startedAt: new Date(now.getTime() - 280_000),
      completedAt: new Date(now.getTime() - 200_000),
      durationMs: 80_000,
    },
    {
      stage: "feature-dev" as any,
      status: "running" as const,
      startedAt: new Date(now.getTime() - 200_000),
    },
    { stage: "feature-validate" as any, status: "pending" as const },
    { stage: "pr-create" as any, status: "pending" as const },
    { stage: "pr-merge" as any, status: "pending" as const },
  ],
  usage: {
    inputTokens: 12_500,
    outputTokens: 3_200,
    cacheReadTokens: 8_000,
    cacheCreationTokens: 2_000,
    costUsd: 0.0342,
    durationMs: 100_000,
  },
  toolCalls: [
    {
      tool: "Read",
      target: "src/views/dashboard/DashboardHtml.ts",
      timestamp: new Date(now.getTime() - 250_000),
      durationMs: 120,
      args: { file_path: "src/views/dashboard/DashboardHtml.ts" },
      result: "File contents read successfully",
    },
    {
      tool: "Bash",
      target: "npm run build",
      timestamp: new Date(now.getTime() - 220_000),
      durationMs: 8_500,
      args: { command: "npm run build" },
      result: "Build successful",
    },
    {
      tool: "Edit",
      target: "src/views/dashboard/DashboardHtml.ts",
      timestamp: new Date(now.getTime() - 210_000),
      durationMs: 45,
      args: {
        file_path: "src/views/dashboard/DashboardHtml.ts",
        old_string: "old",
        new_string: "new",
      },
    },
  ],
};

const mockHistory = [
  {
    issueNumber: 41,
    title: "Fix dashboard CSP nonce policy for inline event handlers",
    branch: "fix/41-csp-nonce",
    startedAt: new Date(now.getTime() - 3_600_000),
    completedAt: new Date(now.getTime() - 3_000_000),
    status: "complete" as const,
    stages: [],
    usage: {
      inputTokens: 9_800,
      outputTokens: 2_100,
      cacheReadTokens: 5_000,
      cacheCreationTokens: 1_500,
      costUsd: 0.0211,
      durationMs: 600_000,
    },
    toolCalls: [],
    timeSavedMs: 540_000,
  },
  {
    issueNumber: 40,
    title: "Add health widget trend range dropdown",
    branch: "feat/40-health-trend-range",
    startedAt: new Date(now.getTime() - 7_200_000),
    completedAt: new Date(now.getTime() - 6_400_000),
    status: "complete" as const,
    stages: [],
    usage: {
      inputTokens: 11_200,
      outputTokens: 2_800,
      cacheReadTokens: 6_000,
      cacheCreationTokens: 1_800,
      costUsd: 0.0278,
      durationMs: 800_000,
    },
    toolCalls: [],
    timeSavedMs: 720_000,
  },
  {
    issueNumber: 39,
    title: "Migrate dashboard to tabbed layout",
    branch: "feat/39-tabbed-layout",
    startedAt: new Date(now.getTime() - 14_400_000),
    completedAt: new Date(now.getTime() - 13_200_000),
    status: "complete" as const,
    stages: [],
    usage: {
      inputTokens: 18_000,
      outputTokens: 4_500,
      cacheReadTokens: 9_000,
      cacheCreationTokens: 3_000,
      costUsd: 0.0445,
      durationMs: 1_200_000,
    },
    toolCalls: [],
    timeSavedMs: 1_080_000,
  },
];

const mockAggregates = {
  totalRuns: 42,
  sessionRuns: 3,
  totalTimeSavedMs: 18_000_000,
  sessionTimeSavedMs: 2_340_000,
  totalCostUsd: 1.24,
  sessionCostUsd: 0.0831,
  successRate: 0.92,
  avgCostPerRun: 0.0295,
  avgTimeSavedPerRun: 428_571,
  totalTokens: 1_250_000,
  sessionTokens: 37_500,
  epicEstimates: [],
  batchAssessments: new Map(),
  crossRepoEpicProgress: [],
  firewallAggregates: {
    totalBlocked: 2,
    totalWarned: 2,
    totalBypassed: 1,
    mostCommonCategory: "destructive" as any,
    mostRecentEvent: new Date(now.getTime() - 900_000),
    categoryBreakdown: {
      destructive: 3,
      exfiltration: 1,
      privilege_escalation: 0,
      prompt_injection: 0,
      path_traversal: 0,
      allowlist: 0,
      unknown: 1,
    },
    toolBreakdown: { Bash: 4, Write: 1 },
  },
  stageAverages: [],
  costPerIssue: [],
};

const mockTimeSavingsConfig = {
  pipelineStart: 0,
  issuePickup: 5,
  featurePlanning: 30,
  featureDev: 120,
  featureValidate: 15,
  prCreate: 10,
  prMerge: 5,
  pipelineFinish: 0,
};

const mockHealthWidgetData = {
  summary: {
    score: 72,
    status: "good" as const,
    components: [
      {
        name: "successRate",
        score: 85,
        weight: 0.3,
        trend: "stable" as const,
        label: "Success Rate",
      },
      {
        name: "costTrend",
        score: 68,
        weight: 0.3,
        trend: "improving" as const,
        label: "Cost Trend",
      },
      {
        name: "failureRate",
        score: 70,
        weight: 0.25,
        trend: "stable" as const,
        label: "Failure Rate",
      },
      {
        name: "cacheHitRate",
        score: 60,
        weight: 0.15,
        trend: "degrading" as const,
        label: "Cache Hit Rate",
      },
    ],
  },
  sparklines: [],
  alerts: [
    {
      level: "warning" as const,
      stage: "feature-dev",
      metric: "costTrend",
      message: "Cost trending upward over last 7 days",
      timestamp: new Date().toISOString(),
    },
  ],
  recommendations: [
    {
      title: "Enable cache warming for feature-dev stage",
      description:
        "Cache hit rate is below optimal threshold. Enable prompt caching to reduce costs.",
      estimatedSavingsUsd: 0.015,
      category: "cache-optimization",
      severity: "medium",
      action: {
        type: "config-patch" as const,
        configPath: "pipeline.cacheWarming",
        suggestedValue: true,
        label: "Enable Cache Warming",
      },
    },
    {
      title: "Reduce token usage in feature-planning",
      description: "Feature planning stage uses more tokens than average.",
      estimatedSavingsUsd: 0.008,
      category: "token-efficiency",
      severity: "low",
      action: {
        type: "config-patch" as const,
        configPath: "stages.featurePlanning.maxTokens",
        suggestedValue: 8000,
        label: "Set Max Tokens to 8000",
      },
    },
  ],
  predictionAccuracy: {
    totalObservations: 12,
    avgEstimated: 185,
    avgActual: 178,
    accuracyPercent: 96.2,
    trend: "improving" as const,
  },
  lastUpdated: new Date().toISOString(),
  isEmpty: false,
  trendChart: [
    { date: "2026-03-01", avgScore: 68, count: 3 },
    { date: "2026-03-02", avgScore: 70, count: 4 },
    { date: "2026-03-03", avgScore: 71, count: 2 },
    { date: "2026-03-04", avgScore: 69, count: 5 },
    { date: "2026-03-05", avgScore: 72, count: 3 },
    { date: "2026-03-06", avgScore: 73, count: 4 },
    { date: "2026-03-07", avgScore: 72, count: 2 },
  ],
  trendAnalysis: {
    direction: "improving" as const,
    message: "Health score improved 5.9% over the last 7 days",
    periodDays: 7,
    percentChange: 5.9,
  },
  trendRange: "7d" as const,
  dimensionSparklines: [],
};

const mockFirewallData = {
  events: [
    {
      timestamp: new Date(now.getTime() - 1_800_000),
      event: "blocked" as const,
      category: "destructive" as any,
      pattern: "rm-rf-pattern",
      content: "Blocked destructive command attempt",
      tool: "Bash",
      branch: "feat/42-playwright-testing",
      context: "feature-dev stage",
    },
    {
      timestamp: new Date(now.getTime() - 900_000),
      event: "warned" as const,
      category: "exfiltration" as any,
      pattern: "curl-external",
      content: "Warning: potential data exfiltration attempt",
      tool: "Bash",
      branch: "feat/42-playwright-testing",
      context: "feature-dev stage",
    },
  ],
  filters: {
    timeRange: "all" as const,
    eventTypes: ["blocked" as const, "warned" as const, "bypassed" as const],
    categories: [],
    searchText: "",
  },
  aggregates: {
    totalBlocked: 1,
    totalWarned: 1,
    totalBypassed: 0,
    mostCommonCategory: "destructive" as any,
    mostRecentEvent: new Date(now.getTime() - 900_000),
    categoryBreakdown: {
      destructive: 1,
      exfiltration: 1,
      privilege_escalation: 0,
      prompt_injection: 0,
      path_traversal: 0,
      allowlist: 0,
      unknown: 0,
    },
    toolBreakdown: { Bash: 2 },
  },
  timeSeriesData: [],
  granularity: "day" as const,
};

const mockHistoryPagination = {
  totalCount: 42,
  hasMore: true,
};

// 5. Generate real HTML using getDashboardHtml()
let html = getDashboardHtml(
  mockWebview,
  mockCurrentRun as any,
  mockHistory as any,
  mockAggregates as any,
  mockTimeSavingsConfig,
  "all",
  mockFirewallData as any,
  null,
  mockHealthWidgetData,
  null,
  [],
  null,
  [],
  null,
  mockHistoryPagination,
  null,
  null,
  null,
  [],
  [],
  null,
  "overview"
);

// 6. Strip CSP meta tag (blocks inline scripts when loaded via file://)
html = html.replace(/<meta http-equiv="Content-Security-Policy"[^>]*>/i, "");

// 7. Remove nonce attributes from script tags (nonce enforcement not active without CSP)
html = html.replace(/ nonce="[^"]*"/g, "");

// 8. Inject acquireVsCodeApi mock BEFORE closing </head>
const apiMock = `<script>
  window.__vscodeMessages = [];
  window.acquireVsCodeApi = function() {
    return {
      postMessage: function(msg) { window.__vscodeMessages.push(msg); },
      setState: function() {},
      getState: function() { return {}; },
    };
  };
</script>`;
html = html.replace("</head>", apiMock + "\n</head>");

// 9. Write output
const OUTPUT_PATH = "/tmp/dashboard-test.html";
writeFileSync(OUTPUT_PATH, html, "utf8");
console.log(`Dashboard HTML written to ${OUTPUT_PATH}`);
console.log(`File size: ${(html.length / 1024).toFixed(1)} KB`);
