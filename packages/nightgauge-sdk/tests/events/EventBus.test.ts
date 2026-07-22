import { describe, it, expect, beforeEach, vi } from "vitest";
import { EventBus } from "../../src/events/EventBus.js";
import {
  WORKFLOW_SCHEMA_VERSION,
  zeroUsage,
  type WorkflowEvent,
  type WorkflowNodeStatus,
} from "../../src/cli/workflow/WorkflowEvent.js";

/** Build a phase node emission for the given status. */
function phase(status: WorkflowNodeStatus, name = "feature-planning", seq = 0): WorkflowEvent {
  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    kind: "phase",
    nodeId: `phase:42:${name}`,
    parentId: "run:42",
    seq,
    ts: new Date().toISOString(),
    status,
    name,
    index: 1,
    total: 6,
  } as WorkflowEvent;
}

/** Build an agent node emission for the given status. */
function agent(
  status: WorkflowNodeStatus,
  seq = 0,
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

describe("EventBus — node-tree sink", () => {
  let bus: EventBus;

  beforeEach(() => {
    bus = new EventBus();
  });

  describe("phase node emission", () => {
    it("delivers a running phase node to a phase subscriber", () => {
      const handler = vi.fn();
      bus.on("phase", handler);

      bus.emit(phase("running", "feature-planning", 1));

      expect(handler).toHaveBeenCalledOnce();
      expect(handler).toHaveBeenCalledWith(
        expect.objectContaining({
          kind: "phase",
          name: "feature-planning",
          status: "running",
          index: 1,
          total: 6,
        })
      );
    });

    it("passes nodeId / parentId / seq / ts through correctly", () => {
      const handler = vi.fn();
      bus.on("phase", handler);

      bus.emit(phase("succeeded", "feature-dev", 9));

      const received = handler.mock.calls[0][0];
      expect(received.nodeId).toBe("phase:42:feature-dev");
      expect(received.parentId).toBe("run:42");
      expect(received.seq).toBe(9);
      expect(typeof received.ts).toBe("string");
    });
  });

  describe("kind discrimination", () => {
    it("routes a phase node only to phase handlers", () => {
      const phaseHandler = vi.fn();
      const agentHandler = vi.fn();
      bus.on("phase", phaseHandler);
      bus.on("agent", agentHandler);

      bus.emit(phase("running", "issue-pickup", 1));

      expect(phaseHandler).toHaveBeenCalledOnce();
      expect(agentHandler).not.toHaveBeenCalled();
    });

    it("routes an agent node only to agent handlers", () => {
      const phaseHandler = vi.fn();
      const agentHandler = vi.fn();
      bus.on("phase", phaseHandler);
      bus.on("agent", agentHandler);

      bus.emit(agent("succeeded", 1));

      expect(agentHandler).toHaveBeenCalledOnce();
      expect(phaseHandler).not.toHaveBeenCalled();
    });
  });

  describe("multiple subscribers", () => {
    it("notifies all handlers for the same node kind", () => {
      const h1 = vi.fn();
      const h2 = vi.fn();
      const h3 = vi.fn();
      bus.on("phase", h1);
      bus.on("phase", h2);
      bus.on("phase", h3);

      bus.emit(phase("running", "feature-dev", 1));

      expect(h1).toHaveBeenCalledOnce();
      expect(h2).toHaveBeenCalledOnce();
      expect(h3).toHaveBeenCalledOnce();
    });
  });

  describe("onAny firehose", () => {
    it("delivers every emission regardless of kind", () => {
      const seen: string[] = [];
      bus.onAny((node) => seen.push(`${node.kind}:${node.status}`));

      bus.emit(phase("running", "feature-dev", 1));
      bus.emit(agent("succeeded", 2));

      expect(seen).toEqual(["phase:running", "agent:succeeded"]);
    });

    it("offAny() stops further firehose delivery", () => {
      const handler = vi.fn();
      bus.onAny(handler);

      bus.emit(phase("running", "feature-dev", 1));
      bus.offAny(handler);
      bus.emit(phase("succeeded", "feature-dev", 2));

      expect(handler).toHaveBeenCalledOnce();
    });
  });

  describe("once subscription", () => {
    it("auto-unsubscribes after the first emission", () => {
      const handler = vi.fn();
      bus.once("phase", handler);

      bus.emit(phase("running", "feature-planning", 1));
      bus.emit(phase("succeeded", "feature-planning", 2));

      expect(handler).toHaveBeenCalledOnce();
    });
  });

  describe("unsubscribe", () => {
    it("stops receiving nodes after off()", () => {
      const handler = vi.fn();
      bus.on("phase", handler);

      bus.emit(phase("succeeded", "feature-dev", 1));
      expect(handler).toHaveBeenCalledOnce();

      bus.off("phase", handler);
      bus.emit(phase("succeeded", "feature-dev", 2));
      expect(handler).toHaveBeenCalledOnce(); // still 1
    });
  });

  describe("removeAllListeners", () => {
    it("removes all listeners for a single kind", () => {
      const handler = vi.fn();
      bus.on("phase", handler);

      bus.removeAllListeners("phase");
      bus.emit(phase("running", "feature-dev", 1));

      expect(handler).not.toHaveBeenCalled();
    });

    it("clears every kind and the onAny firehose when called with no kind", () => {
      const phaseHandler = vi.fn();
      const anyHandler = vi.fn();
      bus.on("phase", phaseHandler);
      bus.onAny(anyHandler);

      bus.removeAllListeners();
      bus.emit(phase("running", "feature-dev", 1));

      expect(phaseHandler).not.toHaveBeenCalled();
      expect(anyHandler).not.toHaveBeenCalled();
    });
  });

  describe("listenerCount", () => {
    it("tracks listener counts per kind", () => {
      expect(bus.listenerCount("phase")).toBe(0);
      expect(bus.listenerCount("agent")).toBe(0);

      const h1 = vi.fn();
      const h2 = vi.fn();
      bus.on("phase", h1);
      bus.on("phase", h2);
      bus.on("agent", h1);

      expect(bus.listenerCount("phase")).toBe(2);
      expect(bus.listenerCount("agent")).toBe(1);
    });
  });

  describe("agent progress coalescing", () => {
    it("drops repeat running ticks for the same node within ~1s but passes status changes", () => {
      const handler = vi.fn();
      bus.on("agent", handler);

      bus.emit(agent("running", 1)); // passes (first)
      bus.emit(agent("running", 2)); // dropped (within window)
      bus.emit(agent("succeeded", 3)); // status change → always passes

      expect(handler).toHaveBeenCalledTimes(2);
    });
  });

  describe("error isolation in handlers", () => {
    it("does not stop other handlers when one throws", () => {
      const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
      const throwingHandler = vi.fn(() => {
        throw new Error("handler error");
      });
      const normalHandler = vi.fn();

      bus.on("phase", throwingHandler);
      bus.on("phase", normalHandler);

      bus.emit(phase("running", "feature-dev", 1));

      expect(throwingHandler).toHaveBeenCalledOnce();
      expect(normalHandler).toHaveBeenCalledOnce();
      expect(errorSpy).toHaveBeenCalled();

      errorSpy.mockRestore();
    });
  });
});
