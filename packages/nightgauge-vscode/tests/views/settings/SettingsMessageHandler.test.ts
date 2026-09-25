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
