/**
 * IpcClientBase — Abstract base class for the IPC client.
 *
 * Contains all lifecycle management (start/stop/restart), transport (call/on),
 * event emitters, and type definitions. The generated IpcClientGenerated class
 * extends this with typed API methods, and IpcClient extends that with the
 * singleton pattern and manual wrapper methods.
 *
 * @see IpcClient.generated.ts — Auto-generated typed API methods
 * @see IpcClient.ts — Final class with singleton and manual wrappers
 * @see internal/ipc/protocol.go — Go-side protocol definition
 */

import { ChildProcess, spawn } from "child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import { createInterface, Interface as ReadlineInterface } from "readline";
import * as vscode from "vscode";
import { BinaryResolver } from "./BinaryResolver";
import { getActiveCallSource, setActiveCallSource } from "./callSource";
import { getGitHubAuthToken, getGitHubAuthTokens } from "../utils/incrediConfig";
import { SecretStorageService, SECRET_KEYS } from "./SecretStorageService";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** JSON-RPC-style request sent to the Go binary. */
interface IpcRequest {
  id: number;
  method: string;
  params?: unknown;
}

/** JSON-RPC-style response from the Go binary. */
interface IpcResponse {
  id: number;
  result?: unknown;
  error?: { code: number; message: string };
}

/** Unsolicited event from the Go binary. */
interface IpcEvent {
  event: string;
  data?: unknown;
}

