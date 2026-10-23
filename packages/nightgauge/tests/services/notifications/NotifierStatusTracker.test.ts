/**
 * Tests for NotifierStatusTracker (#3379).
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { NotifierStatusTracker } from "../../../src/services/notifications/NotifierStatusTracker";

beforeEach(() => {
  NotifierStatusTracker.resetInstance();
  NotifierStatusTracker.initialize();
});

afterEach(() => {
  NotifierStatusTracker.resetInstance();
});

describe("NotifierStatusTracker", () => {
  it("getInstance returns null before initialize", () => {
    NotifierStatusTracker.resetInstance();
    expect(NotifierStatusTracker.getInstance()).toBeNull();
  });

  it("initialize creates a singleton", () => {
    const a = NotifierStatusTracker.getInstance();
    const b = NotifierStatusTracker.getInstance();
    expect(a).toBe(b);
    expect(a).not.toBeNull();
  });

  it("initialize does not throw on second call", () => {
    expect(() => NotifierStatusTracker.initialize()).not.toThrow();
  });

  it("recordSuccess sets lastSuccessAt", () => {
    const tracker = NotifierStatusTracker.getInstance()!;
    const before = Date.now();
    tracker.recordSuccess("discord");
    const status = tracker.getStatus("discord");
    expect(status).toBeDefined();
    expect(status!.lastSuccessAt!.getTime()).toBeGreaterThanOrEqual(before);
    expect(status!.lastErrorAt).toBeUndefined();
  });

  it("recordError sets lastErrorAt and lastError", () => {
    const tracker = NotifierStatusTracker.getInstance()!;
    tracker.recordError("mattermost", "webhook returned 401");
    const status = tracker.getStatus("mattermost");
    expect(status).toBeDefined();
    expect(status!.lastErrorAt).toBeInstanceOf(Date);
    expect(status!.lastError).toBe("webhook returned 401");
    expect(status!.lastSuccessAt).toBeUndefined();
  });

  it("recordSuccess preserves existing error info (only updates success)", () => {
    const tracker = NotifierStatusTracker.getInstance()!;
    tracker.recordError("discord", "timeout");
    tracker.recordSuccess("discord");
    const status = tracker.getStatus("discord");
    expect(status!.lastSuccessAt).toBeInstanceOf(Date);
    expect(status!.lastError).toBe("timeout");
  });

  it("getAllStatuses returns all tracked entries", () => {
    const tracker = NotifierStatusTracker.getInstance()!;
    tracker.recordSuccess("discord");
    tracker.recordError("mattermost", "err");
    const all = tracker.getAllStatuses();
    expect(all.map((s) => s.id).sort()).toEqual(["discord", "mattermost"]);
  });

  it("getStatus returns undefined for unknown id", () => {
    const tracker = NotifierStatusTracker.getInstance()!;
    expect(tracker.getStatus("unknown-notifier")).toBeUndefined();
  });

  it("clear removes all entries", () => {
    const tracker = NotifierStatusTracker.getInstance()!;
    tracker.recordSuccess("discord");
    tracker.clear();
    expect(tracker.getAllStatuses()).toHaveLength(0);
    expect(tracker.getStatus("discord")).toBeUndefined();
  });
});
