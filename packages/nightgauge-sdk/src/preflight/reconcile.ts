import { ACReconcileContextSchema } from "../context/schemas/ac-reconcile.js";
import type { ACReconcileContext } from "../context/schemas/ac-reconcile.js";
import { parseAcceptanceCriteria } from "./parseAcceptanceCriteria.js";
import { selectRule } from "./ruleSelection.js";
import type {
  AggregateStatus,
  ReconciledCriterion,
  RuleEvaluator,
  SuggestedRoute,
} from "./types.js";

/**
 * Aggregate threshold: when at least 80% of ACs are `satisfied` and none
 * are `unsatisfied`, the run aggregates to `mostly-satisfied` and the
 * planner is asked to narrow scope to the unsatisfied/undetectable subset.
 */
export const MOSTLY_SATISFIED_THRESHOLD = 0.8;

export interface ReconcileOptions {
  workdir: string;
  issueNumber: number;
  issueBody: string;
  mainSha: string;
  evaluatedAt?: string;
  /** Override rule registry — used by tests. */
  rules?: readonly RuleEvaluator[];
}

function deriveAggregate(criteria: ReconciledCriterion[]): {
  status: AggregateStatus;
  route: SuggestedRoute;
} {
  if (criteria.length === 0) {
    return {
      status: "no-acs-detected",
      route: {
        approach: "standard",
        focus_acs: [],
        rationale: "No acceptance-criterion checkboxes detected in issue body",
      },
    };
  }

  const counts = { satisfied: 0, partial: 0, unsatisfied: 0, undetectable: 0 };
  for (const c of criteria) counts[c.classification] += 1;

  const total = criteria.length;
  const ratio = counts.satisfied / total;

  if (counts.satisfied === total) {
    return {
      status: "all-satisfied",
      route: {
        approach: "verify-and-close",
        focus_acs: [],
        rationale: "All acceptance criteria satisfied — verify and close without further work",
      },
    };
  }

  if (counts.unsatisfied === total) {
    return {
      status: "unsatisfied",
      route: {
        approach: "standard",
        focus_acs: criteria.map((c) => c.index),
        rationale: "No acceptance criteria satisfied — implement the full plan",
      },
    };
  }

  if (counts.undetectable === total) {
    return {
      status: "undetectable",
      route: {
        approach: "standard",
        focus_acs: [],
        rationale:
          "No acceptance criteria could be deterministically evaluated — proceed with the standard plan",
      },
    };
  }

  if (ratio >= MOSTLY_SATISFIED_THRESHOLD && counts.unsatisfied === 0) {
    const focus = criteria.filter((c) => c.classification !== "satisfied").map((c) => c.index);
    return {
      status: "mostly-satisfied",
      route: {
        approach: "narrow-scope",
        focus_acs: focus,
        rationale: `${counts.satisfied}/${total} criteria satisfied — narrow plan scope to the remaining ${focus.length}`,
      },
    };
  }

  const focus = criteria.filter((c) => c.classification !== "satisfied").map((c) => c.index);
  return {
    status: "partial",
    route: {
      approach: "standard",
      focus_acs: focus,
      rationale: `${counts.satisfied}/${total} criteria satisfied; remaining work warrants standard plan`,
    },
  };
}

/**
 * Run the deterministic AC reconciliation against the working tree.
 *
 * Pure within the given workdir + main SHA: same body + same SHA + same
 * working tree => same result.
 */
export async function reconcileAcceptanceCriteria(
  opts: ReconcileOptions
): Promise<ACReconcileContext> {
  const { workdir, issueNumber, issueBody, mainSha, rules } = opts;
  const evaluatedAt = opts.evaluatedAt ?? new Date().toISOString();

  const acs = parseAcceptanceCriteria(issueBody);
  const reconciled: ReconciledCriterion[] = [];

  for (const ac of acs) {
    const selection = selectRule(ac.text, rules);
    if (!selection) {
      reconciled.push({
        ...ac,
        rule_applied: null,
        classification: "undetectable",
        reason: "No rule matched the acceptance-criterion text",
        evidence: [],
      });
      continue;
    }
    const result = await selection.rule.evaluate({
      workdir,
      ac,
      extracted: selection.extracted,
    });
    reconciled.push({
      ...ac,
      rule_applied: selection.rule.name,
      classification: result.classification,
      reason: result.reason,
      evidence: result.evidence ?? [],
    });
  }

  const { status, route } = deriveAggregate(reconciled);

  const report: ACReconcileContext = {
    schema_version: "1.0",
    issue_number: issueNumber,
    main_sha: mainSha,
    evaluated_at: evaluatedAt,
    acceptance_criteria: reconciled,
    aggregate_status: status,
    suggested_route: route,
  };

  return ACReconcileContextSchema.parse(report);
}
