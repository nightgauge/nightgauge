/**
 * BrownfieldDashboardState - State manager for the brownfield dashboard
 *
 * Loads data via BrownfieldDataService, computes derived metrics
 * (dimension breakdown, progress, deltas), and provides the view model
 * for the HTML generator.
 *
 * @see Issue #1163 - Brownfield Modernization Progress Dashboard
 */

import type { BrownfieldDataService } from "../../services/BrownfieldDataService";
import type {
  BrownfieldDashboardData,
  DimensionBreakdown,
  ModernizationProgress,
  SecuritySeverityCounts,
  BeforeAfterDelta,
  DependencyHealth,
  QuickWin,
  HealthStatus,
} from "./BrownfieldTypes";

/**
 * BrownfieldDashboardState loads and computes all data for the dashboard.
 */
export class BrownfieldDashboardState {
  private cachedData: BrownfieldDashboardData | null = null;

  constructor(private readonly dataService: BrownfieldDataService) {}

  /**
   * Load all data from the data service
   */
  async loadData(): Promise<BrownfieldDashboardData> {
    this.cachedData = await this.dataService.loadAll();
    return this.cachedData;
  }

  /**
   * Get cached data (or load if not cached)
   */
  async getData(): Promise<BrownfieldDashboardData> {
    if (!this.cachedData) {
      return this.loadData();
    }
    return this.cachedData;
  }

  /**
   * Get dimension breakdown sorted by score ascending (worst first)
   */
  getDimensionBreakdown(): DimensionBreakdown[] {
    if (!this.cachedData?.health) return [];

    const dimensions = this.cachedData.health.dimensions;
    return Object.entries(dimensions)
      .map(([name, dim]) => ({
        name,
        score: dim.score,
        status: dim.status,
        weight: dim.weight,
      }))
      .sort((a, b) => a.score - b.score);
  }

  /**
   * Compute modernization progress
   */
  getModernizationProgress(): ModernizationProgress {
    if (!this.cachedData?.plan) {
      return {
        completedTasks: 0,
        totalTasks: 0,
        percent: 0,
        activePhase: null,
        activePhaseIndex: -1,
      };
    }

    const plan = this.cachedData.plan;
    const totalTasks = plan.summary.total_tasks;

    // Heuristic: completed tasks = sum of tasks in phases before the active phase
    // Active phase = first phase with tasks remaining (based on phase ordering)
    // Since the plan doesn't track individual task completion, we use 0 as default
    const completedTasks = 0;
    const percent = totalTasks > 0 ? Math.round((completedTasks / totalTasks) * 100) : 0;

    // Active phase: first phase in the list (phases are ordered 0-5)
    const activePhase = plan.phases.length > 0 ? plan.phases[0] : null;
    const activePhaseIndex = activePhase ? activePhase.phase_number : -1;

    return {
      completedTasks,
      totalTasks,
      percent,
      activePhase,
      activePhaseIndex,
    };
  }

  /**
   * Get quick wins sorted by impact/effort (top 5)
   */
  getQuickWins(): QuickWin[] {
    if (!this.cachedData?.plan) return [];

    return this.cachedData.plan.quick_wins.slice(0, 5);
  }

  /**
   * Get security severity counts aggregated across dimensions
   */
  getSecuritySeverityCounts(): SecuritySeverityCounts {
    if (!this.cachedData?.security) {
      return { critical: 0, high: 0, medium: 0, low: 0, info: 0 };
    }

    return { ...this.cachedData.security.summary.findings_by_severity };
  }

  /**
   * Compute before/after delta from history
   */
  getBeforeAfterDelta(): BeforeAfterDelta | null {
    const history = this.cachedData?.history ?? [];
    if (history.length < 2) return null;

    const initial = history[0];
    const current = history[history.length - 1];

    return {
      initialDate: initial.timestamp,
      initialHealthScore: initial.health_score,
      initialSecurityScore: initial.security_score,
      currentHealthScore: current.health_score,
      currentSecurityScore: current.security_score,
    };
  }

  /**
   * Get dependency health ratios
   */
  getDependencyHealth(): DependencyHealth {
    if (!this.cachedData?.deps) {
      return { total: 0, outdated: 0, vulnerable: 0, upToDatePercent: 0 };
    }

    const summary = this.cachedData.deps.summary;
    const total = summary.total_dependencies;
    const outdated = summary.outdated_count;
    const vulnerable = summary.vulnerable_count;
    const upToDate = total - outdated;
    const upToDatePercent = total > 0 ? Math.round((upToDate / total) * 100) : 0;

    return { total, outdated, vulnerable, upToDatePercent };
  }

  /**
   * Derive health status from a numeric score
   */
  static getStatus(score: number): HealthStatus {
    if (score >= 90) return "excellent";
    if (score >= 70) return "good";
    if (score >= 50) return "fair";
    if (score >= 30) return "poor";
    return "critical";
  }
}
