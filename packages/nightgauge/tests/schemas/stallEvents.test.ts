/**
 * stallEvents.test.ts
 *
 * Schema validation tests for StallEvent types.
 *
 * @see Issue #2652 — Record stall events and user responses in run history
 * @see Issue #2656 — Autonomous mode stall escalation actions
 */

import { describe, it, expect } from "vitest";
import { StallEventSchema } from "../../src/schemas/stallEvents";
import { HistoryStageDetailSchema } from "../../src/schemas/executionHistory";

describe("StallEventSchema", () => {
  const validWarnEvent = {
    timestamp: "2026-04-11T13:15:22.456Z",
    elapsed_ms: 120000,
    threshold_ms: 120000,
    action: "warn" as const,
  };

  it("validates a warn event", () => {
    expect(StallEventSchema.safeParse(validWarnEvent).success).toBe(true);
  });

  it("validates a keep_waiting event", () => {
    const event = { ...validWarnEvent, action: "keep_waiting" as const };
    expect(StallEventSchema.safeParse(event).success).toBe(true);
  });

  it("validates a stop_stage event", () => {
    const event = { ...validWarnEvent, action: "stop_stage" as const };
    expect(StallEventSchema.safeParse(event).success).toBe(true);
  });

  it("validates a kill event", () => {
    const event = { ...validWarnEvent, action: "kill" as const };
    expect(StallEventSchema.safeParse(event).success).toBe(true);
  });

  // Issue #2656 — Autonomous mode stall escalation actions
  it("validates an escalation_pause event", () => {
    const event = { ...validWarnEvent, action: "escalation_pause" as const };
    expect(StallEventSchema.safeParse(event).success).toBe(true);
  });

  it("validates an auto_abort event", () => {
    const event = { ...validWarnEvent, action: "auto_abort" as const };
    expect(StallEventSchema.safeParse(event).success).toBe(true);
  });

  it("validates a resume event", () => {
    const event = { ...validWarnEvent, action: "resume" as const };
    expect(StallEventSchema.safeParse(event).success).toBe(true);
  });

  it("validates an abort event", () => {
    const event = { ...validWarnEvent, action: "abort" as const };
    expect(StallEventSchema.safeParse(event).success).toBe(true);
  });

  it("rejects invalid action values", () => {
    const event = { ...validWarnEvent, action: "restart" };
    expect(StallEventSchema.safeParse(event).success).toBe(false);
  });

  it("rejects negative elapsed_ms", () => {
    const event = { ...validWarnEvent, elapsed_ms: -1 };
    expect(StallEventSchema.safeParse(event).success).toBe(false);
  });

  it("rejects negative threshold_ms", () => {
    const event = { ...validWarnEvent, threshold_ms: -1 };
    expect(StallEventSchema.safeParse(event).success).toBe(false);
  });

  it("rejects missing required fields", () => {
    expect(StallEventSchema.safeParse({ action: "warn" }).success).toBe(false);
    expect(
      StallEventSchema.safeParse({ timestamp: "2026-04-11T00:00:00Z", action: "warn" }).success
    ).toBe(false);
  });
});

describe("HistoryStageDetailSchema with stall_events", () => {
  const baseStage = {
    status: "complete" as const,
  };

  it("validates a stage detail without stall_events (backward compat)", () => {
    expect(HistoryStageDetailSchema.safeParse(baseStage).success).toBe(true);
  });

  it("validates a stage detail with empty stall_events array", () => {
    const stage = { ...baseStage, stall_events: [] };
    expect(HistoryStageDetailSchema.safeParse(stage).success).toBe(true);
  });

  it("validates a stage detail with one warn stall event", () => {
    const stage = {
      ...baseStage,
      stall_events: [
        {
          timestamp: "2026-04-11T13:15:22.456Z",
          elapsed_ms: 120000,
          threshold_ms: 120000,
          action: "warn",
        },
      ],
    };
    expect(HistoryStageDetailSchema.safeParse(stage).success).toBe(true);
  });

  it("validates a stage detail with multiple stall events", () => {
    const stage = {
      ...baseStage,
      stall_events: [
        {
          timestamp: "2026-04-11T13:15:22.456Z",
          elapsed_ms: 120000,
          threshold_ms: 120000,
          action: "warn",
        },
        {
          timestamp: "2026-04-11T13:17:00.000Z",
          elapsed_ms: 220000,
          threshold_ms: 120000,
          action: "keep_waiting",
        },
        {
          timestamp: "2026-04-11T13:20:00.000Z",
          elapsed_ms: 400000,
          threshold_ms: 240000,
          action: "kill",
        },
      ],
    };
    expect(HistoryStageDetailSchema.safeParse(stage).success).toBe(true);
  });

  it("rejects a stall event with invalid action", () => {
    const stage = {
      ...baseStage,
      stall_events: [
        {
          timestamp: "2026-04-11T13:15:22.456Z",
          elapsed_ms: 120000,
          threshold_ms: 120000,
          action: "invalid_action",
        },
      ],
    };
    expect(HistoryStageDetailSchema.safeParse(stage).success).toBe(false);
  });
});
