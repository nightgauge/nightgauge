import { describe, expect, it, vi } from "vitest";
import { SettingsMessageHandler } from "../../../src/views/settings/SettingsMessageHandler";

describe("SettingsMessageHandler", () => {
  it("routes action messages to the callback", () => {
    const onAction = vi.fn();
    const handler = new SettingsMessageHandler({ onAction });

    handler.handleMessage({
      type: "action",
      action: "codex-refresh-models",
      payload: {
        projectNumber: 7,
      },
    });

    expect(onAction).toHaveBeenCalledWith("codex-refresh-models", {
      projectNumber: 7,
    });
  });

  it("supports async action callbacks", async () => {
    const onAction = vi.fn(async () => {});
    const handler = new SettingsMessageHandler({ onAction });

    handler.handleMessage({
      type: "action",
      action: "opencode-refresh-models",
      payload: {
        projectNumber: 7,
      },
    });

    expect(onAction).toHaveBeenCalledWith("opencode-refresh-models", {
      projectNumber: 7,
    });
  });
});

describe("SettingsMessageHandler path validation", () => {
  it("routes a change to a schema path", () => {
    const onChange = vi.fn();
    new SettingsMessageHandler({ onChange }).handleMessage({
      type: "change",
      path: "pipeline.max_concurrent",
      value: 2,
    });
    expect(onChange).toHaveBeenCalledWith("pipeline.max_concurrent", 2, undefined);
  });

  it("drops a change to a path the schema does not define", () => {
    const onChange = vi.fn();
    const handler = new SettingsMessageHandler({ onChange });
    handler.handleMessage({ type: "change", path: "not_a_section.field", value: 1 });
    handler.handleMessage({ type: "change", path: "__proto__.polluted", value: 1 });
    expect(onChange).not.toHaveBeenCalled();
  });

  it("drops list and reset messages to unknown paths", () => {
    const onListAdd = vi.fn();
    const onListRemove = vi.fn();
    const onResetSetting = vi.fn();
    const handler = new SettingsMessageHandler({ onListAdd, onListRemove, onResetSetting });
    handler.handleMessage({ type: "list-add", path: "nope.list", value: "x" });
    handler.handleMessage({ type: "list-remove", path: "nope.list", index: 0 });
    handler.handleMessage({ type: "reset-setting", path: "nope.x", toTier: "default" });
    expect(onListAdd).not.toHaveBeenCalled();
    expect(onListRemove).not.toHaveBeenCalled();
    expect(onResetSetting).not.toHaveBeenCalled();
  });

  it("opens nothing for a documentation link outside docs/", () => {
    const onOpenDoc = vi.fn();
    const handler = new SettingsMessageHandler({ onOpenDoc });
    handler.handleMessage({ type: "open-doc", path: "../../etc/hosts" });
    expect(onOpenDoc).not.toHaveBeenCalled();
    handler.handleMessage({ type: "open-doc", path: "docs/CONFIGURATION.md#pipeline" });
    expect(onOpenDoc).toHaveBeenCalledWith("docs/CONFIGURATION.md#pipeline");
  });
});
