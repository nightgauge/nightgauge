/**
 * Platform API type contracts for the Nightgauge VSCode extension.
 *
 * Plain TypeScript types for all platform API request/response shapes,
 * hand-maintained locally in this file. These are NOT re-exports of
 * `@nightgauge/shared-types` — that package has never been successfully
 * published (no run of the platform repo's release workflow has ever
 * published it to a resolvable registry) and the dependency was removed from
 * this package as unused/unresolvable dead weight (see issue #3900 / PR
 * #3952). Re-adding a `file:` path to a sibling `acme-platform`
 * checkout is not a fix either — that was the exact non-portable, CI-breaking
 * pattern #3952 eliminated. Until the platform repo publishes a genuinely
 * resolvable package, this file is the single canonical local source for
 * these shapes: every consumer in this package imports `Tier` /
 * `LicenseStatus` from here, not from a second hand-rolled copy (verified
 * with a repo-wide public-contract search —
 * no duplicate definitions were found).
 *
 * `api/generated/ts/platform-api.ts` (openapi-typescript output, regenerated
 * via `npm run generate:types`) independently defines a matching
 * `components["schemas"]["Tier"]` enum, but that file lives outside this
 * package's `rootDir` (`./src`) and importing it would require a tsconfig
 * change — tracked separately rather than folded into this pass. Its
 * `LicenseValidateBody.status` field is a bare `string` (not a literal
 * union), so aliasing `LicenseStatus` to it would be a strictness downgrade,
 * not an improvement.
 *
 * Source of truth chain (aspirational, not yet real):
 *   platform tRPC → OpenAPI spec → api/openapi.yaml → generated TS types
 *
 * @see Issue #2091 - Remove Zod schemas, consolidate against OpenAPI spec
 * @see Issue #1456 - Define platform API type contracts aligned with shared-types
 * @see Issue #3900 / PR #3952 - Removed the unused, unpublishable shared-types dependency
 * @see ../../../../docs/ECOSYSTEM.md - Public integration contract
 */

// ============================================================================
// Section 1 — Tier / Domain
// ============================================================================

/**
 * Subscription tier available on the Nightgauge platform.
 * Matches `components["schemas"]["Tier"]` in api/generated/ts/platform-api.ts
 * (not currently imported from there — see file header).
 */
export type Tier = "community" | "pro" | "team" | "enterprise";

/** Billing cycle for a subscription plan. */
export type BillingCycle = "monthly" | "annual";

// ============================================================================
// Section 2 — Error Types
// ============================================================================

export type ValidationErrorCode = "VALIDATION_ERROR" | "MISSING_REQUIRED_FIELD" | "INVALID_FORMAT";

export type AuthErrorCode = "UNAUTHORIZED" | "TOKEN_EXPIRED" | "INVALID_TOKEN" | "FORBIDDEN";

export type ApiLicenseErrorCode =
  | "LICENSE_EXPIRED"
  | "LICENSE_INVALID"
  | "LICENSE_REVOKED"
  | "LICENSE_TIER_EXCEEDED"
  | "LICENSE_MACHINE_LIMIT";

export type RateLimitErrorCode = "RATE_LIMIT_EXCEEDED" | "QUOTA_EXCEEDED";

export type ServerErrorCode = "INTERNAL_ERROR" | "DEPENDENCY_ERROR" | "SERVICE_UNAVAILABLE";

/** Input validation error returned by the platform API. kind: 'validation' */
export interface ValidationError {
  kind: "validation";
  code: ValidationErrorCode;
  message: string;
  statusCode: 400 | 422;
}

/** Authentication/authorization error returned by the platform API. kind: 'auth' */
export interface AuthError {
  kind: "auth";
  code: AuthErrorCode;
  message: string;
  statusCode: 401 | 403;
}

/** License validation error returned by the platform API. kind: 'license' */
export interface LicenseError {
  kind: "license";
  code: ApiLicenseErrorCode;
  message: string;
  statusCode: 402 | 403;
}

/** Rate limit error returned by the platform API. kind: 'rateLimit' */
export interface RateLimitError {
  kind: "rateLimit";
  code: RateLimitErrorCode;
  message: string;
  statusCode: 429;
}

/** Server-side error returned by the platform API. kind: 'server' */
export interface ServerError {
  kind: "server";
  code: ServerErrorCode;
  message: string;
  statusCode: 500 | 503;
}

/**
 * Discriminated union of all platform API error types.
 * Narrow using the `kind` field.
 */
export type ApiError = ValidationError | AuthError | LicenseError | RateLimitError | ServerError;

/**
 * Runtime type guard for ApiError.
 * Structural check on the `kind` discriminant field.
 */
export function isApiError(err: unknown): err is ApiError {
  if (typeof err !== "object" || err === null) return false;
  const obj = err as Record<string, unknown>;
  return (
    typeof obj.kind === "string" &&
    ["validation", "auth", "license", "rateLimit", "server"].includes(obj.kind) &&
    typeof obj.code === "string" &&
    typeof obj.message === "string" &&
    typeof obj.statusCode === "number"
  );
}

