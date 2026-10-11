/**
 * EpicEstimator - Estimate total work time for epics based on sub-issue sizes
 *
 * Aggregates calibrated time estimates for all sub-issues in an epic
 * to provide realistic project timelines.
 *
 * @see Issue #310 - Add actual work time feedback loop
 */

import { exec, execFile } from "child_process";
import * as fs from "fs/promises";
import * as yaml from "js-yaml";
import { promisify } from "util";

const execAsync = promisify(exec);
const execFileAsync = promisify(execFile);

/**
 * Size label from GitHub issues
 */
export type SizeLabel = "XS" | "S" | "M" | "L" | "XL" | null;

/**
 * Sub-issue with estimated time
 */
export interface SubIssueEstimate {
  number: number;
  title: string;
  size: SizeLabel;
  estimated_minutes: number;
  status: "open" | "closed";
}

/**
 * Epic time estimation result
 */
export interface EpicEstimate {
  epic_number: number;
  epic_title: string;
  sub_issues: SubIssueEstimate[];
  total_estimated_minutes: number;
  total_remaining_minutes: number;
  integration_buffer_minutes: number;
  confidence: "high" | "medium" | "low";
  confidence_detail: string;
}

/**
 * Complexity model structure from YAML
 */
interface ComplexityModel {
  work_time_feedback?: {
    enabled: boolean;
    size_averages?: Record<
      NonNullable<SizeLabel>,
      {
        actual_average: number;
        observation_count: number;
      }
    >;
  };
}

/**
 * Default time estimates per size (in minutes)
 * Used when complexity model doesn't exist or has insufficient data
 */
const DEFAULT_ESTIMATES: Record<NonNullable<SizeLabel>, number> = {
  XS: 30,
  S: 120,
  M: 600,
  L: 1920,
  XL: 4800,
};

/**
 * Sub-issues read per request: GitHub's maximum page size.
 */
const SUB_ISSUE_PAGE_SIZE = 100;

/**
 * Pages of an epic's subIssues connection read before the read fails, the
 * first included. Room for 2,000 sub-issues, while bounding the requests one
 * runaway connection can spend. The same cap the nightgauge binary puts on
 * every relationship connection it reads.
 */
const MAX_SUB_ISSUE_PAGES = 20;

/**
 * One page of an epic's sub-issues. `$after` is absent on the first page.
 */
const EPIC_SUB_ISSUES_QUERY = `
  query($owner: String!, $repo: String!, $epicNumber: Int!, $after: String) {
    repository(owner: $owner, name: $repo) {
      issue(number: $epicNumber) {
        title
        subIssues(first: ${SUB_ISSUE_PAGE_SIZE}, after: $after) {
          pageInfo { hasNextPage endCursor }
          nodes {
            number
            title
            state
            labels(first: 10) { nodes { name } }
          }
        }
      }
    }
  }
`;

/**
 * One page of the subIssues connection, as EPIC_SUB_ISSUES_QUERY selects it.
 */
interface SubIssueConnectionPage {
  pageInfo?: { hasNextPage?: boolean; endCursor?: string | null };
  nodes?: Array<{
    number: number;
    title: string;
    state: string;
    labels?: { nodes?: Array<{ name: string }> };
  }>;
}

/**
 * EpicEstimator service
 */
export class EpicEstimator {
  private modelPath: string;
  private workspaceRoot: string | undefined;

  /**
   * @param modelPath - Absolute path of the complexity model, normally
   *   `checkoutPath("complexityModel", workspaceRoot)` (ADR-024 § 7).
   */
  constructor(modelPath: string, workspaceRoot?: string) {
    this.modelPath = modelPath;
    this.workspaceRoot = workspaceRoot;
  }

