/**
 * Shared DashboardAggregates fixture for HTML render tests.
 *
 * Centralizing this lets us add new required fields (like
 * `recentDelta`) without touching every test file. Tests that need
 * non-trivial values pass overrides; everything else gets a quiet,
 * type-correct zero.
 */
import type {
  DashboardAggregates,
  RecentActivityDelta,
} from "../../../../src/views/dashboard/DashboardState";

export const emptyRecentDelta: RecentActivityDelta = {
  runsDelta: 0,
  runsPrior: 0,
  timeSavedDeltaMs: 0,
  timeSavedPriorMs: 0,
  costDeltaUsd: 0,
  costPriorUsd: 0,
  successRatePointsDelta: 0,
  successRateRecent: 0,
  successRatePrior: 0,
  hasEnoughData: false,
  windowDays: 7,
};

export function makeEmptyAggregates(
  overrides: Partial<DashboardAggregates> = {}
): DashboardAggregates {
  return {
    totalRuns: 0,
    sessionRuns: 0,
    totalTimeSavedMs: 0,
    sessionTimeSavedMs: 0,
    totalCostUsd: 0,
    sessionCostUsd: 0,
    successRate: 0,
    avgCostPerRun: 0,
    avgTimeSavedPerRun: 0,
    totalTokens: 0,
    sessionTokens: 0,
    epicEstimates: [],
    crossRepoEpicProgress: [],
    firewallAggregates: null,
    stageAverages: [],
    costPerIssue: [],
    recentDelta: emptyRecentDelta,
    ...overrides,
  };
}
