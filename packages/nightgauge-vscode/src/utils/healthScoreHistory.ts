/**
 * HealthScoreHistory - Writer + Reader for health score snapshots
 *
 * Static utility classes following the ExecutionHistoryWriter/Reader pattern.
 * Persists health score snapshots to a single JSONL file:
 *   .nightgauge/pipeline/health-history.jsonl
 *
 * Non-critical: all operations log warnings on failure, never throw.
 *
 * @see Issue #789 - Persist Health Scores to Disk with 30-Day Trend
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

import {
  HealthScoreSnapshotSchema,
  RecalibrationMarkerSchema,
  type HealthScoreSnapshot,
  type RecalibrationMarker,
} from "../schemas/healthScoreHistory";
import type { TrendAnalysis, TrendChartDay } from "../views/dashboard/HealthWidgetTypes";

/** Relative path from workspace root to the health history file */
const HEALTH_HISTORY_FILE = ".nightgauge/pipeline/health-history.jsonl";

/** Default retention period in days (supports up to 90d range) */
const DEFAULT_RETENTION_DAYS = 90;

export class HealthScoreHistoryWriter {
  /**
   * Append a validated snapshot to the JSONL file, then prune old entries.
   */
  static async appendSnapshot(workspaceRoot: string, snapshot: HealthScoreSnapshot): Promise<void> {
    try {
      const validation = HealthScoreSnapshotSchema.safeParse(snapshot);
      if (!validation.success) {
        console.warn(
          `[Nightgauge] Invalid health snapshot, skipping write: ${validation.error.message}`
        );
        return;
      }

      const filePath = this.getFilePath(workspaceRoot);
      const dir = path.dirname(filePath);

      await fs.mkdir(dir, { recursive: true });
      await fs.appendFile(filePath, JSON.stringify(snapshot) + "\n", "utf-8");

      await this.pruneOldEntries(workspaceRoot);
    } catch (error) {
      console.warn(`[Nightgauge] Failed to write health snapshot: ${error}`);
    }
  }

  /**
   * Remove entries older than the retention period by rewriting the file.
   */
  static async pruneOldEntries(
    workspaceRoot: string,
    retentionDays: number = DEFAULT_RETENTION_DAYS
  ): Promise<void> {
    try {
      const filePath = this.getFilePath(workspaceRoot);
      let content: string;
      try {
        content = await fs.readFile(filePath, "utf-8");
      } catch {
        return; // File doesn't exist
      }

      const cutoff = new Date();
      cutoff.setDate(cutoff.getDate() - retentionDays);

      const lines = content.split("\n");
      const kept: string[] = [];

      for (const line of lines) {
        const trimmed = line.trim();
        if (!trimmed) continue;
        try {
          const record = JSON.parse(trimmed);
          const ts = new Date(record.timestamp);
          if (ts >= cutoff) {
            kept.push(trimmed);
          }
        } catch {
          // Skip malformed lines during pruning
        }
      }

      await fs.writeFile(filePath, kept.join("\n") + (kept.length > 0 ? "\n" : ""), "utf-8");
    } catch (error) {
      console.warn(`[Nightgauge] Failed to prune health history: ${error}`);
    }
  }

  /**
   * Append a recalibration marker to the health history file.
   *
   * The marker resets the trend baseline: subsequent reads via
   * `readSinceLastRecalibration` will only return entries after this marker.
   *
   * @see Issue #1262 - Add health score baseline recalibration after systemic fixes
   */
  static async appendRecalibrationMarker(workspaceRoot: string, reason?: string): Promise<void> {
    try {
      const marker: RecalibrationMarker = {
        schema_version: "1",
        type: "recalibration",
        timestamp: new Date().toISOString(),
        ...(reason ? { reason } : {}),
      };

      const filePath = this.getFilePath(workspaceRoot);
      const dir = path.dirname(filePath);

      await fs.mkdir(dir, { recursive: true });
      await fs.appendFile(filePath, JSON.stringify(marker) + "\n", "utf-8");
    } catch (error) {
      console.warn(`[Nightgauge] Failed to write recalibration marker: ${error}`);
    }
  }

