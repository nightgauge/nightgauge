/**
 * BrownfieldTypes - TypeScript interfaces for brownfield modernization dashboard
 *
 * Matches JSON schemas from brownfield skills:
 * - health-check → .nightgauge/health-report.json
 * - security-audit → .nightgauge/security-audit.json
 * - modernize-plan → .nightgauge/modernization-plan.json
 * - dep-modernize → .nightgauge/dep-modernize-report.json
 *
 * @see Issue #1163 - Brownfield Modernization Progress Dashboard
 */

// ---------------------------------------------------------------------------
// Health Report (from nightgauge-health-check SKILL.md)
// ---------------------------------------------------------------------------

export type HealthStatus = "excellent" | "good" | "fair" | "poor" | "critical";

export type FindingSeverity = "critical" | "high" | "medium" | "low" | "info";

export interface HealthFinding {
  severity: FindingSeverity;
  title: string;
  description: string;
  recommendation: string;
}

export interface HealthDimensionMetrics {
  [key: string]: unknown;
}

export interface HealthDimension {
  score: number;
  status: HealthStatus;
  weight: number;
  findings: HealthFinding[];
  metrics: HealthDimensionMetrics;
}

export interface HealthReportData {
  schema_version: string;
  assessment_date: string;
  summary: {
    overall_health_score: number;
    status: HealthStatus;
    dimensions_assessed: number;
    dimensions_skipped: number;
  };
  dimensions: Record<string, HealthDimension>;
  top_recommendations: HealthRecommendation[];
  created_at: string;
}

export interface HealthRecommendation {
  priority: number;
  action: string;
  impact: string;
  effort: "low" | "medium" | "high";
  dimension: string;
}

// ---------------------------------------------------------------------------
// Security Audit (from nightgauge-security-audit SKILL.md)
// ---------------------------------------------------------------------------

export interface SecurityFinding {
  severity: FindingSeverity;
  title: string;
  description: string;
  cwe?: string;
  cve?: string;
  location?: string;
  recommendation: string;
}

export interface SecurityDimension {
  score: number;
  status: HealthStatus;
  weight: number;
  findings: SecurityFinding[];
  metrics: Record<string, unknown>;
}

export interface SecurityAuditData {
  schema_version: string;
  assessment_date: string;
  summary: {
    overall_security_score: number;
    status: HealthStatus;
    dimensions_assessed: number;
    dimensions_skipped: number;
    total_findings: number;
    findings_by_severity: {
      critical: number;
      high: number;
      medium: number;
      low: number;
      info: number;
    };
  };
  dimensions: Record<string, SecurityDimension>;
  top_recommendations: SecurityRecommendation[];
  created_at: string;
}

export interface SecurityRecommendation {
  priority: number;
  action: string;
  cwe?: string;
  cve?: string;
  impact: string;
  effort: "low" | "medium" | "high";
  dimension: string;
}

// ---------------------------------------------------------------------------
// Modernization Plan (from nightgauge-modernize-plan SKILL.md)
// ---------------------------------------------------------------------------

export type EffortSize = "XS" | "S" | "M" | "L" | "XL";
export type RiskLevel = "low" | "medium" | "high" | "critical";

export interface ModernizationTask {
  id: string;
  title: string;
  description: string;
  rationale: string;
  effort: EffortSize;
  risk: RiskLevel;
  dependencies: string[];
  execution_method: "manual" | "automated" | "ai-assisted";
  source: string;
  source_dimension: string;
}

export interface ModernizationPhase {
  phase_number: number;
  name: string;
  description: string;
  tasks: ModernizationTask[];
  total_story_points: number;
  estimated_sprints: number;
}

export interface QuickWin {
  task_id: string;
  title: string;
  effort: EffortSize;
  impact: string;
  phase: number;
}

export interface ModernizationPlanData {
  schema_version: string;
  generated_at: string;
  summary: {
    total_tasks: number;
    tasks_by_phase: Record<string, number>;
    tasks_by_effort: Record<EffortSize, number>;
    tasks_by_risk: Record<RiskLevel, number>;
    total_story_points: number;
    quick_wins_count: number;
    estimated_sprints: number;
    estimated_weeks: number;
  };
  quick_wins: QuickWin[];
  phases: ModernizationPhase[];
  created_at: string;
}

// ---------------------------------------------------------------------------
// Dependency Modernize Report (from nightgauge-dep-modernize SKILL.md)
// ---------------------------------------------------------------------------

export interface DepCve {
  id: string;
  severity: string;
  description: string;
  fixed_in: string;
}

export interface DepInfo {
  name: string;
  ecosystem: string;
  installed_version: string;
  latest_version: string;
  type: "direct" | "dev" | "peer" | "optional";
  category: string;
  severity: FindingSeverity;
  cves: DepCve[];
  risk: string;
  auto_fixable: boolean;
}

export interface DepUpdateGroup {
  group: number;
  name: string;
  description: string;
  risk: string;
  auto_fixable: boolean;
  dependencies: string[];
  estimated_effort: EffortSize;
  status: "pending" | "applied" | "failed";
}

export interface DepModernizeData {
  schema_version: string;
  generated_at: string;
  summary: {
    total_dependencies: number;
    outdated_count: number;
    vulnerable_count: number;
    deprecated_count: number;
    unmaintained_count: number;
    categories: Record<string, number>;
    auto_fixable: number;
    needs_manual: number;
  };
  dependencies: DepInfo[];
  update_groups: DepUpdateGroup[];
  created_at?: string;
}

// ---------------------------------------------------------------------------
// History Snapshot (for trending)
// ---------------------------------------------------------------------------

export interface BrownfieldSnapshot {
  timestamp: string;
  health_score: number | null;
  security_score: number | null;
  tasks_completed: number;
  tasks_total: number;
}

// ---------------------------------------------------------------------------
// Dashboard View Model
// ---------------------------------------------------------------------------

export interface BrownfieldDashboardData {
  health: HealthReportData | null;
  security: SecurityAuditData | null;
  plan: ModernizationPlanData | null;
  deps: DepModernizeData | null;
  history: BrownfieldSnapshot[];
  hasAnyData: boolean;
}

// ---------------------------------------------------------------------------
// Computed Metrics (produced by BrownfieldDashboardState)
// ---------------------------------------------------------------------------

export interface DimensionBreakdown {
  name: string;
  score: number;
  status: HealthStatus;
  weight: number;
}

export interface ModernizationProgress {
  completedTasks: number;
  totalTasks: number;
  percent: number;
  activePhase: ModernizationPhase | null;
  activePhaseIndex: number;
}

export interface SecuritySeverityCounts {
  critical: number;
  high: number;
  medium: number;
  low: number;
  info: number;
}

export interface BeforeAfterDelta {
  initialDate: string;
  initialHealthScore: number | null;
  initialSecurityScore: number | null;
  currentHealthScore: number | null;
  currentSecurityScore: number | null;
}

export interface DependencyHealth {
  total: number;
  outdated: number;
  vulnerable: number;
  upToDatePercent: number;
}
