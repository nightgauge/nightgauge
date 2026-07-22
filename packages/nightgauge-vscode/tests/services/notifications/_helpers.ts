/**
 * Shared test helpers for notification service unit tests.
 * Extracted per ADR-001 (PR #3380) to reduce duplication across
 * MattermostService.format, .retry, .redact, and NotificationDispatcher.fanout test files.
 */

import { vi } from "vitest";
import type { PipelineStateService } from "../../../src/services/PipelineStateService";
import type { Notifier, PipelineEventContext } from "../../../src/services/notifications/types";

// ─── Logger factory ────────────────────────────────────────────────────────

export function makeLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  };
}

// ─── ConfigBridge factory ──────────────────────────────────────────────────

export function makeConfigBridge(enabled = true) {
  return {
    getEffectiveConfig: vi.fn(() => ({
      config: {
        notifications: {
          mattermost: {
            enabled,
            webhook_env: "MATTERMOST_WEBHOOK_URL",
          },
        },
      },
    })),
  };
}

// ─── Pipeline state snapshot factory ──────────────────────────────────────

export function makeState(
  issueNumber: number,
  outcomeType?: string,
  extra: Record<string, unknown> = {}
) {
  return {
    issue_number: issueNumber,
    title: `Test issue #${issueNumber}`,
    branch: `fix/issue-${issueNumber}`,
    stages: { "issue-pickup": { status: "complete" } },
    tokens: { estimated_cost_usd: 0.05 },
    outcome_type: outcomeType,
    ...extra,
  };
}

// ─── ActiveRun-like factory ────────────────────────────────────────────────

export function makeRun(overrides: Record<string, unknown> = {}) {
  return {
    issueNumber: 42,
    issueTitle: "Test issue",
    branch: "fix/test",
    repoName: "my-repo",
    baseUrl: "https://mm.example.com",
    hookPath: "/hooks/abc",
    postId: "",
    startTime: Date.now(),
    costUsd: 0,
    stageStartTimes: new Map<string, number>(),
    isFinal: false,
    finalPatchRetries: 0,
    editMode: "edit" as const,
    fallbackWarned: false,
    ...overrides,
  };
}

// ─── FakeNotifier ──────────────────────────────────────────────────────────

export interface FakeNotifierCalls {
  initialize: number;
  onPipelineStart: PipelineEventContext[];
  onPipelineUpdate: PipelineEventContext[];
  subscribeToSlot: Array<{ issueNumber: number; repoSlug?: string }>;
  unsubscribeFromSlot: number[];
  dispose: number;
}

export class FakeNotifier implements Notifier {
  calls: FakeNotifierCalls = {
    initialize: 0,
    onPipelineStart: [],
    onPipelineUpdate: [],
    subscribeToSlot: [],
    unsubscribeFromSlot: [],
    dispose: 0,
  };

  initializeReject?: Error;
  onStartThrow?: Error;
  onUpdateThrow?: Error;
  subscribeThrow?: Error;
  disposeThrow?: Error;

  async initialize(): Promise<void> {
    this.calls.initialize += 1;
    if (this.initializeReject) throw this.initializeReject;
  }

  onPipelineStart(ctx: PipelineEventContext): void {
    this.calls.onPipelineStart.push(ctx);
    if (this.onStartThrow) throw this.onStartThrow;
  }

  onPipelineUpdate(ctx: PipelineEventContext): void {
    this.calls.onPipelineUpdate.push(ctx);
    if (this.onUpdateThrow) throw this.onUpdateThrow;
  }

  subscribeToSlot(
    issueNumber: number,
    _slotStateService: PipelineStateService,
    repoSlug?: string
  ): void {
    if (this.subscribeThrow) throw this.subscribeThrow;
    this.calls.subscribeToSlot.push({ issueNumber, repoSlug });
  }

  unsubscribeFromSlot(issueNumber: number): void {
    this.calls.unsubscribeFromSlot.push(issueNumber);
  }

  dispose(): void {
    this.calls.dispose += 1;
    if (this.disposeThrow) throw this.disposeThrow;
  }
}
