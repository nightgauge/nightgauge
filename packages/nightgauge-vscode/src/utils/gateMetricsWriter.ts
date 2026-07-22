/**
 * GateMetricsWriter - JSONL writer/reader for quality gate telemetry
 *
 * Static utility class following the HealthScoreHistoryWriter pattern:
 * no state, no VSCode dependency, testable in isolation.
 *
 * Persists gate metric records to a single JSONL file:
 *   .nightgauge/health/gate-metrics.jsonl
 *
 * Non-critical: all operations log warnings on failure, never throw.
 *
 * @see Issue #1412 - Quality gate hit-rate metrics
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

import { isJudgeVerdict, type JudgeVerdict } from "@nightgauge/sdk";

import { GateMetricRecordSchema, type GateMetricRecord } from "../schemas/gateMetrics";

/** Relative path from workspace root to the gate metrics file */
const GATE_METRICS_FILE = ".nightgauge/health/gate-metrics.jsonl";

/**
 * Canonical gate_name for adversarial anti-hallucination judge verdicts (#3918).
 * The Go FeatureValidateGate.Verify() consumes these as ordinary gate-metrics
 * records — anything other than result="pass" trips the deterministic gate.
 */
const JUDGE_GATE_NAME = "judges" as const;

/** Default retention period in days */
const DEFAULT_RETENTION_DAYS = 90;

export class GateMetricsWriter {
  /**
   * Append a validated gate metric record to the JSONL file.
   *
   * Validates the record via Zod before writing. Creates the parent directory
   * if it does not exist. Silently skips invalid records (non-critical).
   */
  static async appendRecord(workspaceRoot: string, record: GateMetricRecord): Promise<void> {
    try {
      const validation = GateMetricRecordSchema.safeParse(record);
      if (!validation.success) {
        console.warn(
          `[Nightgauge] Invalid gate metric record, skipping write: ${validation.error.message}`
        );
        return;
      }

      const filePath = this.getFilePath(workspaceRoot);
      const dir = path.dirname(filePath);

      await fs.mkdir(dir, { recursive: true });
      await fs.appendFile(filePath, JSON.stringify(record) + "\n", "utf-8");
    } catch (error) {
      console.warn(`[Nightgauge] Failed to write gate metric record: ${error}`);
    }
  }

  /**
   * Fold the adversarial judge verdicts of an orchestrated feature-validate run
   * into the gate-metrics file as `{ gate_name: "judges", result }` records — so
   * an anti-hallucination judge's verdict becomes deterministic gate evidence
   * the Go `FeatureValidateGate.Verify()` already consumes, with ZERO new Go
   * scaffolding (#3918).
   *
   * Verdict → result mapping (fail-closed): only an explicit `"pass"` verdict
   * yields `result: "pass"`. Both `"fail"` and `"uncertain"` yield
   * `result: "fail"` — an unconfirmed claim must not be allowed to slip past the
   * deterministic gate. The Go gate trips on any `result != "pass"`, so a
   * rejected (or merely uncertain) judge verdict fails the gate.
   *
   * Accepts the raw `WorkflowEvent` node stream and selects judge nodes itself,
   * so callers can hand it the run's emitted events without pre-filtering. The
   * judge's `rationale` (truncated to the first line) is carried as
   * `error_summary` on a failing record for triage. Non-critical: never throws.
   *
   * @param workspaceRoot Absolute path to the repository root.
   * @param issueNumber   Issue the run belongs to (gate records are per-issue).
   * @param judgeNodes     JudgeVerdict nodes (or a mixed node stream — non-judge
   *                       nodes are ignored).
   */
  static async appendJudgeVerdicts(
    workspaceRoot: string,
    issueNumber: number,
    judgeNodes: ReadonlyArray<JudgeVerdict | { kind: string }>
  ): Promise<void> {
    const verdicts = judgeNodes.filter((n): n is JudgeVerdict =>
      isJudgeVerdict(n as Parameters<typeof isJudgeVerdict>[0])
    );

    for (const judge of verdicts) {
      const passed = judge.verdict === "pass";
      const errorSummary =
        !passed && judge.rationale ? judge.rationale.split("\n")[0].slice(0, 500) : undefined;

      const record: GateMetricRecord = {
        schema_version: "1",
        timestamp: judge.ts,
        issue_number: issueNumber,
        gate_name: JUDGE_GATE_NAME,
        result: passed ? "pass" : "fail",
        error_summary: errorSummary,
      };

      await this.appendRecord(workspaceRoot, record);
    }
  }

  /**
   * Read all gate metric records from the JSONL file.
   *
   * Skips malformed lines silently. Returns an empty array if the file
   * does not exist.
   */
  static async readAll(workspaceRoot: string): Promise<GateMetricRecord[]> {
    const filePath = this.getFilePath(workspaceRoot);
    let content: string;

    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch {
      return [];
    }

    const records: GateMetricRecord[] = [];
    for (const line of content.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const parsed = JSON.parse(trimmed);
        const validation = GateMetricRecordSchema.safeParse(parsed);
        if (validation.success) {
          records.push(validation.data);
        }
      } catch {
        // Skip malformed lines silently
      }
    }

    return records;
  }

  /**
   * Returns the absolute path to the gate metrics file.
   */
  static getFilePath(workspaceRoot: string): string {
    return path.join(workspaceRoot, GATE_METRICS_FILE);
  }

  /**
   * Remove entries older than the retention period by rewriting the file.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @param retentionDays - Number of days to retain (default: 90)
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
        return; // File doesn't exist — nothing to prune
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
      console.warn(`[Nightgauge] Failed to prune gate metrics: ${error}`);
    }
  }
}
