/**
 * The chat panel's vocabulary and failure views (#1571): the provider is the
 * adapter, the mode is the mode, and each failure renders as its own state.
 */

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  ADAPTER_DISPLAY_NAMES,
  describeProfile,
  turnFailureView,
} from "../../../src/services/conversation/conversationDisplay";
import { parseExecutionProfile } from "../../../src/services/conversation/conversationWire";
import { PERFORMANCE_MODES } from "../../../src/utils/modeProfiles";

describe("ADAPTER_DISPLAY_NAMES", () => {
  it("is the Go registry's adapterDisplayName table", () => {
    const go = readFileSync(
      resolve(__dirname, "../../../../../internal/execution/adapters/registry.go"),
      "utf8"
    );
    const body = go.slice(go.indexOf("func adapterDisplayName("));
    const fn = body.slice(0, body.indexOf("\n}\n"));
    const table: Record<string, string> = {};
    for (const m of fn.matchAll(/case "([^"]+)":\s*\n\s*return "([^"]+)"/g)) table[m[1]!] = m[2]!;
    expect(Object.keys(table).length).toBeGreaterThanOrEqual(8);
    expect(ADAPTER_DISPLAY_NAMES).toEqual(table);
  });
});

describe("describeProfile", () => {
  it("labels the adapter as the provider and the mode as the mode", () => {
    const view = describeProfile({
      adapter: "codex",
      performance_mode: "frontier",
      effort: "high",
    });
    expect(view.provider).toEqual({ text: "Codex", recognized: true });
    expect(view.mode).toEqual({ text: "frontier", recognized: true });
    expect(view.effort).toEqual({ text: "high", recognized: true });
  });

  it("never shows frontier in the provider slot, whatever the wire's display name says", () => {
    const profile = parseExecutionProfile({
      adapter: "frontier",
      adapter_display_name: "Frontier",
      performance_mode: "frontier",
      effort: "high",
    });
    const view = describeProfile(profile!);
    expect(view.provider.recognized).toBe(false);
    expect(view.provider.text.toLowerCase()).not.toContain("frontier");
  });

  it("draws every provider from the display-name table and every mode from PERFORMANCE_MODES", () => {
    for (const [adapter, name] of Object.entries(ADAPTER_DISPLAY_NAMES)) {
      expect(
        describeProfile({ adapter, performance_mode: "elevated", effort: "" }).provider.text
      ).toBe(name);
    }
    for (const mode of PERFORMANCE_MODES) {
      const view = describeProfile({ adapter: "codex", performance_mode: mode, effort: "" });
      expect(view.mode).toEqual({ text: mode, recognized: true });
      expect(Object.values(ADAPTER_DISPLAY_NAMES)).not.toContain(view.mode.text);
    }
  });

  it("shows a mode or effort it does not recognize as received, never mapped", () => {
    const view = describeProfile({ adapter: "codex", performance_mode: "turbo", effort: "ultra" });
    expect(view.mode).toEqual({ text: "turbo", recognized: false });
    expect(view.effort).toEqual({ text: "ultra", recognized: false });
  });

  it("names an empty effort as the model's default", () => {
    expect(
      describeProfile({ adapter: "codex", performance_mode: "elevated", effort: "" }).effort.text
    ).toBe("model default");
  });
});

describe("turnFailureView", () => {
  it("separates a lost host from a failed turn, and keeps the host's detail as received", () => {
    expect(turnFailureView("agent_lost", null).kind).toBe("offline");
    expect(turnFailureView("target_unavailable", null).kind).toBe("offline");
    const hostError = turnFailureView("host_error", "adapter exited 2");
    expect(hostError.kind).toBe("turn_failed");
    expect(hostError.detail).toContain("adapter exited 2");
    expect(turnFailureView("brand_new_reason", null).detail).toContain("brand_new_reason");
  });
});