  /**
   * Estimate total work time for an epic
   *
   * @param epicNumber - GitHub issue number of the epic
   * @returns Epic estimation with sub-issue breakdown
   */
  async estimateEpic(epicNumber: number): Promise<EpicEstimate> {
    // 1. Fetch epic with native sub-issues in a single GraphQL call
    const epic = await this.fetchEpicWithSubIssues(epicNumber);

    if (epic.subIssues.length === 0) {
      throw new Error(
        `Epic #${epicNumber} has no sub-issues. ` +
          `Add sub-issues via GitHub UI or link-sub-issue.sh.`
      );
    }

    // 2. Build sub-issue estimates from the GraphQL data
    const subIssues: SubIssueEstimate[] = [];
    for (const sub of epic.subIssues) {
      const sizeLabel = sub.labels
        ?.find((l) => l.startsWith("size:"))
        ?.replace("size:", "") as SizeLabel;

      const estimatedMinutes = sizeLabel ? await this.getCalibratedEstimate(sizeLabel) : 0;

      subIssues.push({
        number: sub.number,
        title: sub.title,
        size: sizeLabel || null,
        estimated_minutes: estimatedMinutes,
        status: sub.state === "OPEN" ? "open" : "closed",
      });
    }

    // 3. Calculate totals
    const totalEstimated = subIssues.reduce((sum, issue) => sum + issue.estimated_minutes, 0);

    const totalRemaining = subIssues
      .filter((issue) => issue.status === "open")
      .reduce((sum, issue) => sum + issue.estimated_minutes, 0);

    // 4. Add integration buffer (15% for coordination overhead)
    const integrationBuffer = Math.round(totalEstimated * 0.15);

    // 5. Calculate confidence
    const confidence = await this.calculateConfidence(subIssues);

    return {
      epic_number: epicNumber,
      epic_title: epic.title,
      sub_issues: subIssues,
      total_estimated_minutes: totalEstimated + integrationBuffer,
      total_remaining_minutes: totalRemaining + Math.round(totalRemaining * 0.15),
      integration_buffer_minutes: integrationBuffer,
      confidence: confidence.level,
      confidence_detail: confidence.detail,
    };
  }

  /**
   * Fetch an epic and every one of its native sub-issues.
   *
   * Uses the GitHub sub-issues API (parent/child links) instead of parsing
   * body text for "#123" references. Returns the epic title and all sub-issue
   * data (number, title, state, labels).
   *
   * The subIssues connection is read page by page until GitHub reports no
   * next page (#1701). A connection that still has more after
   * MAX_SUB_ISSUE_PAGES pages, or that reports a next page with no cursor to
   * continue from, is an error: an estimate from part of an epic would look
   * like an estimate of the whole of it.
   */
  private async fetchEpicWithSubIssues(epicNumber: number): Promise<{
    title: string;
    subIssues: Array<{
      number: number;
      title: string;
      state: string;
      labels: string[];
    }>;
  }> {
    try {
      // Get owner/repo from current git context
      const { stdout: nwo } = await execAsync(
        "gh repo view --json nameWithOwner -q .nameWithOwner",
        { cwd: this.workspaceRoot }
      );
      const [owner, repo] = nwo.trim().split("/");

      let title = "";
      const subIssues: Array<{ number: number; title: string; state: string; labels: string[] }> =
        [];
      let after: string | undefined;
      for (let page = 1; ; page++) {
        const args = [
          "api",
          "graphql",
          "-F",
          `epicNumber=${epicNumber}`,
          "-f",
          `owner=${owner}`,
          "-f",
          `repo=${repo}`,
          "-f",
          `query=${EPIC_SUB_ISSUES_QUERY}`,
        ];
        if (after !== undefined) {
          args.push("-f", `after=${after}`);
        }
        const { stdout: result } = await execFileAsync("gh", args, { cwd: this.workspaceRoot });

        const response = JSON.parse(result);
        const issue = response.data?.repository?.issue;

        if (!issue) {
          throw new Error(`Epic #${epicNumber} not found`);
        }
        if (page === 1) {
          title = issue.title;
        }

        const connection: SubIssueConnectionPage | undefined = issue.subIssues;
        for (const n of connection?.nodes || []) {
          subIssues.push({
            number: n.number,
            title: n.title,
            state: n.state,
            labels: (n.labels?.nodes || []).map((l) => l.name),
          });
        }

        const pageInfo = connection?.pageInfo;
        if (!pageInfo?.hasNextPage) {
          break;
        }
        if (page >= MAX_SUB_ISSUE_PAGES) {
          throw new Error(
            `sub-issue list truncated: ${subIssues.length} read and more remain ` +
              `after the ${MAX_SUB_ISSUE_PAGES}-page cap`
          );
        }
        if (!pageInfo.endCursor) {
          throw new Error(
            `sub-issue list truncated: page ${page} reports a next page but no cursor`
          );
        }
        after = pageInfo.endCursor;
      }

      return { title, subIssues };
    } catch (error) {
      throw new Error(
        `Failed to fetch epic #${epicNumber}: ${error instanceof Error ? error.message : "Unknown error"}`,
        { cause: error }
      );
    }
  }

