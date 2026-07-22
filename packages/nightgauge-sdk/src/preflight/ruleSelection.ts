import { AC_RULES } from "./ac-rules/index.js";
import type { RuleEvaluator } from "./types.js";

export interface RuleSelection {
  rule: RuleEvaluator;
  extracted: Record<string, string>;
}

/**
 * Pick the first rule whose `applies()` returns a non-null match for the
 * given AC text. Returns null when no rule matches; the reconciler maps
 * that to `undetectable` with reason "no rule matched".
 */
export function selectRule(
  text: string,
  rules: readonly RuleEvaluator[] = AC_RULES
): RuleSelection | null {
  for (const rule of rules) {
    const extracted = rule.applies(text);
    if (extracted) {
      return { rule, extracted };
    }
  }
  return null;
}
