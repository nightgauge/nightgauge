/**
 * Marketing capture — the run data every rendered asset is built from.
 *
 * This is NOT invented. `RUN_338` is a pipeline run that closed issue #338
 * of a private Flutter app the maintainers build with Nightgauge, on
 * 2026-08-29 (PR #351), copied from the run record the pipeline wrote to
 * `.nightgauge/pipeline/history/2026-08-29.jsonl`:
 * stage durations, per-stage cost, token totals and the cache-hit ratio are
 * the real numbers. The sibling runs are today's real merges in the workspace
 * (issue and PR numbers are real); their costs are representative of the
 * size class, not copied from a record.
 *
 * Keep this file the single source so the Discord card, the Slack card and
 * the dashboard frames all agree with each other — a reader comparing the
 * three must see one run, not three stories.
 */

export interface StageRecord {
  status: "complete" | "running" | "failed" | "pending";
  duration_ms: number;
  cost_usd: number;
  model?: string;
  execution_path: "deterministic" | "llm";
  cache_read?: number;
  input?: number;
  output?: number;
  cache_creation?: number;
}

/**
 * The repository is identified by a neutral placeholder: the app that
 * produced the run is a private downstream workspace, and its name is not
 * published from this tree. Override for a private render with
 * `NIGHTGAUGE_MARKETING_REPO=owner/name`.
 */
export const REPO_SLUG = process.env.NIGHTGAUGE_MARKETING_REPO ?? "acme/flutter-app";
export const REPO_NAME = REPO_SLUG.split("/").pop() ?? REPO_SLUG;

/** 83m 46s — the wall clock the pipeline reported for #338. */
export const RUN_338_DURATION_MS = 5_026_000;

export type StageName =
  | "issue-pickup"
  | "feature-planning"
  | "feature-dev"
  | "feature-validate"
  | "pr-create"
  | "pr-merge";

const STAGES_338: Record<StageName, StageRecord> = {
  "issue-pickup": {
    status: "complete",
    duration_ms: 1_111,
    cost_usd: 0,
    execution_path: "deterministic",
  },
  "feature-planning": {
    status: "complete",
    duration_ms: 75_982,
    cost_usd: 0.4084046,
    model: "claude-sonnet-5",
    execution_path: "llm",
    input: 22,
    output: 4_911,
    cache_read: 537_013,
    cache_creation: 62_962,
  },
  "feature-dev": {
    status: "complete",
    duration_ms: 1_330_970,
    cost_usd: 2.3756648,
    model: "claude-fable-5",
    execution_path: "llm",
    input: 164,
    output: 22_330,
    cache_read: 8_234_904,
    cache_creation: 126_264,
  },
  "feature-validate": {
    status: "complete",
    duration_ms: 2_138_408,
    cost_usd: 2.5077744,
    model: "claude-fable-5",
    execution_path: "llm",
    input: 182,
    output: 33_517,
    cache_read: 8_341_862,
    cache_creation: 125_967,
  },
  "pr-create": {
    status: "complete",
    duration_ms: 3_136,
    cost_usd: 0,
    execution_path: "deterministic",
  },
  "pr-merge": {
    status: "complete",
    duration_ms: 525_778,
    cost_usd: 1.4265758,
    model: "claude-fable-5",
    execution_path: "llm",
    input: 90,
    output: 16_562,
    cache_read: 4_068_699,
    cache_creation: 111_759,
  },
};

export const RUN_338 = {
  issue_number: 338,
  title: "Guest auth state: remove the forced-login router gate",
  branch: "feat/338-guest-auth-state-remove-the-forced-login-router-ga",
  base_branch: "main",
  pr_number: 351,
  pr_url: `https://github.com/${REPO_SLUG}/pull/351`,
  outcome_type: "productive",
  complexity: "M",
  file_count: 10,
  budget_estimate_usd: 4.46,
  health_score: 89,
  performance_mode: "frontier" as const,
  model: "claude-fable-5",
  tokens: {
    total_input: 21_182_936,
    total_output: 77_320,
    total_cache_read: 21_182_478,
    total_cache_creation: 426_952,
    estimated_cost_usd: 6.7184196,
  },
  stages: STAGES_338,
};

export interface SiblingRun {
  repoName: string;
  issueNumber: number;
  title: string;
  branch: string;
  prNumber?: number;
  status: "complete" | "running" | "failed";
  currentStage?: string;
  costUsd: number;
  durationMs: number;
  startedAgoMs: number;
  sizeLabel: string;
  model: string;
}

/** Today's other workspace merges — real issue/PR numbers, representative cost. */
export const SIBLING_RUNS: SiblingRun[] = [
  {
    repoName: "nightgauge",
    issueNumber: 1105,
    title: "Give preserved WIP refs a reader and a lifecycle",
    branch: "fix/1105-wip-ref-lifecycle",
    prNumber: 1130,
    status: "complete",
    costUsd: 3.92,
    durationMs: 2_940_000,
    startedAgoMs: 3 * 3_600_000,
    sizeLabel: "M",
    model: "claude-fable-5",
  },
  {
    repoName: "nightgauge",
    issueNumber: 1114,
    title: "Verification is a required heading for every issue type",
    branch: "feat/1114-verification-heading",
    prNumber: 1114,
    status: "complete",
    costUsd: 8.41,
    durationMs: 5_460_000,
    startedAgoMs: 6 * 3_600_000,
    sizeLabel: "L",
    model: "claude-fable-5",
  },
  {
    repoName: "nightgauge",
    issueNumber: 1128,
    title: "Report a long-silent polled task instead of guessing it is wedged",
    branch: "fix/1128-silent-polled-task",
    prNumber: 1128,
    status: "complete",
    costUsd: 1.87,
    durationMs: 1_620_000,
    startedAgoMs: 9 * 3_600_000,
    sizeLabel: "S",
    model: "claude-sonnet-5",
  },
  {
    repoName: "nightgauge",
    issueNumber: 1123,
    title: "A refusal delivered as a closed socket is not a test failure",
    branch: "fix/1123-closed-socket-refusal",
    prNumber: 1125,
    status: "complete",
    costUsd: 2.14,
    durationMs: 1_980_000,
    startedAgoMs: 26 * 3_600_000,
    sizeLabel: "S",
    model: "claude-sonnet-5",
  },
  {
    repoName: "nightgauge",
    issueNumber: 1096,
    title: "Settings webview: notification channel test buttons",
    branch: "feat/1096-notification-test-buttons",
    status: "running",
    currentStage: "feature-dev",
    costUsd: 1.12,
    durationMs: 1_140_000,
    startedAgoMs: 1_140_000,
    sizeLabel: "M",
    model: "claude-fable-5",
  },
];
