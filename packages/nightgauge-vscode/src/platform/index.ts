/**
 * Platform API type contracts for the Nightgauge extension.
 *
 * Defines the platform API contracts the extension uses as plain TypeScript
 * interfaces in ./types. The extension does NOT depend on the published
 * @nightgauge/shared-types package — its contracts live here locally
 * (the workspace resolves shared types via the sibling platform checkout, not
 * a registry; the extension imports none of them). See #3900.
 *
 * @see Issue #1456 - Define platform API type contracts
 * @see Issue #2091 - Remove PlatformApiClient HTTP code and consolidate types
 * @see Issue #3900 - Remove the unused @nightgauge/shared-types dependency
 */
export * from "./types";
export { OfflineManager } from "./OfflineManager";
export { ConnectivityStateBus } from "./ConnectivityStateBus";
export type { ConnectivityChange } from "./ConnectivityStateBus";
export { PlatformStatusBarItem } from "./PlatformStatusBarItem";
export type { PlatformDisplayState } from "./PlatformStatusBarItem";
export { TrialStateStore } from "./TrialState";
export type { TrialRecord, TrialStatus } from "./TrialState";
export { PlatformEnvironmentStatusBarItem } from "./PlatformEnvironmentStatusBarItem";
export { TokenStorage } from "./TokenStorage";
export type { ITokenStorage, TokenKey, TokenChangeEvent } from "./TokenStorage";
export { TokenRefreshManager } from "./TokenRefreshManager";
export type { IOnDemandTokenRefresher } from "./TokenRefreshManager";
export { SessionManager } from "./SessionManager";
export type { SessionState, SessionData, SessionStateEvent } from "./SessionManager";
export { MachineFingerprint } from "./MachineFingerprint";
export { LicensePreflight } from "./LicensePreflight";
export type { LicensePreflightResult } from "./LicensePreflight";
export {
  TierGate,
  TierRequiredError,
  FEATURE_TIER_MAP,
  RoleRequiredError,
  ACTION_ROLE_MAP,
} from "./TierGate";
export type { FeatureName, TierCheckResult, ActionName, RoleCheckResult } from "./TierGate";
export { SkillContextAssembler } from "./SkillContextAssembler";
export type { SkillContext } from "./SkillContextAssembler";
