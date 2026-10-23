/**
 * Tests for the local free-trial record store + countdown derivation.
 *
 * @see Issue #1138 - Commercialization: trial countdown
 */

import { describe, it, expect, vi } from "vitest";
import { TrialStateStore, type TrialRecord } from "../../src/platform/TrialState";

function makeMemento() {
  const map = new Map<string, unknown>();
  return {
    get: vi.fn((key: string) => map.get(key)),
    update: vi.fn(async (key: string, value: unknown) => {
      if (value === undefined) map.delete(key);
      else map.set(key, value);
    }),
    keys: () => [...map.keys()],
  };
}

const DAY = 24 * 60 * 60 * 1000;
const NOW = new Date("2026-06-15T00:00:00Z").getTime();

function record(over: Partial<TrialRecord> = {}): TrialRecord {
  return {
    tier: "pro",
    expiresAt: new Date(NOW + 10 * DAY).toISOString(),
    runAllowance: 50,
    startedAt: new Date(NOW).toISOString(),
    ...over,
  };
}

describe("TrialStateStore", () => {
  it("round-trips set → get and clears", async () => {
    const store = new TrialStateStore(makeMemento() as never);
    expect(store.get()).toBeUndefined();
    const rec = record();
    await store.set(rec);
    expect(store.get()).toEqual(rec);
    await store.clear();
    expect(store.get()).toBeUndefined();
  });

  it("status() is null with no record", () => {
    const store = new TrialStateStore(makeMemento() as never);
    expect(store.status(NOW)).toBeNull();
  });

  it("reports an active trial with ceil'd days remaining", async () => {
    const store = new TrialStateStore(makeMemento() as never);
    await store.set(record({ expiresAt: new Date(NOW + 9.2 * DAY).toISOString() }));
    const s = store.status(NOW);
    expect(s).not.toBeNull();
    expect(s!.active).toBe(true);
    expect(s!.expired).toBe(false);
    expect(s!.daysRemaining).toBe(10); // ceil(9.2)
    expect(s!.record.runAllowance).toBe(50);
  });

  it("reports a 1-day floor on the final day", async () => {
    const store = new TrialStateStore(makeMemento() as never);
    await store.set(record({ expiresAt: new Date(NOW + 0.4 * DAY).toISOString() }));
    expect(store.status(NOW)!.daysRemaining).toBe(1);
  });

  it("reports expired once the expiry has passed", async () => {
    const store = new TrialStateStore(makeMemento() as never);
    await store.set(record({ expiresAt: new Date(NOW - DAY).toISOString() }));
    const s = store.status(NOW);
    expect(s!.active).toBe(false);
    expect(s!.expired).toBe(true);
    expect(s!.daysRemaining).toBe(0);
  });

  it("returns null for an unparseable expiry", async () => {
    const store = new TrialStateStore(makeMemento() as never);
    await store.set(record({ expiresAt: "not-a-date" }));
    expect(store.status(NOW)).toBeNull();
  });
});
