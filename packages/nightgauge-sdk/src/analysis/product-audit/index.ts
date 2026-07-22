/**
 * Product Audit — 8-dimension quality audit orchestrator
 *
 * Coordinates dimensions 5-8 (test coverage, security, dependencies, CI/CD).
 * Dimensions 1-4 (API alignment, lifecycle, documentation, feature parity)
 * are handled by Go binary (`nightgauge audit`) and shell-based runners.
 *
 * @see skills/nightgauge-product-audit/SKILL.md
 * @see schemas/product-audit-finding-v1.json
 * @see schemas/product-audit-report-v1.json
 * @see Issue #2366 — Implement Dimensions 5-8
 */

export { runDimension5 } from "./dimensions/dimension-5-coverage.js";
export type { CoverageFinding, Dimension5Result } from "./dimensions/dimension-5-coverage.js";

export { runDimension6 } from "./dimensions/dimension-6-security.js";
export type {
  SecurityFinding,
  SecurityConfig,
  Dimension6Result,
} from "./dimensions/dimension-6-security.js";

export { runDimension7 } from "./dimensions/dimension-7-dependencies.js";
export type { DependencyFinding, Dimension7Result } from "./dimensions/dimension-7-dependencies.js";

export { runDimension8 } from "./dimensions/dimension-8-cicd.js";
export type { CiCdFinding, Dimension8Result } from "./dimensions/dimension-8-cicd.js";

export { parseLcov, findLcovFiles } from "./utils/coverage-parser.js";
export type { FileCoverage, CoverageParseResult } from "./utils/coverage-parser.js";

export {
  parseWorkflowContent,
  findContinueOnErrorSteps,
  getAllStepNames,
  hasStepMatching,
} from "./utils/workflow-parser.js";
export type { ParsedWorkflow, WorkflowJob, WorkflowStep } from "./utils/workflow-parser.js";

export {
  scanFileContent,
  filterByConfidence,
  hasFalsePositiveHint,
  redactSecret,
  extractContext,
} from "./utils/pattern-matcher.js";
export type { PatternMatch, PatternDefinition } from "./utils/pattern-matcher.js";