// ============================================================================
// Section 3 — Skill Types
// ============================================================================

/** A versioned prompt template targeting a specific model. */
export interface SkillVariant {
  id: string;
  model: string;
  promptTemplate: string;
  version: string;
}

/** A skill available on the Nightgauge platform. */
export interface Skill {
  id: string;
  name: string;
  description: string;
  requiredTier: Tier;
  variants: SkillVariant[];
}

/** The resolved output of a skill lookup. */
export interface SkillResolution {
  skillId: string;
  variantId: string;
  model: string;
  resolvedContent: string;
  version: string;
}

/** Request payload for POST /v1/skills/resolve. */
export interface SkillResolveRequest {
  skillId: string;
  model: string;
  context?: Record<string, string>;
}

/** Response from POST /v1/skills/resolve. */
export interface SkillResolveResponse {
  resolution: SkillResolution;
  /** ISO 8601 */
  resolvedAt: string;
}

// ============================================================================
// Section 4 — License Types
// ============================================================================

export type LicenseKeyFormat = "live" | "test" | "ci";

export type LicenseStatus = "active" | "expired" | "revoked" | "suspended";

/** Result returned by POST /v1/license/validate. */
export interface LicenseValidationResult {
  valid: boolean;
  status: LicenseStatus;
  tier: Tier;
  /** ISO 8601 expiry timestamp, or null for community tier. */
  expiresAt: string | null;
  expiresSoon: boolean;
  machineBound: boolean;
  machineCount: number;
}

/** Request body for POST /v1/license/validate. */
export interface LicenseValidateRequest {
  key: string;
  machineId?: string;
  hostname?: string;
  platform?: string;
}

/** Response from POST /v1/license/validate. */
export type LicenseValidateResponse = LicenseValidationResult;

// ============================================================================
// Section 5 — OAuth / Auth Types
// ============================================================================

/** Response from POST /v1/auth/device/code. */
export interface OAuthDeviceCodeResponse {
  device_code: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete?: string;
  /** Seconds until device code expires. */
  expires_in: number;
  /** Seconds between polls. */
  interval: number;
}

/** Successful token response from POST /v1/auth/device/token. */
export interface OAuthTokenResponse {
  access_token: string;
  refresh_token: string;
  token_type: "Bearer";
  /** Seconds until access_token expires. */
  expires_in: number;
}

/** Pending or rate-limit response from POST /v1/auth/device/token. */
export interface OAuthPendingResponse {
  status: "authorization_pending" | "slow_down";
}

/** Terminal error response from POST /v1/auth/device/token. */
export interface OAuthPollError {
  status: "expired_token" | "access_denied";
}

/** Input body for POST /v1/auth/device/token. */
export interface OAuthDeviceTokenInput {
  device_code: string;
}

/** Request body for POST /v1/auth/token (refresh token grant). */
export interface OAuthRefreshTokenInput {
  grant_type: "refresh_token";
  refresh_token: string;
}

/** Response from POST /v1/auth/token (refresh). Same shape as OAuthTokenResponse. */
export type OAuthRefreshTokenResponse = OAuthTokenResponse;

/** Request body for POST /v1/auth/github. */
export interface GitHubTokenExchangeRequest {
  github_access_token: string;
}

/** Response from POST /v1/auth/github. Same shape as OAuthTokenResponse. */
export type GitHubTokenExchangeResponse = OAuthTokenResponse;

/** Alias for OAuthDeviceCodeResponse. */
export type DeviceCodeResponse = OAuthDeviceCodeResponse;

/** Alias for OAuthTokenResponse. */
export type AuthTokenResponse = OAuthTokenResponse;

// ============================================================================
// Section 6 — Session Types
// ============================================================================

/** Input for the auth.session.create mutation. */
export interface CreateSessionInput {
  pipelineRunId: string;
}

/** Output of the auth.session.create mutation. */
export interface CreateSessionOutput {
  /** Raw session token with prefix ibs_. Returned once; not re-retrievable. */
  sessionToken: string;
  /** ISO 8601 expiry timestamp. */
  expiresAt: string;
  tier: Tier;
}

// ============================================================================
// Section 7 — Analytics Types
// ============================================================================

export type AnalyticsPeriod = "day" | "week" | "month";

/** A single usage row returned by GET /v1/analytics/usage. */
export interface UsageRow {
  eventType: string;
  total: number;
  /** ISO 8601 period start timestamp. */
  periodStart: string;
  /** ISO 8601 period end timestamp. */
  periodEnd: string;
}

/** Response from GET /v1/analytics/usage. */
export interface UsageSummary {
  rows: UsageRow[];
}

