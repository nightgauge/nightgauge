/**
 * AdapterDoctorPanel lifecycle tests (Issue #4031): singleton reuse, the
 * refresh re-entrancy guard, non-refresh message no-op, dispose reset, and
 * graceful handling of a rejected refresh. Mirrors the RecoveryDialog mock.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AdapterDoctorReport } from "../../../src/views/doctor/AdapterDoctorHtml";

let capturedMessageHandler: ((msg: any) => void) | null;
let capturedDisposeHandler: (() => void) | null;
let createWebviewPanelMock: ReturnType<typeof vi.fn>;
let showErrorMessageMock: ReturnType<typeof vi.fn>;

function buildMockPanel() {
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
    reveal: vi.fn(),
    onDidDispose: vi.fn((handler: () => void) => {
      capturedDisposeHandler = handler;
      return { dispose: vi.fn() };
    }),
    dispose: vi.fn(),
  };
}

vi.mock("vscode", () => ({
  ViewColumn: { One: 1 },
  window: {
    createWebviewPanel: vi.fn(),
    showErrorMessage: vi.fn(),
  },
}));

function report(generatedAt: string): AdapterDoctorReport {
  return { rows: [], stages: [], generatedAt, binaryResolved: true, notes: [] };
}

let AdapterDoctorPanel: typeof import("../../../src/views/doctor/AdapterDoctorPanel").AdapterDoctorPanel;

beforeEach(async () => {
  const vscode = await import("vscode");
  createWebviewPanelMock = vscode.window.createWebviewPanel as any;
  showErrorMessageMock = vscode.window.showErrorMessage as any;
  createWebviewPanelMock.mockReset();
  showErrorMessageMock.mockReset();
  ({ AdapterDoctorPanel } = await import("../../../src/views/doctor/AdapterDoctorPanel"));
});

afterEach(() => {
  // Reset the singleton so each test starts clean.
  AdapterDoctorPanel.current?.dispose();
});

describe("AdapterDoctorPanel (#4031)", () => {
  it("creates a webview with a CSP'd HTML body on first show", () => {
    const panel = buildMockPanel();
    createWebviewPanelMock.mockReturnValue(panel);

    AdapterDoctorPanel.show(report("T1"), async () => report("T1"));

    expect(createWebviewPanelMock).toHaveBeenCalledTimes(1);
    expect(panel.webview.html).toContain("Adapter Doctor");
    expect(panel.webview.html).toContain("Content-Security-Policy");
  });

  it("reuses the singleton panel and swaps the refresh callback on a second show", async () => {
    const panel = buildMockPanel();
    createWebviewPanelMock.mockReturnValue(panel);

    const refreshA = vi.fn(async () => report("A"));
    const refreshB = vi.fn(async () => report("B"));
    AdapterDoctorPanel.show(report("first"), refreshA);
    AdapterDoctorPanel.show(report("second"), refreshB);

    // Only one webview ever created (singleton); the second show revealed it.
    expect(createWebviewPanelMock).toHaveBeenCalledTimes(1);
    expect(panel.reveal).toHaveBeenCalled();

    // A refresh now invokes the LATEST callback (refreshB), not refreshA.
    await capturedMessageHandler!({ type: "refresh" });
    expect(refreshB).toHaveBeenCalledTimes(1);
    expect(refreshA).not.toHaveBeenCalled();
  });

  it("ignores a concurrent refresh while one is in flight (re-entrancy guard)", async () => {
    const panel = buildMockPanel();
    createWebviewPanelMock.mockReturnValue(panel);

    let resolveRefresh!: (r: AdapterDoctorReport) => void;
    const refresh = vi.fn(() => new Promise<AdapterDoctorReport>((res) => (resolveRefresh = res)));
    AdapterDoctorPanel.show(report("init"), refresh);

    const first = capturedMessageHandler!({ type: "refresh" }); // starts, stays pending
    await capturedMessageHandler!({ type: "refresh" }); // should be dropped
    expect(refresh).toHaveBeenCalledTimes(1);

    resolveRefresh(report("done"));
    await first;
    // After completion a new refresh is accepted again. The mock invokes
    // refresh() synchronously (before its internal await), so we don't await
    // this third call — its promise is intentionally left pending.
    void capturedMessageHandler!({ type: "refresh" });
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("ignores non-refresh messages", async () => {
    const panel = buildMockPanel();
    createWebviewPanelMock.mockReturnValue(panel);
    const refresh = vi.fn(async () => report("x"));
    AdapterDoctorPanel.show(report("init"), refresh);

    await capturedMessageHandler!({ type: "something-else" });
    await capturedMessageHandler!(undefined);
    expect(refresh).not.toHaveBeenCalled();
  });

  it("surfaces an error and stays usable when refresh rejects", async () => {
    const panel = buildMockPanel();
    createWebviewPanelMock.mockReturnValue(panel);
    const refresh = vi
      .fn()
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce(report("recovered"));
    AdapterDoctorPanel.show(report("init"), refresh);

    await capturedMessageHandler!({ type: "refresh" });
    expect(showErrorMessageMock).toHaveBeenCalledWith(expect.stringContaining("boom"));

    // refreshing flag was reset → a subsequent refresh still runs.
    await capturedMessageHandler!({ type: "refresh" });
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("resets the singleton on dispose so a later show builds a fresh panel", () => {
    const first = buildMockPanel();
    createWebviewPanelMock.mockReturnValueOnce(first);
    AdapterDoctorPanel.show(report("one"), async () => report("one"));
    expect(AdapterDoctorPanel.current).toBeDefined();

    capturedDisposeHandler!(); // user closed the panel
    expect(AdapterDoctorPanel.current).toBeUndefined();

    const second = buildMockPanel();
    createWebviewPanelMock.mockReturnValueOnce(second);
    AdapterDoctorPanel.show(report("two"), async () => report("two"));
    expect(createWebviewPanelMock).toHaveBeenCalledTimes(2);
  });
});
