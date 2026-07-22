/**
 * pipelineState.stallEscalation.test.ts
 *
 * Schema validation tests for stall escalation types added to pipelineState.ts.
 *
 * @see Issue #2656 — Autonomous mode stall escalation and pause
 * @see packages/nightgauge-vscode/src/schemas/pipelineState.ts
 */

import { describe, it, expect } from "vitest";
import {
  StallEscalationLevelSchema,
  StallEscalationMetadataSchema,
  PauseForStallPayloadSchema,
  PauseResolutionSchema,
} from "../../src/schemas/pipelineState";

// ============================================================================
// StallEscalationLevelSchema
// ============================================================================

describe("StallEscalationLevelSchema", () => {
  it("validates 'status_bar'", () => {
    expect(StallEscalationLevelSchema.safeParse("status_bar").success).toBe(true);
  });

  it("validates 'output_panel'", () => {
    expect(StallEscalationLevelSchema.safeParse("output_panel").success).toBe(true);
  });

  it("validates 'notification'", () => {
    expect(StallEscalationLevelSchema.safeParse("notification").success).toBe(true);
  });

  it("validates 'discord'", () => {
    expect(StallEscalationLevelSchema.safeParse("discord").success).toBe(true);
  });

  it("validates 'pause'", () => {
    expect(StallEscalationLevelSchema.safeParse("pause").success).toBe(true);
  });

  it("rejects invalid level 'email'", () => {
    expect(StallEscalationLevelSchema.safeParse("email").success).toBe(false);
  });

  it("rejects empty string", () => {
    expect(StallEscalationLevelSchema.safeParse("").success).toBe(false);
  });
});

// ============================================================================
// StallEscalationMetadataSchema
// ============================================================================

describe("StallEscalationMetadataSchema", () => {
  const validMetadata = {
    level: "notification" as const,
    elapsed_ms: 300000,
    stall_threshold_ms: 120000,
    extreme_threshold_ms: 600000,
    last_escalation_at: "2026-04-11T14:00:00.000Z",
    escalation_count: 3,
  };

  it("validates a full metadata object", () => {
    expect(StallEscalationMetadataSchema.safeParse(validMetadata).success).toBe(true);
  });

  it("validates metadata at level 'pause'", () => {
    const metadata = { ...validMetadata, level: "pause" as const, escalation_count: 5 };
    expect(StallEscalationMetadataSchema.safeParse(metadata).success).toBe(true);
  });

  it("validates metadata with zero elapsed_ms", () => {
    const metadata = { ...validMetadata, elapsed_ms: 0 };
    expect(StallEscalationMetadataSchema.safeParse(metadata).success).toBe(true);
  });

  it("rejects negative elapsed_ms", () => {
    const metadata = { ...validMetadata, elapsed_ms: -1 };
    expect(StallEscalationMetadataSchema.safeParse(metadata).success).toBe(false);
  });

  it("rejects negative stall_threshold_ms", () => {
    const metadata = { ...validMetadata, stall_threshold_ms: -100 };
    expect(StallEscalationMetadataSchema.safeParse(metadata).success).toBe(false);
  });

  it("rejects negative extreme_threshold_ms", () => {
    const metadata = { ...validMetadata, extreme_threshold_ms: -1 };
    expect(StallEscalationMetadataSchema.safeParse(metadata).success).toBe(false);
  });

  it("rejects negative escalation_count", () => {
    const metadata = { ...validMetadata, escalation_count: -1 };
    expect(StallEscalationMetadataSchema.safeParse(metadata).success).toBe(false);
  });

  it("rejects invalid level value", () => {
    const metadata = { ...validMetadata, level: "sms" };
    expect(StallEscalationMetadataSchema.safeParse(metadata).success).toBe(false);
  });

  it("rejects missing required fields", () => {
    expect(StallEscalationMetadataSchema.safeParse({ level: "status_bar" }).success).toBe(false);
  });

  it("rejects non-integer elapsed_ms", () => {
    const metadata = { ...validMetadata, elapsed_ms: 100.5 };
    expect(StallEscalationMetadataSchema.safeParse(metadata).success).toBe(false);
  });
});

// ============================================================================
// PauseForStallPayloadSchema
// ============================================================================

