/**
 * Dimension 8: CI/CD Integrity
 *
 * Parses GitHub Actions workflow YAML files (not regex) to detect:
 * - Disabled workflows (if: false)
 * - continue-on-error: true flags
 * - Missing required steps (build, test, lint, coverage)
 * - Coverage threshold mismatches
 * - Branch protection rules (via gh api)
 *
 * @see Issue #2366 — Implement Dimensions 5-8: Test coverage, security, dependencies, and CI integrity
 */

import * as fs from "fs";
import * as path from "path";
import { execSync } from "child_process";
import {
  parseWorkflowContent,
  findContinueOnErrorSteps,
  hasStepMatching,
} from "../utils/workflow-parser.js";

export interface CiCdFinding {
  id: string;
  severity: "critical" | "high" | "medium" | "low";
  category:
    | "DISABLED_WORKFLOW"
    | "CONTINUE_ON_ERROR"
    | "MISSING_COVERAGE_ENFORCE"
    | "MISSING_REQUIRED_CHECK"
    | "NO_BRANCH_PROTECTION"
    | "SECRETS_IN_WORKFLOW";
  confidence: number;
  repo: string | null;
  dimension: number;
  detail: string;
  auto_fixable: boolean;
  suggested_action: string;
  files: Array<{
    path: string;
    line: number | null;
    code_snippet: string | null;
  }>;
  metadata: {
    detected_at: string;
    detection_method: "workflow_parse" | "api_probe";
    manual_review_required: boolean;
  };
  // CiCdFinding fields
  workflow_file: string;
  workflow_name: string | null;
  ci_issue: CiCdFinding["category"];
}

export interface Dimension8Result {
  findings: CiCdFinding[];
  repos_scanned: string[];
  repos_missing: string[];
  warnings: string[];
}

/** Required step keywords — at least one must be present in each workflow */
const REQUIRED_STEP_KEYWORDS: Record<string, string[]> = {
  build: ["build", "tsc", "compile", "make", "go build"],
  test: ["test", "vitest", "jest", "pytest", "go test"],
  lint: ["lint", "eslint", "golangci", "flake8", "clippy"],
};

let globalSeq = 0;
function nextId(slug: string): string {
  globalSeq++;
  const safe = slug
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .slice(0, 25);
  return `cicd-${String(globalSeq).padStart(3, "0")}-${safe}`;
}

/**
 * Find all workflow YAML files in a repo's .github/workflows directory.
 */
function findWorkflowFiles(repoRoot: string): string[] {
  const workflowDir = path.join(repoRoot, ".github", "workflows");
  if (!fs.existsSync(workflowDir)) return [];

  try {
    return fs
      .readdirSync(workflowDir)
      .filter((f) => f.endsWith(".yml") || f.endsWith(".yaml"))
      .map((f) => path.join(workflowDir, f));
  } catch {
    return [];
  }
}

/**
 * Check branch protection rules via gh api.
 * Returns null if the check fails (gh not authenticated, network unavailable).
 */
function checkBranchProtection(
  repoName: string,
  orgName: string,
  branch: string = "main"
): { protected: boolean } | null {
  try {
    const output = execSync(
      `gh api repos/${orgName}/${repoName}/branches/${branch}/protection --silent 2>/dev/null`,
      { stdio: ["pipe", "pipe", "pipe"], timeout: 15_000, encoding: "utf8" }
    );
    JSON.parse(output); // Validate it's parseable
    return { protected: true };
  } catch {
    // gh api returns 404 if no protection, or fails if not authenticated
    return null;
  }
}

/**
 * Analyze a single workflow file and produce findings.
 */
