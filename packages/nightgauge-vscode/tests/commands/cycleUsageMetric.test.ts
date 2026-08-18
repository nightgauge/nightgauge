/**
 * Tests for the nightgauge.cycleUsageMetric command (Issue #659)
 *
 * Covers:
 * - Registers under the expected command id
 * - Advances the status bar's selection and persists the new window id
 * - Does not touch workspace state when there is nothing to cycle to
 *
 * @see Issue #659 - Status bar usage meter with click-to-cycle metrics
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  commands: {
    registerCommand: vi.fn((command: string, handler: () => void) => ({
      command,
      handler,
      dispose: vi.fn(),
    })),
  },
}));

import * as vscode from "vscode";
import {
  registerCycleUsageMetricCommand,
  USAGE_WINDOW_STATE_KEY,
} from "../../src/commands/cycleUsageMetric";
import type { StatusBarManager } from "../../src/utils/statusBar";

function makeContext() {
  const store = new Map<string, unknown>();
  return {
    workspaceState: {
      get: vi.fn((key: string, fallback?: unknown) => (store.has(key) ? store.get(key) : fallback)),
      update: vi.fn((key: string, value: unknown) => {
        store.set(key, value);
        return Promise.resolve();
      }),
    },
    store,
  } as unknown as vscode.ExtensionContext & { store: Map<string, unknown> };
}

function makeStatusBar(cycleResult: string | null) {
  return {
    cycleUsageWindow: vi.fn(() => cycleResult),
  } as unknown as StatusBarManager;
}

describe("registerCycleUsageMetricCommand", () => {
  beforeEach(() => {
    vi.mocked(vscode.commands.registerCommand).mockClear();
  });

  it("registers under nightgauge.cycleUsageMetric", () => {
    const context = makeContext();
    registerCycleUsageMetricCommand(context, makeStatusBar(null));

    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.cycleUsageMetric",
      expect.any(Function)
    );
  });

  it("persists the new window id to workspace state when cycling succeeds", () => {
    const context = makeContext();
    const statusBar = makeStatusBar("local-telemetry:daily");
    registerCycleUsageMetricCommand(context, statusBar);

    const handler = vi.mocked(vscode.commands.registerCommand).mock.calls[0][1] as () => void;
    handler();

    expect(statusBar.cycleUsageWindow).toHaveBeenCalledOnce();
    expect(context.workspaceState.update).toHaveBeenCalledWith(
      USAGE_WINDOW_STATE_KEY,
      "local-telemetry:daily"
    );
  });

  it("does not touch workspace state when there is nothing to cycle to", () => {
    const context = makeContext();
    const statusBar = makeStatusBar(null); // no snapshot / unknown snapshot
    registerCycleUsageMetricCommand(context, statusBar);

    const handler = vi.mocked(vscode.commands.registerCommand).mock.calls[0][1] as () => void;
    handler();

    expect(statusBar.cycleUsageWindow).toHaveBeenCalledOnce();
    expect(context.workspaceState.update).not.toHaveBeenCalled();
  });
});
