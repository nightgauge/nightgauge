/**
 * Regression tests: tokenParser warns when total_cost_usd is absent but tokens are present.
 *
 * @see tokenParser.ts
 * @see Issue #2845 - Cost field may not update in OutputWindow
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { parseStreamJsonLine, TokenAccumulator } from "../../src/utils/tokenParser";
import { computeStageCost } from "../../src/utils/computeStageCost";

describe("parseStreamJsonLine — missing total_cost_usd diagnostic", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(console, "warn").mockImplementation(() => {});
  });

  it("should warn when total_cost_usd is absent but tokens are present", () => {
    const line = JSON.stringify({
      type: "result",
      usage: { input_tokens: 100, output_tokens: 50 },
    });

    const result = parseStreamJsonLine(line);

    expect(result?.usage?.costUsd).toBe(0);
    expect(console.warn).toHaveBeenCalledWith(
      expect.stringContaining("total_cost_usd missing but tokens present")
    );
    expect(console.warn).toHaveBeenCalledWith(expect.stringContaining("100/50"));
  });

  it("should not warn when total_cost_usd is present and non-zero", () => {
    const line = JSON.stringify({
      type: "result",
      usage: { input_tokens: 100, output_tokens: 50 },
      total_cost_usd: 0.0045,
    });

    const result = parseStreamJsonLine(line);

    expect(result?.usage?.costUsd).toBe(0.0045);
    expect(console.warn).not.toHaveBeenCalled();
  });

  it("should not warn when tokens are zero and total_cost_usd is absent", () => {
    const line = JSON.stringify({
      type: "result",
      usage: { input_tokens: 0, output_tokens: 0 },
    });

    parseStreamJsonLine(line);

    expect(console.warn).not.toHaveBeenCalled();
  });

  it("should not warn when usage field is absent", () => {
    const line = JSON.stringify({ type: "result", session_id: "abc" });

    const result = parseStreamJsonLine(line);

    expect(result?.usage).toBeUndefined();
    expect(console.warn).not.toHaveBeenCalled();
  });

  it("should warn when only input tokens are present with no cost", () => {
    const line = JSON.stringify({
      type: "result",
      usage: { input_tokens: 500, output_tokens: 0 },
    });

    parseStreamJsonLine(line);

    expect(console.warn).toHaveBeenCalledWith(
      expect.stringContaining("total_cost_usd missing but tokens present")
    );
  });
});

/**
 * #1657: an accumulator constructed with ("opencode", model) resolves its cost
 * through computeStageCost's provider-of-the-model rules, whatever the result
 * envelope reported as total_cost_usd.
 */
describe("TokenAccumulator — opencode cost resolution (#1657)", () => {
  function accumulate(model: string, reportedCostUsd: number) {
    const acc = new TokenAccumulator("opencode", model);
    const parsed = parseStreamJsonLine(
      JSON.stringify({
        type: "result",
        usage: { input_tokens: 120_000, output_tokens: 8_000 },
        total_cost_usd: reportedCostUsd,
      })
    );
    acc.add(parsed!.usage!);
    return acc.getTotal();
  }

  beforeEach(() => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
  });

  it("a local model is a stamped zero, even named after a registry model and reporting a cost", () => {
    const total = accumulate("lmstudio/claude-sonnet-5", 0.42);
    expect(total.costUsd).toBe(0);
    expect(total.costSource).toBe("computed");
  });

  it("anthropic/claude-sonnet-5 books the claude adapter's registry cost", () => {
    const total = accumulate("anthropic/claude-sonnet-5", 0);
    const viaClaude = computeStageCost("claude", "claude-sonnet-5", {
      input: 120_000,
      output: 8_000,
      cache_read: 0,
      cache_creation_5m: 0,
      cache_creation_1h: 0,
    });
    expect(viaClaude.cost_usd).toBeGreaterThan(0);
    expect(total.costUsd).toBe(viaClaude.cost_usd);
    expect(total.costSource).toBe("computed");
  });

  it("a hosted run reporting cost 0 that the registry cannot price is unstamped", () => {
    const total = accumulate("openai/gpt-9-preview", 0);
    expect(total.costUsd).toBe(0);
    expect(total.costSource).toBe("unknown");
  });
});
