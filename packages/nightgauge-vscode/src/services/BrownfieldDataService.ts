/**
 * BrownfieldDataService - File watcher and data loader for brownfield assessment JSONs
 *
 * Reads assessment reports from the checkout's `reports/` directory and
 * watches for file changes. Manages history snapshots in the checkout's
 * `brownfield-history/` for trend visualization. Both are in the per-checkout
 * directory (`.git/nightgauge-worktree/` for a main checkout, ADR-024 § 7).
 *
 * Pattern: Follows PipelineStateService for file watching and event emission.
 *
 * @see Issue #1163 - Brownfield Modernization Progress Dashboard
 */

import * as vscode from "vscode";
import { mkdirSync } from "node:fs";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import { writeFileAtomic } from "../utils/atomicWrite";
import { checkoutPath, isUsableWorkspaceRoot } from "../utils/cloneLayout";
import type {
  HealthReportData,
  SecurityAuditData,
  ModernizationPlanData,
  DepModernizeData,
  BrownfieldSnapshot,
  BrownfieldDashboardData,
} from "../views/brownfield/BrownfieldTypes";

/** Maximum number of history snapshots to retain */
const MAX_HISTORY_SNAPSHOTS = 100;

/** Assessment file names in the checkout's reports/ directory */
const ASSESSMENT_FILES = {
  health: "health-report.json",
  security: "security-audit.json",
  plan: "modernization-plan.json",
  deps: "dep-modernize-report.json",
} as const;

/**
 * BrownfieldDataService loads assessment JSON files and watches for changes.
 *
 * @example
 * ```typescript
 * const service = new BrownfieldDataService(workspaceRoot);
 * service.onDataChanged(() => refreshDashboard());
 * const data = await service.loadAll();
 * ```
 */
export class BrownfieldDataService implements vscode.Disposable {
  // Null when the workspace root is not a usable checkout: nothing is
  // watched, loaders report no data, and history is never written.
  private readonly reportsDir: string | null = null;
  private readonly historyDir: string | null = null;
  private readonly historyFile: string | null = null;
  private readonly disposables: vscode.Disposable[] = [];

  private readonly _onDataChanged = new vscode.EventEmitter<void>();
  readonly onDataChanged = this._onDataChanged.event;

  constructor(private readonly workspaceRoot: string) {
    if (isUsableWorkspaceRoot(workspaceRoot)) {
      this.reportsDir = checkoutPath(workspaceRoot, "reports");
      this.historyDir = checkoutPath(workspaceRoot, "brownfieldHistory");
      this.historyFile = path.join(this.historyDir, "brownfield-snapshots.json");
    }

    this.initializeWatchers();
  }

  /**
   * Set up file watchers for all assessment JSON files
   */
  private initializeWatchers(): void {
    this.disposables.push(this._onDataChanged);
    if (!this.reportsDir) return;
    try {
      // The watcher's base directory must exist; it is inside the git dir.
      mkdirSync(this.reportsDir, { recursive: true });
    } catch {
      return;
    }
    // Rooted at the reports directory itself: it is under the git directory,
    // which workspace-wide watchers exclude.
    const pattern = new vscode.RelativePattern(
      vscode.Uri.file(this.reportsDir),
      "{health-report.json,security-audit.json,modernization-plan.json,dep-modernize-report.json}"
    );

    const watcher = vscode.workspace.createFileSystemWatcher(pattern);

    watcher.onDidCreate(() => this._onDataChanged.fire());
    watcher.onDidChange(() => this._onDataChanged.fire());
    watcher.onDidDelete(() => this._onDataChanged.fire());

    this.disposables.push(watcher);
  }

  /**
   * Load all assessment data and compose into dashboard view model
   */
  async loadAll(): Promise<BrownfieldDashboardData> {
    const [health, security, plan, deps, history] = await Promise.all([
      this.loadHealth(),
      this.loadSecurity(),
      this.loadPlan(),
      this.loadDeps(),
      this.loadHistory(),
    ]);

    const hasAnyData = !!(health || security || plan || deps);

    // Record snapshot if data changed
    if (hasAnyData) {
      await this.maybeRecordSnapshot(health, security, plan);
    }

    return { health, security, plan, deps, history, hasAnyData };
  }

