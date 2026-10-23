/**
 * Tests for notifierSetupReport (#1115).
 *
 * The old behaviour was an unconditional instruction to hand-edit YAML at a
 * path that was usually the wrong tier, fired even when the setting was
 * already present — and, for Slack, after prompting for the channel and then
 * throwing it away.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";

// Typed parameter so `mock.calls[0][0]` is not a zero-length tuple under the
// stricter test-tree typecheck (vitest itself does not catch this).
const writeGlobal = vi.fn(async (_config: Record<string, unknown>) => ({
  success: true,
}));
const getEffectiveConfig = vi.fn();

vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: { getInstance: () => ({ getEffectiveConfig }) },
}));
vi.mock("../../src/views/settings/NightgaugeYamlService", () => ({
  NightgaugeYamlService: class {
    writeGlobal = writeGlobal;
  },
}));
vi.mock("../../src/config/settings", () => ({ getWorkspaceRoot: () => "/tmp/ws" }));

import { reportNotifierSetup } from "../../src/commands/notifierSetupReport";

function withSlack(slack: Record<string, unknown>) {
  getEffectiveConfig.mockReturnValue({ config: { notifications: { slack } } });
}

describe("reportNotifierSetup (#1115)", () => {
  let info: ReturnType<typeof vi.fn>;
  let warn: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.clearAllMocks();
    writeGlobal.mockResolvedValue({ success: true });
    info = vi.fn();
    warn = vi.fn(async () => undefined);
    (vscode.window as never as Record<string, unknown>).showInformationMessage = info;
    (vscode.window as never as Record<string, unknown>).showWarningMessage = warn;
  });

  it("does not tell an already-configured operator to finish setup", async () => {
    // The reported bug: channel set for months, still nagged to add it.
    withSlack({ enabled: true, channel: "C0BT82HTXLK" });

    await reportNotifierSetup("slack", { channel: "C0BT82HTXLK" });

    expect(writeGlobal).not.toHaveBeenCalled();
    const msg = String(info.mock.calls[0][0]);
    expect(msg).toContain("configured and enabled");
    expect(msg).toContain("C0BT82HTXLK");
    // The two things the old message got wrong.
    expect(msg).not.toContain("Add the channel");
    expect(msg).not.toContain(".nightgauge/config.yaml to finish");
  });

  it("persists the channel it was given instead of discarding it", async () => {
    withSlack({ enabled: true, channel: "" });

    await reportNotifierSetup("slack", { channel: "C0NEWCHAN" });

    expect(writeGlobal).toHaveBeenCalledTimes(1);
    expect(writeGlobal.mock.calls[0][0]).toEqual({
      notifications: { slack: { channel: "C0NEWCHAN" } },
    });
    expect(String(info.mock.calls[0][0])).toContain("C0NEWCHAN");
  });

  it("writes to the machine tier, never the project tier", async () => {
    // notifications.* is a property of this machine. The old message named
    // .nightgauge/config.yaml (project), which shadows rather than configures.
    withSlack({});

    await reportNotifierSetup("slack", { channel: "C0ABC1234" });

    expect(writeGlobal).toHaveBeenCalled(); // writeGlobal == ~/.nightgauge/config.yaml
    expect(String(info.mock.calls[0][0])).toContain("~/.nightgauge/config.yaml");
  });

  it("enables a notifier that is not yet enabled", async () => {
    getEffectiveConfig.mockReturnValue({
      config: { notifications: { discord: { enabled: false } } },
    });

    await reportNotifierSetup("discord");

    expect(writeGlobal.mock.calls[0][0]).toEqual({
      notifications: { discord: { enabled: true } },
    });
  });

  it("falls back to naming the exact key and offering Settings, not raw YAML", async () => {
    writeGlobal.mockResolvedValue({ success: false });
    getEffectiveConfig.mockReturnValue({ config: { notifications: { mattermost: {} } } });

    await reportNotifierSetup("mattermost");

    const msg = String(warn.mock.calls[0][0]);
    expect(msg).toContain("notifications.mattermost.enabled: true");
    expect(warn.mock.calls[0][1]).toBe("Open Settings");
    // Never point at the project-tier path again.
    expect(msg).not.toContain(".nightgauge/config.yaml");
  });
});
