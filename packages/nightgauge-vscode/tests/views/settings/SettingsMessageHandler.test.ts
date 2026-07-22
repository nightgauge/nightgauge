import { describe, expect, it, vi } from "vitest";
import { SettingsMessageHandler } from "../../../src/views/settings/SettingsMessageHandler";

describe("SettingsMessageHandler", () => {
  it("routes action messages to the callback", () => {
    const onAction = vi.fn();
    const handler = new SettingsMessageHandler({ onAction });

    handler.handleMessage({
      type: "action",
      action: "lm-studio-refresh-models",
      payload: {
        "lm_studio.base_url": "http://localhost:1234/v1",
      },
    });

    expect(onAction).toHaveBeenCalledWith("lm-studio-refresh-models", {
      "lm_studio.base_url": "http://localhost:1234/v1",
    });
  });

  it("supports async action callbacks", async () => {
    const onAction = vi.fn(async () => {});
    const handler = new SettingsMessageHandler({ onAction });

    handler.handleMessage({
      type: "action",
      action: "lm-studio-load-model",
      payload: {
        "lm_studio.model": "openai/gpt-oss-20b",
      },
    });

    expect(onAction).toHaveBeenCalledWith("lm-studio-load-model", {
      "lm_studio.model": "openai/gpt-oss-20b",
    });
  });
});