describe("PauseForStallPayloadSchema", () => {
  const validPayload = {
    reason: "stall_extreme" as const,
    issue_number: 42,
    stage: "feature-dev" as const,
    elapsed_ms: 600000,
    threshold_ms: 600000,
    timeout_ms: 1800000,
  };

  it("validates a full payload", () => {
    expect(PauseForStallPayloadSchema.safeParse(validPayload).success).toBe(true);
  });

  it("validates with different valid pipeline stages", () => {
    const stages = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ] as const;
    for (const stage of stages) {
      const payload = { ...validPayload, stage };
      expect(PauseForStallPayloadSchema.safeParse(payload).success).toBe(true);
    }
  });

  it("rejects reason other than 'stall_extreme'", () => {
    const payload = { ...validPayload, reason: "timeout" };
    expect(PauseForStallPayloadSchema.safeParse(payload).success).toBe(false);
  });

  it("rejects zero issue_number", () => {
    const payload = { ...validPayload, issue_number: 0 };
    expect(PauseForStallPayloadSchema.safeParse(payload).success).toBe(false);
  });

  it("rejects negative issue_number", () => {
    const payload = { ...validPayload, issue_number: -5 };
    expect(PauseForStallPayloadSchema.safeParse(payload).success).toBe(false);
  });

  it("rejects invalid stage", () => {
    const payload = { ...validPayload, stage: "invalid-stage" };
    expect(PauseForStallPayloadSchema.safeParse(payload).success).toBe(false);
  });

  it("rejects negative elapsed_ms", () => {
    const payload = { ...validPayload, elapsed_ms: -1 };
    expect(PauseForStallPayloadSchema.safeParse(payload).success).toBe(false);
  });

  it("rejects negative threshold_ms", () => {
    const payload = { ...validPayload, threshold_ms: -1 };
    expect(PauseForStallPayloadSchema.safeParse(payload).success).toBe(false);
  });

  it("rejects negative timeout_ms", () => {
    const payload = { ...validPayload, timeout_ms: -1 };
    expect(PauseForStallPayloadSchema.safeParse(payload).success).toBe(false);
  });

  it("accepts zero for ms fields", () => {
    const payload = { ...validPayload, elapsed_ms: 0, threshold_ms: 0, timeout_ms: 0 };
    expect(PauseForStallPayloadSchema.safeParse(payload).success).toBe(true);
  });

  it("rejects non-integer ms fields", () => {
    const payload = { ...validPayload, elapsed_ms: 100.5 };
    expect(PauseForStallPayloadSchema.safeParse(payload).success).toBe(false);
  });

  it("rejects missing required fields", () => {
    expect(PauseForStallPayloadSchema.safeParse({ reason: "stall_extreme" }).success).toBe(false);
  });
});

// ============================================================================
// PauseResolutionSchema
// ============================================================================

describe("PauseResolutionSchema", () => {
  const validResolution = {
    action: "resume" as const,
    issue_number: 42,
    stage: "feature-dev" as const,
    resolved_at: "2026-04-11T14:30:00.000Z",
  };

  it("validates a resume resolution", () => {
    expect(PauseResolutionSchema.safeParse(validResolution).success).toBe(true);
  });

  it("validates an abort resolution", () => {
    const resolution = { ...validResolution, action: "abort" as const };
    expect(PauseResolutionSchema.safeParse(resolution).success).toBe(true);
  });

  it("rejects invalid action", () => {
    const resolution = { ...validResolution, action: "retry" };
    expect(PauseResolutionSchema.safeParse(resolution).success).toBe(false);
  });

  it("rejects negative issue_number", () => {
    const resolution = { ...validResolution, issue_number: -1 };
    expect(PauseResolutionSchema.safeParse(resolution).success).toBe(false);
  });

  it("rejects zero issue_number", () => {
    const resolution = { ...validResolution, issue_number: 0 };
    expect(PauseResolutionSchema.safeParse(resolution).success).toBe(false);
  });

  it("rejects invalid stage", () => {
    const resolution = { ...validResolution, stage: "build" };
    expect(PauseResolutionSchema.safeParse(resolution).success).toBe(false);
  });

  it("rejects invalid datetime", () => {
    const resolution = { ...validResolution, resolved_at: "not-a-date" };
    expect(PauseResolutionSchema.safeParse(resolution).success).toBe(false);
  });

  it("rejects missing required fields", () => {
    expect(PauseResolutionSchema.safeParse({ action: "resume" }).success).toBe(false);
  });
});