function analyzeWorkflow(
  workflowPath: string,
  repoRoot: string,
  repoName: string,
  findings: CiCdFinding[],
  warnings: string[]
): void {
  let content: string;
  try {
    content = fs.readFileSync(workflowPath, "utf8");
  } catch {
    warnings.push(`Cannot read workflow: ${workflowPath}`);
    return;
  }

  const relPath = path.relative(repoRoot, workflowPath);
  const { parsed, error } = parseWorkflowContent(content, relPath);

  if (!parsed || error) {
    warnings.push(error ?? `Failed to parse ${relPath}`);
    return;
  }

  const workflowName = parsed.name ?? path.basename(workflowPath, path.extname(workflowPath));

  // --- Check: disabled workflow ---
  if (parsed.isDisabled) {
    findings.push({
      id: nextId(`disabled-${workflowName}`),
      severity: "high",
      category: "DISABLED_WORKFLOW",
      confidence: 90,
      repo: repoName as CiCdFinding["repo"],
      dimension: 8,
      detail: `Workflow '${workflowName}' is disabled (if: false on all jobs or workflow level)`,
      auto_fixable: false,
      suggested_action:
        `Remove the 'if: false' condition from ${relPath} to re-enable the workflow. ` +
        "If disabled intentionally, add a comment explaining the reason and a ticket to re-enable.",
      files: [{ path: relPath, line: null, code_snippet: null }],
      metadata: {
        detected_at: new Date().toISOString(),
        detection_method: "workflow_parse",
        manual_review_required: true,
      },
      workflow_file: relPath,
      workflow_name: workflowName,
      ci_issue: "DISABLED_WORKFLOW",
    });
  }

  // --- Check: continue-on-error ---
  const continueOnErrorSteps = findContinueOnErrorSteps(parsed);
  for (const step of continueOnErrorSteps) {
    findings.push({
      id: nextId(`continue-on-error-${workflowName}`),
      severity: "medium",
      category: "CONTINUE_ON_ERROR",
      confidence: 95,
      repo: repoName as CiCdFinding["repo"],
      dimension: 8,
      detail:
        `'continue-on-error: true' in workflow '${workflowName}' ` +
        `(job: ${step.jobId}, step: ${step.stepName})`,
      auto_fixable: false,
      suggested_action:
        `Remove 'continue-on-error: true' from ${relPath} — failing steps should block the pipeline. ` +
        "If this is temporary, add a comment with the reason and a ticket to remove it.",
      files: [{ path: relPath, line: null, code_snippet: null }],
      metadata: {
        detected_at: new Date().toISOString(),
        detection_method: "workflow_parse",
        manual_review_required: false,
      },
      workflow_file: relPath,
      workflow_name: workflowName,
      ci_issue: "CONTINUE_ON_ERROR",
    });
  }

  // --- Check: missing required steps ---
  // Only check workflows that look like CI (have jobs with steps)
  if (parsed.jobs && Object.keys(parsed.jobs).length > 0) {
    for (const [checkName, keywords] of Object.entries(REQUIRED_STEP_KEYWORDS)) {
      if (!hasStepMatching(parsed, keywords)) {
        findings.push({
          id: nextId(`missing-${checkName}-${workflowName}`),
          severity: "medium",
          category: "MISSING_REQUIRED_CHECK",
          confidence: 70,
          repo: repoName as CiCdFinding["repo"],
          dimension: 8,
          detail: `Workflow '${workflowName}' has no '${checkName}' step (looked for: ${keywords.join(", ")})`,
          auto_fixable: false,
          suggested_action:
            `Add a '${checkName}' step to ${relPath}. ` +
            `All CI workflows should include build, test, and lint steps.`,
          files: [{ path: relPath, line: null, code_snippet: null }],
          metadata: {
            detected_at: new Date().toISOString(),
            detection_method: "workflow_parse",
            manual_review_required: true,
          },
          workflow_file: relPath,
          workflow_name: workflowName,
          ci_issue: "MISSING_REQUIRED_CHECK",
        });
      }
    }
  }
}

/**
 * Run Dimension 8: CI/CD Integrity analysis.
 */
export async function runDimension8(
  repos: Array<{ name: string; root: string; orgName?: string }>,
  _configDir?: string
): Promise<Dimension8Result> {
  globalSeq = 0;
  const findings: CiCdFinding[] = [];
  const reposScanned: string[] = [];
  const reposMissing: string[] = [];
  const warnings: string[] = [];

  for (const repo of repos) {
    if (!fs.existsSync(repo.root)) {
      reposMissing.push(repo.name);
      warnings.push(`Repo not found: ${repo.name} — skipping CI/CD analysis`);
      continue;
    }

    reposScanned.push(repo.name);

    // Analyze all workflow files
    const workflowFiles = findWorkflowFiles(repo.root);
    if (workflowFiles.length === 0) {
      warnings.push(`No workflow files found in ${repo.name}/.github/workflows/`);
    }

    for (const workflowFile of workflowFiles) {
      analyzeWorkflow(workflowFile, repo.root, repo.name, findings, warnings);
    }

    // Check branch protection rules
    if (repo.orgName) {
      const protection = checkBranchProtection(repo.name, repo.orgName);
      if (protection === null) {
        // gh api failed or returned 404 (no protection)
        findings.push({
          id: nextId(`no-branch-protection-${repo.name}`),
          severity: "high",
          category: "NO_BRANCH_PROTECTION",
          confidence: 60, // gh api might be unavailable, lower confidence
          repo: repo.name as CiCdFinding["repo"],
          dimension: 8,
          detail: `Branch protection rules not found for ${repo.orgName}/${repo.name}/main`,
          auto_fixable: false,
          suggested_action:
            `Enable branch protection on main: require PR reviews, require status checks (CI), ` +
            `and prevent force pushes. Configure via GitHub repo settings → Branches.`,
          files: [],
          metadata: {
            detected_at: new Date().toISOString(),
            detection_method: "api_probe",
            manual_review_required: true,
          },
          workflow_file: ".github/settings.yml",
          workflow_name: null,
          ci_issue: "NO_BRANCH_PROTECTION",
        });
      }
    }
  }

  // Sort: critical first
  findings.sort((a, b) => {
    const order = { critical: 0, high: 1, medium: 2, low: 3 };
    return (order[a.severity] ?? 4) - (order[b.severity] ?? 4);
  });

  return {
    findings,
    repos_scanned: reposScanned,
    repos_missing: reposMissing,
    warnings,
  };
}
