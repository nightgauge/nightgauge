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
import { writeFileSync, mkdirSync } from "fs";
import { join } from "path";

// 3. Build minimal mock webview (only cspSource used; CSP meta stripped below)
const mockWebview = { cspSource: "https://mock.vscode-cdn.net" } as any;

// 4. Rich mock data — ensures all interactive sections render with clickable elements

const now = new Date();

const mockCurrentRun = {
  issueNumber: 42,
  title: "Add Playwright-based interactive testing",
  branch: "",
  startedAt: new Date(now.getTime() - 300_000),
  status: "failed" as const,
  terminalFailureKind: "orchestrator_crash" as const,
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
      status: "failed" as const,
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
    runId: "run-orchestrator-crash-41",
    issueNumber: 41,
    title: "Recovered orchestrator crash",
    branch: "",
    startedAt: new Date(now.getTime() - 3_600_000),
    completedAt: new Date(now.getTime() - 3_000_000),
    status: "failed" as const,
    terminalFailureKind: "orchestrator_crash" as const,
    stages: mockCurrentRun.stages,
    usage: {
      inputTokens: 9_800,
      outputTokens: 2_100,
      cacheReadTokens: 5_000,
      cacheCreationTokens: 1_500,
      costUsd: 0.0211,
      durationMs: 600_000,
    },
    toolCalls: mockCurrentRun.toolCalls,
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

// ---------------------------------------------------------------------------
// 5. postProcess() — the transforms every generated fixture needs to be
//    loadable and visible outside a real VS Code host. Shared by the
//    original single-fixture output below and the tab/state matrix added
//    for Issue #751.
// ---------------------------------------------------------------------------

function postProcess(rawHtml: string, opts: { embedApiMock: boolean }): string {
  let out = rawHtml;

  // Strip CSP meta tag (blocks inline scripts when loaded via file://)
  out = out.replace(/<meta http-equiv="Content-Security-Policy"[^>]*>/i, "");

  // Remove nonce attributes from script tags (nonce enforcement not active without CSP)
  out = out.replace(/ nonce="[^"]*"/g, "");

  // file:// has no VS Code host supplying theme variables. Give visual
  // verification representative contrast so visibility means perceptibility,
  // not merely a non-empty white-on-white DOM box.
  out = out.replace(
    "<style>",
    `<style>
    :root {
      --vscode-foreground: #cccccc;
      --vscode-editor-background: #1e1e1e;
      --vscode-editorWidget-background: #252526;
      --vscode-panel-border: #454545;
      --vscode-charts-red: #f14c4c;
      --vscode-charts-green: #89d185;
      --vscode-charts-blue: #75beff;
      --vscode-charts-yellow: #cca700;
      --vscode-descriptionForeground: #9d9d9d;
      --vscode-textLink-foreground: #4daafc;
    }
    body { background: var(--vscode-editor-background); color: var(--vscode-foreground); }
  `
  );

  // The original single-fixture path embeds a basic acquireVsCodeApi mock
  // directly in the file for backward compatibility with
  // DashboardInteractions.playwright.ts, which loads it bare via
  // page.goto('file://...') with no loader helper of its own.
  //
  // The tab/state matrix added for Issue #751 deliberately skips this: an
  // embedded <script> here executes AFTER Playwright's addInitScript-injected
  // mock (page scripts always run after init scripts) and would silently
  // overwrite it — including the real setState()/getState() the tab
  // state-restoration tests depend on
  // (tests/playwright/helpers/webview-loader.ts's loadWebviewFromFile()).
  // Those fixtures rely on the loader for the mock instead.
  if (opts.embedApiMock) {
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
    out = out.replace("</head>", apiMock + "\n</head>");
  }

  return out;
}

// ---------------------------------------------------------------------------
// 6. buildHtml() — getDashboardHtml() has 42 positional parameters; this
//    wraps it with the same fixed "rich mock data" prefix generate-dashboard-
//    html.ts has always used, and exposes only the parameters relevant to
//    tab/state fixtures (Issue #751) as a named `overrides` object. Omitted
//    overrides render each tab's own "not yet loaded" state, matching what
//    the real dashboard shows before its first lazy-load fetch.
// ---------------------------------------------------------------------------

interface DashboardHtmlOverrides {
  activeTab?: string;
  auditLogData?: unknown;
  discoveryActivityData?: unknown;
  platformCostData?: unknown;
  healthAnalyticsData?: unknown;
  runsData?: unknown;
  trendsData?: unknown;
  complianceData?: unknown;
  retentionIntegrityData?: unknown;
  dependabotData?: unknown;
}

// getDependabotTabHtml() distinguishes state === undefined (loading) from
// state === null (empty) — the ONE tab data field in this matrix where that
// distinction is load-bearing (every other tab's renderer treats `!data` as
// a single "not loaded yet" branch, so `?? null` is safe for those). A plain
// `overrides.dependabotData ?? null` would collapse an explicit
// `{ dependabotData: undefined }` fixture override back into `null`, since
// `??` cannot tell "key present with value undefined" from "key absent" —
// only the `in` operator can. Fixtures that don't mention dependabotData at
// all still default to `null` (empty), the neutral choice for a fixture that
// isn't testing the Dependencies tab.
function resolveDependabotData(overrides: DashboardHtmlOverrides): unknown {
  return "dependabotData" in overrides ? overrides.dependabotData : null;
}

function buildHtml(overrides: DashboardHtmlOverrides, opts: { embedApiMock: boolean }): string {
  const raw = getDashboardHtml(
    mockWebview,
    null,
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
    overrides.activeTab ?? "overview",
    null, // platformQuotaData
    (overrides.auditLogData ?? null) as any,
    (overrides.discoveryActivityData ?? null) as any,
    null, // pipelineSlotsView
    "all", // modeFilter
    null, // perModeRollup
    [], // stallThresholdRows
    null, // modeMismatchAdvisory
    [], // costCapWarningRows
    [], // budgetVsActualStats
    (overrides.platformCostData ?? null) as any,
    "7d", // costDateRange
    (overrides.healthAnalyticsData ?? null) as any,
    null, // healthFetchedAt
    (overrides.runsData ?? null) as any,
    (overrides.trendsData ?? null) as any,
    (overrides.complianceData ?? null) as any,
    (overrides.retentionIntegrityData ?? null) as any,
    resolveDependabotData(overrides) as any,
    null // usagePanelState
  );
  return postProcess(raw, opts);
}

// 7. Write the original single fixture — unchanged output, unchanged callers
// (DashboardInteractions.playwright.ts).
const OUTPUT_PATH = "/tmp/dashboard-test.html";
const defaultHtml = buildHtml({}, { embedApiMock: true });
writeFileSync(OUTPUT_PATH, defaultHtml, "utf8");
console.log(`Dashboard HTML written to ${OUTPUT_PATH}`);
console.log(`File size: ${(defaultHtml.length / 1024).toFixed(1)} KB`);

// ---------------------------------------------------------------------------
// 8. Tab/state fixture matrix (Issue #751) — populated / empty / loading /
//    each PlatformFailureKind for every platform-backed tab, plus one
//    "everything populated" fixture per tab (all 13) for the tab-activation
//    and screenshot suites. Written to /tmp/dashboard-fixtures/.
// ---------------------------------------------------------------------------

const FIXTURES_DIR = "/tmp/dashboard-fixtures";
mkdirSync(FIXTURES_DIR, { recursive: true });

function writeFixture(name: string, overrides: DashboardHtmlOverrides): void {
  const out = buildHtml(overrides, { embedApiMock: false });
  writeFileSync(join(FIXTURES_DIR, `${name}.html`), out, "utf8");
}

// --- Shared failure builder -------------------------------------------------

type FailureKind = "unauthorized" | "forbidden" | "server_error" | "offline" | "not_configured";

function makeFailure(kind: FailureKind, endpoint: string, status?: number) {
  const messages: Record<FailureKind, string> = {
    unauthorized: "Stored session token has expired",
    forbidden: "Your role does not include the analytics:read scope",
    server_error: "Internal Server Error",
    offline: "connect ECONNREFUSED 127.0.0.1:8443",
    not_configured: "No platform credential is configured",
  };
  return { ok: false, kind, endpoint, status, message: messages[kind] };
}

const ALL_FAILURE_KINDS: FailureKind[] = [
  "unauthorized",
  "forbidden",
  "server_error",
  "offline",
  "not_configured",
];

// --- Runs tab (full 5-kind matrix — the tab named in #751 as the priority
//     case: a retry that renders nothing observable is the defect that
//     started this issue) -----------------------------------------------

// No filters and no total: GET /v1/analytics/runs accepts limit/cursor only
// and paginates by keyset (#801).
const runsPagination = { page: 0, pageSize: 20, hasMore: false, cursorStack: [] };
const runsEntry = (n: number, stages: unknown[] = []) => ({
  issue_number: n,
  title: `Fix flaky retry test #${n}`,
  branch: `fix/${n}-flaky-retry`,
  outcome: n % 2 === 0 ? "productive" : "verify-and-close",
  duration_ms: 480_000 + n * 1000,
  total_cost_usd: "0.0421",
  started_at: new Date(now.getTime() - n * 3_600_000).toISOString(),
  stages,
});

// Entry 701 carries a stage row so the InboundRendering suite has a real
// [data-stage-name] target for the runDetailLiveUpdate inbound message.
const runs701Stages = [
  {
    name: "feature-dev",
    model: "claude-sonnet-5",
    duration_ms: 42_000,
    input_tokens: 8_000,
    output_tokens: 1_200,
    cost_usd: "0.0210",
    retry_count: 0,
  },
];

writeFixture("runs--populated", {
  activeTab: "runs",
  runsData: {
    entries: [runsEntry(701, runs701Stages), runsEntry(702), runsEntry(703)],
    pagination: runsPagination,
    isLoading: false,
    hasAccess: true,
  },
});
writeFixture("runs--empty", {
  activeTab: "runs",
  runsData: {
    entries: [],
    pagination: runsPagination,
    isLoading: false,
    hasAccess: true,
  },
});
writeFixture("runs--loading", {
  activeTab: "runs",
  runsData: {
    entries: [],
    pagination: runsPagination,
    isLoading: true,
    hasAccess: true,
  },
});
for (const kind of ALL_FAILURE_KINDS) {
  writeFixture(`runs--failure-${kind}`, {
    activeTab: "runs",
    runsData: {
      entries: [],
      pagination: runsPagination,
      isLoading: false,
      hasAccess: false,
      failure: makeFailure(kind, "platform.getAnalyticsRuns", kind === "forbidden" ? 403 : 500),
    },
  });
}

// --- Compliance tab (full 5-kind matrix — the tab PlatformFailureHtml.ts's
//     own docstring names as the motivating bug: a rejected credential told
//     to "upgrade your plan") ------------------------------------------

// Shaped from GET /v1/audit/reports' own rows: reportType is the platform's
// SOC2/ISO27001 casing, status is pending|complete|failed, dates are ISO, and
// there is no downloadUrl on a list row — the artifact is resolved on demand
// (#803). This generator is typechecked by nothing (issue 499), so a payload
// that drifts from ComplianceData is only caught when a renderer throws.
const complianceReport = (id: string, status: string, errorMessage?: string) => ({
  id,
  reportType: "SOC2",
  status,
  startDate: "2026-07-01T00:00:00.000Z",
  endDate: "2026-07-31T00:00:00.000Z",
  format: "pdf",
  errorMessage,
  createdAt: new Date(now.getTime() - 86_400_000).toISOString(),
});

writeFixture("compliance--populated", {
  activeTab: "compliance",
  complianceData: {
    reports: [
      complianceReport("rep-1", "complete"),
      complianceReport("rep-2", "pending"),
      complianceReport("rep-3", "failed", "Report generation timed out"),
    ],
    filters: {},
    isLoading: false,
    hasAccess: true,
    isGenerating: false,
  },
});
writeFixture("compliance--empty", {
  activeTab: "compliance",
  complianceData: {
    reports: [],
    filters: {},
    isLoading: false,
    hasAccess: true,
    isGenerating: false,
  },
});
writeFixture("compliance--loading", {
  activeTab: "compliance",
  complianceData: {
    reports: [],
    filters: {},
    isLoading: true,
    hasAccess: true,
    isGenerating: false,
  },
});
for (const kind of ALL_FAILURE_KINDS) {
  writeFixture(`compliance--failure-${kind}`, {
    activeTab: "compliance",
    complianceData: {
      reports: [],
      filters: {},
      isLoading: false,
      hasAccess: false,
      isGenerating: false,
      failure: makeFailure(kind, "platform.auditListReports", kind === "forbidden" ? 403 : 500),
    },
  });
}

// --- Trends tab (populated needs >= 7 entries per tab's SPARSE_THRESHOLD;
//     unauthorized + server_error as representative sign-in vs retry CTAs) -

// successRate is a percentage (0-100), as the endpoint reports it; there is no
// cost metric and no comparison series (#801).
const trendEntry = (daysAgo: number) => ({
  date: new Date(now.getTime() - daysAgo * 86_400_000).toISOString().slice(0, 10),
  successRate: 80 + (daysAgo % 3) * 5,
  totalRuns: 4 + (daysAgo % 5),
  totalTokens: 120_000 + (daysAgo % 7) * 10_000,
});
const trendsResult = () => ({
  entries: Array.from({ length: 10 }, (_, i) => trendEntry(i)),
  granularity: "daily",
  dateFrom: new Date(now.getTime() - 30 * 86_400_000).toISOString(),
  dateTo: new Date(now.getTime()).toISOString(),
  repos: ["nightgauge/nightgauge"],
  targetSuccessRate: 95,
});

writeFixture("trends--populated", {
  activeTab: "trends",
  trendsData: {
    result: trendsResult(),
    isLoading: false,
    hasAccess: true,
    dateRange: "30d",
  },
});
writeFixture("trends--empty", {
  activeTab: "trends",
  trendsData: { result: null, isLoading: false, hasAccess: true, dateRange: "30d" },
});
writeFixture("trends--loading", {
  activeTab: "trends",
  trendsData: { result: null, isLoading: true, hasAccess: true, dateRange: "30d" },
});
for (const kind of ["unauthorized", "server_error"] as FailureKind[]) {
  writeFixture(`trends--failure-${kind}`, {
    activeTab: "trends",
    trendsData: {
      result: null,
      isLoading: false,
      hasAccess: false,
      dateRange: "30d",
      failure: makeFailure(kind, "platform.getAnalyticsTrends"),
    },
  });
}

// --- Health (analytics) tab -------------------------------------------------

const healthDimension = (name: string, score: number) => ({
  name,
  label: name.charAt(0).toUpperCase() + name.slice(1),
  score,
  findings:
    score < 70
      ? [
          {
            severity: "warning",
            title: `${name} needs attention`,
            description: `${name} has been trending below target for the selected period.`,
            recommendation: null,
            issue_number: null,
          },
        ]
      : [],
});

writeFixture("health--populated", {
  activeTab: "health",
  healthAnalyticsData: {
    result: {
      overall_score: 82,
      dimensions: [healthDimension("velocity", 90), healthDimension("stability", 65)],
      generated_at: new Date(now.getTime() - 3_600_000).toISOString(),
      period_days: 30,
      total_runs: 41,
    },
    hasAccess: true,
    isLoading: false,
  },
});
writeFixture("health--empty", {
  activeTab: "health",
  healthAnalyticsData: { result: null, hasAccess: true, isLoading: false },
});
writeFixture("health--loading", {
  activeTab: "health",
  healthAnalyticsData: { result: null, hasAccess: true, isLoading: true },
});
for (const kind of ["not_configured", "server_error"] as FailureKind[]) {
  writeFixture(`health--failure-${kind}`, {
    activeTab: "health",
    healthAnalyticsData: {
      result: null,
      hasAccess: false,
      isLoading: false,
      failure: makeFailure(kind, "platform.getAnalyticsHealth"),
    },
  });
}

// --- Cost (platform) tab ----------------------------------------------------

writeFixture("cost--populated", {
  activeTab: "cost",
  platformCostData: {
    result: {
      totalInputTokens: 120_000,
      totalOutputTokens: 30_000,
      totalTokens: 150_000,
      totalCostUsd: "3.4210",
      breakdown: {
        byModel: [{ modelId: "claude-sonnet-5", costUsd: "3.4210", tokens: 150_000 }],
        byProject: [{ projectId: null, costUsd: "3.4210" }],
        byDay: [{ date: "2026-08-18", costUsd: "0.5100" }],
      },
    },
    isLoading: false,
  },
});
writeFixture("cost--empty", {
  activeTab: "cost",
  platformCostData: { result: null, isLoading: false },
});
writeFixture("cost--loading", {
  activeTab: "cost",
  platformCostData: { result: null, isLoading: true },
});
for (const kind of ["unauthorized", "offline"] as FailureKind[]) {
  writeFixture(`cost--failure-${kind}`, {
    activeTab: "cost",
    platformCostData: {
      result: null,
      isLoading: false,
      failure: makeFailure(kind, "platform.getAnalyticsCost"),
    },
  });
}

// --- Dependabot (Dependencies) tab — no PlatformFailureKind classification;
//     state === undefined (loading) / null (empty) / fetchError (string) ---

const dependabotPR = (n: number) => ({
  number: n,
  title: `chore(deps): bump left-pad to 2.0.${n}`,
  url: `https://github.com/nightgauge/nightgauge/pull/${n}`,
  nodeId: `pr-node-${n}`,
  repo: "nightgauge/nightgauge",
  prType: "security",
  checkStatus: "SUCCESS",
  isStale: false,
  staleDays: 1,
});

writeFixture("dependencies--loading", { activeTab: "dependencies", dependabotData: undefined });
writeFixture("dependencies--empty", { activeTab: "dependencies", dependabotData: null });
writeFixture("dependencies--populated", {
  activeTab: "dependencies",
  dependabotData: {
    data: { prs: [dependabotPR(801), dependabotPR(802)], securityCount: 1, staleCount: 0 },
  },
});
writeFixture("dependencies--fetch-error", {
  activeTab: "dependencies",
  dependabotData: {
    data: { prs: [], securityCount: 0, staleCount: 0 },
    fetchError: "GitHub API rate limit exceeded",
  },
});

// --- Audit tab (+ Retention & Integrity panel) ------------------------------

const auditFilters = { dateFrom: "", dateTo: "", actionFilter: "", userFilter: "" };
const auditPagination = { page: 0, totalCount: 0, hasMore: false };
const auditEntry = (n: number) => ({
  id: `audit-${n}`,
  timestamp: new Date(now.getTime() - n * 60_000).toISOString(),
  action: "pipeline.run",
  userEmail: "operator@example.com",
  status: "success",
  resourceType: "issue",
  resourceId: String(700 + n),
});

writeFixture("audit--populated", {
  activeTab: "audit",
  auditLogData: {
    entries: [auditEntry(1), auditEntry(2)],
    filters: auditFilters,
    pagination: { ...auditPagination, totalCount: 2 },
    isLoading: false,
    hasAccess: true,
  },
});
writeFixture("audit--empty", {
  activeTab: "audit",
  auditLogData: {
    entries: [],
    filters: auditFilters,
    pagination: auditPagination,
    isLoading: false,
    hasAccess: true,
  },
});
writeFixture("audit--loading", { activeTab: "audit", auditLogData: undefined });
writeFixture("audit--no-access", {
  activeTab: "audit",
  auditLogData: {
    entries: [],
    filters: auditFilters,
    pagination: auditPagination,
    isLoading: false,
    hasAccess: false,
  },
});
writeFixture("audit--local-fallback", {
  activeTab: "audit",
  auditLogData: {
    entries: [auditEntry(1)],
    filters: auditFilters,
    pagination: { ...auditPagination, totalCount: 1 },
    isLoading: false,
    hasAccess: true,
    isLocalFallback: true,
    localDataLabel: "Showing local telemetry — platform unreachable",
  },
});

writeFixture("retention--populated", {
  activeTab: "audit",
  auditLogData: {
    entries: [auditEntry(1)],
    filters: auditFilters,
    pagination: { ...auditPagination, totalCount: 1 },
    isLoading: false,
    hasAccess: true,
  },
  retentionIntegrityData: {
    retentionConfig: { retentionDays: 730, updatedAt: "2026-06-01" },
    integrityResult: null,
    isLoading: false,
    isVerifying: false,
    hasAccess: true,
  },
});
writeFixture("retention--loading", {
  activeTab: "audit",
  auditLogData: {
    entries: [auditEntry(1)],
    filters: auditFilters,
    pagination: { ...auditPagination, totalCount: 1 },
    isLoading: false,
    hasAccess: true,
  },
  retentionIntegrityData: {
    retentionConfig: null,
    integrityResult: null,
    isLoading: true,
    isVerifying: false,
    hasAccess: true,
  },
});
writeFixture("retention--no-access", {
  activeTab: "audit",
  auditLogData: {
    entries: [auditEntry(1)],
    filters: auditFilters,
    pagination: { ...auditPagination, totalCount: 1 },
    isLoading: false,
    hasAccess: true,
  },
  retentionIntegrityData: {
    retentionConfig: null,
    integrityResult: null,
    isLoading: false,
    isVerifying: false,
    hasAccess: false,
  },
});

// --- Discovery tab (local-file-based; no PlatformFailureKind) --------------

writeFixture("discovery--populated", {
  activeTab: "discovery",
  discoveryActivityData: {
    releaseWatch: {
      triggered_by: "schedule",
      new_version: "0.42.0",
      since_version: "0.41.0",
      status: "completed",
      issues_created: [
        {
          number: 900,
          title: "Adopt new SDK feature",
          url: "https://github.com/nightgauge/nightgauge/issues/900",
        },
      ],
      issues_backlogged: [],
      issues_deduped: [],
      completed_at: new Date(now.getTime() - 3_600_000).toISOString(),
      error: null,
    },
    continuousImprovement: null,
    backlog: [],
    summary: {
      issuesCreatedThisWeek: 1,
      proposalsCreatedThisWeek: 0,
      pendingBacklogCount: 0,
      lastReleaseWatchAt: new Date(now.getTime() - 3_600_000).toISOString(),
      lastContinuousImprovementAt: null,
    },
  },
});
writeFixture("discovery--empty", {
  activeTab: "discovery",
  discoveryActivityData: {
    releaseWatch: null,
    continuousImprovement: null,
    backlog: [],
    summary: {
      issuesCreatedThisWeek: 0,
      proposalsCreatedThisWeek: 0,
      pendingBacklogCount: 0,
      lastReleaseWatchAt: null,
      lastContinuousImprovementAt: null,
    },
  },
});
writeFixture("discovery--unavailable", { activeTab: "discovery", discoveryActivityData: null });

// --- Tab activation matrix: one "everything populated" fixture per tab, for
//     the 13-tab activation/lazy-load/screenshot suite. Reuses the populated
//     variants above so every platform tab shows realistic content
//     regardless of which tab is active. ------------------------------------

const ALL_POPULATED: DashboardHtmlOverrides = {
  auditLogData: {
    entries: [auditEntry(1), auditEntry(2)],
    filters: auditFilters,
    pagination: { ...auditPagination, totalCount: 2 },
    isLoading: false,
    hasAccess: true,
  },
  discoveryActivityData: {
    releaseWatch: null,
    continuousImprovement: null,
    backlog: [],
    summary: {
      issuesCreatedThisWeek: 2,
      proposalsCreatedThisWeek: 1,
      pendingBacklogCount: 3,
      lastReleaseWatchAt: null,
      lastContinuousImprovementAt: null,
    },
  },
  platformCostData: {
    result: {
      totalInputTokens: 120_000,
      totalOutputTokens: 30_000,
      totalTokens: 150_000,
      totalCostUsd: "3.4210",
      breakdown: {
        byModel: [{ modelId: "claude-sonnet-5", costUsd: "3.4210", tokens: 150_000 }],
        byProject: [{ projectId: null, costUsd: "3.4210" }],
        byDay: [{ date: "2026-08-18", costUsd: "0.5100" }],
      },
    },
    isLoading: false,
  },
  healthAnalyticsData: {
    result: {
      overall_score: 82,
      dimensions: [healthDimension("velocity", 90), healthDimension("stability", 65)],
      generated_at: new Date(now.getTime() - 3_600_000).toISOString(),
      period_days: 30,
      total_runs: 41,
    },
    hasAccess: true,
    isLoading: false,
  },
  runsData: {
    entries: [runsEntry(701), runsEntry(702)],
    pagination: runsPagination,
    isLoading: false,
    hasAccess: true,
  },
  trendsData: {
    result: trendsResult(),
    isLoading: false,
    hasAccess: true,
    dateRange: "30d",
  },
  complianceData: {
    reports: [complianceReport("rep-1", "complete")],
    filters: {},
    isLoading: false,
    hasAccess: true,
    isGenerating: false,
  },
  dependabotData: {
    data: { prs: [dependabotPR(801)], securityCount: 1, staleCount: 0 },
  },
};

const ALL_TAB_IDS = [
  "overview",
  "pipeline",
  "analytics",
  "history",
  "epics",
  "audit",
  "discovery",
  "cost",
  "health",
  "runs",
  "trends",
  "compliance",
  "dependencies",
];

for (const tabId of ALL_TAB_IDS) {
  writeFixture(`tab-activation--${tabId}`, { ...ALL_POPULATED, activeTab: tabId });
}

console.log(
  `Dashboard fixture matrix: 44 tab/state files + ${ALL_TAB_IDS.length} tab-activation files written to ${FIXTURES_DIR}`
);
