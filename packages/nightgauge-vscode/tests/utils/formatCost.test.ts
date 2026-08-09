import { describe, it, expect } from "vitest";
import { formatCost } from "../../src/utils/formatCost";

// #333 decision E: there is exactly ONE formatCost, and it is the tiered
// implementation. A flat 3-decimal render either lies about sub-cent costs
// ($0.000) or reads as a non-currency for real dollars ($75.000).
//
// It lives in utils/ (not in the notifier transport) so `utils/tokenParser.ts`
// can import it without a utils → services edge.
describe("formatCost", () => {
  it("renders sub-cent costs to four decimals so a real cost is never shown as free", () => {
    expect(formatCost(0)).toBe("$0.0000");
    expect(formatCost(0.0001)).toBe("$0.0001");
    expect(formatCost(0.00456)).toBe("$0.0046");
    expect(formatCost(0.0099)).toBe("$0.0099");
  });

  it("renders sub-dollar costs to three decimals", () => {
    expect(formatCost(0.01)).toBe("$0.010");
    expect(formatCost(0.123)).toBe("$0.123");
    expect(formatCost(0.999)).toBe("$0.999");
  });

  it("renders dollar-and-up costs as two-decimal currency", () => {
    expect(formatCost(1.0)).toBe("$1.00");
    expect(formatCost(1.5)).toBe("$1.50");
    expect(formatCost(75.0)).toBe("$75.00");
    expect(formatCost(1234.56)).toBe("$1234.56");
  });
});
