/**
 * DiscoveryActivityService — reads autonomous discovery run history for the dashboard.
 *
 * Reads state files written by GitHub Actions workflows:
 * - `.nightgauge/release-watch/creation-log*.json` — release-watch run
 *   results. Multi-provider (#4054): each provider writes its own
 *   `creation-log-<provider>.json` (legacy single `creation-log.json` is still
 *   read), and they are aggregated across providers for the dashboard.
 * - `.nightgauge/improvement-runs/latest.json` — continuous-improvement run results
 *
 * Also reads the release-watch backlog to surface pending proposals.
 *
 * @consumers Dashboard.ts (lazy-loaded on discovery tab activation)
 * @see Issue #2434 — activate autonomous self-improvement loop
 * @see docs/SCHEDULED_DISCOVERY.md — scheduled discovery documentation
 */

import * as fs from "fs";
import * as path from "path";

const ONE_WEEK_MS = 7 * 24 * 60 * 60 * 1000;

export interface CreatedIssue {
  number: number;
  title: string;
  url: string;
  score?: number;
}

export interface BacklogEntry {
  title: string;
  score: number;
  reason: string;
  created_at?: string;
}

export interface ReleaseWatchRunData {
  schema_version?: string;
  run_started_at: string;
  triggered_by: string;
  new_version: string;
  since_version: string;
  status: "running" | "completed" | "failed";
  issues_created: CreatedIssue[];
  issues_backlogged: BacklogEntry[];
  issues_deduped: string[];
  completed_at: string | null;
  error: string | null;
}

export interface ContinuousImprovementRunData {
  schema_version?: string;
  run_started_at: string;
  triggered_by: string;
  mode: "dogfood" | "customer";
  create_issues: boolean;
  dry_run: boolean;
  status: "running" | "completed" | "failed";
  proposals_created: CreatedIssue[];
  proposals_backlogged: BacklogEntry[];
  completed_at: string | null;
  error: string | null;
}

export interface DiscoveryActivityData {
  releaseWatch: ReleaseWatchRunData | null;
  continuousImprovement: ContinuousImprovementRunData | null;
  backlog: BacklogEntry[];
  /** Summary counts for the dashboard widget header */
  summary: {
    issuesCreatedThisWeek: number;
    proposalsCreatedThisWeek: number;
    pendingBacklogCount: number;
    lastReleaseWatchAt: string | null;
    lastContinuousImprovementAt: string | null;
  };
}

export class DiscoveryActivityService {
  private readonly releaseWatchDir: string;
  private readonly improvementRunLogPath: string;
  private readonly backlogPath: string;

  constructor(workspaceRoot: string) {
    this.releaseWatchDir = path.join(workspaceRoot, ".nightgauge", "release-watch");
    this.improvementRunLogPath = path.join(
      workspaceRoot,
      ".nightgauge",
      "improvement-runs",
      "latest.json"
    );
    this.backlogPath = path.join(workspaceRoot, ".nightgauge", "release-watch", "backlog.json");
  }

  /**
   * Read and aggregate all discovery activity data for the dashboard.
   * Returns null values for any file that does not exist yet (pre-first-run state).
   */
  async getActivityData(): Promise<DiscoveryActivityData> {
    // Multi-provider (#4054): read every provider's creation-log and aggregate.
    const releaseWatchRuns = this.readReleaseWatchRuns();
    const releaseWatch = this.aggregateReleaseWatch(releaseWatchRuns);
    const continuousImprovement = this.readJson<ContinuousImprovementRunData>(
      this.improvementRunLogPath
    );
    const backlog = this.readBacklog();

    const oneWeekAgo = Date.now() - ONE_WEEK_MS;

    // Count issues per run whose timestamp is within the last 7 days, summed
    // across providers. Counting per-run (not off the aggregate's single
    // newest timestamp) avoids over-counting a stale provider's issues just
    // because another provider ran today. Individual issues don't carry their
    // own created_at, so the run timestamp is the best available proxy.
    const issuesCreatedThisWeek = releaseWatchRuns.reduce((sum, run) => {
      const ts = run.completed_at ?? run.run_started_at ?? null;
      return ts && new Date(ts).getTime() > oneWeekAgo
        ? sum + (run.issues_created ?? []).length
        : sum;
    }, 0);

    const ciRunTs =
      continuousImprovement?.completed_at ?? continuousImprovement?.run_started_at ?? null;
    const proposalsCreatedThisWeek =
      ciRunTs && new Date(ciRunTs).getTime() > oneWeekAgo
        ? (continuousImprovement?.proposals_created ?? []).length
        : 0;

    return {
      releaseWatch,
      continuousImprovement,
      backlog,
      summary: {
        issuesCreatedThisWeek,
        proposalsCreatedThisWeek,
        pendingBacklogCount: backlog.length,
        lastReleaseWatchAt: releaseWatch?.completed_at ?? releaseWatch?.run_started_at ?? null,
        lastContinuousImprovementAt:
          continuousImprovement?.completed_at ?? continuousImprovement?.run_started_at ?? null,
      },
    };
  }

