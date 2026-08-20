/**
 * RecoveryDialog.test.ts — verify panel lifecycle, message round-trip,
 * cancel-on-close, and repeatable observational actions.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { RecoveryRequiredPayload } from "@nightgauge/sdk";

let capturedMessageHandler: ((msg: any) => void) | null;
let capturedDisposeHandler: (() => void) | null;
let mockPanelDispose: ReturnType<typeof vi.fn>;
let createWebviewPanelMock: ReturnType<typeof vi.fn>;
let mockWebviewPostMessage: ReturnType<typeof vi.fn>;

function buildMockPanel() {
  mockPanelDispose = vi.fn();
  capturedMessageHandler = null;
  capturedDisposeHandler = null;

  mockWebviewPostMessage = vi.fn();
  return {
    webview: {
      html: "",
      cspSource: "test-csp",
      onDidReceiveMessage: vi.fn((handler: (msg: any) => void) => {
        capturedMessageHandler = handler;
        return { dispose: vi.fn() };
      }),
      postMessage: mockWebviewPostMessage,
    },
    onDidDispose: vi.fn((handler: () => void) => {
      capturedDisposeHandler = handler;
      return { dispose: vi.fn() };
    }),
    dispose: mockPanelDispose,
    visible: true,
  };
}

vi.mock("vscode", () => ({
  Uri: {
    joinPath: vi.fn((_uri: any, ...parts: string[]) => ({ fsPath: `/mock/${parts.join("/")}` })),
    file: vi.fn((p: string) => ({ fsPath: p })),
  },
  ViewColumn: { One: 1 },
  window: {
    createWebviewPanel: vi.fn(),
  },
  commands: {
    executeCommand: vi.fn(),
  },
}));

const basePayload: RecoveryRequiredPayload = {
  issueNumber: 42,
  triggeringStage: "feature-dev",
  producingStage: "feature-planning",
  errorKind: "MISSING_INPUT_FILE",
  errorDetail: "missing planning context",
  runState: "paused",
  availableActions: ["open-run-state-directory", "cancel"],
};

beforeEach(async () => {
  const vscode = await import("vscode");
  createWebviewPanelMock = vscode.window.createWebviewPanel as any;
  createWebviewPanelMock.mockReset();
  createWebviewPanelMock.mockImplementation(() => buildMockPanel());
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("RecoveryDialog", () => {
  it("creates a webview panel and resolves on action message", async () => {
    const { RecoveryDialog } = await import("../../../src/views/recovery/RecoveryDialog");
    const dialog = new RecoveryDialog({ fsPath: "/ext" } as any);

    const promise = dialog.show(basePayload);

    expect(createWebviewPanelMock).toHaveBeenCalledTimes(1);
    expect(capturedMessageHandler).not.toBeNull();

    capturedMessageHandler!({
      type: "action",
      action: "open-run-state-directory",
      confirmed: true,
    });

    const result = await promise;
    expect(result.action).toBe("open-run-state-directory");
  });

  it("resolves with cancel when the panel is closed", async () => {
    const { RecoveryDialog } = await import("../../../src/views/recovery/RecoveryDialog");
    const dialog = new RecoveryDialog({ fsPath: "/ext" } as any);
    const promise = dialog.show(basePayload);
    capturedDisposeHandler!();
    const result = await promise;
    expect(result.action).toBe("cancel");
  });

  it("ignores messages that aren't confirmed", async () => {
    const { RecoveryDialog } = await import("../../../src/views/recovery/RecoveryDialog");
    const dialog = new RecoveryDialog({ fsPath: "/ext" } as any);
    const promise = dialog.show(basePayload);

    capturedMessageHandler!({
      type: "action",
      action: "open-run-state-directory",
      confirmed: false,
    });
    capturedMessageHandler!({ type: "action", action: "cancel", confirmed: true });

    await expect(promise).resolves.toEqual({ action: "cancel" });
  });

  it("ignores malformed and unavailable webview actions", async () => {
    const { RecoveryDialog } = await import("../../../src/views/recovery/RecoveryDialog");
    const dialog = new RecoveryDialog({ fsPath: "/ext" } as any);
    const promise = dialog.show({ ...basePayload, availableActions: ["cancel"] });

    capturedMessageHandler!({
      type: "action",
      action: "restart-from-beginning",
      confirmed: true,
    });
    capturedMessageHandler!({
      type: "action",
      action: "open-run-state-directory",
      confirmed: true,
    });
    capturedMessageHandler!({
      type: "action",
      action: "run-producing-stage",
      confirmed: true,
      injected: true,
    });
    capturedMessageHandler!({ type: "action", action: "not-an-action", confirmed: true });
    capturedMessageHandler!({ type: "action", action: "cancel", confirmed: true });

    await expect(promise).resolves.toEqual({ action: "cancel" });
  });

  it("waits for another action in the same panel", async () => {
    const { RecoveryDialog } = await import("../../../src/views/recovery/RecoveryDialog");
    const dialog = new RecoveryDialog({ fsPath: "/ext" } as any);
    const first = dialog.show(basePayload);
    capturedMessageHandler!({
      type: "action",
      action: "open-run-state-directory",
      confirmed: true,
    });
    await expect(first).resolves.toEqual({ action: "open-run-state-directory" });

    const second = dialog.waitForNextAction();
    expect(mockWebviewPostMessage).toHaveBeenCalledWith({ type: "recoveryActionComplete" });
    capturedMessageHandler!({ type: "action", action: "cancel", confirmed: true });

    await expect(second).resolves.toEqual({ action: "cancel" });
    expect(createWebviewPanelMock).toHaveBeenCalledOnce();
  });
});
