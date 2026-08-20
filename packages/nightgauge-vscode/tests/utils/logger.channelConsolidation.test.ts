/**
 * logger.channelConsolidation.test.ts
 *
 * Six output channels — Nightgauge, Autonomous, Codex Setup, Go Backend,
 * Pipeline, Plugin Setup — used to split diagnostics across places an
 * operator had to already know to look. This verifies the consolidation
 * primitives in logger.ts: the memoized main channel is a true singleton
 * (not several channels that merely share a display name), the retired
 * channels' wrapper redacts and tags without letting one subsystem clear or
 * dispose another's history, and the IPC transport mirror redacts too.
 *
 * @see Issue #749 - Consolidate six output channels, add Show Diagnostics
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      name: "Nightgauge",
      append: vi.fn(),
      appendLine: vi.fn(),
      replace: vi.fn(),
      clear: vi.fn(),
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

import * as vscode from "vscode";
import {
  getMainChannel,
  getPrefixedMainChannel,
  createMainLogger,
  logDiagnosticMirror,
  resetMainChannelForTests,
  MAIN_CHANNEL_NAME,
} from "../../src/utils/logger";

describe("logger.ts — shared channel consolidation (#749)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetMainChannelForTests();
  });

  it("getMainChannel() creates the channel once and reuses it on every call", () => {
    const first = getMainChannel();
    const second = getMainChannel();

    expect(first).toBe(second);
    expect(vscode.window.createOutputChannel).toHaveBeenCalledTimes(1);
    expect(vscode.window.createOutputChannel).toHaveBeenCalledWith(MAIN_CHANNEL_NAME);
  });

  it("getPrefixedMainChannel() writes to the same underlying channel as getMainChannel()", () => {
    const main = getMainChannel();
    const prefixed = getPrefixedMainChannel("codex-setup");

    prefixed.appendLine("Starting Codex asset installation...");

    // Still exactly one real channel created, regardless of how many
    // wrappers/Loggers point at it.
    expect(vscode.window.createOutputChannel).toHaveBeenCalledTimes(1);
    expect(main.appendLine).toHaveBeenCalledWith(
      "[codex-setup] Starting Codex asset installation..."
    );
  });

  it("createMainLogger() writes to the same channel too — one destination, not three", () => {
    const main = getMainChannel();
    const logger = createMainLogger();

    logger.info("Activating Nightgauge extension");

    expect(vscode.window.createOutputChannel).toHaveBeenCalledTimes(1);
    expect(main.appendLine).toHaveBeenCalledTimes(1);
    expect(main.appendLine.mock.calls[0][0]).toContain("Activating Nightgauge extension");
  });

  it("redacts secrets on the prefixed wrapper before they reach the channel", () => {
    const main = getMainChannel();
    const prefixed = getPrefixedMainChannel("plugin-setup");
    const token = `ghp_${"a".repeat(24)}`;

    prefixed.appendLine(`Installed with token ${token}`);

    const written = main.appendLine.mock.calls[0][0] as string;
    expect(written).not.toContain(token);
    expect(written).toContain("[REDACTED:GH_TOKEN]");
  });

  it("does not let one subsystem clear another's history on the shared channel", () => {
    const main = getMainChannel();
    const prefixed = getPrefixedMainChannel("autonomous");

    prefixed.clear();

    expect(main.clear).not.toHaveBeenCalled();
  });

  it("does not let one subsystem dispose the shared channel out from under another", () => {
    const main = getMainChannel();
    const prefixed = getPrefixedMainChannel("pipeline");

    prefixed.dispose();

    expect(main.dispose).not.toHaveBeenCalled();
  });

  it("show()/hide() still delegate through the wrapper", () => {
    const main = getMainChannel();
    const prefixed = getPrefixedMainChannel("autonomous");

    prefixed.show(true);
    prefixed.hide();

    expect(main.show).toHaveBeenCalledWith(true, undefined);
    expect(main.hide).toHaveBeenCalled();
  });

  it("logDiagnosticMirror() redacts and tags endpoint + status onto the shared channel", () => {
    const main = getMainChannel();
    const token = `ghp_${"b".repeat(24)}`;

    logDiagnosticMirror("platform.getCostAnalytics", 401, `unauthorized: Bearer ${token}`);

    expect(vscode.window.createOutputChannel).toHaveBeenCalledTimes(1);
    const written = main.appendLine.mock.calls[0][0] as string;
    expect(written).toContain("platform.getCostAnalytics");
    expect(written).toContain("status=401");
    expect(written).not.toContain(token);
  });

  it("resetMainChannelForTests() forces the next call to create a fresh channel", () => {
    getMainChannel();
    expect(vscode.window.createOutputChannel).toHaveBeenCalledTimes(1);

    resetMainChannelForTests();
    getMainChannel();

    expect(vscode.window.createOutputChannel).toHaveBeenCalledTimes(2);
  });
});
