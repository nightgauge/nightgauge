/**
 * TierGate — Central tier-aware feature gating utility.
 *
 * Provides a centralized mapping of features to their minimum required tier
 * and methods to check/guard feature access. Used by command handlers and
 * tree view providers to restrict premium features by subscription tier.
 *
 * Also provides RBAC role-checking methods (checkRole/guardRole) for team-tier
 * users. Role checks only apply to team and enterprise tier users.
 *
 * @see Issue #1472 - Add tier-aware feature gating throughout extension UI
 * @see Issue #1483 - Add RBAC-aware UI element visibility based on user role
 * @see Issue #1452 - Epic: Platform API integration
 */

import type { Tier, TeamRole } from "./types";

/** Tier ordering for comparison — higher index = higher tier. */
const TIER_ORDER: Record<Tier, number> = {
  community: 0,
  pro: 1,
  team: 2,
  enterprise: 3,
};

/**
 * Feature names used by gating calls throughout the extension.
 *
 * Each feature maps to a minimum required tier in FEATURE_TIER_MAP.
 */
export type FeatureName =
  | "batch-processing"
  | "concurrent-pipelines"
  | "team-dashboard"
  | "advanced-analytics"
  | "sso"
  | "ciKeys"
  | "customSkills"
  | "auditLogs"
  | "mobile"
  | "web";

/**
 * Centralized feature → minimum required tier mapping.
 *
 * Product decision: the entire LOCAL product is free to everyone. Cloud /
 * server-backed features are not paywalled, they are simply not offered yet —
 * their command surface is hidden behind `nightgauge.cloudEnabled` (the
 * master switch, default off; see manifest config `nightgauge.cloud.enabled`).
 * This map therefore splits along local-vs-cloud, NOT along a pricing ladder:
 *
 *  - LOCAL features → `community` (free for everyone). These run entirely on the
 *    user's machine against their own Claude/Codex keys and require no account:
 *    `batch-processing` (local epic/batch queueing), `concurrent-pipelines`
 *    (local concurrent execution slots), `ciKeys` (local CI-key config), and
 *    `customSkills` (local skill overrides). Because `community` is the floor,
 *    `check()` always returns allowed and the old "upgrade to Pro" prompts on
 *    these call sites are never reached.
 *
 *  - CLOUD features → kept on their higher tier. These require the hosted
 *    platform / an account and their commands are hidden while `cloudEnabled`
 *    is off, so the gate is unreachable in the free-local configuration. When
 *    cloud is switched back on later these tiers become live again with no
 *    further change: `team-dashboard` (team management surface) → team;
 *    `web` (hosted web dashboard) → team; `advanced-analytics` (hosted
 *    analytics dashboard) → pro; `mobile` (companion mobile app) → pro;
 *    `sso` → enterprise; `auditLogs` (hosted audit trail) → enterprise.
 */
export const FEATURE_TIER_MAP: Record<FeatureName, Tier> = {
  // LOCAL — free for everyone (community floor)
  "batch-processing": "community",
  "concurrent-pipelines": "community",
  ciKeys: "community",
  customSkills: "community",
  // CLOUD — gated, and command-hidden while cloud.enabled is off
  "team-dashboard": "team",
  "advanced-analytics": "pro",
  sso: "enterprise",
  auditLogs: "enterprise",
  mobile: "pro",
  web: "team",
};

const UPGRADE_URL = "https://nightgauge.dev/pricing";

/** Result of a tier check — structured for UI consumption. */
export interface TierCheckResult {
  allowed: boolean;
  requiredTier: Tier;
  upgradeUrl: string;
}

/** Error thrown when a feature requires a higher tier than the user has. */
export class TierRequiredError extends Error {
  constructor(
    public readonly feature: FeatureName,
    public readonly requiredTier: Tier,
    public readonly currentTier: Tier,
    public readonly upgradeUrl: string
  ) {
    super(`Feature '${feature}' requires ${requiredTier} (current: ${currentTier})`);
    this.name = "TierRequiredError";
  }
}

// ============================================================================
// Role-based access control (RBAC) — team/enterprise tier only
// ============================================================================

/** Role ordering for comparison — higher index = higher role. */
const ROLE_ORDER: Record<TeamRole, number> = {
  viewer: 0,
  developer: 1,
  admin: 2,
  owner: 3,
};

/**
 * Action names used by role-based gating calls throughout the extension.
 * Each action maps to a minimum required role in ACTION_ROLE_MAP.
 * RBAC only applies to team-tier users — non-team callers skip role checks.
 */
export type ActionName =
  | "manage-team" // invite/remove/promote members
  | "manage-billing" // access billing portal
  | "view-team" // view team member list (read-only)
  | "view-analytics" // view usage analytics
  | "run-pipeline"; // trigger pipeline runs

/** Centralized action → minimum required role mapping. */
export const ACTION_ROLE_MAP: Record<ActionName, TeamRole> = {
  "manage-team": "admin",
  "manage-billing": "admin",
  "view-team": "viewer",
  "view-analytics": "developer",
  "run-pipeline": "developer",
};

/** Result of a role check — structured for UI consumption. */
export interface RoleCheckResult {
  allowed: boolean;
  requiredRole: TeamRole;
}

/** Error thrown when an action requires a higher role than the user has. */
export class RoleRequiredError extends Error {
  constructor(
    public readonly action: ActionName,
    public readonly requiredRole: TeamRole,
    public readonly currentRole: TeamRole
  ) {
    super(`Action '${action}' requires role ${requiredRole} (current: ${currentRole})`);
    this.name = "RoleRequiredError";
  }
}

/**
 * TierGate — stateless utility for checking feature access by tier.
 *
 * Instantiated once in bootstrap/services.ts and injected into command
 * handlers and tree view providers.
 */
export class TierGate {
  /**
   * Check whether a feature is available for the given tier.
   * Returns a structured result — does NOT throw.
   */
  check(feature: FeatureName, currentTier: Tier): TierCheckResult {
    const requiredTier = FEATURE_TIER_MAP[feature];
    const allowed = TIER_ORDER[currentTier] >= TIER_ORDER[requiredTier];
    return { allowed, requiredTier, upgradeUrl: UPGRADE_URL };
  }

  /**
   * Throw TierRequiredError if the feature is unavailable for the given tier.
   * For use in command handlers and service methods.
   */
  guard(feature: FeatureName, currentTier: Tier): void {
    const result = this.check(feature, currentTier);
    if (!result.allowed) {
      throw new TierRequiredError(feature, result.requiredTier, currentTier, result.upgradeUrl);
    }
  }

  /**
   * Check whether an action is available for the given role.
   * Non-team callers should NOT call this — RBAC only applies to team tiers.
   * Returns a structured result — does NOT throw.
   */
  checkRole(action: ActionName, currentRole: TeamRole): RoleCheckResult {
    const requiredRole = ACTION_ROLE_MAP[action];
    const allowed = ROLE_ORDER[currentRole] >= ROLE_ORDER[requiredRole];
    return { allowed, requiredRole };
  }

  /**
   * Throw RoleRequiredError if the action is unavailable for the given role.
   * For use in command handlers gating team management actions.
   */
  guardRole(action: ActionName, currentRole: TeamRole): void {
    const result = this.checkRole(action, currentRole);
    if (!result.allowed) {
      throw new RoleRequiredError(action, result.requiredRole, currentRole);
    }
  }
}