  /**
   * Read every per-provider release-watch creation log. Matches both the legacy
   * single `creation-log.json` and the multi-provider `creation-log-<provider>.json`
   * files (#4054). Returns an empty array pre-first-run.
   */
  private readReleaseWatchRuns(): ReleaseWatchRunData[] {
    let entries: string[];
    try {
      if (!fs.existsSync(this.releaseWatchDir)) {
        return [];
      }
      entries = fs.readdirSync(this.releaseWatchDir);
    } catch (err) {
      console.warn(`[DiscoveryActivityService] Failed to read ${this.releaseWatchDir}:`, err);
      return [];
    }
    return entries
      .filter((f) => /^creation-log.*\.json$/.test(f))
      .map((f) => this.readJson<ReleaseWatchRunData>(path.join(this.releaseWatchDir, f)))
      .filter((r): r is ReleaseWatchRunData => r != null);
  }

  /**
   * Collapse multiple per-provider runs into one view for the dashboard widget:
   * union the issue arrays, surface the most-recent run's headline fields, and
   * fold status (running > failed > completed) + errors. Returns null when there
   * are no runs.
   */
  private aggregateReleaseWatch(runs: ReleaseWatchRunData[]): ReleaseWatchRunData | null {
    if (runs.length === 0) {
      return null;
    }
    const ts = (r: ReleaseWatchRunData): number =>
      new Date(r.completed_at ?? r.run_started_at ?? 0).getTime();
    const newest = [...runs].sort((a, b) => ts(b) - ts(a))[0];
    if (runs.length === 1) {
      return newest;
    }
    const status: ReleaseWatchRunData["status"] = runs.some((r) => r.status === "running")
      ? "running"
      : runs.some((r) => r.status === "failed")
        ? "failed"
        : "completed";
    const errors = runs
      .map((r) => r.error)
      .filter((e): e is string => !!e)
      .join("; ");
    return {
      schema_version: newest.schema_version,
      run_started_at: newest.run_started_at,
      triggered_by: newest.triggered_by,
      new_version: newest.new_version,
      since_version: newest.since_version,
      status,
      issues_created: runs.flatMap((r) => r.issues_created ?? []),
      issues_backlogged: runs.flatMap((r) => r.issues_backlogged ?? []),
      issues_deduped: runs.flatMap((r) => r.issues_deduped ?? []),
      completed_at: newest.completed_at,
      error: errors || null,
    };
  }

  private readJson<T>(filePath: string): T | null {
    try {
      if (!fs.existsSync(filePath)) {
        return null;
      }
      const raw = fs.readFileSync(filePath, "utf-8");
      return JSON.parse(raw) as T;
    } catch (err) {
      console.warn(`[DiscoveryActivityService] Failed to parse ${filePath}:`, err);
      return null;
    }
  }

  private readBacklog(): BacklogEntry[] {
    try {
      if (!fs.existsSync(this.backlogPath)) {
        return [];
      }
      const raw = fs.readFileSync(this.backlogPath, "utf-8");
      const parsed = JSON.parse(raw);
      // Backlog may be an array or an object with an `entries` array
      if (Array.isArray(parsed)) {
        return parsed as BacklogEntry[];
      }
      if (Array.isArray(parsed.entries)) {
        return parsed.entries as BacklogEntry[];
      }
      return [];
    } catch (err) {
      console.warn(`[DiscoveryActivityService] Failed to parse ${this.backlogPath}:`, err);
      return [];
    }
  }
}