  /**
   * Returns the absolute path to the health history file.
   */
  static getFilePath(workspaceRoot: string): string {
    return path.join(workspaceRoot, HEALTH_HISTORY_FILE);
  }
}

export class HealthScoreHistoryReader {
  /**
   * Read the most recent recalibration marker from the health history file.
   * Returns null if no recalibration marker exists.
   *
   * @see Issue #1262 - Add health score baseline recalibration after systemic fixes
   */
  static async getMostRecentRecalibration(
    workspaceRoot: string
  ): Promise<RecalibrationMarker | null> {
    const filePath = HealthScoreHistoryWriter.getFilePath(workspaceRoot);
    let content: string;

    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch {
      return null;
    }

    let latest: RecalibrationMarker | null = null;

    for (const line of content.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const parsed = JSON.parse(trimmed);
        if (parsed.type === "recalibration") {
          const validation = RecalibrationMarkerSchema.safeParse(parsed);
          if (validation.success) {
            if (!latest || new Date(validation.data.timestamp) > new Date(latest.timestamp)) {
              latest = validation.data;
            }
          }
        }
      } catch {
        // Skip malformed lines
      }
    }

    return latest;
  }

  /**
   * Read all snapshots from the health history file.
   * Skips malformed lines with a warning.
   */
  static async readAll(workspaceRoot: string): Promise<HealthScoreSnapshot[]> {
    const filePath = HealthScoreHistoryWriter.getFilePath(workspaceRoot);
    let content: string;

    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch {
      return [];
    }

    const records: HealthScoreSnapshot[] = [];
    for (const line of content.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const parsed = JSON.parse(trimmed);
        const validation = HealthScoreSnapshotSchema.safeParse(parsed);
        if (validation.success) {
          records.push(validation.data);
        } else {
          console.warn(
            `[Nightgauge] Skipping malformed health history line: ${validation.error.message}`
          );
        }
      } catch {
        console.warn("[Nightgauge] Skipping unparseable health history line");
      }
    }

    return records;
  }

  /**
   * Read snapshots within a date range (inclusive).
   *
   * When a recalibration marker exists, the effective start date is the later
   * of `startDate` and the recalibration timestamp — so old data from before
   * a systemic-fix recalibration does not drag down the trend.
   *
   * @see Issue #1262 - Add health score baseline recalibration after systemic fixes
   */
  static async readDateRange(
    workspaceRoot: string,
    startDate: Date,
    endDate: Date
  ): Promise<HealthScoreSnapshot[]> {
    const recalibration = await this.getMostRecentRecalibration(workspaceRoot);
    const effectiveStart =
      recalibration && new Date(recalibration.timestamp) > startDate
        ? new Date(recalibration.timestamp)
        : startDate;

    const all = await this.readAll(workspaceRoot);
    return all.filter((s) => {
      const ts = new Date(s.timestamp);
      return ts >= effectiveStart && ts <= endDate;
    });
  }

  /**
   * Group snapshots by day and compute daily average scores.
   */
  static aggregateByDay(snapshots: HealthScoreSnapshot[]): TrendChartDay[] {
    const dayMap = new Map<string, { totalScore: number; count: number }>();

    for (const s of snapshots) {
      const date = s.timestamp.split("T")[0]; // YYYY-MM-DD
      const entry = dayMap.get(date) ?? { totalScore: 0, count: 0 };
      entry.totalScore += s.score;
      entry.count++;
      dayMap.set(date, entry);
    }

    const result: TrendChartDay[] = [];
    for (const [date, data] of dayMap) {
      result.push({
        date,
        avgScore: Math.round(data.totalScore / data.count),
        count: data.count,
      });
    }

    return result.sort((a, b) => a.date.localeCompare(b.date));
  }

  /**
   * Group snapshots by hour and compute hourly average scores.
   * Used for the 24h trend range where daily aggregation is too coarse.
   */
  static aggregateByHour(snapshots: HealthScoreSnapshot[]): TrendChartDay[] {
    const hourMap = new Map<string, { totalScore: number; count: number }>();

    for (const s of snapshots) {
      // Extract YYYY-MM-DDTHH as the bucket key
      const hourKey = s.timestamp.slice(0, 13); // "2026-03-07T14"
      const entry = hourMap.get(hourKey) ?? { totalScore: 0, count: 0 };
      entry.totalScore += s.score;
      entry.count++;
      hourMap.set(hourKey, entry);
    }

    const result: TrendChartDay[] = [];
    for (const [date, data] of hourMap) {
      result.push({
        date,
        avgScore: Math.round(data.totalScore / data.count),
        count: data.count,
      });
    }

    return result.sort((a, b) => a.date.localeCompare(b.date));
  }

  /**
   * Analyze trend by comparing recent half vs prior half of the data.
   * The comparison window adapts to the selected range:
   * - 24h: recent 12h vs prior 12h
   * - 7d: recent 3d vs prior 3d
   * - 30d: recent 7d vs prior 7d
   * - 90d: recent 14d vs prior 14d
   *
   * @param comparisonBuckets - Number of buckets for each half of the comparison
   */
  static analyzeTrend(dailyData: TrendChartDay[], comparisonBuckets: number = 7): TrendAnalysis {
    if (dailyData.length === 0) {
      return {
        direction: "stable",
        message: "Not enough data for trend analysis",
        periodDays: comparisonBuckets,
        percentChange: 0,
      };
    }

    if (dailyData.length === 1) {
      return {
        direction: "stable",
        message: "Tracking started",
        periodDays: comparisonBuckets,
        percentChange: 0,
      };
    }

    // Sort by date descending for slicing
    const sorted = [...dailyData].sort((a, b) => b.date.localeCompare(a.date));

    const recent = sorted.slice(0, comparisonBuckets);
    const prior = sorted.slice(comparisonBuckets, comparisonBuckets * 2);

    if (prior.length === 0) {
      return {
        direction: "stable",
        message: "Not enough history for trend comparison",
        periodDays: comparisonBuckets,
        percentChange: 0,
      };
    }

    const recentAvg = recent.reduce((sum, d) => sum + d.avgScore, 0) / recent.length;
    const priorAvg = prior.reduce((sum, d) => sum + d.avgScore, 0) / prior.length;

    if (priorAvg === 0) {
      return {
        direction: "stable",
        message: "Insufficient baseline data",
        periodDays: comparisonBuckets,
        percentChange: 0,
      };
    }

    const percentChange = Math.round(((recentAvg - priorAvg) / priorAvg) * 100);

    let direction: "improving" | "stable" | "declining";
    let message: string;

    // The analysis is a recent-vs-prior split. The prior phrasing
    // ("over last N days") implied a single contiguous window, which
    // misled users when the chart range was wider than the comparison
    // window — e.g. a 7-day chart with a 3-day comparison rendered as
    // "Health improved 3% over last 3 days" alongside seven date labels.
    // The new wording makes the comparison explicit so chart and message
    // are independently truthful.
    const windowLabel =
      comparisonBuckets < 1
        ? "the recent period"
        : comparisonBuckets === 12
          ? "prior 12 hours"
          : `prior ${comparisonBuckets} day${comparisonBuckets === 1 ? "" : "s"}`;

    if (percentChange > 2) {
      direction = "improving";
      message = `Health improved ${percentChange}% vs ${windowLabel}`;
    } else if (percentChange < -2) {
      direction = "declining";
      message = `Health declined ${Math.abs(percentChange)}% vs ${windowLabel}`;
    } else {
      direction = "stable";
      message = `Health stable vs ${windowLabel}`;
    }

    return { direction, message, periodDays: comparisonBuckets, percentChange };
  }
}