/** Response from GET /v1/analytics/usage with date range filter. */
export interface UsageRangeSummary {
  rows: UsageRow[];
}

/** Client-side event emission type for POST /analytics/events. */
export interface AnalyticsEvent {
  eventType: string;
  payload?: Record<string, unknown>;
  /** ISO 8601 */
  timestamp?: string;
}

// --- DashboardSummary nested types ---

/** An alert banner included in DashboardSummary. */
export interface AlertBanner {
  threshold: 80 | 100;
  message: string;
  showUpgradeCTA: boolean;
}

/** Quota fields in DashboardSummary. */
export interface DashboardQuota {
  runsUsedToday: number;
  runsLimit: number | null;
  quotaPercent: number | null;
  /** ISO 8601 timestamp for next daily quota reset. */
  nextReset: string;
}

/** Usage totals in DashboardSummary. */
export interface DashboardUsage {
  tokenUsageThisPeriod: number;
  pipelineRunsThisPeriod: number;
}

/** Team info in DashboardSummary. */
export interface DashboardTeam {
  activeMemberCount: number;
}

/** Period metadata in DashboardSummary. */
export interface DashboardPeriod {
  /** ISO 8601 period start timestamp. */
  start: string;
  /** ISO 8601 period end timestamp. */
  end: string;
  type: AnalyticsPeriod;
}

/** A recent pipeline run entry in DashboardSummary.recentRuns. */
export interface RecentPipelineRun {
  id: string;
  /** ISO 8601 timestamp. */
  recordedAt: string;
  quantity: number;
  metadata: Record<string, unknown> | null;
}

/** Per-model cost breakdown from GET /v1/analytics/cost. */
export interface CostByModel {
  modelId: string;
  costUsd: string;
  tokens: number;
}

/** Per-day cost entry from GET /v1/analytics/cost. */
export interface CostByDay {
  date: string;
  costUsd: string;
}

/** Response from GET /v1/analytics/cost. */
export interface CostAnalyticsResponse {
  totalInputTokens: number;
  totalOutputTokens: number;
  totalTokens: number;
  totalCostUsd: string;
  breakdown: {
    byModel: CostByModel[];
    byProject: Array<{ projectId: string | null; costUsd: string }>;
    byDay: CostByDay[];
  };
}

/** Dashboard summary returned by GET /v1/analytics/dashboard. */
export interface AnalyticsDashboardResponse {
  period: DashboardPeriod;
  quota: DashboardQuota;
  usage: DashboardUsage;
  team: DashboardTeam;
  recentRuns: RecentPipelineRun[];
  alertBanner: AlertBanner | null;
}

// ============================================================================
// Section 8 — Team Types
// ============================================================================

export type TeamRole = "owner" | "admin" | "developer" | "viewer";

/** A single team member entry. */
export interface TeamMember {
  memberId: string;
  accountId: string;
  role: TeamRole;
  /** ISO 8601 timestamp when the member joined. */
  joinedAt: string;
  name?: string;
  email?: string;
  status?: "active" | "invited";
}

/** Response from GET /v1/team/members. */
export interface TeamListResponse {
  members: TeamMember[];
}

// ============================================================================
// Section 9 — Platform Health
// ============================================================================

export type ServiceStatus = "ok" | "degraded" | "down";

/** Response from GET /v1/health. */
export interface PlatformHealthResponse {
  status: ServiceStatus;
  version: string;
  /** Per-service operational status map. */
  services: Record<string, ServiceStatus>;
  /** ISO 8601 timestamp. */
  timestamp: string;
}

// ============================================================================
// Section 10 — Offline / Connection State
// ============================================================================

/** Connection state of the extension to the Nightgauge platform. */
export type ConnectionState = "online" | "degraded" | "offline";

/** Event emitted when connection state transitions. */
export interface ConnectionStateEvent {
  previous: ConnectionState;
  current: ConnectionState;
  at: string;
  /** Reason for the transition. */
  reason: string;
}

/** A fallback implementation for a platform-dependent operation. */
export type FallbackStrategy<T = unknown> = () => T | Promise<T>;

/** Abstraction for the health check — allows injection. */
export interface IHealthChecker {
  checkHealth(): Promise<{ reachable: boolean; degraded?: boolean }>;
}

/** Configuration for OfflineManager. */
export interface OfflineManagerConfig {
  /** Returns the current platform base URL. Called on each health check tick. */
  getBaseUrl: () => string;
  intervalMs: number;
  timeoutMs: number;
  failureThreshold: number;
}

// ============================================================================
// Section 11 — Billing Portal Types
// ============================================================================

/** Response from POST /v1/billing/portal-session. */
export interface BillingPortalSessionResponse {
  url: string;
  /** ISO 8601 timestamp when the portal session expires. */
  expiresAt: string;
}

// ============================================================================
// Re-exports — keep backward compatibility for modules importing from here
// ============================================================================

export type { TokenChangeEvent } from "./TokenStorage";
