/**
 * ApprovalDialog.test.ts
 *
 * Unit tests for ApprovalDialog class focusing on:
 * - Panel creation and disposal
 * - Message handler for `action` type (approve/edit/skip/cancel)
 * - Promise resolution per action type
 * - Auto-accept flag behavior (env var NIGHTGAUGE_AUTO_ACCEPT_STAGES)
 * - onDidDispose cleanup (closing the panel resolves as 'cancel')
 *
 * @see Issue #1241 - Add Vitest unit tests for OutputWindow and ApprovalDialog
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// ---------------------------------------------------------------------------
// Captured webview message handler and dispose handler
// ---------------------------------------------------------------------------

let capturedMessageHandler: ((msg: any) => void) | null;
let capturedDisposeHandler: (() => void) | null;
let mockPostMessage: ReturnType<typeof vi.fn>;
let mockPanelDispose: ReturnType<typeof vi.fn>;

function buildMockPanel() {
  mockPostMessage = vi.fn();
  mockPanelDispose = vi.fn();
  capturedMessageHandler = null;
  capturedDisposeHandler = null;

  return {
    webview: {
      html: "",
      onDidReceiveMessage: vi.fn((handler: (msg: any) => void) => {
        capturedMessageHandler = handler;
        return { dispose: vi.fn() };
      }),
      postMessage: mockPostMessage,
    },
    reveal: vi.fn(),
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
    joinPath: vi.fn((_uri: any, ...parts: string[]) => ({
      fsPath: `/mock/${parts.join("/")}`,
    })),
    file: vi.fn((p: string) => ({ fsPath: p })),
  },
  ViewColumn: { One: 1, Two: 2 },
  window: {
    createWebviewPanel: vi.fn(() => buildMockPanel()),
    showInformationMessage: vi.fn(),
    showWarningMessage: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    workspaceFolders: undefined,
    getConfiguration: vi.fn(() => ({ get: vi.fn() })),
  },
}));

vi.mock("fs", () => ({
  readFileSync: vi.fn(() => ""),
  existsSync: vi.fn(() => false),
}));

vi.mock("../../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(() => ({
    exists: false,
    path: "",
    isLegacy: false,
  })),
  logDeprecationWarning: vi.fn(),
}));

vi.mock("../../../src/views/approval/ApprovalDialogHtml", () => ({
  getApprovalDialogHtml: vi.fn(() => "<html></html>"),
}));

// ---------------------------------------------------------------------------
// Import under test
// ---------------------------------------------------------------------------

import { ApprovalDialog } from "../../../src/views/approval/ApprovalDialog";
import type { ApprovalAction } from "../../../src/views/approval/ApprovalDialog";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeDialog() {
  const extensionUri = { fsPath: "/mock/ext" } as any;
  return new ApprovalDialog(extensionUri);
}

// ---------------------------------------------------------------------------
// Tests — Panel creation
// ---------------------------------------------------------------------------

describe("ApprovalDialog — panel creation", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Ensure auto-accept env var is unset
    delete process.env.NIGHTGAUGE_AUTO_ACCEPT_STAGES;
  });

  it("creates a webview panel when show() is called", async () => {
    const { window } = await import("vscode");
    const dialog = makeDialog();

    // Trigger show but don't await — we resolve it immediately below
    const showPromise = dialog.show("feature-planning", 42, "# Plan");
    capturedMessageHandler?.({ type: "action", action: "approve" });
    await showPromise;

    expect(window.createWebviewPanel).toHaveBeenCalledOnce();
    expect(window.createWebviewPanel).toHaveBeenCalledWith(
      "incrediApprovalDialog",
      "Review Plan #42",
      expect.anything(),
      expect.objectContaining({
        enableScripts: true,
        retainContextWhenHidden: true,
      })
    );
  });

  it("registers onDidReceiveMessage and onDidDispose handlers", async () => {
    const { window } = await import("vscode");
    const panel = buildMockPanel();
    (window.createWebviewPanel as ReturnType<typeof vi.fn>).mockReturnValueOnce(panel);

    const dialog = makeDialog();
    const showPromise = dialog.show("feature-dev", 99, "plan");

    // Resolve via message handler
    capturedMessageHandler?.({ type: "action", action: "approve" });
    await showPromise;

    expect(panel.webview.onDidReceiveMessage).toHaveBeenCalledOnce();
    expect(panel.onDidDispose).toHaveBeenCalledOnce();
  });
});

// ---------------------------------------------------------------------------
// Tests — Promise resolution per action
// ---------------------------------------------------------------------------

describe("ApprovalDialog — promise resolution per action type", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    delete process.env.NIGHTGAUGE_AUTO_ACCEPT_STAGES;
  });

  const actions: ApprovalAction[] = ["approve", "edit", "skip", "cancel"];

  for (const action of actions) {
    it(`resolves with action="${action}" when webview sends action message`, async () => {
      const dialog = makeDialog();
      const showPromise = dialog.show("feature-planning", 1, "content");

      capturedMessageHandler?.({ type: "action", action });
      const result = await showPromise;

      expect(result).toEqual({ action });
    });
  }

  it("ignores messages with unknown type", async () => {
    const dialog = makeDialog();
    const showPromise = dialog.show("feature-planning", 1, "content");

    // Send an unrecognized message — should not resolve the promise yet
    capturedMessageHandler?.({ type: "unknown", data: "foo" });

    // Then send the real one
    capturedMessageHandler?.({ type: "action", action: "skip" });
    const result = await showPromise;

    expect(result).toEqual({ action: "skip" });
  });
});

// ---------------------------------------------------------------------------
// Tests — onDidDispose cleanup (closing panel = cancel)
// ---------------------------------------------------------------------------

describe("ApprovalDialog — onDidDispose cleanup", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    delete process.env.NIGHTGAUGE_AUTO_ACCEPT_STAGES;
  });

  it("resolves with cancel when the panel is closed via X button", async () => {
    const dialog = makeDialog();
    const showPromise = dialog.show("feature-dev", 7, "plan");

    // Simulate user closing the panel
    capturedDisposeHandler?.();
    const result = await showPromise;

    expect(result).toEqual({ action: "cancel" });
  });

  it("dispose() on the dialog disposes the panel", async () => {
    const { window } = await import("vscode");
    const panel = buildMockPanel();
    (window.createWebviewPanel as ReturnType<typeof vi.fn>).mockReturnValueOnce(panel);

    const dialog = makeDialog();
    const showPromise = dialog.show("feature-dev", 5, "plan");

    dialog.dispose();
    expect(panel.dispose).toHaveBeenCalled();

    // Clean up dangling promise
    capturedDisposeHandler?.();
    await showPromise.catch(() => {});
  });
});

// ---------------------------------------------------------------------------
// Tests — Auto-accept flag behavior
// ---------------------------------------------------------------------------

describe("ApprovalDialog — auto-accept behavior", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    delete process.env.NIGHTGAUGE_AUTO_ACCEPT_STAGES;
  });

  afterEach(() => {
    delete process.env.NIGHTGAUGE_AUTO_ACCEPT_STAGES;
  });

  it("returns immediate approve when NIGHTGAUGE_AUTO_ACCEPT_STAGES=true", async () => {
    process.env.NIGHTGAUGE_AUTO_ACCEPT_STAGES = "true";
    const dialog = makeDialog();

    const result = await dialog.show("feature-planning", 42, "# Plan");

    expect(result).toEqual({ action: "approve" });
  });

  it("does NOT create a panel when auto-accepting", async () => {
    const { window } = await import("vscode");
    process.env.NIGHTGAUGE_AUTO_ACCEPT_STAGES = "true";

    const dialog = makeDialog();
    await dialog.show("feature-planning", 42, "# Plan");

    expect(window.createWebviewPanel).not.toHaveBeenCalled();
  });

  it("shows a panel normally when auto-accept is not set", async () => {
    const { window } = await import("vscode");
    const dialog = makeDialog();

    const showPromise = dialog.show("feature-planning", 42, "# Plan");
    capturedMessageHandler?.({ type: "action", action: "approve" });
    await showPromise;

    expect(window.createWebviewPanel).toHaveBeenCalledOnce();
  });
});

// ---------------------------------------------------------------------------
// Tests — updateContent
// ---------------------------------------------------------------------------

describe("ApprovalDialog — updateContent", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    delete process.env.NIGHTGAUGE_AUTO_ACCEPT_STAGES;
  });

  it("postMessages an update when panel is open", async () => {
    const dialog = makeDialog();
    const showPromise = dialog.show("feature-planning", 1, "original");

    dialog.updateContent("updated plan");

    expect(mockPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "update", content: "updated plan" })
    );

    capturedMessageHandler?.({ type: "action", action: "approve" });
    await showPromise;
  });

  it("does not throw when panel is not open", () => {
    const dialog = makeDialog();
    expect(() => dialog.updateContent("some content")).not.toThrow();
  });
});