/** Pending request waiting for a response. */
interface PendingRequest {
  resolve: (value: unknown) => void;
  reject: (reason: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

/** Event handler callback. */
export type EventHandler = (data: unknown) => void;

// ---------------------------------------------------------------------------
// Workspace types (matches Go internal/ipc/protocol.go Workspace* structs)
// ---------------------------------------------------------------------------

/** Result from workspace.setRoot. */
export interface WorkspaceSetRootResult {
  ok: boolean;
}

/** Result from workspace.registerRepo. */
export interface WorkspaceRegisterRepoResult {
  ok: boolean;
}

/** Result from workspace.configureForgeInstance. */
export interface ConfigureForgeInstanceResult {
  ok: boolean;
  /** Echoes the resolved forge kind (currently "github" | "gitlab"). */
  kind: string;
}

/** Result from notifications.reloadTokens. */
export interface NotificationsReloadTokensResult {
  ok: boolean;
}

/** Result from notifications.checkAuthorization. */
export interface CheckAuthorizationResult {
  allowed: boolean;
  mappedIdentity?: string;
  reason: string;
}

// ---------------------------------------------------------------------------
// Config types (matches Go internal/ipc/protocol.go ConfigGet* structs)
// ---------------------------------------------------------------------------

/** Result from config.getProjectConfig. */
export interface ConfigGetProjectResult {
  owner: string;
  projectNumber: number;
  defaultRepo?: string;
  ownerType?: string;
}

/** Result from config.getHealthThresholds. */
export interface ConfigGetHealthThresholdsResult {
  warningThreshold: number;
  criticalThreshold: number;
  emergencyThreshold: number;
  policiesEnabled: boolean;
  actionsEnabled: boolean;
  feedbackLoopEnabled: boolean;
}

/** One row in the per-key tier audit report (mirrors Go config.TierAuditEntry). */
export interface TierAuditEntry {
  key: string;
  effectiveTier: string; // "machine" | "project" | "local" | "default"
  effectiveSource: string; // absolute file path or "default"
  targetTier: string; // "team" | "machine" | "local" | "runtime" | "unknown"
  status: string; // "OK" | "DRIFT" | "UNCLASSIFIED"
}

/** Result from config.tierAudit. */
export interface ConfigTierAuditResult {
  entries: TierAuditEntry[];
  hasDrift: boolean;
}

// ---------------------------------------------------------------------------
// Board types (matches Go internal/github/board.go output)
// ---------------------------------------------------------------------------

export interface BoardItem {
  id: string;
  number: number;
  title: string;
  state: string;
  status: string;
  priority: string;
  size: string;
  labels: string[];
  assignees: string[];
  repo: string;
  url: string;
  createdAt?: string;
  updatedAt?: string;
  isPR?: boolean;
  isEpic: boolean;
  parentIssueNumber?: number;
  parentIssueTitle?: string;
  blockedBy?: Array<{
    number: number;
    title: string;
    state: string;
    repo?: string;
  }>;
  blocking?: Array<{
    number: number;
    title: string;
    state: string;
    repo?: string;
  }>;
  subIssues?: Array<{
    number: number;
    title: string;
    state: string;
    repo?: string;
  }>;
}

/** Per-status item counts from board.counts (matches Go StatusCounts). */
export interface StatusCounts {
  ready: number;
  inProgress: number;
  inReview: number;
  done: number;
  backlog: number;
}

/** GitHub API rate limit info (matches Go RateLimitInfo). */
export interface RateLimitInfo {
  remaining: number;
  limit: number;
  resetAt: number; // Unix timestamp
}

/** GitHub token scope validation result (matches Go GitHubAuthCheckResult). */
export interface GitHubAuthCheckResult {
  valid: boolean;
  login: string;
  scopes: string[];
  missingScopes: string[];
  orgMemberships: string[];
  resolution: string;
  error?: string;
}

export interface IssueDetail {
  number: number;
  title: string;
  body: string;
  state: string;
  /** GitHub close reason: "COMPLETED" | "NOT_PLANNED" | "REOPENED" (empty for OPEN). */
  stateReason?: string;
  labels: string[];
  assignees: string[];
  url: string;
  id?: string;
  isEpic: boolean;
  parentIssueId?: string;
  parentIssueNumber?: number;
  milestone?: string;
  subIssues?: Array<{ number: number; title: string; state: string }>;
  blockedBy?: Array<{
    number: number;
    title: string;
    state: string;
    repo?: string;
  }>;
  blocking?: Array<{
    number: number;
    title: string;
    state: string;
    repo?: string;
  }>;
}

export interface EpicProgress {
  number: number;
  title: string;
  total: number;
  closed: number;
  open: number;
  percentComplete: number;
  subIssues: Array<{ number: number; title: string; state: string }>;
}

export interface PipelineStatus {
  stage: string;
}

export interface ExecutionInfo {
  id: string;
  issueNumber: number;
  stage: string;
  startedAt: string;
  worktreePath?: string;
}

export interface ComplexityResult {
  score: number;
  reasoning: string;
  confidence: number;
}

export interface ModelRouteResult {
  model: string;
  reasoning: string;
}

export interface FailureClassification {
  category: string;
  retryable: boolean;
  suggestedAction: string;
}

export interface CostEstimate {
  totalTokens: number;
  estimatedCost: number;
  breakdown: Record<string, number>;
}

export interface PlatformStatus {
  mode: string;
  tier?: string;
  message?: string;
}

/**
 * Result of platform.license / platform.validateLicense (matches Go
 * platform.LicenseInfo). Field names are camelCase — the Go struct carries
 * explicit JSON tags (#4156; before that fix the struct had none, so the
 * wire format was PascalCase and these fields were silently `undefined`).
 */
export interface LicenseInfo {
  tier: string;
  valid: boolean;
  /** One of "active"/"expired"/"revoked"/"suspended", or absent when unknown. */
  status?: string;
  machineBound?: boolean;
  machineCount?: number;
  /** ISO 8601, or absent/null for no expiry (community tier). */
  expiresAt?: string | null;
  expiresSoon?: boolean;
}

/** Result of platform.startTrial (matches Go platform.TrialResult). */
export interface TrialResult {
  licenseKey: string;
  tier: string;
  trial: boolean;
  expiresAt: string;
  runAllowance: number;
}

/** Resolved skill from platform.resolveSkill (matches Go platform.CachedSkill). */
export interface CachedSkill {
  stage: string;
  content: string;
  version: string;
  variant: string;
  cachedAt: string;
}

/** Generic status acknowledgment from fire-and-forget IPC methods. */
export interface StatusOK {
  status: string;
}

/** Usage analytics summary from platform.getUsageSummary (matches Go platform.UsageSummaryResult). */
export interface UsageSummaryResult {
  totalRuns: number;
  successRatePct: number;
  totalCostUsd: number;
  totalTokens: number;
  period: string;
}

/** Cost analytics result from platform.getCostAnalytics (matches Go platform.CostAnalyticsResult). */
export interface CostAnalyticsResult {
  totalInputTokens: number;
  totalOutputTokens: number;
  totalTokens: number;
  totalCostUsd: string;
  breakdown: {
    byModel: Array<{ modelId: string; costUsd: string; tokens: number }>;
    byProject: Array<{ projectId: string | null; costUsd: string }>;
    byDay: Array<{ date: string; costUsd: string }>;
  };
}

/** Platform analytics health dimension from GET /v1/analytics/health (#3318). */
export interface AnalyticsHealthDimension {
  name: string;
  score: number;
  label: string;
  findings: Array<{
    severity: "critical" | "high" | "warning" | "info";
    title: string;
    description: string;
    recommendation: string;
    issue_number?: number;
  }>;
}

/** Platform analytics health response from platform.getAnalyticsHealth (#3318). */
export interface AnalyticsHealthResult {
  overall_score: number;
  dimensions: AnalyticsHealthDimension[];
  generated_at: string;
  period_days: number;
  total_runs: number;
}

/** Per-stage detail entry for a pipeline run (#3319). */
export interface RunsStageEntry {
  name: string;
  model: string;
  duration_ms: number;
  input_tokens: number;
  output_tokens: number;
  cost_usd: string;
  retry_count: number;
  failure_category?: string;
}

/** A single run entry from GET /v1/analytics/runs (#3319). */
export interface RunsEntry {
  issue_number: number;
  title: string;
  branch: string;
  outcome: string;
  duration_ms: number;
  total_cost_usd: string;
  started_at: string;
  stages?: RunsStageEntry[];
}

/** Paginated result from platform.getAnalyticsRuns (#3319). */
export interface AnalyticsRunsResult {
  entries: RunsEntry[];
  total_count: number;
  next_cursor?: string;
  has_more: boolean;
}

/** A single time-bucketed trend data point from GET /v1/analytics/trends (#3320). */
export interface TrendEntry {
  date: string;
  successRate: number;
  costPerRun: number;
  totalRuns: number;
}

/** Platform analytics trends result from GET /v1/analytics/trends (#3320). */
export interface AnalyticsTrendsResult {
  current: TrendEntry[];
  previous: TrendEntry[];
  period: string;
}

/** Single compliance report entry in a list result (#3322). */
export interface ComplianceReportEntry {
  id: string;
  reportType: string;
  status: "pending" | "processing" | "ready" | "failed";
  startDate: string;
  endDate: string;
  format: string;
  downloadUrl?: string;
  createdAt: string;
}

/** Result of platform.auditGenerateReport (#3322). */
export interface ComplianceReportResult {
  id: string;
  status: string;
  reportType: string;
  startDate: string;
  endDate: string;
  format: string;
  createdAt: string;
}

/** Paginated list of compliance reports from platform.auditListReports (#3322). */
export interface ComplianceReportsPage {
  reports: ComplianceReportEntry[];
  nextCursor?: string;
  hasMore: boolean;
}

/** Detail of a single compliance report from platform.auditGetReport (#3322). */
export interface ComplianceReportDetail {
  id: string;
  reportType: string;
  status: string;
  startDate: string;
  endDate: string;
  format: string;
  downloadUrl?: string;
  createdAt: string;
}

/** Audit retention configuration from audit.getRetentionConfig (#3323). */
export interface RetentionConfig {
  retentionDays: number;
  updatedAt?: string;
}

/** Result from audit.verifyIntegrity — hash-chain verification result (#3323). */
export interface IntegrityResult {
  valid: boolean;
  checkedCount: number;
  windowDays: number;
  message: string;
  checkedAt: string;
}

/** Result of platform.syncTelemetry (matches Go ipc.PlatformSyncTelemetryResult). */
export interface PlatformSyncTelemetryResult {
  synced: number;
  failed: number;
  errors?: string[];
}

/** Team member entry from platform.getTeamMembers (matches Go platform.TeamMemberResult). */
export interface TeamMemberResult {
  userId: string;
  email: string;
  name?: string;
  role: string;
  joinedAt: string;
}

/** Billing portal session from platform.createPortalSession (matches Go platform.PortalSessionResult). */
export interface PortalSessionResult {
  url: string;
}

/** Platform health response from platform.healthCheck (matches Go api.HealthResponse). */
export interface HealthResponse {
  status: string;
  version: string;
  uptime_seconds: number;
  dependencies: Record<string, { status: string; latency_ms?: number }>;
}

/** Token response from auth.exchangeGitHub / auth.refresh (matches Go api.AuthTokenResponse). */
export interface AuthTokenResponse {
  access_token: string;
  refresh_token: string;
  expires_in: number;
  token_type: string;
  status: string;
  email?: string;
}

/** Device code result from auth.deviceFlowStart (matches Go api.AuthDeviceCodeResult). */
export interface AuthDeviceCodeResult {
  device_code: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete?: string;
  expires_in: number;
  interval: number;
}

/** Poll result from auth.deviceFlowPoll — either authorized (with tokens) or pending. */
export interface AuthDeviceFlowPollResult {
  status: "authorized" | "authorization_pending" | "slow_down";
  access_token?: string;
  refresh_token?: string;
  expires_in?: number;
  token_type?: string;
}

/** IPC-layer queue item (matches Go orchestrator.QueueItem). */
export interface IpcQueueItem {
  repo: string;
  issueNumber: number;
  title: string;
  priority: number;
  status: string;
  labels?: string[];
  blockedBy?: Array<{ number: number; title: string; state: string }>;
  epicOrder?: number;
  isBatch?: boolean;
  epicNumber?: number;
  addedAt: string;
  position: number;
}

/** IPC-layer queue state (matches Go orchestrator.QueueState). */
export interface IpcQueueState {
  schema_version: string;
  status: string;
  items: IpcQueueItem[];
  updated_at: string;
}

export interface RunPipelineResult {
  executionId: string;
  issueNumber: number;
  status: string;
  totalInputTokens?: number;
  totalOutputTokens?: number;
  totalCostUSD?: number;
  perStage?: Array<{
    stage: string;
    inputTokens: number;
    outputTokens: number;
  }>;
}

/**
 * Mattermost slash-command IPC event payload from the Go inbound
 * receiver (`internal/notifications/inbound/dispatcher.go`). Mirrors
 * MattermostSlashEvent on the Go side — the raw webhook fields are
 * preserved and `parsed_command` is the typed PipelineCommand.
 */
export interface MattermostSlashEvent {
  team_id?: string;
  channel_id?: string;
  channel_name?: string;
  user_id?: string;
  user_name?: string;
  command?: string;
  text?: string;
  trigger_word?: string;
  trigger_id?: string;
  response_url?: string;
  parsed_command: MattermostParsedCommand;
}

/** Discriminant tags for MattermostParsedCommand.type. */
export type MattermostCommandType =
  | "status"
  | "run"
  | "pause"
  | "resume"
  | "stop"
  | "queue.add"
  | "queue.remove"
  | "queue.list"
  | "health"
  | "help"
  | "unknown";

export interface MattermostParsedCommand {
  type: MattermostCommandType;
  issue_number?: number;
  repo?: string;
  raw_text: string;
}

/** Matches the Go pipeline.complete event payload from internal/ipc/server.go */
export interface PipelineCompleteEvent {
  executionId: string;
  issueNumber: number;
  success: boolean;
  totalInputTokens: number;
  totalOutputTokens: number;
  /** Always 0 until cost computation is added to the Go layer. */
  totalCostUSD: number;
  perStage: Array<{
    stage: string;
    inputTokens: number;
    outputTokens: number;
  }>;
}

export interface HealthAnalysis {
  overallScore: number;
  dimensions: Record<string, { score: number; findings: string[] }>;
  recommendations: string[];
}

// ---------------------------------------------------------------------------
// Git types (matches Go internal/git/service.go output)
// ---------------------------------------------------------------------------

export interface GitStatusResult {
  isClean: boolean;
  stagedFiles?: Array<{ path: string; status: string }>;
  unstagedFiles?: Array<{ path: string; status: string }>;
  untrackedFiles?: string[];
}

export interface GitLogEntry {
  hash: string;
  message: string;
  author: string;
  date: string;
}

export interface GitCleanupMergedBranchesResult {
  deleted: string[];
  count: number;
}

// ---------------------------------------------------------------------------
// PR list type (matches Go pkg/types PullRequest)
// ---------------------------------------------------------------------------

export interface PullRequestDetail {
  nodeId: string;
  number: number;
  title: string;
  body?: string;
  state: string;
  headRef: string;
  baseRef: string;
  repo: string;
  url: string;
  mergeable?: string;
  reviewStatus?: string;
  checkStatus?: string;
  labels?: string[];
  isDraft: boolean;
  createdAt?: string;
}

// ---------------------------------------------------------------------------
// Remote command types (matches Go internal/ipc/protocol.go Remote* structs)
// ---------------------------------------------------------------------------

/** A single entry from remote.getCommandHistory. */
export interface RemoteCommandHistoryEntry {
  id: string;
  type: string;
  status: "success" | "failure" | "pending";
  receivedAt: string;
  completedAt?: string;
  durationMs?: number;
  error?: string;
}

/** Result from remote.getCommandHistory. */
export interface RemoteGetCommandHistoryResult {
  commands: RemoteCommandHistoryEntry[];
}

/** Result from agent.acknowledgeCommand — runId assigned by the platform. */
export interface AgentAcknowledgeCommandResult {
  runId: string;
}

/** Result from remote.getPollingStatus. */
export interface RemotePollingStatus {
  active: boolean;
  lastPolledAt?: string;
  pendingCount: number;
  errorCount: number;
}

/** Result from epic.readContext — raw epic context JSON. */
export interface EpicContextResult {
  [key: string]: unknown;
}

/** Result from autonomous.* methods — autonomous scheduler state snapshot. */
export interface AutonomousStatusResult {
  status: string;
  /** Why the scheduler is currently paused/safety-tripped (Issue #3251). */
  pauseReason?: string;
  /** Structured tag identifying who/what paused the scheduler (Issue #3251). */
  pauseTriggeredBy?: string;
  /** ISO-8601 timestamp the pause/safety-trip transition was recorded. */
  pausedAt?: string;
  startedAt: string;
  lastScanAt: string;
  running: { repo: string; number: number; title: string; startedAt: string }[];
  completed: {
    repo: string;
    number: number;
    title: string;
    completedAt: string;
  }[];
  failed: {
    repo: string;
    number: number;
    title: string;
    failedAt: string;
    reason?: string;
    /** Number of times this issue has failed — set by the Go scheduler's
     * dedup-on-write path. Legacy rows written before that existed omit
     * this field; readers should treat its absence as 1. */
    attemptCount?: number;
    /** Timestamp of the first failure for this issue. Legacy rows omit. */
    firstFailedAt?: string;
  }[];
  remaining: number;
  tokensSpent: number;
  tokensCeiling: number;
  cyclesRun: number;
  safety?: {
    tripReason?: string;
    consecutiveFailures: number;
    tokensUsed: number;
  };
  /**
   * Issue #3431 — global Anthropic-quota cooldown deadline. ISO-8601 wall-clock
   * time after which the scheduler resumes dispatching. Set when an upstream
   * 5-hour rate-limit bucket is known-exhausted; auto-clears on expiry.
   *
   * Surfaced here (Issue #3446) so the status bar + autonomousRun prompt can
   * render "Autonomous: cooldown until …" instead of the misleading "running".
   */
  quotaCooldownUntil?: string;
  /** Human-readable reason for the active cooldown (Issue #3431/#3446). */
  quotaCooldownReason?: string;
  /**
   * Issue #3446 — per-cycle rejection reasons. When a runCycle is suppressed
   * by an active cooldown the scheduler records `{ "quota-cooldown": 1 }` so
   * the TS output channel can log a "[cooldown] Dispatch suppressed …" line
   * on each blocked scan. Other entries (e.g. "blocked-by-open-dep") are
   * informational diagnostics.
   */
  lastRejectionReasons?: Record<string, number>;
  /**
   * Issue #3640 — config-coherence warnings produced at Run() startup.
   * Each entry: `{ severity: "warn"|"info", kind: string, message: string }`.
   * Absent when no warnings exist (omitempty on the Go side).
   */
  configWarnings?: { severity: string; kind: string; message: string }[];
}

/** One open sub-issue holding a stalled epic back, and why (#4073). */
export interface StuckBlocker {
  number: number;
  title: string;
  reason: string;
}

/**
 * One epic flagged as stalled by the no-silent-stall watchdog (#4073): open
 * with open sub-issues but zero eligible work, no running pipeline, and no
 * sub-issue actively recovering.
 */
export interface StuckEpic {
  repo: string;
  number: number;
  title: string;
  detectedAt: string;
  blockers: StuckBlocker[];
}

/** Result from autonomous.stuckEpics — the most recent idle-scan snapshot. */
export interface StuckEpicsResult {
  stuckEpics: StuckEpic[];
}

/** Result from pipeline.setMaxConcurrent / pipeline.getMaxConcurrent. */
export interface PipelineMaxConcurrentResult {
  maxConcurrent: number;
  persisted: boolean;
}

/**
 * Result from autonomous.clearIssueFailures — number of per-issue lifetime
 * failure counters cleared (#3020).
 */
export interface AutonomousClearIssueFailuresResult {
  cleared: number;
}

/**
 * Result from autonomous.clearQuotaCooldown — Issue #3446.
 *
 * `cleared` is false when no cooldown was active at call time (no-op clear).
 * `previousUntil` is the ISO-8601 deadline that was in effect immediately
 * before the clear — empty when no cooldown was active.
 */
export interface AutonomousClearQuotaCooldownResult {
  cleared: boolean;
  previousUntil?: string;
}

// ============================================================================
// Action Center — DecisionRequest store (ADR 015)
// ============================================================================

/** One machine-actionable choice on a DecisionRequest card. `verb` MUST be in
 * the closed verb registry; `args` are bounded by the request. */
export interface AttentionOption {
  id: string;
  label: string;
  verb: string;
  args?: Record<string, unknown>;
  style?: "primary" | "default" | "danger";
}

/** ADR-013 trace back-reference pinning the node that raised the request. */
export interface AttentionTraceRef {
  run_id: string;
  producer: string;
  seq: number;
}

/** Card context: repo/issue/run/stage/cost + the trace back-reference. */
export interface AttentionContext {
  repo: string;
  issue?: number;
  run_id?: string;
  stage?: string;
  cost_so_far_usd?: number;
  blocker?: string;
  trace_ref?: AttentionTraceRef;
}

/** Lifecycle state machine + audit for one DecisionRequest. */
export interface AttentionLifecycle {
  state: "open" | "acknowledged" | "resolved" | "expired";
  acknowledged?: { actor: string; at: string };
  resolved?: {
    actor: string;
    at: string;
    option_id: string;
    steer_text?: string;
    note?: string;
  };
  expired?: { at: string; applied: string };
}

/** A DecisionRequest — the materialized card the Action Center renders. */
export interface AttentionRequestView {
  schema_version: number;
  id: string;
  idempotency_key: string;
  kind: "unblock" | "approve" | "choose" | "provide_input" | "handoff" | "resume";
  severity: "fyi" | "blocking_run" | "blocking_fleet";
  title: string;
  body: string;
  context: AttentionContext;
  producer: string;
  options: AttentionOption[];
  steer?: { enabled: boolean; hint?: string };
  created_at: string;
  expires_at: string;
  default_action: string;
  lifecycle: AttentionLifecycle;
}

/** Result from attention.list — open (and optionally terminal) requests,
 * ordered most-severe-then-newest. */
export interface AttentionListResult {
  requests: AttentionRequestView[];
}

/** Result from attention.resolve. `ok` is false when the verb side-effect
 * failed (the resolution itself still applied, once). */
export interface AttentionResolveResult {
  ok: boolean;
  alreadyResolved: boolean;
}

/** Result from attention.acknowledge. */
export interface AttentionAcknowledgeResult {
  ok: boolean;
}

/** Payload of the `attention.event` push (created|updated|acknowledged|
 * resolved|expired). */
export interface AttentionEvent {
  action: string;
  request: AttentionRequestView;
}

/**
 * Bridged quota/cooldown snapshot from workflow.quotaState — Issue #3909.
 *
 * Single-sources the Go-side ratelimit/cooldown signals (the GitHub
 * REST/GraphQL bucket from the shared tracker + the global dispatch cooldown
 * from the autonomous scheduler, which covers both the Anthropic 5-hour bucket
 * and the GitHub-quota suspension) so the WorkflowExecutor (#3908) can gate a
 * large fan-out without duplicating any quota arithmetic in TypeScript.
 *
 * `exhausted` is the single derived gate signal: true when a dispatch cooldown
 * is active OR the GitHub bucket is depleted to zero. The executor proceeds
 * when false (a status=allowed stall) and defers until `resetsAt`/
 * `cooldownUntil` when true (genuine exhaustion). `remaining`/`limit` are -1
 * and `resetsAt` is 0 when no tracker reading is available.
 */
export interface WorkflowQuotaStateResult {
  remaining: number;
  limit: number;
  resetsAt: number;
  cooldownUntil?: string;
  cooldownReason?: string;
  bucket?: string;
  exhausted: boolean;
}

/**
 * Result from pipeline.cancelActiveForNetworkOutage — Issue #3296.
 *
 * Returned by the Go scheduler after walking the active-stage map and
 * cancelling each context with cause ErrNetworkUnavailable. Empty array
 * when no stages were running at call time.
 */
export interface CancelActiveForNetworkOutageResult {
  cancelledIssues: number[];
}

/** Result from focus.show, focus.set, focus.clear — current focus state. */
export interface FocusShowResult {
  activeLens: string;
  description: string;
  setAt: string;
  setBy: string;
  boosts: Record<string, number> | null;
  keywords: string[] | null;
}

/** One entry in the forge.list result. */
export interface ForgeListEntry {
  id: string;
  kind: string;
  base_url: string;
  auth_method: string;
  ca_bundle?: string;
}

/** Result from forge.list. */
export interface ForgeListResult {
  forges: ForgeListEntry[];
}

/** Result from forge.connectionTest. */
export interface ForgeConnectionTestResult {
  ok: boolean;
  latency_ms: number;
  version?: string;
  scopes?: string[];
  error?: string;
}

/** Result from focus.list — all available lenses. */
export interface FocusListResult {
  activeLens: string;
  lenses: {
    name: string;
    description: string;
    boosts: Record<string, number> | null;
    keywords: string[] | null;
    builtin: boolean;
    active: boolean;
  }[];
}

// ---------------------------------------------------------------------------
// Knowledge metrics types (matches internal/knowledge/metrics.Result) — #3600
// ---------------------------------------------------------------------------

/** Aggregated knowledge totals for the window. */
export interface KnowledgeMetricsTotals {
  writes: number;
  reads: number;
  recalls: number;
  recall_hits: number;
  graduations: number;
  scaffolds: number;
  prunes: number;
  indexes: number;
  validates: number;
  stats: number;
  events_in_range: number;
}

/** Per-stage row in the bar chart. */
export interface KnowledgeMetricsPerStage {
  stage: string;
  reads: number;
  writes: number;
  recalls: number;
  recall_hits: number;
}

export interface KnowledgeMetricsTopRecalled {
  path: string;
  hits: number;
}

export interface KnowledgeMetricsStaleEntry {
  path: string;
  last_touched_at?: string;
  days_since_touch: number;
}

export interface KnowledgeMetricsGraduationEntry {
  timestamp: string;
  issue_number?: number;
  path?: string;
  mode: string;
}

/**
 * One ranked recall hit returned by knowledge.search and
 * knowledge.relatedToIssue (#2964). Mirrors `recall.RecallHit` in Go.
 */
export interface KnowledgeRecallHit {
  rank: number;
  score: number;
  path: string;
  kind: string;
  issue_number?: number;
  tags?: string[];
  snippet: string;
  graduated?: boolean;
}

/** Result from knowledge.search (#2964). */
export interface KnowledgeSearchResult {
  hits: KnowledgeRecallHit[];
  total_hits: number;
}

/** Result from knowledge.backlinks (#2964). */
export interface KnowledgeBacklinksResult {
  backlinks: string[];
}

/** Result from knowledge.relatedToIssue (#2964). */
export interface KnowledgeRelatedToIssueResult {
  hits: KnowledgeRecallHit[];
}

/** Result from knowledge.metrics — full aggregator payload. */
export interface KnowledgeMetricsResult {
  window_days: number;
  stale_days: number;
  status: "enabled" | "empty" | "disabled";
  generated_at: string;
  hit_rate?: number;
  totals: KnowledgeMetricsTotals;
  per_stage: KnowledgeMetricsPerStage[];
  top_recalled: KnowledgeMetricsTopRecalled[];
  stale_entries: KnowledgeMetricsStaleEntry[];
  graduation_history: KnowledgeMetricsGraduationEntry[];
}

/**
 * Result payload for `diagnostics.recordStageExit` (#3619).
 *
 * Mirrors `internal/ipc.RecordStageExitResult`. `recorded` is true when the
 * Go-side `WriteStageExitRecord` appended a JSONL line to today's
 * `.nightgauge/pipeline/exit-records/<UTC-day>.jsonl` file. False
 * results are reserved for forward-compat — current implementation either
 * returns `true` or surfaces an error via the IPC envelope.
 */
export interface RecordStageExitResult {
  recorded: boolean;
}

// ---------------------------------------------------------------------------
// IpcClientBase
// ---------------------------------------------------------------------------

export abstract class IpcClientBase implements vscode.Disposable {
  protected process: ChildProcess | null = null;
  private readline: ReadlineInterface | null = null;
  private pending = new Map<number, PendingRequest>();
  private eventHandlers = new Map<string, Set<EventHandler>>();
  private nextId = 1;
  private binaryPath: string | null = null;
  private workspaceRoot: string | null = null;
  private resolvedGitHubToken: string | null = null;
  private resolvedTokenSource: string | null = null;
  private resolvedLicenseKey: string | null = null;
  private readonly tokenCache = new Map<string, string>();
  private outputChannel: vscode.OutputChannel | null = null;
  private logFileStream: fs.WriteStream | null = null;
  protected starting = false;
  protected disposed = false;
  private restartTimer: ReturnType<typeof setTimeout> | null = null;
  protected restartAttempts = 0;
  protected readonly maxRestartAttempts = 5;
  private readonly restartBackoffMs = 2000;

  protected readonly _onDidChangeStatus = new vscode.EventEmitter<boolean>();
  readonly onDidChangeStatus = this._onDidChangeStatus.event;

  protected readonly _onPipelineComplete = new vscode.EventEmitter<PipelineCompleteEvent>();
  /** Fires when the Go backend emits a pipeline.complete event. */
  readonly onPipelineComplete = this._onPipelineComplete.event;

  protected readonly _onMattermostCommand = new vscode.EventEmitter<MattermostSlashEvent>();
  /** Fires when the Go backend emits a mattermost.command event. */
  readonly onMattermostCommand = this._onMattermostCommand.event;

  protected constructor() {
    // Output channel is lazy-initialized in log() to avoid calling
    // vscode.window.createOutputChannel during test imports.
    this.on("pipeline.complete", (data) => {
      this._onPipelineComplete.fire(data as PipelineCompleteEvent);
    });
  }

  // -------------------------------------------------------------------------
  // Lifecycle
  // -------------------------------------------------------------------------

  /** Start the Go binary in serve mode. Resolves when ready. */
  async start(): Promise<void> {
    if (this.process || this.starting) {
      return;
    }
    this.starting = true;

    try {
      const path = await this.resolveBinaryPath();
      if (!path) {
        throw new Error("Go binary not found. Install via: brew install nightgauge");
      }
      this.binaryPath = path;
      await this.resolveGitHubToken();
      await this.resolveLicenseKey();
      this.spawnProcess();
      this._onDidChangeStatus.fire(true);
    } finally {
      this.starting = false;
    }
  }

  /** Whether the IPC connection is active. */
  get isConnected(): boolean {
    return this.process !== null && !this.process.killed;
  }

  private spawnProcess(): void {
    if (!this.binaryPath) return;

    const args = ["serve"];
    if (this.workspaceRoot) {
      args.push("--workspace", this.workspaceRoot);
    }
    this.log("Starting Go backend: " + this.binaryPath + " " + args.join(" "));

    const env = { ...process.env };
    // Always use the resolved token — config-based tokens take priority over
    // the GITHUB_TOKEN env var (which is step 3 in the priority chain).
    // When the token was resolved from the env var itself, this is a no-op.
    if (this.resolvedGitHubToken) {
      env.GITHUB_TOKEN = this.resolvedGitHubToken;
    }

    // Forward platform config to Go binary via env vars (Issue #XXXX)
    // The Go binary reads NIGHTGAUGE_PLATFORM_URL, NIGHTGAUGE_API_KEY,
    // and NIGHTGAUGE_LICENSE_KEY as defaults for its --platform-url, --api-key,
    // and --license-key flags. Without these, platformClient stays nil and all
    // platform.* IPC methods return "platform client not configured".
    this.forwardPlatformEnv(env);

    const proc = spawn(this.binaryPath, args, {
      stdio: ["pipe", "pipe", "pipe"],
      env,
    });

    this.process = proc;

    // Read newline-delimited JSON from stdout
    this.readline = createInterface({ input: proc.stdout! });
    this.readline.on("line", (line) => this.handleLine(line));

    // Log stderr for diagnostics
    proc.stderr?.on("data", (chunk: Buffer) => {
      this.log(`[stderr] ${chunk.toString().trimEnd()}`);
    });

    proc.on("error", (err) => {
      this.log(`Process error: ${err.message}`);
      this.handleProcessExit(-1);
    });

    proc.on("exit", (code) => {
      this.log(`Process exited with code ${code}`);
      this.handleProcessExit(code ?? 1);
    });
  }

  private handleLine(line: string): void {
    if (!line.trim()) return;

    let parsed: IpcResponse | IpcEvent;
    try {
      parsed = JSON.parse(line);
    } catch {
      this.log(`Invalid JSON from Go binary: ${line.substring(0, 200)}`);
      return;
    }

    // Unsolicited event (no id)
    if ("event" in parsed && typeof (parsed as IpcEvent).event === "string") {
      const evt = parsed as IpcEvent;
      const handlers = this.eventHandlers.get(evt.event);
      if (handlers) {
        for (const handler of handlers) {
          try {
            handler(evt.data);
          } catch (err) {
            this.log(`Event handler error for ${evt.event}: ${err}`);
          }
        }
      }
      return;
    }

    // Response to a pending request
    const resp = parsed as IpcResponse;
    if (resp.id === undefined) return;

    // Got a valid response — process is healthy, reset restart counter
    this.restartAttempts = 0;

    const pending = this.pending.get(resp.id);
    if (!pending) {
      this.log(`No pending request for id ${resp.id}`);
      return;
    }

    this.pending.delete(resp.id);
    clearTimeout(pending.timer);

    if (resp.error) {
      pending.reject(new Error(`IPC error ${resp.error.code}: ${resp.error.message}`));
    } else {
      pending.resolve(resp.result);
    }
  }

  private handleProcessExit(code: number): void {
    const pendingCount = this.pending.size;
    this.log(
      `Go backend process died (code=${code}, pending_requests=${pendingCount}, ` +
        `restart_attempt=${this.restartAttempts}/${this.maxRestartAttempts})`
    );
    this.cleanup();
    this._onDidChangeStatus.fire(false);

    // Reject all pending requests
    for (const [id, pending] of this.pending) {
      clearTimeout(pending.timer);
      pending.reject(new Error(`Go backend exited with code ${code}`));
      this.pending.delete(id);
    }

    // Auto-restart with backoff (capped at maxRestartAttempts)
    if (!this.disposed && this.restartAttempts < this.maxRestartAttempts) {
      this.restartAttempts++;
      const delay = this.restartBackoffMs * Math.pow(2, this.restartAttempts - 1);
      this.log(
        `Restarting Go backend in ${delay}ms (attempt ${this.restartAttempts}/${this.maxRestartAttempts})`
      );
      this.restartTimer = setTimeout(() => {
        this.start().catch((err) => {
          this.log(`Restart failed: ${err}`);
        });
      }, delay);
    } else if (!this.disposed) {
      this.log(
        "Go backend failed to start after maximum restart attempts. " +
          "Check that GITHUB_TOKEN is available (gh auth login)."
      );
      vscode.window
        .showErrorMessage(
          "Nightgauge: Go backend failed to start after " +
            this.maxRestartAttempts +
            " attempts. Ensure GitHub CLI is authenticated (gh auth login).",
          "Retry",
          "Open Terminal"
        )
        .then((choice) => {
          if (choice === "Retry") {
            this.restartAttempts = 0;
            this.start().catch((err) => this.log(`Manual retry failed: ${err}`));
          } else if (choice === "Open Terminal") {
            const terminal = vscode.window.createTerminal("GitHub Auth");
            terminal.show();
            terminal.sendText("gh auth login");
          }
        });
    }
  }

  private cleanup(): void {
    this.readline?.close();
    this.readline = null;
    this.process = null;
  }

  dispose(): void {
    this.disposed = true;
    if (this.restartTimer) {
      clearTimeout(this.restartTimer);
    }
    for (const [, pending] of this.pending) {
      clearTimeout(pending.timer);
      pending.reject(new Error("IPC client disposed"));
    }
    this.pending.clear();
    if (this.process && !this.process.killed) {
      this.process.kill("SIGTERM");
    }
    this.cleanup();
    this.outputChannel?.dispose();
    this.logFileStream?.end();
    this.logFileStream = null;
    this._onDidChangeStatus.dispose();
    this._onPipelineComplete.dispose();
    this._onMattermostCommand.dispose();
  }

  // -------------------------------------------------------------------------
  // IPC transport
  // -------------------------------------------------------------------------

  /**
   * GitHub API methods that hit GitHub's REST or GraphQL endpoints (via the
   * Go binary). Every call to one of these is logged with timestamp and key
   * params so rate-limit incidents can be traced to their source.
   * #3509 — add visibility before optimizing call frequency.
   */
  private static readonly GITHUB_API_METHODS = new Set([
    "board.list",
    "board.counts",
    "board.updateStatus",
    "issue.view",
    "issue.viewMany",
    "issue.create",
    "issue.createSubIssue",
    "issue.linkSubIssue",
    "issue.close",
    "issue.reopen",
    "pr.view",
    "pr.create",
    "epic.progress",
    "epic.checkCompletion",
    "epic.transitionStatus",
    "github.rateLimit",
  ]);

  /**
   * Caller label for the next GitHub-API call(s), e.g. "stall-watchdog" (#360).
   * Backed by the standalone callSource module so call sites can tag fetches
   * (via `withCallSource`) without importing this heavily-mocked module. Kept as
   * a delegating accessor so existing setters (`IpcClientBase.activeCallSource =
   * "user-refresh"`) keep working.
   */
  static get activeCallSource(): string | undefined {
    return getActiveCallSource();
  }
  static set activeCallSource(source: string | undefined) {
    setActiveCallSource(source);
  }

  /** Send a request and await a typed response. */
  async call<T>(method: string, params?: unknown): Promise<T> {
    if (IpcClientBase.GITHUB_API_METHODS.has(method)) {
      const source = getActiveCallSource() ?? "unknown";
      const ts = new Date().toISOString();
      const keyParams = this.summariseGithubCallParams(method, params);
      this.log(`[GITHUB-API] ${ts} ${method}${keyParams} src=${source}`);
    }

    if (!this.isConnected) {
      await this.start();
    }

    if (!this.process?.stdin?.writable) {
      throw new Error("Go backend not connected");
    }

    const id = this.nextId++;
    const request: IpcRequest = { id, method };
    if (params !== undefined) {
      request.params = params;
    }

    return new Promise<T>((resolve, reject) => {
      const timeoutMs = this.getTimeoutMs();
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`IPC request ${method} timed out after ${timeoutMs}ms`));
      }, timeoutMs);

      this.pending.set(id, {
        resolve: resolve as (value: unknown) => void,
        reject,
        timer,
      });

      const json = JSON.stringify(request) + "\n";
      this.process!.stdin!.write(json, (err) => {
        if (err) {
          this.pending.delete(id);
          clearTimeout(timer);
          reject(new Error(`Failed to write to Go backend: ${err.message}`));
        }
      });
    });
  }

  // -------------------------------------------------------------------------
  // Event subscription
  // -------------------------------------------------------------------------

  /** Extract key identifiers from GitHub API call params for concise log lines. */
  private summariseGithubCallParams(method: string, params: unknown): string {
    if (!params || typeof params !== "object") return "";
    const p = params as Record<string, unknown>;
    const parts: string[] = [];
    if (p.owner) parts.push(`owner=${p.owner}`);
    if (p.repo) parts.push(`repo=${p.repo}`);
    if (p.projectNumber) parts.push(`proj=${p.projectNumber}`);
    if (p.status) parts.push(`status=${p.status}`);
    if (p.number !== undefined) parts.push(`#${p.number}`);
    if (p.numbers && Array.isArray(p.numbers)) parts.push(`numbers=[${p.numbers.join(",")}]`);
    if (method === "board.updateStatus" && p.itemId) parts.push(`item=${p.itemId}`);
    return parts.length > 0 ? ` (${parts.join(" ")})` : "";
  }

  /** Subscribe to unsolicited events from the Go binary. */
  on(event: string, handler: EventHandler): { dispose: () => void } {
    let handlers = this.eventHandlers.get(event);
    if (!handlers) {
      handlers = new Set();
      this.eventHandlers.set(event, handlers);
    }
    handlers.add(handler);
    return { dispose: () => handlers!.delete(handler) };
  }

  // -------------------------------------------------------------------------
  // Helpers
  // -------------------------------------------------------------------------

  /**
   * Resolve a GitHub token for a specific gh CLI user.
   * Uses an in-memory cache to avoid repeated subprocess calls.
   */
  protected async resolveGitHubTokenForUser(user: string): Promise<string | null> {
    if (this.tokenCache.has(user)) {
      return this.tokenCache.get(user)!;
    }
    try {
      const { execFile } = await import("child_process");
      const { promisify } = await import("util");
      const execFileAsync = promisify(execFile);
      const { stdout } = await execFileAsync("gh", ["auth", "token", "--user", user], {
        timeout: 5000,
      });
      const token = stdout.trim();
      if (token) {
        this.tokenCache.set(user, token);
        return token;
      }
    } catch {
      this.log(`WARNING: gh auth token --user ${user} failed. Check: gh auth status`);
    }
    return null;
  }

  /**
   * Resolve the GitHub user for the current workspace from config files.
   * Reads github_user (per-repo) and github_auth.users (global fallback)
   * using the same simple YAML line-scanning pattern as forwardPlatformEnv.
   * Priority: per-repo github_user > github_auth.users[owner] > null
   */
  private resolveGitHubUserFromConfig(): string | null {
    const fs = require("fs") as typeof import("fs");
    const path = require("path") as typeof import("path");
    const os = require("os") as typeof import("os");

    const configPaths: string[] = [];
    if (this.workspaceRoot) {
      configPaths.push(path.join(this.workspaceRoot, ".nightgauge", "config.yaml"));
    }
    configPaths.push(path.join(os.homedir(), ".nightgauge", "config.yaml"));

    let githubUser: string | null = null;
    let owner: string | null = null;
    const authUsers: Record<string, string> = {};

    for (const configPath of configPaths) {
      if (!fs.existsSync(configPath)) continue;
      try {
        const content = fs.readFileSync(configPath, "utf-8");
        const lines = content.split("\n");
        let inGithubAuth = false;
        let inUsers = false;

        for (const line of lines) {
          const trimmed = line.trim();

          // github_user: only from workspace config (first path)
          if (
            trimmed.startsWith("github_user:") &&
            this.workspaceRoot &&
            configPath.startsWith(this.workspaceRoot)
          ) {
            const val = trimmed.replace("github_user:", "").trim();
            if (val) githubUser = val;
          }

          if (trimmed.startsWith("owner:") && !owner) {
            const val = trimmed.replace("owner:", "").trim();
            if (val) owner = val;
          }

          if (trimmed === "github_auth:") {
            inGithubAuth = true;
            continue;
          }
          if (inGithubAuth && trimmed === "users:") {
            inUsers = true;
            continue;
          }
          if (inUsers) {
            if (trimmed === "" || /^\S/.test(line)) {
              inUsers = false;
              inGithubAuth = false;
              continue;
            }
            const match = trimmed.match(/^(\S+):\s*(.+)$/);
            if (match) {
              authUsers[match[1]] = match[2].trim();
            }
          }
        }
      } catch {
        // Config read failure is non-fatal
      }
    }

    if (githubUser) return githubUser;
    if (owner && authUsers[owner]) return authUsers[owner];
    return null;
  }

  /**
   * Resolve a GitHub token from config.yaml (github_auth.token or github_auth.tokens[owner]).
   *
   * Priority:
   *   1. github_auth.token (project-level PAT, supports env:VAR_NAME)
   *   2. github_auth.tokens[owner] (per-org mapping, supports env:VAR_NAME)
   *
   * Returns { token, source } or null if no config token is found.
   *
   * @see Issue #2670 - Config-based token resolution
   */
  private resolveTokenFromConfig(): { token: string; source: string } | null {
    // 1. Project-level token from github_auth.token
    const directToken = getGitHubAuthToken(this.workspaceRoot ?? undefined);
    if (directToken) {
      return { token: directToken, source: "config (github_auth.token)" };
    }

    // 2. Per-org token from github_auth.tokens[owner]
    const tokensMap = getGitHubAuthTokens(this.workspaceRoot ?? undefined);
    const owner = this.resolveOwnerFromConfig();
    if (owner && tokensMap[owner]) {
      return { token: tokensMap[owner], source: `config (github_auth.tokens.${owner})` };
    }

    // Check all available org tokens if no specific owner found
    const entries = Object.entries(tokensMap);
    if (entries.length > 0) {
      const [firstOwner, firstToken] = entries[0];
      return { token: firstToken, source: `config (github_auth.tokens.${firstOwner})` };
    }

    return null;
  }

  /**
   * Resolve the project owner from config.yaml (the `owner:` field).
   */
  private resolveOwnerFromConfig(): string | null {
    const fsModule = require("fs") as typeof import("fs");
    const pathModule = require("path") as typeof import("path");
    const os = require("os") as typeof import("os");

    const configPaths: string[] = [];
    if (this.workspaceRoot) {
      configPaths.push(pathModule.join(this.workspaceRoot, ".nightgauge", "config.yaml"));
    }
    configPaths.push(pathModule.join(os.homedir(), ".nightgauge", "config.yaml"));

    for (const configPath of configPaths) {
      if (!fsModule.existsSync(configPath)) continue;
      try {
        const content = fsModule.readFileSync(configPath, "utf-8");
        for (const line of content.split("\n")) {
          const trimmed = line.trim();
          if (trimmed.startsWith("owner:")) {
            const val = trimmed.replace("owner:", "").trim();
            if (val) return val;
          }
        }
      } catch {
        // non-fatal
      }
    }
    return null;
  }

  /**
   * Resolve GITHUB_TOKEN from config, environment, or `gh auth token`.
   * VSCode's extension host often lacks shell profile env vars.
   *
   * Priority chain (first non-null wins):
   *   1. github_auth.token in config.yaml (supports env:VAR_NAME)
   *   2. github_auth.tokens[owner] in config.yaml (supports env:VAR_NAME)
   *   3. GITHUB_TOKEN environment variable
   *   4. Per-repo github_user → gh auth token --user X
   *   5. gh auth token (default user)
   *
   * @see Issue #2670 - Config-based token resolution
   */
  private async resolveGitHubToken(): Promise<void> {
    // 1 & 2: Check config.yaml for github_auth.token / github_auth.tokens
    const configResult = this.resolveTokenFromConfig();
    if (configResult) {
      this.resolvedGitHubToken = configResult.token;
      this.resolvedTokenSource = configResult.source;
      this.log(`[IpcClientBase] Resolved GITHUB_TOKEN from ${configResult.source}`);
      return;
    }

    // 3: Already in environment
    if (process.env.GITHUB_TOKEN) {
      this.resolvedGitHubToken = process.env.GITHUB_TOKEN;
      this.resolvedTokenSource = "env (GITHUB_TOKEN)";
      this.log("[IpcClientBase] Resolved GITHUB_TOKEN from environment");
      return;
    }

    // 4: Check per-repo config for github_user mapping
    const githubUser = this.resolveGitHubUserFromConfig();
    if (githubUser) {
      const token = await this.resolveGitHubTokenForUser(githubUser);
      if (token) {
        this.resolvedGitHubToken = token;
        this.resolvedTokenSource = `gh (--user ${githubUser})`;
        this.log(`[IpcClientBase] Obtained GITHUB_TOKEN via gh auth token --user ${githubUser}`);
        return;
      }
      // Warn but continue to fallback
      vscode.window.showWarningMessage(
        `Nightgauge: Failed to resolve token for GitHub user "${githubUser}". ` +
          `Run: gh auth login --user ${githubUser}`
      );
    }

    // 5: Try gh CLI (default active user)
    try {
      const { exec } = await import("child_process");
      const { promisify } = await import("util");
      const execAsync = promisify(exec);
      const { stdout } = await execAsync("gh auth token", { timeout: 5000 });
      const token = stdout.trim();
      if (token) {
        this.resolvedGitHubToken = token;
        this.resolvedTokenSource = "gh (default user)";
        this.log("[IpcClientBase] Obtained GITHUB_TOKEN via gh auth token");
        return;
      }
    } catch {
      // gh not installed or not authenticated
    }

    this.log(
      "WARNING: GITHUB_TOKEN not found in config, environment, or gh auth token. " +
        "The Go backend will fail to start. Run: gh auth login"
    );

    // Show user-facing notification with action button
    vscode.window
      .showWarningMessage(
        "Nightgauge: GitHub authentication required. " +
          'Run "gh auth login" in your terminal to authenticate.',
        "Open Terminal"
      )
      .then((choice) => {
        if (choice === "Open Terminal") {
          const terminal = vscode.window.createTerminal("GitHub Auth");
          terminal.show();
          terminal.sendText("gh auth login");
        }
      });
  }

  /**
   * Get the resolved token source for debugging/display purposes.
   * Returns null if token resolution hasn't been performed yet.
   */
  get tokenSource(): string | null {
    return this.resolvedTokenSource;
  }

  /** Read the license key from VSCode SecretStorage (OS keychain). */
  private async resolveLicenseKey(): Promise<void> {
    if (process.env.NIGHTGAUGE_LICENSE_KEY) {
      this.resolvedLicenseKey = process.env.NIGHTGAUGE_LICENSE_KEY;
      this.log("[IpcClientBase] Using NIGHTGAUGE_LICENSE_KEY from environment");
      return;
    }
    const svc = SecretStorageService.getInstance();
    if (!svc) return;
    const key = await svc.getSecret(SECRET_KEYS.platformLicenseKey);
    if (key) {
      this.resolvedLicenseKey = key;
      this.log("[IpcClientBase] Resolved license key from SecretStorage");
    }
  }

  private async resolveBinaryPath(): Promise<string | null> {
    // Resolve workspace root (for --workspace flag, not for binary location)
    this.workspaceRoot = this.resolveWorkspaceRoot();

    return BinaryResolver.fromVSCode().resolve();
  }

  /**
   * Resolve the workspace root containing .nightgauge/config.yaml.
   * This is passed to the Go binary via --workspace flag.
   */
  private resolveWorkspaceRoot(): string | null {
    const { existsSync } = require("fs");
    const { join } = require("path");
    for (const folder of vscode.workspace.workspaceFolders ?? []) {
      const configPath = join(folder.uri.fsPath, ".nightgauge", "config.yaml");
      if (existsSync(configPath)) {
        return folder.uri.fsPath;
      }
    }
    // Fall back to first workspace folder
    return vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? null;
  }

  /**
   * Forward platform configuration to the Go binary via environment variables.
   *
   * Reads platform.api_url, platform.license_key from config YAML files
   * (workspace-level first, then global ~/.nightgauge/config.yaml) and
   * sets the corresponding NIGHTGAUGE_* env vars so the Go binary
   * initializes its platform client.
   *
   * Env vars already set in the process environment take precedence.
   */
  private forwardPlatformEnv(env: Record<string, string | undefined>): void {
    const fs = require("fs") as typeof import("fs");
    const path = require("path") as typeof import("path");
    const os = require("os") as typeof import("os");

    // Inject license key from SecretStorage (resolved asynchronously in start()).
    // This takes priority over any YAML-based value.
    if (this.resolvedLicenseKey && !env.NIGHTGAUGE_LICENSE_KEY) {
      env.NIGHTGAUGE_LICENSE_KEY = this.resolvedLicenseKey;
      this.log("Platform config: NIGHTGAUGE_LICENSE_KEY set from SecretStorage");
    }

    // Collect candidate config paths: workspace first, global second
    const configPaths: string[] = [];
    if (this.workspaceRoot) {
      configPaths.push(path.join(this.workspaceRoot, ".nightgauge", "config.yaml"));
    }
    const globalConfig = path.join(os.homedir(), ".nightgauge", "config.yaml");
    configPaths.push(globalConfig);

    // Simple YAML key extraction — avoids importing a YAML parser in the
    // critical startup path. Handles:
    //   platform:
    //     api_url: https://...
    //     api_key: ...
    // Note: license_key is no longer read from YAML (migrated to SecretStorage).
    const envMap: Record<string, { yamlKey: string; envKey: string }> = {
      api_url: {
        yamlKey: "api_url",
        envKey: "NIGHTGAUGE_PLATFORM_URL",
      },
      api_key: {
        yamlKey: "api_key",
        envKey: "NIGHTGAUGE_API_KEY",
      },
    };

    for (const configPath of configPaths) {
      if (!fs.existsSync(configPath)) continue;

      try {
        const content = fs.readFileSync(configPath, "utf-8");
        const lines = content.split("\n");
        let inPlatform = false;

        for (const line of lines) {
          const trimmed = line.trim();

          // Detect platform: top-level section
          if (trimmed === "platform:") {
            inPlatform = true;
            continue;
          }

          // Exit platform section on new top-level key (not indented)
          if (
            inPlatform &&
            trimmed &&
            !trimmed.startsWith("#") &&
            /^[a-z_]+:/.test(trimmed) &&
            !line.startsWith(" ") &&
            !line.startsWith("\t")
          ) {
            inPlatform = false;
            continue;
          }

          if (!inPlatform) continue;

          const match = trimmed.match(/^([a-z_]+):\s*(.+)$/);
          if (!match) continue;
          const [, key, rawValue] = match;
          const value = rawValue.replace(/^['"]|['"]$/g, "").trim();

          const mapping = envMap[key];
          if (mapping && value && !env[mapping.envKey]) {
            env[mapping.envKey] = value;
            this.log(`Platform config: ${mapping.envKey} set from ${configPath}`);
          }
        }
      } catch (err) {
        this.log(`Warning: failed to read platform config from ${configPath}: ${err}`);
      }
    }
  }

  private getTimeoutMs(): number {
    const config = vscode.workspace.getConfiguration("nightgauge.backend");
    return config.get<number>("timeoutSeconds", 30) * 1000;
  }

  protected log(message: string): void {
    const line = `[${new Date().toISOString()}] ${message}`;
    if (!this.outputChannel) {
      try {
        this.outputChannel = vscode.window.createOutputChannel("Nightgauge Go Backend");
      } catch {
        // Not in a VS Code host (e.g. tests)
        console.log(line);
        return;
      }
    }
    this.outputChannel.appendLine(line);

    // Also write to persistent log file so diagnostics survive extension reloads
    this.writeToLogFile(line);
  }

  /**
   * Write a log line to the persistent IPC log file.
   * The file is created lazily in .nightgauge/logs/ipc-client.log
   * and survives extension reloads / output channel disposal.
   */
  private writeToLogFile(line: string): void {
    if (!this.logFileStream && this.workspaceRoot) {
      try {
        // Skip persistent logging in uninitialized repos. Creating
        // .nightgauge/logs/ here would re-spawn the scaffolding we
        // intentionally avoid before /nightgauge:repo-init has run —
        // the output channel still captures the same content.
        const configPath = path.join(this.workspaceRoot, ".nightgauge", "config.yaml");
        if (!fs.existsSync(configPath)) {
          return;
        }
        const logDir = path.join(this.workspaceRoot, ".nightgauge", "logs");
        fs.mkdirSync(logDir, { recursive: true });
        const logPath = path.join(logDir, "ipc-client.log");

        // Rotate: if over 5 MB, truncate to last 1 MB
        try {
          const stat = fs.statSync(logPath);
          if (stat.size > 5 * 1024 * 1024) {
            const data = fs.readFileSync(logPath);
            const keep = data.subarray(data.length - 1024 * 1024);
            fs.writeFileSync(logPath, keep);
          }
        } catch {
          // File doesn't exist yet — fine
        }

        this.logFileStream = fs.createWriteStream(logPath, { flags: "a" });
      } catch {
        // Can't create log file — skip silently
        return;
      }
    }
    this.logFileStream?.write(line + "\n");
  }
}
