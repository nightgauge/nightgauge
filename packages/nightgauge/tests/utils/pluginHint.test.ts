import { afterEach, describe, expect, it } from "vitest";
import { isPluginEnabled, pluginHint, setPluginState } from "../../src/utils/pluginHint";
import type { PluginState } from "../../src/services/PluginSetupService";

describe("pluginHint (#2760)", () => {
  afterEach(() => setPluginState("unknown"));

  it("names the command when the plugin is enabled", () => {
    setPluginState("enabled");

    expect(isPluginEnabled()).toBe(true);
    expect(pluginHint("retro", "fallback")).toBe("/nightgauge:retro");
    expect(pluginHint("retro", "fallback", (command) => `Run ${command}.`)).toBe(
      "Run /nightgauge:retro."
    );
  });

  it.each<PluginState>(["unknown", "absent", "failed", "disabled"])(
    "gives the fallback when the state is %s",
    (state) => {
      setPluginState(state);

      expect(isPluginEnabled()).toBe(false);
      expect(pluginHint("retro", "fallback", (command) => `Run ${command}.`)).toBe("fallback");
    }
  );

  it("gives the fallback before any state is published", () => {
    expect(pluginHint("retro", "fallback")).toBe("fallback");
  });
});
