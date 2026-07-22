/**
 * RecoveryDialog.test.ts — verify panel lifecycle, message round-trip,
 * cancel-on-close, and chained-rerender depth cap. Issue #3239.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { RecoveryRequiredPayload } from "@nightgauge/sdk";

let capturedMessageHandler: ((msg: any) => void) | null;
let capturedDisposeHandler: (() => void) | null;
let mockPanelDispose: ReturnType<typeof vi.fn>;
let createWebviewPanelMock: ReturnType<typeof vi.fn>;
let showWarningMessageMock: ReturnType<typeof vi.fn>;

function buildMockPanel() {
  mockPanelDispose = vi.fn();
  capturedMessageHandler = null;
  capturedDisposeHandler = null;

  return {
    webview: {
      html: "",
      cspSource: "test-csp",
      onDidReceiveMessage: vi.fn((handler: (msg: any) => void) => {
        capturedMessageHandler = handler;
        return { dispose: vi.fn() };
      }),
      postMessage: vi.fn(),
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
    showWarningMessage: vi.fn(),
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
  availableActions: [
    "resume-from-paused-stage",
    "run-producing-stage",
    "restart-from-beginning",
    "discard-run",
    "open-run-state-directory",
    "cancel",
  ],
};

beforeEach(async () => {
  const vscode = await import("vscode");
  createWebviewPanelMock = vscode.window.createWebviewPanel as any;
  showWarningMessageMock = vscode.window.showWarningMessage as any;
  createWebviewPanelMock.mockReset();
  createWebviewPanelMock.mockImplementation(() => buildMockPanel());
  showWarningMessageMock.mockReset();
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
      action: "resume-from-paused-stage",
      confirmed: true,
    });

    const result = await promise;
    expect(result.action).toBe("resume-from-paused-stage");
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

    capturedMessageHandler!({ type: "action", action: "discard-run", confirmed: false });

    // Promise still pending — confirmed:false is dropped.
    let resolved = false;
    promise.then(() => {
      resolved = true;
    });
    await new Promise((r) => setTimeout(r, 10));
    expect(resolved).toBe(false);

    capturedMessageHandler!({ type: "action", action: "cancel", confirmed: true });
    const result = await promise;
    expect(result.action).toBe("cancel");
  });

  it("auto-cancels when chain depth exceeds the cap", async () => {
    const { RecoveryDialog } = await import("../../../src/views/recovery/RecoveryDialog");
    const dialog = new RecoveryDialog({ fsPath: "/ext" } as any);

    // Burn through three chained shows, each resolved by user action.
    for (let i = 0; i < 3; i++) {
      const promise = dialog.show(basePayload);
      capturedMessageHandler!({ type: "action", action: "cancel", confirmed: true });
      await promise;
    }

    // Fourth call exceeds MAX_CHAIN_DEPTH=3 → resolves immediately as cancel.
    const result = await dialog.show(basePayload);
    expect(result.action).toBe("cancel");
    expect(showWarningMessageMock).toHaveBeenCalled();
  });
});
