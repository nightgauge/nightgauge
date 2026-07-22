/**
 * Integration tests — Event-Driven State Management
 *
 * Workflow 4: EventBus.on() → emit() → handler receives correct data.
 *
 * The EventBus carries the canonical node-tree WorkflowEvent contract (run /
 * phase / agent / judge, each with nodeId / parentId / seq / ISO ts). These
 * tests exercise subscription by node kind, the `onAny` firehose, multi-listener
 * delivery, `once`/`off`, listener counting, and the ~1 Hz `agent` progress
 * coalescing. Uses a real EventBus (not mocked).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { EventBus } from "../../../events/EventBus.js";
import {
  WORKFLOW_SCHEMA_VERSION,
  zeroUsage,
  type WorkflowEvent,
  type WorkflowNodeStatus,
} from "../../../cli/workflow/WorkflowEvent.js";

const RUN_NODE_ID = "run:42";

function phaseNode(overrides: Partial<WorkflowEvent> = {}): WorkflowEvent {
  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    kind: "phase",
    nodeId: "phase:42:feature-dev",
    parentId: RUN_NODE_ID,
    seq: 0,
    ts: new Date().toISOString(),
    status: "running",
    name: "feature-dev",
    index: 2,
    total: 6,
    ...overrides,
  } as WorkflowEvent;
}

function agentNode(
  status: WorkflowNodeStatus,
  seq: number,
  nodeId = "agent:42:feature-dev"
): WorkflowEvent {
  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    kind: "agent",
    nodeId,
    parentId: "phase:42:feature-dev",
    seq,
    ts: new Date().toISOString(),
    status,
    agentId: "feature-dev",
    provider: "claude",
    usage: zeroUsage(),
  } as WorkflowEvent;
}

function runNode(status: WorkflowNodeStatus, seq: number): WorkflowEvent {
  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    kind: "run",
    nodeId: RUN_NODE_ID,
    parentId: null,
    seq,
    ts: new Date().toISOString(),
    status,
    runId: RUN_NODE_ID,
    issueNumber: 42,
    backend: "sdk-fanout",
    startedAt: new Date().toISOString(),
  } as WorkflowEvent;
}

describe("Event-Driven State Workflow", () => {
  let bus: EventBus;

  beforeEach(() => {
    bus = new EventBus();
  });

  describe("node-kind lifecycle delivery", () => {
    it("delivers a running phase node to a phase subscriber", () => {
      const handler = vi.fn();
      bus.on("phase", handler);

      bus.emit(phaseNode({ status: "running", seq: 1 }));

      expect(handler).toHaveBeenCalledOnce();
      expect(handler.mock.calls[0][0]).toMatchObject({
        kind: "phase",
        name: "feature-dev",
        status: "running",
        parentId: RUN_NODE_ID,
      });
    });

    it("delivers a succeeded phase node with seq ordering", () => {
      const handler = vi.fn();
      bus.on("phase", handler);

      bus.emit(phaseNode({ status: "succeeded", seq: 7 }));

      expect(handler).toHaveBeenCalledOnce();
      expect(handler.mock.calls[0][0]).toMatchObject({ status: "succeeded", seq: 7 });
    });

    it("delivers a failed agent node carrying the terminal kind", () => {
      const handler = vi.fn();
      bus.on("agent", handler);

      bus.emit({ ...agentNode("failed", 3), terminalKind: "error" } as WorkflowEvent);

      const received = handler.mock.calls[0][0];
      expect(received.status).toBe("failed");
      expect(received.terminalKind).toBe("error");
    });
  });

  describe("event ordering", () => {
    it("emits phase running before phase succeeded for a stage lifecycle", () => {
      const received: string[] = [];
      bus.on("phase", (node) => received.push(node.status));

      bus.emit(phaseNode({ status: "running", seq: 1 }));
      bus.emit(phaseNode({ status: "succeeded", seq: 4 }));

      expect(received).toEqual(["running", "succeeded"]);
    });

    it("delivers events to listeners in registration order", () => {
      const order: number[] = [];
      bus.on("phase", () => order.push(1));
      bus.on("phase", () => order.push(2));
      bus.on("phase", () => order.push(3));

      bus.emit(phaseNode({ status: "running", seq: 1 }));

      expect(order).toEqual([1, 2, 3]);
    });
  });

  describe("multiple listeners", () => {
    it("delivers the same node to all registered listeners", () => {
      const h1 = vi.fn();
      const h2 = vi.fn();
      const h3 = vi.fn();
      bus.on("phase", h1);
      bus.on("phase", h2);
      bus.on("phase", h3);

      bus.emit(phaseNode({ status: "running", seq: 1 }));

      expect(h1).toHaveBeenCalledOnce();
      expect(h2).toHaveBeenCalledOnce();
      expect(h3).toHaveBeenCalledOnce();
    });

    it("does not deliver a node to listeners of a different kind", () => {
      const phaseHandler = vi.fn();
      const runHandler = vi.fn();
      bus.on("phase", phaseHandler);
      bus.on("run", runHandler);

      bus.emit(phaseNode({ status: "running", seq: 1 }));

      expect(phaseHandler).toHaveBeenCalledOnce();
      expect(runHandler).not.toHaveBeenCalled();
    });
  });

  describe("onAny firehose", () => {
    it("delivers every emission regardless of kind", () => {
      const seen: string[] = [];
      bus.onAny((node) => seen.push(node.kind));

      bus.emit(runNode("running", 0));
      bus.emit(phaseNode({ status: "running", seq: 1 }));
      bus.emit(agentNode("succeeded", 2));

      expect(seen).toEqual(["run", "phase", "agent"]);
    });
  });

  describe("agent progress coalescing (~1 Hz/node)", () => {
    beforeEach(() => {
      vi.useFakeTimers();
    });
    afterEach(() => {
      vi.useRealTimers();
    });

    it("drops consecutive running emissions for the same agent node within 1s", () => {
      const handler = vi.fn();
      bus.on("agent", handler);

      // First running tick passes; the next three within the window are dropped.
      bus.emit(agentNode("running", 1));
      bus.emit(agentNode("running", 2));
      bus.emit(agentNode("running", 3));
      bus.emit(agentNode("running", 4));

      expect(handler).toHaveBeenCalledOnce();

      // After the 1s window, a fresh running tick passes again.
      vi.advanceTimersByTime(1001);
      bus.emit(agentNode("running", 5));
      expect(handler).toHaveBeenCalledTimes(2);
    });

    it("never drops a status change, even within the coalescing window", () => {
      const handler = vi.fn();
      bus.on("agent", handler);

      bus.emit(agentNode("running", 1)); // passes (first)
      bus.emit(agentNode("running", 2)); // dropped (within window)
      bus.emit(agentNode("succeeded", 3)); // status change → always passes

      expect(handler).toHaveBeenCalledTimes(2);
      expect(handler.mock.calls[1][0].status).toBe("succeeded");
    });

    it("coalesces per node — distinct agent nodes are throttled independently", () => {
      const handler = vi.fn();
      bus.on("agent", handler);

      bus.emit(agentNode("running", 1, "agent:42:a"));
      bus.emit(agentNode("running", 2, "agent:42:b"));
      // Both first ticks pass (different nodeIds); a repeat of A is dropped.
      bus.emit(agentNode("running", 3, "agent:42:a"));

      expect(handler).toHaveBeenCalledTimes(2);
    });
  });

  describe("once() subscription", () => {
    it("fires handler exactly once and auto-unregisters", () => {
      const handler = vi.fn();
      bus.once("phase", handler);

      bus.emit(phaseNode({ status: "running", seq: 1 }));
      bus.emit(phaseNode({ status: "succeeded", seq: 2 }));

      expect(handler).toHaveBeenCalledOnce();
    });
  });

  describe("off() unsubscription", () => {
    it("stops delivering events after off() is called", () => {
      const handler = vi.fn();
      bus.on("phase", handler);

      bus.emit(phaseNode({ status: "succeeded", seq: 1 }));
      bus.off("phase", handler);
      bus.emit(phaseNode({ status: "succeeded", seq: 2 }));

      expect(handler).toHaveBeenCalledOnce();
    });
  });

  describe("listener count", () => {
    it("listenerCount() returns accurate count", () => {
      expect(bus.listenerCount("phase")).toBe(0);

      const h1 = vi.fn();
      const h2 = vi.fn();
      bus.on("phase", h1);
      bus.on("phase", h2);

      expect(bus.listenerCount("phase")).toBe(2);

      bus.off("phase", h1);
      expect(bus.listenerCount("phase")).toBe(1);
    });
  });

  describe("run terminal", () => {
    it("delivers the root run node terminal with finished status", () => {
      const handler = vi.fn();
      bus.on("run", handler);

      bus.emit(runNode("succeeded", 12));

      expect(handler).toHaveBeenCalledOnce();
      const node = handler.mock.calls[0][0];
      expect(node.status).toBe("succeeded");
      expect(node.parentId).toBeNull();
    });
  });
});
