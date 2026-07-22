/**
 * RecommendationTracker - Persists recommendation history to JSONL for
 * effectiveness assessment and recurring-finding detection.
 *
 * Storage: .nightgauge/pipeline/recommendation-history.jsonl
 * Each entry records a generated issue, the dimension score at creation time,
 * and (after assessment) the score after the issue was closed.
 *
 * Non-critical: all operations wrapped in try/catch, failures are silently
 * swallowed rather than breaking the pipeline.
 *
 * @see Issue #1103 - Recommendation Tracking
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import { execSync } from "node:child_process";

import type {
  RecommendationHistoryEntry,
  RecommendationReport,
  RecommendationEffectivenessScore,
  RecurringFinding,
  HealthAnalysisResult,
  HealthDimension,
  FindingToIssueResult,
} from "./types.js";

import { computeTrend } from "./statistics.js";

/** Relative path from workspace root to the recommendation history file */
const RECOMMENDATION_HISTORY_FILE = ".nightgauge/pipeline/recommendation-history.jsonl";

/** Default retention period in days */
const DEFAULT_RETENTION_DAYS = 90;

export class RecommendationTracker {
  /**
   * Append a single recommendation history entry to the JSONL file.
   *
   * @param workspaceRoot - Absolute path to repo root
   * @param entry - The entry to append
   */
  static async append(workspaceRoot: string, entry: RecommendationHistoryEntry): Promise<void> {
    try {
      const filePath = this.getFilePath(workspaceRoot);
      const dir = path.dirname(filePath);
      await fs.mkdir(dir, { recursive: true });
      await fs.appendFile(filePath, JSON.stringify(entry) + "\n", "utf-8");
    } catch {
      // Non-critical — never break the pipeline
    }
  }

  /**
   * Read and parse all entries from the JSONL file.
   *
   * @param workspaceRoot - Absolute path to repo root
   * @returns Parsed entries in chronological order
   */
  static async readAll(workspaceRoot: string): Promise<RecommendationHistoryEntry[]> {
    const filePath = this.getFilePath(workspaceRoot);
    let content: string;

    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch {
      return [];
    }

    const entries: RecommendationHistoryEntry[] = [];
    for (const line of content.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const parsed = JSON.parse(trimmed) as RecommendationHistoryEntry;
        if (parsed.schema_version === "1") {
          entries.push(parsed);
        }
      } catch {
        // Skip malformed lines
      }
    }

