/**
 * FindingToIssueEngine - Convert Health Findings into GitHub Issues
 *
 * Deterministic engine that processes HealthAnalysisResult findings into
 * well-structured GitHub issues with proper labels, priority, sizing,
 * acceptance criteria, and implementation guidance.
 *
 * When 3+ related findings exist in a dimension, automatically groups
 * them into an epic with sub-issues.
 *
 * @see Issue #1102 - Finding-to-Issue Generation Engine
 */

import { execSync } from "node:child_process";
import type {
  HealthAnalysisResult,
  Finding,
  HealthDimension,
  CrossReference,
  FindingToIssueConfig,
  GeneratedIssue,
  EpicGroup,
  FindingToIssueResult,
} from "./types.js";
import { DEFAULT_FINDING_TO_ISSUE_CONFIG } from "./types.js";
import { severityMeetsThreshold, findingToLabels } from "./severityMapping.js";
import {
  formatIssueTitle,
  formatIssueBody,
  formatEpicTitle,
  formatEpicBody,
} from "./issueTemplates.js";

// ── Helpers ──────────────────────────────────────────────────────

/** Extract all findings from a HealthAnalysisResult across all dimensions. */
function extractFindings(result: HealthAnalysisResult): Finding[] {
  const findings: Finding[] = [];
  for (const dimResult of Object.values(result.dimensions)) {
    if (dimResult) {
      findings.push(...dimResult.findings);
    }
  }
  return findings;
}

/** Group findings by their dimension. */
function groupByDimension(findings: Finding[]): Map<HealthDimension, Finding[]> {
  const groups = new Map<HealthDimension, Finding[]>();
  for (const finding of findings) {
    const existing = groups.get(finding.dimension) ?? [];
    existing.push(finding);
    groups.set(finding.dimension, existing);
  }
  return groups;
}

/**
 * Find cross-references that relate to a specific finding.
 * A cross-reference is relevant if it mentions the finding's ID
 * in its correlatedFindings array.
 */
function findRelatedCrossRefs(findingId: string, crossRefs: CrossReference[]): CrossReference[] {
  return crossRefs.filter((cr) => cr.correlatedFindings.includes(findingId));
}

/**
 * Parse an issue number from a GitHub issue URL.
 * E.g. "https://github.com/owner/repo/issues/123" → 123
 */
function parseIssueNumber(url: string): number | undefined {
  const match = url.match(/\/issues\/(\d+)$/);
  return match ? parseInt(match[1], 10) : undefined;
}

// ── Engine ───────────────────────────────────────────────────────

export class FindingToIssueEngine {
  private readonly config: FindingToIssueConfig;

  constructor(config?: Partial<FindingToIssueConfig>) {
    this.config = { ...DEFAULT_FINDING_TO_ISSUE_CONFIG, ...config };
  }

  /**
   * Convert health analysis findings into GitHub issues.
   *
   * @param analysisResult - Output from HealthAnalysisEngine.analyze()
   * @param healthReportRef - Optional reference to the report (e.g., filename)
   * @returns Result with created/previewed issues
   */
  async generate(
    analysisResult: HealthAnalysisResult,
    healthReportRef?: string
  ): Promise<FindingToIssueResult> {
    // Step 1: Extract all findings
    const allFindings = extractFindings(analysisResult);

    // Step 2: Filter by severity threshold
    const filtered = allFindings.filter((f) =>
      severityMeetsThreshold(f.severity, this.config.severityThreshold)
    );

    // Step 3: Group by dimension to detect epic candidates
    const dimensionGroups = groupByDimension(filtered);

    const result: FindingToIssueResult = {
      totalFindings: allFindings.length,
      filteredFindings: filtered.length,
      duplicatesSkipped: 0,
      issuesCreated: 0,
      epicsCreated: 0,
      generatedIssues: [],
      epicGroups: [],
      dryRun: this.config.dryRun,
      healthReportRef,
    };

    // Step 4: Process each dimension group
    for (const [dimension, findings] of dimensionGroups) {
      if (findings.length >= this.config.epicGroupingThreshold) {
        // Epic grouping: create epic + sub-issues
        const epicGroup = await this.processEpicGroup(
          dimension,
          findings,
          analysisResult.crossReferences,
          healthReportRef
        );
        result.epicGroups.push(epicGroup);
        result.epicsCreated += epicGroup.epicNumber !== undefined ? 1 : 0;
        result.generatedIssues.push(...epicGroup.subIssues);
        result.issuesCreated += epicGroup.subIssues.filter(
          (si) => si.issueNumber !== undefined && !si.skipped
        ).length;
        result.duplicatesSkipped += epicGroup.subIssues.filter((si) => si.skipped).length;
      } else {
        // Standalone issues
        for (const finding of findings) {
          const generated = await this.processStandaloneFinding(
            finding,
            analysisResult.crossReferences,
            healthReportRef
          );
          result.generatedIssues.push(generated);
          if (generated.skipped) {
            result.duplicatesSkipped++;
          } else if (generated.issueNumber !== undefined) {
            result.issuesCreated++;
          }
        }
      }
    }

    return result;
  }

