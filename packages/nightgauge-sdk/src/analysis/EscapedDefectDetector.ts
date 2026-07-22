/**
 * EscapedDefectDetector — post-merge CI check-run query via `gh` CLI.
 *
 * Compares CI failures against the stored selected-test snapshot from the
 * validate context. Any failing test file that was NOT in the selected set
 * is an "escaped defect" — evidence that the dependency graph has a gap.
 *
 * Gap records are appended to `.nightgauge/pipeline/graph-gaps.jsonl`
 * for downstream analysis by `SourceToTestGraph.buildSourceToTestGraph()`.
 *
 * Requires `gh` CLI with GitHub Actions access. When `gh` is unavailable,
 * all methods return empty arrays and log a warning rather than throwing.
 *
 * @see Issue #1975 - Validation & Cost Tracking
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

import { GraphGapRecordSchema, type GraphGapRecord } from "./selective-test-metrics-types.js";

const execFileAsync = promisify(execFile);

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/** Default path relative to the repo root */
export const DEFAULT_GAP_LOG_PATH = ".nightgauge/pipeline/graph-gaps.jsonl";

/**
 * Regex for extracting test file paths from CI job output text.
 * Matches paths like `packages/foo/src/__tests__/bar.test.ts`.
 * Best-effort: unrecognized formats are logged but not treated as errors.
 */
const TEST_FILE_REGEX = /(?:packages|src)\/[^\s,'"]+\.(?:test|spec)\.[jt]sx?/g;

// ---------------------------------------------------------------------------
// EscapedDefectDetector
// ---------------------------------------------------------------------------

export class EscapedDefectDetector {
  constructor(private readonly gapLogPath: string = DEFAULT_GAP_LOG_PATH) {}

  /**
   * Query GitHub Actions check runs for a merged PR's commit SHA and return
   * the names/output text of failing jobs.
   *
   * Uses: `gh api /repos/{owner}/{repo}/commits/{sha}/check-runs`
   *
   * @returns Array of failing job names (best-effort — empty when `gh` unavailable)
   */
  async fetchFailingCIJobs(
    owner: string,
    repo: string,
    prSha: string
  ): Promise<Array<{ name: string; outputText: string }>> {
    try {
      const { stdout } = await execFileAsync("gh", [
        "api",
        `/repos/${owner}/${repo}/commits/${prSha}/check-runs`,
        "--jq",
        '[.check_runs[] | select(.conclusion == "failure") | {name: .name, text: (.output.text // "")}]',
      ]);

      const raw = JSON.parse(stdout.trim()) as Array<{
        name: string;
        text: string;
      }>;

      return raw.map((job) => ({ name: job.name, outputText: job.text }));
    } catch {
      // gh unavailable or API error — return empty, no throw
      return [];
    }
  }

  /**
   * Extract test file paths from CI job output text using regex heuristics.
   * Returns an empty array for unrecognized output formats.
   */
  parseTestFilesFromOutput(outputText: string): string[] {
    const matches = new Set<string>();
    let match: RegExpExecArray | null;
    const regex = new RegExp(TEST_FILE_REGEX.source, TEST_FILE_REGEX.flags);

    while ((match = regex.exec(outputText)) !== null) {
      matches.add(match[0]);
    }

    return [...matches];
  }

  /**
   * Query failing CI jobs after a PR merges, compare against selected test
   * snapshot, and return escaped defect records.
   *
   * An escaped defect is a failing test file that was NOT in the selected set —
   * meaning the dependency graph did not map the changed source to that test.
   *
   * @param params.selectedTestFiles - Snapshot from validate-{N}.json
   */
  async detect(params: {
    issueNumber: number;
    prNumber: number;
    prSha: string;
    selectedTestFiles: string[];
    owner: string;
    repo: string;
  }): Promise<GraphGapRecord[]> {
    const { issueNumber, prNumber, prSha, selectedTestFiles, owner, repo } = params;

    const failingJobs = await this.fetchFailingCIJobs(owner, repo, prSha);
    if (failingJobs.length === 0) {
      return [];
    }

    const selectedSet = new Set(selectedTestFiles);
    const gaps: GraphGapRecord[] = [];
    const detectedAt = new Date().toISOString();

    for (const job of failingJobs) {
      const testFiles = this.parseTestFilesFromOutput(job.outputText);

      for (const testFile of testFiles) {
        if (!selectedSet.has(testFile)) {
          gaps.push({
            schema_version: "1.0",
            record_type: "graph_gap",
            issue_number: issueNumber,
            pr_number: prNumber,
            detected_at: detectedAt,
            failing_ci_job: job.name,
            failing_test_file: testFile,
            was_in_selected_set: false,
            gap_description: `CI job "${job.name}" failed on ${testFile} which was not in the selected test set — dependency graph edge missing`,
          });
        }
      }
    }

    return gaps;
  }

  /**
   * Append gap records to the graph-gaps JSONL file.
   *
   * @param gaps - Gap records to append (no-op if empty)
   */
  async recordGaps(gaps: GraphGapRecord[]): Promise<void> {
    if (gaps.length === 0) return;

    const dir = path.dirname(this.gapLogPath);
    await fs.mkdir(dir, { recursive: true });

    const lines = gaps.map((g) => JSON.stringify(g)).join("\n") + "\n";
    await fs.appendFile(this.gapLogPath, lines, "utf-8");
  }

  /**
   * Read all recorded graph gap records from the JSONL file.
   * Malformed lines are silently skipped.
   */
  async readAll(): Promise<GraphGapRecord[]> {
    let content: string;
    try {
      content = await fs.readFile(this.gapLogPath, "utf-8");
    } catch {
      return [];
    }

    const records: GraphGapRecord[] = [];
    for (const line of content.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const parsed = JSON.parse(trimmed);
        const validated = GraphGapRecordSchema.parse(parsed);
        records.push(validated);
      } catch {
        // Skip malformed lines
      }
    }

    return records;
  }
}
