/**
 * autonomousStallConfig.test.ts
 *
 * Tests for stall escalation config fields added to AutonomousConfigSchema.
 *
 * @see Issue #2656 — Autonomous mode stall escalation and pause
 * @see packages/nightgauge-vscode/src/config/schema.ts
 */

import { describe, it, expect } from "vitest";
import { AutonomousConfigSchema } from "../../src/config/schema";

describe("AutonomousConfigSchema — stall escalation fields", () => {
  it("accepts empty object (all fields optional)", () => {
    expect(AutonomousConfigSchema.safeParse({}).success).toBe(true);
  });

  it("validates stall_escalation_enabled: true", () => {
    const config = { stall_escalation_enabled: true };
    const result = AutonomousConfigSchema.safeParse(config);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.stall_escalation_enabled).toBe(true);
    }
  });

  it("validates stall_escalation_enabled: false", () => {
    const config = { stall_escalation_enabled: false };
    const result = AutonomousConfigSchema.safeParse(config);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.stall_escalation_enabled).toBe(false);
    }
  });

  it("validates stall_pause_timeout with a 30-minute value", () => {
    const config = { stall_pause_timeout: 1800000 };
    const result = AutonomousConfigSchema.safeParse(config);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.stall_pause_timeout).toBe(1800000);
    }
  });

  it("validates stall_pause_timeout: 0 (immediate auto-abort)", () => {
    const config = { stall_pause_timeout: 0 };
    const result = AutonomousConfigSchema.safeParse(config);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.stall_pause_timeout).toBe(0);
    }
  });

  it("rejects negative stall_pause_timeout", () => {
    const config = { stall_pause_timeout: -1 };
    expect(AutonomousConfigSchema.safeParse(config).success).toBe(false);
  });

  it("rejects non-integer stall_pause_timeout", () => {
    const config = { stall_pause_timeout: 1800.5 };
    expect(AutonomousConfigSchema.safeParse(config).success).toBe(false);
  });

  it("validates stall_detection_minutes with the default issue threshold", () => {
    const config = { stall_detection_minutes: 60 };
    const result = AutonomousConfigSchema.safeParse(config);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.stall_detection_minutes).toBe(60);
    }
  });

  it("rejects stall_detection_minutes below 1", () => {
    const config = { stall_detection_minutes: 0 };
    expect(AutonomousConfigSchema.safeParse(config).success).toBe(false);
  });

  it("validates auto_redispatch_stalled as a boolean", () => {
    const config = { auto_redispatch_stalled: true };
    const result = AutonomousConfigSchema.safeParse(config);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.auto_redispatch_stalled).toBe(true);
    }
  });

  it("rejects stall_escalation_enabled as string", () => {
    const config = { stall_escalation_enabled: "true" };
    expect(AutonomousConfigSchema.safeParse(config).success).toBe(false);
  });

  it("validates both stall fields together with other autonomous fields", () => {
    const config = {
      scan_interval: "30s",
      max_concurrent: 2,
      stall_escalation_enabled: true,
      stall_pause_timeout: 1800000,
      stall_detection_minutes: 60,
      auto_redispatch_stalled: false,
    };
    const result = AutonomousConfigSchema.safeParse(config);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.stall_escalation_enabled).toBe(true);
      expect(result.data.stall_pause_timeout).toBe(1800000);
      expect(result.data.stall_detection_minutes).toBe(60);
      expect(result.data.auto_redispatch_stalled).toBe(false);
      expect(result.data.scan_interval).toBe("30s");
      expect(result.data.max_concurrent).toBe(2);
    }
  });

  it("validates without stall fields (backward compatibility)", () => {
    const config = {
      scan_interval: "1m",
      max_concurrent: 3,
      budget_ceiling: 500000,
    };
    const result = AutonomousConfigSchema.safeParse(config);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.stall_escalation_enabled).toBeUndefined();
      expect(result.data.stall_pause_timeout).toBeUndefined();
      expect(result.data.stall_detection_minutes).toBeUndefined();
      expect(result.data.auto_redispatch_stalled).toBeUndefined();
    }
  });
});
