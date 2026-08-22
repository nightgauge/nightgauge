/**
 * The operator's declared Claude plan (Issue #808).
 *
 * ## Why a declaration exists at all
 *
 * Nightgauge can only *observe* a subscription allowance once a
 * `rate_limit_event` reading has arrived. Until then —
 * a fresh install, a machine where the status-line feed has never been wired,
 * any window where it is broken — `ClaudeRateLimitUsageProvider` reports
 * nothing and the footer falls through to locally-derived dollar windows that
 * describe pay-per-token billing. An operator on Max 20x is shown a **different
 * billing model** than the one they are on.
 *
 * ADR 018 forbids *inferring* a plan from the adapter id, and that rule is
 * intact: a declaration is not an inference. This closes the gap the ADR
 * deliberately left open rather than contradicting it.
 *
 * ## The line this module does not cross
 *
 * A declaration decides WHICH WINDOWS EXIST. It never supplies the numbers in
 * them. An observed reading outranks it for utilization always, and a declared
 * plan with no reading yet produces windows with `used: null` — rendered as
 * "awaiting first reading", never as a zero and never as a bar.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 */

import * as vscode from "vscode";
import type { UsageWindow, UsageWindowScope } from "./types";

/**
 * What the operator says they are on.
 *
 * `not-declared` is the default and preserves today's behaviour exactly: the
 * plan follows the observed signal, and nothing else changes.
 */
export type ClaudePlanDeclaration = "not-declared" | "max-20x" | "max-5x" | "pro" | "api";

export const CLAUDE_PLAN_SETTING = "nightgauge.usage.claudePlan";

const DECLARATIONS: readonly ClaudePlanDeclaration[] = [
  "not-declared",
  "max-20x",
  "max-5x",
  "pro",
  "api",
];

/**
 * Coerce a configuration value to a declaration.
 *
 * An unrecognised value becomes `not-declared` rather than throwing or being
 * guessed at: a typo in a settings file must degrade to today's behaviour, not
 * assert a plan the operator did not choose.
 */
export function parseClaudePlanDeclaration(raw: unknown): ClaudePlanDeclaration {
  return typeof raw === "string" && (DECLARATIONS as readonly string[]).includes(raw)
    ? (raw as ClaudePlanDeclaration)
    : "not-declared";
}

/** True for the declarations that mean "I am on a refilling-allowance plan". */
export function isSubscriptionDeclaration(plan: ClaudePlanDeclaration): boolean {
  return plan === "max-20x" || plan === "max-5x" || plan === "pro";
}

/**
 * The windows a declared subscription plan has, with nothing measured in them.
 *
 * Only the two allowances every Max/Pro plan carries. `daily` is deliberately
 * absent: this codebase has observed a `daily` bucket, but it is not part of
 * what a subscription is documented to have, and a declaration must not invent
 * a period the operator's plan may not include.
 */
const DECLARED_SCOPES: readonly { scope: UsageWindowScope; label: string }[] = [
  { scope: "rolling", label: "Session (5h)" },
  { scope: "weekly", label: "This week" },
];

/**
 * Window shells for a declared plan: the periods, and an explicit absence
 * where the figures go.
 *
 * `used: null` is the whole point. `used: 0` would render as "0% used", which
 * is a fabricated utilization — the exact thing ADR 018 and
 * `ClaudeRateLimitStore`'s drop-expired-readings rule exist to prevent.
 * `limit: 100` matches the unit the observed path uses, so a shell and a filled
 * window are the same shape and a later reading simply replaces the shell.
 */
export function declaredPlanWindows(providerId: string): UsageWindow[] {
  return DECLARED_SCOPES.map(({ scope, label }) => ({
    id: `${providerId}:${scope}`,
    label,
    scope,
    used: null,
    limit: 100,
    unit: "percent" as const,
    resetsAt: null,
    confidence: "unknown" as const,
  }));
}

/**
 * Read the declaration out of VS Code settings.
 *
 * The one place in this module that touches `vscode`; everything above is pure
 * so the rules stay directly testable.
 */
export function readClaudePlanDeclaration(): ClaudePlanDeclaration {
  return parseClaudePlanDeclaration(
    vscode.workspace.getConfiguration("nightgauge.usage").get<string>("claudePlan")
  );
}
