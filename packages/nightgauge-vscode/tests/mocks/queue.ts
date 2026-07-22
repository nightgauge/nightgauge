/**
 * Queue Mock Factories
 *
 * Mock factories for queue-related test data.
 *
 * @see Issue #236 - Queue Issues When Pipeline Active
 */

import type {
  QueueItem,
  QueueBatchItem,
  QueueState,
  QueueStatus,
  QueueConfig,
} from "../../src/types/queue";
import { DEFAULT_QUEUE_CONFIG } from "../../src/types/queue";
import type { BatchStrategy, IssueGroup } from "@nightgauge/sdk";

/**
 * Create a mock queue item with sensible defaults
 */
export function createMockQueueItem(overrides: Partial<QueueItem> = {}): QueueItem {
  return {
    issueNumber: 42,
    title: "Test Issue",
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    ...overrides,
  };
}

/**
 * Create multiple mock queue items
 */
export function createMockQueueItems(count: number): QueueItem[] {
  return Array.from({ length: count }, (_, i) =>
    createMockQueueItem({
      issueNumber: 100 + i,
      title: `Test Issue ${i + 1}`,
      position: i + 1,
    })
  );
}

/**
 * Create a mock queue state with sensible defaults
 */
export function createMockQueueState(overrides: Partial<QueueState> = {}): QueueState {
  return {
    schema_version: "1.0",
    status: "idle" as QueueStatus,
    items: [],
    config: DEFAULT_QUEUE_CONFIG,
    updated_at: new Date().toISOString(),
    ...overrides,
  };
}

/**
 * Create a mock queue config with sensible defaults
 */
export function createMockQueueConfig(overrides: Partial<QueueConfig> = {}): Required<QueueConfig> {
  return {
    ...DEFAULT_QUEUE_CONFIG,
    ...overrides,
  };
}

/**
 * Create a populated queue state with multiple items
 */
export function createPopulatedQueueState(
  itemCount: number,
  status: QueueStatus = "waiting"
): QueueState {
  return createMockQueueState({
    status,
    items: createMockQueueItems(itemCount),
  });
}

/**
 * Create a mock batch queue item
 *
 * @see Issue #803 - Queue Integration for Epic-Level Batching
 */
export function createMockQueueBatchItem(overrides: Partial<QueueBatchItem> = {}): QueueBatchItem {
  return {
    issueNumber: 799,
    title: "Epic #799 — 3 issues (batch)",
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    isBatch: true,
    epicNumber: 799,
    issueNumbers: [800, 801, 802],
    batchStrategy: "batch" as BatchStrategy,
    groups: [
      {
        issueNumbers: [800, 801, 802],
        groupReason: "High file overlap enables batching",
        sharedFiles: ["src/services/Pipeline.ts"],
        estimatedTokens: 90000,
      },
    ] as IssueGroup[],
    estimatedSavings: {
      tokensSaved: 100000,
      costUsd: 0.66,
      runsReduced: 2,
    },
    ...overrides,
  };
}
