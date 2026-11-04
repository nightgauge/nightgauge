import { describe, expect, it } from "vitest";
import { PickupStartGate } from "../../../src/services/notifications/pickupStartGate";

describe("PickupStartGate", () => {
  it("releases a held start once", () => {
    const gate = new PickupStartGate<number>();
    gate.hold("k", 1);
    expect(gate.has("k")).toBe(true);
    expect(gate.release("k")).toBe(1);
    expect(gate.release("k")).toBeUndefined();
  });

  it("drops a held start on a deferred finalization", () => {
    const gate = new PickupStartGate<number>();
    gate.hold("k", 1);
    expect(gate.settle("k", "deferred")).toBeUndefined();
    expect(gate.has("k")).toBe(false);
  });

  it("releases on a non-deferred or missing outcome", () => {
    const gate = new PickupStartGate<number>();
    gate.hold("a", 1);
    gate.hold("b", 2);
    expect(gate.settle("a", "failed")).toBe(1);
    expect(gate.settle("b", undefined)).toBe(2);
  });

  it("keys holds independently", () => {
    const gate = new PickupStartGate<string>();
    gate.hold("a", "x");
    gate.drop("b");
    expect(gate.release("a")).toBe("x");
  });
});
