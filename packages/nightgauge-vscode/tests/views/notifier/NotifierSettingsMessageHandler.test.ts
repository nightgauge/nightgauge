/**
 * Tests for NotifierSettingsMessageHandler (#3379).
 *
 * Validates the webview → extension message routing protocol.
 */

import { describe, it, expect, vi } from "vitest";
import { NotifierSettingsMessageHandler } from "../../../src/views/notifier/NotifierSettingsMessageHandler";

function makeCallbacks() {
  return {
    onGetState: vi.fn(),
    onNotifierAdd: vi.fn(),
    onNotifierAction: vi.fn(),
  };
}

describe("NotifierSettingsMessageHandler", () => {
  it("getState → onGetState invoked", async () => {
    const cb = makeCallbacks();
    const h = new NotifierSettingsMessageHandler(cb);
    await h.handleMessage({ type: "getState" });
    expect(cb.onGetState).toHaveBeenCalledTimes(1);
  });

  it("notifier-add with discord → onNotifierAdd('discord')", async () => {
    const cb = makeCallbacks();
    const h = new NotifierSettingsMessageHandler(cb);
    await h.handleMessage({ type: "notifier-add", notifierType: "discord" });
    expect(cb.onNotifierAdd).toHaveBeenCalledWith("discord");
  });

  it("notifier-add with mattermost → onNotifierAdd('mattermost')", async () => {
    const cb = makeCallbacks();
    const h = new NotifierSettingsMessageHandler(cb);
    await h.handleMessage({ type: "notifier-add", notifierType: "mattermost" });
    expect(cb.onNotifierAdd).toHaveBeenCalledWith("mattermost");
  });

  it("notifier-add with invalid type is silently ignored", async () => {
    const cb = makeCallbacks();
    const h = new NotifierSettingsMessageHandler(cb);
    await h.handleMessage({ type: "notifier-add", notifierType: "slack" });
    expect(cb.onNotifierAdd).not.toHaveBeenCalled();
  });

  it("notifier-action test → onNotifierAction('test', id)", async () => {
    const cb = makeCallbacks();
    const h = new NotifierSettingsMessageHandler(cb);
    await h.handleMessage({ type: "notifier-action", action: "test", id: "discord" });
    expect(cb.onNotifierAction).toHaveBeenCalledWith("test", "discord");
  });

  it("notifier-action remove → onNotifierAction('remove', id)", async () => {
    const cb = makeCallbacks();
    const h = new NotifierSettingsMessageHandler(cb);
    await h.handleMessage({ type: "notifier-action", action: "remove", id: "mattermost" });
    expect(cb.onNotifierAction).toHaveBeenCalledWith("remove", "mattermost");
  });

  it("notifier-action with invalid action is silently ignored", async () => {
    const cb = makeCallbacks();
    const h = new NotifierSettingsMessageHandler(cb);
    await h.handleMessage({ type: "notifier-action", action: "edit", id: "discord" });
    expect(cb.onNotifierAction).not.toHaveBeenCalled();
  });

  it("notifier-action with empty id is silently ignored", async () => {
    const cb = makeCallbacks();
    const h = new NotifierSettingsMessageHandler(cb);
    await h.handleMessage({ type: "notifier-action", action: "test", id: "" });
    expect(cb.onNotifierAction).not.toHaveBeenCalled();
  });

  it("unknown type is silently ignored", async () => {
    const cb = makeCallbacks();
    const h = new NotifierSettingsMessageHandler(cb);
    await h.handleMessage({ type: "garbage" });
    expect(cb.onGetState).not.toHaveBeenCalled();
    expect(cb.onNotifierAdd).not.toHaveBeenCalled();
    expect(cb.onNotifierAction).not.toHaveBeenCalled();
  });

  it("null message is silently ignored", async () => {
    const cb = makeCallbacks();
    const h = new NotifierSettingsMessageHandler(cb);
    await h.handleMessage(null);
    expect(cb.onGetState).not.toHaveBeenCalled();
  });

  it("non-object message is silently ignored", async () => {
    const cb = makeCallbacks();
    const h = new NotifierSettingsMessageHandler(cb);
    await h.handleMessage("getState");
    expect(cb.onGetState).not.toHaveBeenCalled();
  });
});
