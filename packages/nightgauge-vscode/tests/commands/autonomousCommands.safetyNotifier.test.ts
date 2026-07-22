// Issue #3605 bullet C — autonomous safety-pause Discord notifier.
//
// Pins the contract between Go's `autonomous.statusChanged` event and the
// DiscordService.notifySafetyPause webhook so a regression in either path
// (drop the allowlist, miss a triggered-by tag, fire on unrelated triggers)
// is caught at unit-test speed instead of "wait for #3499 to recur and
// notice the silent pause again."
//
// Scope: this file ONLY tests the predicate + dispatch logic in
// autonomousCommands.ts. The DiscordService webhook POST itself is exercised
// by tests/services/DiscordService.test.ts.

import { describe, expect, it, beforeEach, vi } from "vitest";

import {
  CASCADE_PAUSE_TRIGGERS,
  setAutonomousSafetyNotifier,
} from "../../src/commands/autonomousCommands";

describe("autonomousCommands — CASCADE_PAUSE_TRIGGERS", () => {
  it("includes the rate-limit circuit-breaker tag (Issue #3577)", () => {
    // Production tag emitted by rateLimitCircuitBreaker.tripBreaker(); pre-
    // #3605 this trigger only fired a VSCode toast. Now it also lights up
    // Discord via the safety-notifier allowlist.
    expect(CASCADE_PAUSE_TRIGGERS.has("rate-limit-circuit-breaker")).toBe(true);
  });

  it("includes the cascading-failures safety tag (Issue #3605 C)", () => {
    expect(CASCADE_PAUSE_TRIGGERS.has("safety:cascading-failures")).toBe(true);
  });

  it("includes the lifetime-failure-cap tag (halts ALL dispatching — unattended operators need the ping)", () => {
    // Revised 2026-07-11: the lifetime cap doesn't just sideline one issue —
    // it flips the whole scheduler to safety_tripped. A transient VSCode
    // toast was the only signal when it stopped the bowlsheet factory for
    // 2 hours. Dark-factory operation requires the remote ping.
    expect(CASCADE_PAUSE_TRIGGERS.has("safety:lifetime-failure-cap")).toBe(true);
  });

  it("does NOT include per-issue safety tags that pause nothing global (budget-ceiling, health-gate)", () => {
    // These fan out via the toast/log path because they affect a single
    // issue without halting the scheduler.
    expect(CASCADE_PAUSE_TRIGGERS.has("safety:budget-ceiling")).toBe(false);
    expect(CASCADE_PAUSE_TRIGGERS.has("safety:health-gate")).toBe(false);
  });

  it("does NOT include user-initiated pauses (the operator clicked the button)", () => {
    // User-initiated pauses carry `pauseTriggeredBy="user"`. Firing a
    // Discord ping for those would be noise — the operator just clicked
    // the "Pause Autonomous" button and is already aware.
    expect(CASCADE_PAUSE_TRIGGERS.has("user")).toBe(false);
  });
});

describe("setAutonomousSafetyNotifier", () => {
  beforeEach(() => {
    // Each test starts with no notifier so leakage across tests is
    // impossible. Production wiring lands the notifier exactly once at
    // bootstrap (services.ts) and never unregisters.
    setAutonomousSafetyNotifier(null);
  });

  it("accepts a notifier function without throwing", () => {
    const spy = vi.fn().mockResolvedValue(undefined);
    expect(() => setAutonomousSafetyNotifier(spy)).not.toThrow();
  });

  it("accepts null to unregister an existing notifier", () => {
    setAutonomousSafetyNotifier(vi.fn());
    expect(() => setAutonomousSafetyNotifier(null)).not.toThrow();
  });

  // Note: the actual fan-out from the IPC handler is integration-tested in
  // tests/services/DiscordService.test.ts (which mounts the full handler
  // and drives an `autonomous.statusChanged` payload). This module-level
  // file just pins the registration shape so a refactor doesn't drop the
  // setter from the public surface.
});
