import { describe, it, expect } from "vitest";
import {
  NotificationRouter,
  DEFAULT_ROUTER,
} from "../../../src/services/notifications/NotificationRouter";
import type { NotifierRoutingRule } from "../../../src/config/schema";

describe("NotificationRouter", () => {
  describe("DEFAULT_ROUTER (no rules)", () => {
    it("delivers all events to any notifier id", () => {
      expect(DEFAULT_ROUTER.shouldDeliver("discord", "pipeline.start")).toBe(true);
      expect(DEFAULT_ROUTER.shouldDeliver("mattermost", "pipeline.failure")).toBe(true);
      expect(DEFAULT_ROUTER.shouldDeliver("unknown-id", "stall.warning")).toBe(true);
    });
  });

  describe("shouldDeliver — unknown notifier id", () => {
    it("delivers when notifier id is not in routing table (backward compat)", () => {
      const router = new NotificationRouter([
        { id: "discord", type: "discord", events: ["pipeline.failure"] },
      ]);
      expect(router.shouldDeliver("mattermost", "pipeline.start")).toBe(true);
      expect(router.shouldDeliver("unknown", "pipeline.complete")).toBe(true);
    });
  });

  describe("route-by-event (allowlist)", () => {
    it("delivers when event is in allowlist", () => {
      const router = new NotificationRouter([
        { id: "discord", type: "discord", events: ["pipeline.failure", "stall.warning"] },
      ]);
      expect(router.shouldDeliver("discord", "pipeline.failure")).toBe(true);
      expect(router.shouldDeliver("discord", "stall.warning")).toBe(true);
    });

    it("blocks when event is not in allowlist", () => {
      const router = new NotificationRouter([
        { id: "discord", type: "discord", events: ["pipeline.failure"] },
      ]);
      expect(router.shouldDeliver("discord", "pipeline.start")).toBe(false);
      expect(router.shouldDeliver("discord", "pipeline.update")).toBe(false);
      expect(router.shouldDeliver("discord", "pipeline.complete")).toBe(false);
    });

    it("delivers all events when events array is empty (null allowlist)", () => {
      const router = new NotificationRouter([{ id: "discord", type: "discord", events: [] }]);
      expect(router.shouldDeliver("discord", "pipeline.start")).toBe(true);
      expect(router.shouldDeliver("discord", "pipeline.failure")).toBe(true);
    });

    it("delivers all events when events field is absent", () => {
      const router = new NotificationRouter([{ id: "discord", type: "discord" }]);
      expect(router.shouldDeliver("discord", "pipeline.start")).toBe(true);
      expect(router.shouldDeliver("discord", "budget.warning")).toBe(true);
    });
  });

  describe("suppress (denylist)", () => {
    it("blocks suppressed event even when event is in allowlist", () => {
      const router = new NotificationRouter([
        {
          id: "discord",
          type: "discord",
          events: ["pipeline.start", "pipeline.update", "pipeline.failure"],
          suppress: ["pipeline.update"],
        },
      ]);
      expect(router.shouldDeliver("discord", "pipeline.update")).toBe(false);
      expect(router.shouldDeliver("discord", "pipeline.start")).toBe(true);
      expect(router.shouldDeliver("discord", "pipeline.failure")).toBe(true);
    });

    it("suppress alone (no events allowlist) blocks specified events", () => {
      const router = new NotificationRouter([
        { id: "mattermost", type: "mattermost", suppress: ["pipeline.update", "stage.start"] },
      ]);
      expect(router.shouldDeliver("mattermost", "pipeline.update")).toBe(false);
      expect(router.shouldDeliver("mattermost", "stage.start")).toBe(false);
      expect(router.shouldDeliver("mattermost", "pipeline.failure")).toBe(true);
    });
  });

  describe("multi-notifier-same-event", () => {
    it("two notifiers with overlapping allowlists both receive shared events", () => {
      const rules: NotifierRoutingRule[] = [
        {
          id: "discord-alerts",
          type: "discord",
          events: ["pipeline.failure", "stall.warning", "budget.warning"],
        },
        {
          id: "mattermost-success",
          type: "mattermost",
          events: ["pipeline.complete", "pipeline.failure"],
        },
      ];
      const router = new NotificationRouter(rules);

      // pipeline.failure is in both allowlists
      expect(router.shouldDeliver("discord-alerts", "pipeline.failure")).toBe(true);
      expect(router.shouldDeliver("mattermost-success", "pipeline.failure")).toBe(true);

      // pipeline.complete is only in mattermost's allowlist
      expect(router.shouldDeliver("discord-alerts", "pipeline.complete")).toBe(false);
      expect(router.shouldDeliver("mattermost-success", "pipeline.complete")).toBe(true);

      // stall.warning is only in discord's allowlist
      expect(router.shouldDeliver("discord-alerts", "stall.warning")).toBe(true);
      expect(router.shouldDeliver("mattermost-success", "stall.warning")).toBe(false);
    });
  });

  describe("route-by-channel (id-based routing)", () => {
    it("two discord notifiers with different ids get disjoint event streams", () => {
      const rules: NotifierRoutingRule[] = [
        {
          id: "discord-pipeline",
          type: "discord",
          channel: "#pipeline-alerts",
          events: ["pipeline.failure", "pipeline.start"],
        },
        {
          id: "discord-stages",
          type: "discord",
          channel: "#stage-events",
          events: ["stage.start", "stage.complete", "stage.failure"],
        },
      ];
      const router = new NotificationRouter(rules);

      expect(router.shouldDeliver("discord-pipeline", "pipeline.failure")).toBe(true);
      expect(router.shouldDeliver("discord-pipeline", "stage.start")).toBe(false);
      expect(router.shouldDeliver("discord-stages", "stage.start")).toBe(true);
      expect(router.shouldDeliver("discord-stages", "pipeline.failure")).toBe(false);
    });
  });

  describe("suppress takes precedence", () => {
    it("suppress wins over allowlist when event appears in both", () => {
      const router = new NotificationRouter([
        {
          id: "discord",
          type: "discord",
          events: ["pipeline.start", "pipeline.failure"],
          suppress: ["pipeline.failure"],
        },
      ]);
      expect(router.shouldDeliver("discord", "pipeline.failure")).toBe(false);
      expect(router.shouldDeliver("discord", "pipeline.start")).toBe(true);
    });
  });
});
