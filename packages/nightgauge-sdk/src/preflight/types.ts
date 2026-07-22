/**
 * Preflight types — deterministic AC reconciliation.
 *
 * @see docs/CONTEXT_ARCHITECTURE.md — ac-reconcile-{N}.json schema
 */

export type Classification = "satisfied" | "partial" | "unsatisfied" | "undetectable";

export type AggregateStatus =
  | "all-satisfied"
  | "mostly-satisfied"
  | "partial"
  | "unsatisfied"
  | "undetectable"
  | "no-acs-detected";

export type SuggestedApproach = "verify-and-close" | "narrow-scope" | "standard";

export interface AcceptanceCriterion {
  /** 0-based position in the issue body */
  index: number;
  /** Trimmed checkbox text */
  text: string;
  checkbox_state: "checked" | "unchecked";
}

export interface RuleResult {
  classification: Classification;
  /** Human-readable evidence string */
  reason: string;
  /** File paths, line refs, etc. */
  evidence?: string[];
}

export interface RuleContext {
  /** Absolute path to the working tree the rule should evaluate against. */
  workdir: string;
  ac: AcceptanceCriterion;
  /** Captured groups from the rule's `applies()` regex. */
  extracted: Record<string, string>;
}

export interface RuleEvaluator {
  readonly name: string;
  /** Returns extracted params when the rule applies, or null when not applicable. */
  readonly applies: (text: string) => Record<string, string> | null;
  readonly evaluate: (ctx: RuleContext) => Promise<RuleResult>;
}

export interface ReconciledCriterion extends AcceptanceCriterion {
  rule_applied: string | null;
  classification: Classification;
  reason: string;
  evidence: string[];
}

export interface SuggestedRoute {
  approach: SuggestedApproach;
  /** Indices into the AC array that the planner should focus on. */
  focus_acs: number[];
  rationale: string;
}

export interface ACReconcileReport {
  schema_version: "1.0";
  issue_number: number;
  main_sha: string;
  evaluated_at: string;
  acceptance_criteria: ReconciledCriterion[];
  aggregate_status: AggregateStatus;
  suggested_route: SuggestedRoute;
}
