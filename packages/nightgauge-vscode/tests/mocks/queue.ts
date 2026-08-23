/**
 * Queue Mock Factories
 *
 * Mock factories for queue-related test data.
 *
 * @see Issue #236 - Queue Issues When Pipeline Active
 */

import type { QueueItem, QueueState, QueueStatus, QueueConfig } from "../../src/types/queue";
import { DEFAULT_QUEUE_CONFIG } from "../../src/types/queue";

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