    return entries;
  }

  /**
   * Enforce retention by removing entries older than the specified days.
   *
   * @param workspaceRoot - Absolute path to repo root
   * @param retentionDays - Number of days to retain (default: 90)
   */
  static async enforceRetention(
    workspaceRoot: string,
    retentionDays: number = DEFAULT_RETENTION_DAYS
  ): Promise<void> {
    const filePath = this.getFilePath(workspaceRoot);
    let content: string;

    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch {
      return; // File doesn't exist — nothing to do
    }

    const cutoff = new Date();
    cutoff.setDate(cutoff.getDate() - retentionDays);

    const lines = content.split("\n");
    const kept: string[] = [];

    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const record = JSON.parse(trimmed) as RecommendationHistoryEntry;
        const ts = new Date(record.created_at);
        if (ts >= cutoff) {
          kept.push(trimmed);
        }
      } catch {
        // Skip malformed lines during pruning
      }
    }

    try {
      await fs.writeFile(filePath, kept.join("\n") + (kept.length > 0 ? "\n" : ""), "utf-8");
    } catch {
      // Non-critical
    }
  }

  /**
   * Record recommendation history entries from a FindingToIssueResult.
   *
   * Called immediately after FindingToIssueEngine.generate() to persist
   * the generated issues alongside the current dimension scores.
   *
   * @param workspaceRoot - Absolute path to repo root
   * @param issueResult - Result from FindingToIssueEngine.generate()
   * @param analysisResult - The HealthAnalysisResult used to generate the issues
   */
  static async recordFromIssueResult(
    workspaceRoot: string,
    issueResult: FindingToIssueResult,
    analysisResult: HealthAnalysisResult
  ): Promise<void> {
    try {
      for (const generatedIssue of issueResult.generatedIssues) {
        if (generatedIssue.skipped) continue;

        const dimension = generatedIssue.dimension as HealthDimension;

        // Extract recommendation text from body if available, otherwise use title
        let recommendation = generatedIssue.title;
        if (generatedIssue.body) {
          const recMatch = generatedIssue.body.match(
            /##\s*Recommendation[s]?\s*\n+([\s\S]*?)(?:\n##|$)/i
          );
          if (recMatch?.[1]) {
            recommendation = recMatch[1].trim();
          }
        }

        const entry: RecommendationHistoryEntry = {
          schema_version: "1",
          finding_id: generatedIssue.findingId,
          created_at: new Date().toISOString(),
          severity: generatedIssue.severity,
          dimension,
          title: generatedIssue.title,
          recommendation,
          issue_number: generatedIssue.issueNumber,
          issue_url: generatedIssue.issueUrl,
          issue_state: generatedIssue.issueNumber ? "open" : "not_created",
          metric_before: analysisResult.dimensions[dimension]?.score,
          health_report_ref: issueResult.healthReportRef,
        };

        await this.append(workspaceRoot, entry);
      }
    } catch {
      // Non-critical
    }
  }

  /**
   * Cross-reference all open entries against GitHub to refresh issue states.
   *
   * Runs `gh issue view` for each entry that has an issue_number and is not
   * already closed. Updates issue_state and assessed_at in place, then rewrites
   * the file.
   *
   * @param workspaceRoot - Absolute path to repo root
   * @returns Updated entries
   */
  static async crossReference(workspaceRoot: string): Promise<RecommendationHistoryEntry[]> {
    const entries = await this.readAll(workspaceRoot);

    const updated = entries.map((entry) => {
      if (!entry.issue_number || entry.issue_state === "closed") {
        return entry;
      }

      try {
        const raw = execSync(`gh issue view ${entry.issue_number} --json state`, {
          encoding: "utf-8",
          timeout: 15000,
        });
        const parsed = JSON.parse(raw) as { state: string };
        const ghState = parsed.state?.toLowerCase();

        return {
          ...entry,
          issue_state: ghState === "closed" ? ("closed" as const) : ("open" as const),
          assessed_at: new Date().toISOString(),
        };
      } catch {
        // gh command failed — leave entry unchanged
        return entry;
      }
    });

    try {
      await this.rewriteEntries(workspaceRoot, updated);
    } catch {
      // Non-critical
    }

    return updated;
  }

  /**
   * Assess the effectiveness of past recommendations against current scores.
   *
   * 1. Refreshes issue states via crossReference()
   * 2. For closed issues, computes metric_after and improvement_percent
   * 3. Computes aggregate effectiveness scores
   * 4. Detects recurring findings
   * 5. Builds a self-assessment summary
   *
   * @param workspaceRoot - Absolute path to repo root
   * @param currentAnalysis - The most recent HealthAnalysisResult
   * @returns Full RecommendationReport
   */
  static async assessEffectiveness(
    workspaceRoot: string,
    currentAnalysis: HealthAnalysisResult
  ): Promise<RecommendationReport> {
    // Refresh issue states from GitHub
    let entries = await this.crossReference(workspaceRoot);

    // Compute metric_after and improvement_percent for closed entries
    entries = entries.map((entry) => {
      if (entry.issue_state !== "closed") return entry;

      const metricAfter = currentAnalysis.dimensions[entry.dimension]?.score;
      const metricBefore = entry.metric_before;

      let improvementPercent: number | undefined;
      if (metricBefore !== undefined && metricAfter !== undefined) {
        if (metricBefore === 0) {
          improvementPercent = metricAfter > 0 ? 100 : 0;
        } else {
          improvementPercent = ((metricAfter - metricBefore) / metricBefore) * 100;
        }
      }

      return {
        ...entry,
        metric_after: metricAfter,
        improvement_percent: improvementPercent,
      };
    });

    try {
      await this.rewriteEntries(workspaceRoot, entries);
    } catch {
      // Non-critical
    }

    // Compute effectiveness score
    const implementedEntries = entries.filter((e) => e.issue_state === "closed");
    const implementedCount = implementedEntries.length;
    const pendingCount = entries.filter((e) => e.issue_state === "open").length;
    const notCreatedCount = entries.filter((e) => e.issue_state === "not_created").length;

    const improvedCount = implementedEntries.filter(
      (e) => e.improvement_percent !== undefined && e.improvement_percent > 0
    ).length;

    const noEffectCount = implementedCount - improvedCount;

    const effectivenessPercent =
      implementedCount > 0 ? (improvedCount / implementedCount) * 100 : 0;

    const effectiveness: RecommendationEffectivenessScore = {
      total_recommendations: entries.length,
      implemented_count: implementedCount,
      pending_count: pendingCount,
      not_created_count: notCreatedCount,
      improved_count: improvedCount,
      no_effect_count: noEffectCount,
      effectiveness_percent: effectivenessPercent,
    };

    // Detect recurring findings
    const recurringFindings = this.detectRecurringFindings(entries);

    // Build self_assessment from health report data in entries
    const healthReportRefs = [...new Set(entries.map((e) => e.health_report_ref).filter(Boolean))];
    const totalHealthChecks = healthReportRefs.length;

    // Compute avg finding count per health check using entries grouped by report
    const entriesByReport = new Map<string, RecommendationHistoryEntry[]>();
    for (const entry of entries) {
      const ref = entry.health_report_ref ?? "__none__";
      const group = entriesByReport.get(ref) ?? [];
      group.push(entry);
      entriesByReport.set(ref, group);
    }

    const findingCounts = [...entriesByReport.values()].map((g) => g.length);
    const avgFindingCount =
      findingCounts.length > 0
        ? findingCounts.reduce((s, v) => s + v, 0) / findingCounts.length
        : 0;

    // Trend of finding counts over time (lower is better — fewer findings = improvement)
    const { direction: findingCountTrend } = computeTrend(findingCounts, 0.05);

    // Follow-through rate: implemented / total with an issue
    const withIssue = entries.filter((e) => e.issue_number !== undefined).length;
    const recommendationFollowThroughRate = entries.length > 0 ? withIssue / entries.length : 0;

    // Overall effectiveness classification
    let overallEffectiveness: "effective" | "mixed" | "ineffective";
    if (effectivenessPercent >= 60) {
      overallEffectiveness = "effective";
    } else if (effectivenessPercent >= 30) {
      overallEffectiveness = "mixed";
    } else {
      overallEffectiveness = "ineffective";
    }

    return {
      assessed_at: new Date().toISOString(),
      effectiveness,
      recurring_findings: recurringFindings,
      self_assessment: {
        total_health_checks: totalHealthChecks,
        avg_finding_count: avgFindingCount,
        finding_count_trend: findingCountTrend,
        recommendation_follow_through_rate: recommendationFollowThroughRate,
        overall_effectiveness: overallEffectiveness,
      },
      entries,
    };
  }

  /**
   * Detect findings that have recurred across multiple recommendation cycles.
   *
   * Groups entries by normalized title (lowercase, trimmed, [HEALTH] prefix
   * removed). Groups with 2 or more entries are considered recurring.
   *
   * @param entries - All recommendation history entries to analyze
   * @returns Array of RecurringFinding descriptors
   */
  static detectRecurringFindings(entries: RecommendationHistoryEntry[]): RecurringFinding[] {
    const groups = new Map<string, RecommendationHistoryEntry[]>();

    for (const entry of entries) {
      const normalized = entry.title
        .replace(/^\[HEALTH\]\s*/i, "")
        .toLowerCase()
        .trim();
      const group = groups.get(normalized) ?? [];
      group.push(entry);
      groups.set(normalized, group);
    }

    const recurring: RecurringFinding[] = [];

    for (const [, group] of groups) {
      if (group.length < 2) continue;

      const sorted = [...group].sort(
        (a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime()
      );

      const issueNumbers = group
        .map((e) => e.issue_number)
        .filter((n): n is number => n !== undefined);

      // all_closed: every entry that has an issue_number must be closed
      const entriesWithIssue = group.filter((e) => e.issue_number !== undefined);
      const allClosed =
        entriesWithIssue.length > 0 && entriesWithIssue.every((e) => e.issue_state === "closed");

      recurring.push({
        finding_title: sorted[0].title,
        dimension: sorted[0].dimension,
        occurrence_count: group.length,
        first_seen: sorted[0].created_at,
        last_seen: sorted[sorted.length - 1].created_at,
        issue_numbers: issueNumbers,
        all_closed: allClosed,
      });
    }

    return recurring;
  }

  /**
   * Returns the absolute path to the recommendation history file.
   *
   * @param workspaceRoot - Absolute path to repo root
   */
  static getFilePath(workspaceRoot: string): string {
    return path.join(workspaceRoot, RECOMMENDATION_HISTORY_FILE);
  }

  /**
   * Overwrite the JSONL file with the provided entries.
   *
   * @param workspaceRoot - Absolute path to repo root
   * @param entries - Entries to write
   */
  private static async rewriteEntries(
    workspaceRoot: string,
    entries: RecommendationHistoryEntry[]
  ): Promise<void> {
    const filePath = this.getFilePath(workspaceRoot);
    const lines = entries.map((e) => JSON.stringify(e));
    await fs.writeFile(filePath, lines.join("\n") + (lines.length > 0 ? "\n" : ""), "utf-8");
  }
}
