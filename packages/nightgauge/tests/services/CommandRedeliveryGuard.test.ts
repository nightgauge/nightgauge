/**
 * CommandRedeliveryGuard.test.ts
 *
 * One decision and one accepted ack per command id, however often the
 * platform delivers it (#2334 review).
 */

import { describe, it, expect, vi } from "vitest";
import { CommandRedeliveryGuard } from "../../src/services/CommandRedeliveryGuard";

describe("CommandRedeliveryGuard", () => {
  it("decides once and, once the ack is accepted, drops every later copy", async () => {
    const guard = new CommandRedeliveryGuard<string>();
    const decide = vi.fn().mockResolvedValue("applied");
    const send = vi.fn().mockResolvedValue(true);

    await Promise.all([guard.consume("c1", decide, send), guard.consume("c1", decide, send)]);
    await guard.consume("c1", decide, send);

    expect(decide).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("re-sends the first decision while the platform has not accepted it", async () => {
    const guard = new CommandRedeliveryGuard<string>();
    const decide = vi.fn().mockResolvedValueOnce("applied").mockResolvedValue("no-op");
    const send = vi.fn().mockResolvedValueOnce(false).mockResolvedValue(true);

    await guard.consume("c1", decide, send);
    await guard.consume("c1", decide, send);
    await guard.consume("c1", decide, send);

    expect(decide).toHaveBeenCalledTimes(1);
    expect(send.mock.calls).toEqual([["applied"], ["applied"]]);
  });

  it("sends nothing for a command it decided not to acknowledge", async () => {
    const guard = new CommandRedeliveryGuard<string>();
    const send = vi.fn().mockResolvedValue(true);

    await guard.consume("c1", async () => null, send);
    await guard.consume("c1", async () => "late", send);

    expect(send).not.toHaveBeenCalled();
  });

  it("starts deciding before it returns, so same-tick commands apply in arrival order", () => {
    const guard = new CommandRedeliveryGuard<string>();
    const order: string[] = [];
    void guard.consume(
      "a",
      async () => (order.push("a"), "a"),
      async () => true
    );
    void guard.consume(
      "b",
      async () => (order.push("b"), "b"),
      async () => true
    );
    expect(order).toEqual(["a", "b"]);
  });

  it("consumes every copy of a command with no id, which it cannot recognise", async () => {
    const guard = new CommandRedeliveryGuard<string>();
    const decide = vi.fn().mockResolvedValue("x");
    await guard.consume("", decide, async () => true);
    await guard.consume("", decide, async () => true);
    expect(decide).toHaveBeenCalledTimes(2);
  });

  it("remembers a consumed id from its first copy on, and never an empty one", async () => {
    const guard = new CommandRedeliveryGuard<string>(2);
    expect(guard.remembers("a")).toBe(false);
    const first = guard.consume(
      "a",
      async () => "x",
      async () => true
    );
    expect(guard.remembers("a")).toBe(true); // before its decision settles
    await first;
    await guard.consume(
      "",
      async () => "x",
      async () => true
    );
    expect(guard.remembers("")).toBe(false);
    for (const id of ["b", "c"])
      await guard.consume(
        id,
        async () => "x",
        async () => true
      );
    expect(guard.remembers("a")).toBe(false); // forgotten beyond capacity
  });

  it("forgets the oldest ids beyond its capacity", async () => {
    const guard = new CommandRedeliveryGuard<string>(2);
    const decide = vi.fn().mockResolvedValue("x");
    const send = vi.fn().mockResolvedValue(true);
    for (const id of ["a", "b", "c"]) await guard.consume(id, decide, send);
    await guard.consume("a", decide, send); // forgotten: decided afresh
    await guard.consume("c", decide, send); // remembered: dropped
    expect(decide).toHaveBeenCalledTimes(4);
  });
});