  /**
   * Get calibrated estimate for a size label
   *
   * Reads from complexity model if available, falls back to defaults.
   */
  private async getCalibratedEstimate(size: SizeLabel): Promise<number> {
    if (!size) {
      return 0;
    }

    try {
      const content = await fs.readFile(this.modelPath, "utf-8");
      const model = yaml.load(content) as ComplexityModel;

      const feedback = model.work_time_feedback;
      if (!feedback || !feedback.enabled) {
        return DEFAULT_ESTIMATES[size];
      }

      // Use calibrated average if we have enough data (3+ observations)
      const sizeAverage = feedback.size_averages?.[size];
      if (sizeAverage && sizeAverage.observation_count >= 3) {
        return Math.round(sizeAverage.actual_average);
      }

      // Fall back to default
      return DEFAULT_ESTIMATES[size];
    } catch {
      // Model file doesn't exist or is corrupted - use defaults
      return DEFAULT_ESTIMATES[size];
    }
  }

  /**
   * Calculate confidence level based on observation counts
   */
  private async calculateConfidence(
    subIssues: SubIssueEstimate[]
  ): Promise<{ level: "high" | "medium" | "low"; detail: string }> {
    try {
      const content = await fs.readFile(this.modelPath, "utf-8");
      const model = yaml.load(content) as ComplexityModel;

      const feedback = model.work_time_feedback;
      if (!feedback || !feedback.enabled) {
        return {
          level: "low",
          detail: "Using default estimates (no historical data)",
        };
      }

      // Count observations per size
      const sizeObsCounts: Record<string, number> = {};
      for (const issue of subIssues) {
        if (issue.size) {
          const sizeAvg = feedback.size_averages?.[issue.size];
          sizeObsCounts[issue.size] = sizeAvg?.observation_count || 0;
        }
      }

      const avgObsCount =
        Object.values(sizeObsCounts).reduce((a, b) => a + b, 0) / Object.keys(sizeObsCounts).length;

      if (avgObsCount >= 10) {
        return {
          level: "high",
          detail: `Based on ${Math.round(avgObsCount)} avg observations per size`,
        };
      } else if (avgObsCount >= 5) {
        return {
          level: "medium",
          detail: `Based on ${Math.round(avgObsCount)} avg observations per size`,
        };
      } else {
        return {
          level: "low",
          detail: `Limited data (${Math.round(avgObsCount)} avg observations per size)`,
        };
      }
    } catch {
      return {
        level: "low",
        detail: "Using default estimates (no historical data)",
      };
    }
  }

  /**
   * Format epic estimate as human-readable string
   */
  static format(estimate: EpicEstimate): string {
    const hours = Math.round(estimate.total_estimated_minutes / 60);
    const days = (estimate.total_estimated_minutes / (8 * 60)).toFixed(1);

    let output = `Epic #${estimate.epic_number}: ${estimate.epic_title}\n\n`;
    output += `Sub-issues:\n`;

    for (const issue of estimate.sub_issues) {
      const statusIcon = issue.status === "closed" ? "✓" : " ";
      const sizeStr = issue.size || "?";
      const minutes = issue.estimated_minutes;
      output += `  [${statusIcon}] #${issue.number} (${sizeStr}): ${minutes} min - ${issue.title}\n`;
    }

    const remainingHours = Math.round(estimate.total_remaining_minutes / 60);
    const remainingDays = (estimate.total_remaining_minutes / (8 * 60)).toFixed(1);

    output += `\n`;
    output += `Total estimate: ${estimate.total_estimated_minutes} minutes (${hours}h / ~${days} days)\n`;
    output += `Remaining work: ${estimate.total_remaining_minutes} minutes (${remainingHours}h / ~${remainingDays} days)\n`;
    output += `Integration buffer: ${estimate.integration_buffer_minutes} minutes (15%)\n`;
    output += `\n`;
    output += `Confidence: ${estimate.confidence.toUpperCase()} - ${estimate.confidence_detail}\n`;

    return output;
  }
}