  /**
   * Load health report from reports/health-report.json
   */
  async loadHealth(): Promise<HealthReportData | null> {
    return this.loadJsonFile<HealthReportData>(ASSESSMENT_FILES.health);
  }

  /**
   * Load security audit from reports/security-audit.json
   */
  async loadSecurity(): Promise<SecurityAuditData | null> {
    return this.loadJsonFile<SecurityAuditData>(ASSESSMENT_FILES.security);
  }

  /**
   * Load modernization plan from reports/modernization-plan.json
   */
  async loadPlan(): Promise<ModernizationPlanData | null> {
    return this.loadJsonFile<ModernizationPlanData>(ASSESSMENT_FILES.plan);
  }

  /**
   * Load dependency modernize report from reports/dep-modernize-report.json
   */
  async loadDeps(): Promise<DepModernizeData | null> {
    return this.loadJsonFile<DepModernizeData>(ASSESSMENT_FILES.deps);
  }

  /**
   * Load history snapshots from brownfield-history/brownfield-snapshots.json
   */
  async loadHistory(): Promise<BrownfieldSnapshot[]> {
    if (!this.historyFile) return [];
    try {
      const content = await fs.readFile(this.historyFile, "utf-8");
      const data = JSON.parse(content);
      if (Array.isArray(data)) {
        return data;
      }
      return [];
    } catch {
      return [];
    }
  }

  /**
   * Record a snapshot if scores have changed since last snapshot
   */
  private async maybeRecordSnapshot(
    health: HealthReportData | null,
    security: SecurityAuditData | null,
    plan: ModernizationPlanData | null
  ): Promise<void> {
    const history = await this.loadHistory();
    const lastSnapshot = history[history.length - 1] ?? null;

    const healthScore = health?.summary?.overall_health_score ?? null;
    const securityScore = security?.summary?.overall_security_score ?? null;
    const tasksTotal = plan?.summary?.total_tasks ?? 0;
    // Completed tasks: count tasks in completed phases (heuristic)
    const tasksCompleted = 0; // Will be computed by state manager

    // Skip if nothing changed
    if (
      lastSnapshot &&
      lastSnapshot.health_score === healthScore &&
      lastSnapshot.security_score === securityScore &&
      lastSnapshot.tasks_total === tasksTotal
    ) {
      return;
    }

    const snapshot: BrownfieldSnapshot = {
      timestamp: new Date().toISOString(),
      health_score: healthScore,
      security_score: securityScore,
      tasks_completed: tasksCompleted,
      tasks_total: tasksTotal,
    };

    history.push(snapshot);

    // Prune oldest if over limit
    while (history.length > MAX_HISTORY_SNAPSHOTS) {
      history.shift();
    }

    await this.saveHistory(history);
  }

  /**
   * Save history snapshots to disk
   */
  private async saveHistory(history: BrownfieldSnapshot[]): Promise<void> {
    if (!this.historyDir || !this.historyFile) return;
    try {
      await fs.mkdir(this.historyDir, { recursive: true });
      // Atomic — a crash mid-write would otherwise lose the whole history (#1210).
      await writeFileAtomic(this.historyFile, JSON.stringify(history, null, 2));
    } catch {
      // Non-critical — history save failure should not break dashboard
    }
  }

  /**
   * Load and parse a JSON file from the checkout's reports/ directory
   */
  private async loadJsonFile<T>(filename: string): Promise<T | null> {
    if (!this.reportsDir) return null;
    try {
      const filePath = path.join(this.reportsDir, filename);
      const content = await fs.readFile(filePath, "utf-8");
      return JSON.parse(content) as T;
    } catch {
      return null;
    }
  }

  dispose(): void {
    while (this.disposables.length) {
      this.disposables.pop()?.dispose();
    }
  }
}
