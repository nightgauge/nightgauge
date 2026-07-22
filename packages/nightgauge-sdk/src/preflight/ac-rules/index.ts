/**
 * Registry of preflight AC rules.
 *
 * Order matters: the first rule whose `applies()` returns a non-null match
 * wins. Most-specific rules come first.
 */
import type { RuleEvaluator } from "../types.js";
import workflowJobNamed from "./workflow-job-named.js";
import branchProtectionRulePresent from "./branch-protection-rule-present.js";
import npmScriptDefined from "./npm-script-defined.js";
import docSectionPresent from "./doc-section-present.js";
import grepForSymbol from "./grep-for-symbol.js";
import fileExists from "./file-exists.js";

export const AC_RULES: readonly RuleEvaluator[] = [
  workflowJobNamed,
  branchProtectionRulePresent,
  npmScriptDefined,
  docSectionPresent,
  grepForSymbol,
  fileExists,
];

export {
  workflowJobNamed,
  branchProtectionRulePresent,
  npmScriptDefined,
  docSectionPresent,
  grepForSymbol,
  fileExists,
};
