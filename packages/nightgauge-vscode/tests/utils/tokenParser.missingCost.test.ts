/**
 * Regression tests: tokenParser warns when total_cost_usd is absent but tokens are present.
 *
 * @see tokenParser.ts
 * @see Issue #2845 - Cost field may not update in OutputWindow
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { parseStreamJsonLine } from "../../src/utils/tokenParser";

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
