/**
 * monitoringResolver.budgetEvalCadence.test.ts
 *
 * Tests for getBudgetEvalCadenceMs (Issue #254) — the configurable, throttled
 * cadence at which the orchestrator re-evaluates budget/ceiling thresholds
 * against the live in-stage cost estimate. Priority: env → config → default,
 * with a floor for any non-zero value and 0 as an explicit "disable throttle".
 */

import { describe, it, expect, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: undefined },
}));

import { getBudgetEvalCadenceMs } from "../../../src/utils/resolvers/monitoringResolver";

const ENV = "NIGHTGAUGE_PIPELINE_BUDGET_EVAL_CADENCE_MS";

describe("getBudgetEvalCadenceMs (#254)", () => {
  beforeEach(() => {
    delete process.env[ENV];
  });

  it("defaults to 5000ms with no env and no workspace root", () => {
    expect(getBudgetEvalCadenceMs()).toBe(5000);
  });

  it("honors a valid env override", () => {
    process.env[ENV] = "3000";
    try {
      expect(getBudgetEvalCadenceMs()).toBe(3000);
    } finally {
      delete process.env[ENV];
    }
  });

  it("floors a small non-zero env value to 1000ms (never armed for ~every message)", () => {
    process.env[ENV] = "50";
    try {
      expect(getBudgetEvalCadenceMs()).toBe(1000);
    } finally {
      delete process.env[ENV];
    }
  });

  it("treats env '0' as an explicit disable (evaluate on every snapshot)", () => {
    process.env[ENV] = "0";
    try {
      expect(getBudgetEvalCadenceMs()).toBe(0);
    } finally {
      delete process.env[ENV];
    }
  });

  it("falls back to the default for a non-numeric env value", () => {
    process.env[ENV] = "not-a-number";
    try {
      expect(getBudgetEvalCadenceMs()).toBe(5000);
    } finally {
      delete process.env[ENV];
    }
  });

  it("ignores a negative env value (falls back to default)", () => {
    process.env[ENV] = "-2000";
    try {
      // parseInt("-2000") = -2000 which is < 0 → rejected → default
      expect(getBudgetEvalCadenceMs()).toBe(5000);
    } finally {
      delete process.env[ENV];
    }
  });
});
