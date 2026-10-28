/**
 * applyRoutingExclusions — the extension's dispatch-time exclusion gate
 * (#2063), the TS pair of Go's eligibleDispatchModel + wire-effort descent.
 */

import { describe, it, expect, vi } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [] },
  window: { createOutputChannel: vi.fn(() => ({ appendLine: vi.fn() })) },
  extensions: { getExtension: vi.fn(() => null) },
}));

import { applyRoutingExclusions, type ModelDecision } from "../../src/utils/skillRunner";

const auto = (model: string, effort?: ModelDecision["effort"]): ModelDecision => ({
  model,
  source: "auto",
  effort,
});

describe("applyRoutingExclusions (#2063)", () => {
  it("lowers an excluded automatic band to the strongest eligible band", () => {
    const got = applyRoutingExclusions(auto("fable"), {
      models: ["claude-fable-5-1"],
      efforts: [],
    });
    expect(got.model).toBe("opus");
  });

  it("descends an excluded effort to the next lower allowed one", () => {
    const got = applyRoutingExclusions(auto("opus", "max"), {
      models: [],
      efforts: ["max", "xhigh"],
    });
    expect(got.effort).toBe("high");
  });

  it("is a no-op without exclusions and for explicit pins", () => {
    const d = auto("fable", "max");
    expect(applyRoutingExclusions(d, { models: [], efforts: [] })).toBe(d);
    const pinned: ModelDecision = { model: "fable", source: "config" };
    expect(applyRoutingExclusions(pinned, { models: ["claude-fable-5-1"], efforts: [] })).toBe(
      pinned
    );
  });
});
