/**
 * WorkflowEventSink — the write boundary for the canonical workflow event tree.
 *
 * Backends (the `SdkFanoutRunner` floor, the native Claude offload) emit through
 * a sink; consumers (VSCode/dashboard/Flutter forwarders, the durable journal)
 * implement one. Keeping the boundary minimal — a single `emit` plus an optional
 * `flush` — lets the same backend drive an in-memory tree, an SSE forwarder, and
 * a durable journal without knowing which.
 *
 * @see docs/WORKFLOW_ORCHESTRATION.md
 * @see Issue #3904
 */

import type { WorkflowEvent, WorkflowNode } from "./WorkflowEvent.js";

/** A consumer of workflow event emissions. */
export interface WorkflowEventSink {
  /** Append one node emission to the stream. Must not throw on the hot path. */
  emit(event: WorkflowEvent): void;
  /** Optional flush for batched / durable sinks. */
  flush?(): Promise<void>;
}

/**
 * In-memory sink that collects every emission and can fold them into the live
 * tree. Used by the `SdkFanoutRunner` reference floor and by tests; it is the
 * executable proof that the contract is consumable.
 */
export class ArrayWorkflowEventSink implements WorkflowEventSink {
  private readonly events: WorkflowEvent[] = [];

  emit(event: WorkflowEvent): void {
    this.events.push(event);
  }

  /** All emissions in arrival order. */
  getEvents(): readonly WorkflowEvent[] {
    return this.events;
  }

  /**
   * Fold the stream into the latest state per node (last write wins by `seq`).
   * Returns nodes keyed by `nodeId`.
   */
  latestByNode(): Map<string, WorkflowNode> {
    const latest = new Map<string, WorkflowNode>();
    for (const ev of this.events) {
      const prior = latest.get(ev.nodeId);
      if (!prior || ev.seq >= prior.seq) {
        latest.set(ev.nodeId, ev);
      }
    }
    return latest;
  }
}

/**
 * Monotonic sequence counter. Each call returns the next `seq` for a run. A
 * separate counter is created per run so sequences are dense and ordered.
 */
export function createSeqCounter(start = 0): () => number {
  let n = start;
  return () => n++;
}
