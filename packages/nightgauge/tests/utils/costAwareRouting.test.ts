/**
 * costAwareRouting.test.ts (Issue #21)
 *
 * Unit tests for the cost-aware routing helpers that feed the adaptive router's
 * cost-per-success logic within the mode envelope.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [{ uri: { fsPath: "/no/such/workspace" } }] },
}));

import type { ExecutionHistoryRecord } from "@nightgauge/sdk";
import {
  buildCostPerSuccessFromRecords,
  getCostPerSuccessContext,
  isCostAwareRoutingEnabled,
  __primeCostContextCache,
  __clearCostContextCache,
} from "../../src/utils/costAwareRouting";

function rec(
  stage: string,
  model: string,
  success: boolean,
  costUsd: number
): ExecutionHistoryRecord {
  return {
    issueNumber: 1,
    stage,
    model,
    success,
    retries: 0,
    inputTokens: 1000,
    outputTokens: 100,
    costUsd,
    durationMs: 1000,
    timestamp: "2026-07-10T00:00:00.000Z",
  };
}

describe("buildCostPerSuccessFromRecords (Issue #21)", () => {
  it("returns undefined for empty history", () => {
    expect(buildCostPerSuccessFromRecords([])).toBeUndefined();
  });

  it("aggregates per model:stage with totalCost, successCount, totalCount", () => {
    const ctx = buildCostPerSuccessFromRecords([
      rec("feature-dev", "sonnet", true, 1),
      rec("feature-dev", "sonnet", true, 1),
      rec("feature-dev", "sonnet", false, 1),
      rec("feature-dev", "opus", true, 3),
      rec("feature-dev", "opus", true, 3),
    ]);
    expect(ctx).toBeDefined();
    const sonnet = ctx!.history["sonnet:feature-dev"];
    expect(sonnet.totalCount).toBe(3);
    expect(sonnet.successCount).toBe(2);
    expect(sonnet.totalCostUsd).toBeCloseTo(3, 5);
    const opus = ctx!.history["opus:feature-dev"];
    expect(opus.totalCount).toBe(2);
    expect(opus.successCount).toBe(2);
    expect(opus.totalCostUsd).toBeCloseTo(6, 5);
  });
});

describe("getCostPerSuccessContext cache (Issue #21)", () => {
  beforeEach(() => __clearCostContextCache());
  afterEach(() => __clearCostContextCache());

  it("returns a primed context synchronously", () => {
    __primeCostContextCache("/ws", {
      history: { "sonnet:feature-dev": { totalCostUsd: 1, successCount: 5, totalCount: 5 } },
    });
    const ctx = getCostPerSuccessContext("/ws");
    expect(ctx?.history["sonnet:feature-dev"].successCount).toBe(5);
  });

  it("returns undefined on a cold cache (fires background refresh, fail-open)", () => {
    expect(getCostPerSuccessContext("/no/such/workspace")).toBeUndefined();
  });
});

describe("isCostAwareRoutingEnabled (Issue #21)", () => {
  const KEY = "NIGHTGAUGE_MODEL_ROUTING_COST_AWARE";
  afterEach(() => {
    delete process.env[KEY];
  });

  it("defaults to true when unset", () => {
    delete process.env[KEY];
    expect(isCostAwareRoutingEnabled("/no/such/workspace")).toBe(true);
  });

  it("honors an env override of false", () => {
    process.env[KEY] = "false";
    expect(isCostAwareRoutingEnabled("/no/such/workspace")).toBe(false);
  });

  it("honors an env override of true", () => {
    process.env[KEY] = "true";
    expect(isCostAwareRoutingEnabled("/no/such/workspace")).toBe(true);
  });
});
