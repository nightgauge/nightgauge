/**
 * Workflow Parser — GitHub Actions YAML reader
 *
 * Parses .github/workflows/*.yml files using js-yaml (not regex).
 * Extracts job definitions, step names, conditions, and flags
 * that indicate CI integrity issues.
 */

import * as yaml from "js-yaml";

export interface WorkflowStep {
  name?: string;
  uses?: string;
  run?: string;
  "continue-on-error"?: boolean;
  if?: string | boolean;
}

export interface WorkflowJob {
  name?: string;
  if?: string | boolean;
  steps?: WorkflowStep[];
  "continue-on-error"?: boolean;
}

export interface ParsedWorkflow {
  /** Workflow display name */
  name?: string;
  /** Trigger events */
  on?: unknown;
  /** Jobs map */
  jobs?: Record<string, WorkflowJob>;
  /** Whether the entire workflow is disabled (top-level if: false) */
  isDisabled: boolean;
  /** Raw parsed object for additional inspection */
  raw: unknown;
}

export interface WorkflowParseError {
  file: string;
  error: string;
}

export interface WorkflowParseResult {
  workflows: Array<{ file: string; parsed: ParsedWorkflow }>;
  errors: WorkflowParseError[];
}

/**
 * Parse a single workflow YAML string.
 * Returns null on parse failure and records the error.
 */
export function parseWorkflowContent(
  content: string,
  filePath: string
): { parsed: ParsedWorkflow | null; error?: string } {
  let raw: unknown;
  try {
    raw = yaml.load(content);
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    return { parsed: null, error: `YAML parse error in ${filePath}: ${msg}` };
  }

  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return { parsed: null, error: `Empty or non-object workflow: ${filePath}` };
  }

  const doc = raw as Record<string, unknown>;

  // Detect top-level disable: "if: false" at workflow root or on all jobs
  const topIf = doc["if"];
  const isDisabled =
    topIf === false ||
    topIf === "false" ||
    isAllJobsDisabled(doc["jobs"] as Record<string, WorkflowJob> | undefined);

  const parsed: ParsedWorkflow = {
    name: typeof doc["name"] === "string" ? doc["name"] : undefined,
    on: doc["on"],
    jobs: doc["jobs"] as Record<string, WorkflowJob> | undefined,
    isDisabled,
    raw,
  };

  return { parsed };
}

function isAllJobsDisabled(jobs: Record<string, WorkflowJob> | undefined): boolean {
  if (!jobs || Object.keys(jobs).length === 0) return false;
  return Object.values(jobs).every((j) => j?.if === false || j?.if === "false");
}

/**
 * Find all steps with continue-on-error: true across all jobs.
 * Returns list of { jobId, stepIndex, stepName } for each match.
 */
export function findContinueOnErrorSteps(
  workflow: ParsedWorkflow
): Array<{ jobId: string; stepIndex: number; stepName: string }> {
  const results: Array<{
    jobId: string;
    stepIndex: number;
    stepName: string;
  }> = [];

  if (!workflow.jobs) return results;

  for (const [jobId, job] of Object.entries(workflow.jobs)) {
    // Job-level continue-on-error
    if (job?.["continue-on-error"] === true) {
      results.push({ jobId, stepIndex: -1, stepName: `(job: ${jobId})` });
    }

    // Step-level continue-on-error
    if (Array.isArray(job?.steps)) {
      for (let i = 0; i < job.steps.length; i++) {
        const step = job.steps[i];
        if (step?.["continue-on-error"] === true) {
          results.push({
            jobId,
            stepIndex: i,
            stepName: step.name ?? step.run?.split("\n")[0] ?? `step-${i}`,
          });
        }
      }
    }
  }

  return results;
}

/**
 * Get all step names across all jobs in a workflow (lowercased for matching).
 */
export function getAllStepNames(workflow: ParsedWorkflow): string[] {
  const names: string[] = [];
  if (!workflow.jobs) return names;

  for (const job of Object.values(workflow.jobs)) {
    if (Array.isArray(job?.steps)) {
      for (const step of job.steps) {
        if (step?.name) names.push(step.name.toLowerCase());
        if (step?.run) names.push(step.run.toLowerCase().split("\n")[0]);
        if (step?.uses) names.push(step.uses.toLowerCase());
      }
    }
  }

  return names;
}

/**
 * Check if a workflow has a step matching any of the given keywords.
 * Used to verify build, test, lint, coverage steps exist.
 */
export function hasStepMatching(workflow: ParsedWorkflow, keywords: string[]): boolean {
  const stepNames = getAllStepNames(workflow);
  return keywords.some((kw) => stepNames.some((name) => name.includes(kw.toLowerCase())));
}
