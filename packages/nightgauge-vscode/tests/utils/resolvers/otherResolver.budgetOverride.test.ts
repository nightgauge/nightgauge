/**
 * otherResolver.budgetOverride.test.ts
 *
 * Issue #305 — the budget-ceiling card's primary remedy was INERT on the exact
 * path #305 wired it onto.
 *
 * Resolving "Raise to $X & retry" runs `orchestrator.WriteBudgetCeilingOverride`,
 * which writes `.nightgauge/pipeline/budget-override.json`. Exactly one function
 * in the tree read that file back: Go's `PipelineBudgetCeilingUSD`, as
 * `max(config, override)`. The extension resolved its ceiling through
 * `getPipelineCeilingConfig`, which read env vars and config.yaml only — so the
 * re-dispatched extension run enforced the OLD ceiling, tripped the same
 * between-stage check, and re-raised the same idempotency key. Every click cost
 * up to another ceiling of tokens and the operator got no signal that their
 * action had done nothing.
 *
 * These tests pin the two halves that failure needed: the override is read at
 * all, and it is layered with Go's precedence rather than a different one.
 *
 * @see internal/orchestrator/scheduler.go — PipelineBudgetCeilingUSD
 * @see internal/orchestrator/attention_verb_primitives.go — WriteBudgetCeilingOverride
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: undefined },
}));

import { getPipelineCeilingConfig } from "../../../src/utils/resolvers/otherResolver";
import { PipelineBudgetCeiling } from "../../../src/utils/pipelineBudgetCeiling";

const ENV_KEYS = [
  "NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_ENABLED",
  "NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_CEILING_USD",
  "NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_OVERRIDE_CEILING_USD",
  "NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_WARN_THRESHOLD_USD",
];

let root: string;

/** Write config.yaml the way `pipeline.token_budget_ceiling.ceiling_usd` ships. */
function writeConfiguredCeiling(ceilingUsd: number): void {
  const dir = path.join(root, ".nightgauge");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(
    path.join(dir, "config.yaml"),
    ["pipeline:", "  token_budget_ceiling:", `    ceiling_usd: ${ceilingUsd}`, ""].join("\n"),
    "utf-8"
  );
}

/** Byte-for-byte what `WriteBudgetCeilingOverride` persists. */
function writeRuntimeOverride(ceilingUsd: number): void {
  const dir = path.join(root, ".nightgauge", "pipeline");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(
    path.join(dir, "budget-override.json"),
    JSON.stringify(
      {
        schema_version: 1,
        ceiling_usd: ceilingUsd,
        raised_by: "octocat",
        raised_at: new Date().toISOString(),
        reason: "action-center: budget.raiseCeiling",
      },
      null,
      2
    ),
    "utf-8"
  );
}

/** What the between-stage stop actually enforces, given a resolved config. */
function effectiveCeiling(): number {
  return new PipelineBudgetCeiling(getPipelineCeilingConfig(root)).getEffectiveCeiling();
}

describe("getPipelineCeilingConfig honors the Action Center's runtime ceiling override (#305)", () => {
  beforeEach(() => {
    root = fs.mkdtempSync(path.join(os.tmpdir(), "ng-budget-override-"));
    for (const key of ENV_KEYS) delete process.env[key];
  });

  afterEach(() => {
    fs.rmSync(root, { recursive: true, force: true });
    for (const key of ENV_KEYS) delete process.env[key];
  });

  it("enforces the configured ceiling when no card has been resolved", () => {
    writeConfiguredCeiling(75);
    expect(effectiveCeiling()).toBe(75);
  });

  it("moves the enforced ceiling after budget.raiseCeiling writes the override", () => {
    // The failure scenario, end to end: a run for octocat/acme#42 hits $75, the
    // operator clicks "Raise to $112.50 & retry", the daemon writes the
    // override, and the RE-DISPATCHED extension run must run under $112.50.
    writeConfiguredCeiling(75);
    expect(effectiveCeiling()).toBe(75);

    writeRuntimeOverride(112.5);

    expect(effectiveCeiling()).toBe(112.5);
    // …and the check that stopped the run no longer stops it at the old spend.
    const check = new PipelineBudgetCeiling(getPipelineCeilingConfig(root)).check(80);
    expect(check.shouldStop).toBe(false);
    expect(check.effectiveCeilingUsd).toBe(112.5);
  });

  it("takes max(config, override) — a stale override can never LOWER a raised ceiling", () => {
    // Go's rule verbatim: `maxFloat64(base, readBudgetCeilingOverrideUSD(root))`.
    // Replace-semantics here would mean a month-old $90 override silently
    // capping a ceiling the operator has since raised to $300 in config, and the
    // two paths would enforce different numbers for the same run.
    writeConfiguredCeiling(300);
    writeRuntimeOverride(90);
    expect(effectiveCeiling()).toBe(300);
  });

  it("applies the override on the env-var branch too, matching Go's env arm", () => {
    // PipelineBudgetCeilingUSD returns max(env, override) when the env var is
    // set. An override read that only ran on the config.yaml branch would drift
    // from Go for anyone using the env var.
    process.env.NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_CEILING_USD = "50";
    writeRuntimeOverride(150);
    expect(effectiveCeiling()).toBe(150);
  });

  it("treats a missing, malformed, or non-positive override as no override", () => {
    writeConfiguredCeiling(75);
    expect(effectiveCeiling()).toBe(75); // missing

    const file = path.join(root, ".nightgauge", "pipeline", "budget-override.json");
    fs.mkdirSync(path.dirname(file), { recursive: true });

    fs.writeFileSync(file, "{ not json", "utf-8");
    expect(effectiveCeiling()).toBe(75);

    fs.writeFileSync(file, JSON.stringify({ schema_version: 1, ceiling_usd: 0 }), "utf-8");
    expect(effectiveCeiling()).toBe(75);

    fs.writeFileSync(file, JSON.stringify({ schema_version: 1, ceiling_usd: -5 }), "utf-8");
    expect(effectiveCeiling()).toBe(75);

    fs.writeFileSync(file, JSON.stringify({ schema_version: 1, ceiling_usd: "lots" }), "utf-8");
    expect(effectiveCeiling()).toBe(75);
  });
});