  /**
   * Process a group of findings as an epic with sub-issues.
   */
  private async processEpicGroup(
    dimension: HealthDimension,
    findings: Finding[],
    crossRefs: CrossReference[],
    healthReportRef?: string
  ): Promise<EpicGroup> {
    const epicTitle = formatEpicTitle(dimension, findings.length);
    const epicBody = formatEpicBody(dimension, findings, healthReportRef);
    const epicLabels = ["type:epic", `component:health-${dimension}`];

    const epicGroup: EpicGroup = {
      dimension,
      findings,
      epicTitle,
      epicBody,
      epicLabels,
      subIssues: [],
    };

    if (this.config.dryRun) {
      // Dry-run: populate structure without creating
      for (const finding of findings) {
        const relatedCrossRefs = findRelatedCrossRefs(finding.id, crossRefs);
        epicGroup.subIssues.push({
          findingId: finding.id,
          title: formatIssueTitle(finding),
          body: formatIssueBody(finding, healthReportRef, relatedCrossRefs),
          labels: findingToLabels(finding),
          severity: finding.severity,
          dimension: finding.dimension,
        });
      }
      return epicGroup;
    }

    // Create the epic issue first
    const epicNumber = await this.createIssue(epicTitle, epicBody, epicLabels);
    epicGroup.epicNumber = epicNumber;

    // Create sub-issues linked to the epic
    for (const finding of findings) {
      const relatedCrossRefs = findRelatedCrossRefs(finding.id, crossRefs);
      const title = formatIssueTitle(finding);
      const body = formatIssueBody(finding, healthReportRef, relatedCrossRefs);
      const labels = findingToLabels(finding);

      // Check for duplicates
      const isDuplicate = await this.checkDuplicate(title);
      if (isDuplicate) {
        epicGroup.subIssues.push({
          findingId: finding.id,
          title,
          body,
          labels,
          severity: finding.severity,
          dimension: finding.dimension,
          epicNumber,
          skipped: true,
          skipReason: "Duplicate issue detected",
        });
        continue;
      }

      // Create sub-issue via create-sub-issue.sh
      const subIssueResult = epicNumber
        ? await this.createSubIssue(epicNumber, title, body, labels)
        : await this.createIssueWithProject(title, body, labels);

      epicGroup.subIssues.push({
        findingId: finding.id,
        title,
        body,
        labels,
        severity: finding.severity,
        dimension: finding.dimension,
        issueNumber: subIssueResult.issueNumber,
        issueUrl: subIssueResult.issueUrl,
        epicNumber,
      });
    }

    return epicGroup;
  }

  /**
   * Process a standalone finding as an individual issue.
   */
  private async processStandaloneFinding(
    finding: Finding,
    crossRefs: CrossReference[],
    healthReportRef?: string
  ): Promise<GeneratedIssue> {
    const relatedCrossRefs = findRelatedCrossRefs(finding.id, crossRefs);
    const title = formatIssueTitle(finding);
    const body = formatIssueBody(finding, healthReportRef, relatedCrossRefs);
    const labels = findingToLabels(finding);

    if (this.config.dryRun) {
      return {
        findingId: finding.id,
        title,
        body,
        labels,
        severity: finding.severity,
        dimension: finding.dimension,
      };
    }

    // Check for duplicates
    const isDuplicate = await this.checkDuplicate(title);
    if (isDuplicate) {
      return {
        findingId: finding.id,
        title,
        body,
        labels,
        severity: finding.severity,
        dimension: finding.dimension,
        skipped: true,
        skipReason: "Duplicate issue detected",
      };
    }

    // Create standalone issue via gh + add-to-project.sh
    const result = await this.createIssueWithProject(title, body, labels);
    return {
      findingId: finding.id,
      title,
      body,
      labels,
      severity: finding.severity,
      dimension: finding.dimension,
      issueNumber: result.issueNumber,
      issueUrl: result.issueUrl,
    };
  }

  /**
   * Check if an issue with a similar title already exists.
   * Uses `gh issue list --search` for fuzzy title matching.
   */
  private async checkDuplicate(title: string): Promise<boolean> {
    const searchTerm = title.replace(/^\[HEALTH\]\s*/, "");
    const repoFlag = this.config.repository ? `--repo ${this.config.repository}` : "";

    try {
      const stdout = execSync(
        `gh issue list --search "${this.escapeShell(searchTerm)}" --state open --json number,title --limit 5 ${repoFlag}`,
        { encoding: "utf-8", timeout: 15000 }
      ).trim();

      const issues = JSON.parse(stdout || "[]") as Array<{
        number: number;
        title: string;
      }>;

      // Check for fuzzy match: existing title contains our search term
      // or our search term contains the existing title
      const normalizedSearch = searchTerm.toLowerCase();
      return issues.some((issue) => {
        const normalizedTitle = issue.title.replace(/^\[HEALTH\]\s*/i, "").toLowerCase();
        return (
          normalizedTitle.includes(normalizedSearch) || normalizedSearch.includes(normalizedTitle)
        );
      });
    } catch {
      // If dedup check fails, proceed without dedup
      return false;
    }
  }

