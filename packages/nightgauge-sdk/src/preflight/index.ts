/**
 * Preflight — deterministic AC reconciliation (Issue #3003).
 *
 * Public exports for the reconciler, rule registry, parser, and types.
 * No LLM tokens are consumed by this module.
 */

export { parseAcceptanceCriteria } from "./parseAcceptanceCriteria.js";
export { selectRule } from "./ruleSelection.js";
export type { RuleSelection } from "./ruleSelection.js";
export {
  reconcileAcceptanceCriteria,
  MOSTLY_SATISFIED_THRESHOLD,
  type ReconcileOptions,
} from "./reconcile.js";
export { AC_RULES } from "./ac-rules/index.js";
export type {
  AcceptanceCriterion,
  AggregateStatus,
  Classification,
  ReconciledCriterion,
  RuleContext,
  RuleEvaluator,
  RuleResult,
  SuggestedApproach,
  SuggestedRoute,
  ACReconcileReport,
} from "./types.js";