  /**
   * Create a GitHub issue via `gh issue create`.
   * Returns the created issue number.
   */
  private async createIssue(
    title: string,
    body: string,
    labels: string[]
  ): Promise<number | undefined> {
    const repoFlag = this.config.repository ? `--repo ${this.config.repository}` : "";
    const labelFlags = labels.map((l) => `--label "${l}"`).join(" ");

    try {
      const stdout = execSync(
        `gh issue create --title "${this.escapeShell(title)}" --body "${this.escapeShell(body)}" ${labelFlags} ${repoFlag}`,
        { encoding: "utf-8", timeout: 30000 }
      ).trim();

      return parseIssueNumber(stdout);
    } catch {
      return undefined;
    }
  }

  /**
   * Create an issue and add it to the project board via `add-to-project.sh`.
   */
  private async createIssueWithProject(
    title: string,
    body: string,
    labels: string[]
  ): Promise<{ issueNumber?: number; issueUrl?: string }> {
    const issueNumber = await this.createIssue(title, body, labels);
    if (issueNumber === undefined) {
      return {};
    }

    // Sync to project board
    const hooksDir = this.resolveHooksDir();
    const addToProject = `${hooksDir}/add-to-project.sh`;
    const repoFlag = this.config.repository ? `--repo ${this.config.repository}` : "";

    try {
      execSync(`"${addToProject}" ${issueNumber} ${repoFlag}`, {
        encoding: "utf-8",
        timeout: 30000,
      });
    } catch {
      // Project sync failure is non-fatal
    }

    const repoUrl = this.config.repository
      ? `https://github.com/${this.config.repository}`
      : this.getRepoUrl();

    return {
      issueNumber,
      issueUrl: repoUrl ? `${repoUrl}/issues/${issueNumber}` : undefined,
    };
  }

  /**
   * Create a sub-issue linked to a parent epic via `create-sub-issue.sh`.
   */
  private async createSubIssue(
    parentIssueNumber: number,
    title: string,
    body: string,
    labels: string[]
  ): Promise<{ issueNumber?: number; issueUrl?: string }> {
    const hooksDir = this.resolveHooksDir();
    const script = `${hooksDir}/create-sub-issue.sh`;
    const repoFlag = this.config.repository ? `--repo ${this.config.repository}` : "";
    const labelFlags = labels.map((l) => `--label "${l}"`).join(" ");

    try {
      const stdout = execSync(
        `"${script}" ${parentIssueNumber} "${this.escapeShell(title)}" "${this.escapeShell(body)}" ${repoFlag} ${labelFlags}`,
        { encoding: "utf-8", timeout: 30000 }
      ).trim();

      const parsed = JSON.parse(stdout) as {
        issue_number: number;
        issue_url: string;
      };
      return {
        issueNumber: parsed.issue_number,
        issueUrl: parsed.issue_url,
      };
    } catch {
      return {};
    }
  }

  /** Resolve the hooks directory path. */
  private resolveHooksDir(): string {
    if (this.config.hooksDir) return this.config.hooksDir;
    return "claude-plugins/nightgauge/hooks/lib";
  }

  /** Get the current repository URL. */
  private getRepoUrl(): string | undefined {
    try {
      return execSync("gh repo view --json url -q .url", {
        encoding: "utf-8",
        timeout: 10000,
      }).trim();
    } catch {
      return undefined;
    }
  }

  /**
   * Escape a string for safe use inside a double-quoted shell argument.
   *
   * Escapes characters that are interpreted by the shell even inside double
   * quotes: backslash, double-quote, dollar sign, backtick, and exclamation
   * mark (history expansion). Strips literal newlines and carriage returns
   * to prevent argument injection via multi-line input.
   *
   * Note: This is a defense-in-depth measure for AI-generated content.
   * For user-controlled input, prefer execFileSync with array arguments
   * (see WorktreeManager.ts for the recommended pattern).
   */
  private escapeShell(str: string): string {
    return str
      .replace(/\\/g, "\\\\") // backslash must be first
      .replace(/"/g, '\\"')
      .replace(/\$/g, "\\$")
      .replace(/`/g, "\\`")
      .replace(/!/g, "\\!")
      .replace(/\r?\n/g, " "); // collapse newlines to spaces
  }
}
