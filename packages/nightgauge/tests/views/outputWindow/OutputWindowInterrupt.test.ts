/**
 * OutputWindowInterrupt.test.ts - Tests for handleInterrupt() delegation (Issue #851)
 *
 * Verifies that the Stop button in OutputWindow delegates to the
 * abortPipeline command instead of calling orchestrator.stop() directly,
 * preventing zombie pipeline state.
 *
 * @see src/views/outputWindow/OutputWindow.ts
 * @see src/commands/abortPipeline.ts
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import * as vscode from "vscode";
import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";
import type { HeadlessOrchestrator } from "../../../src/services/HeadlessOrchestrator";

// Minimal workspace state mock
function createMockWorkspaceState(): any {
  const storage = new Map<string, any>();
  return {
    get: vi.fn((key: string) => storage.get(key)),
    update: vi.fn((key: string, value: any) => {
      storage.set(key, value);
      return Promise.resolve();
    }),
  };
}

// Minimal extension URI mock
function createMockExtensionUri(): any {
  return {
    fsPath: "/mock/extension",
    toString: () => "/mock/extension",
  };
}

describe("OutputWindow handleInterrupt (Issue #851)", () => {
  let outputWindow: OutputWindow;
  let mockOrchestrator: HeadlessOrchestrator;

  beforeEach(() => {
    vi.clearAllMocks();

    outputWindow = new OutputWindow(createMockExtensionUri(), createMockWorkspaceState());

    mockOrchestrator = {
      getIsRunning: vi.fn().mockReturnValue(false),
      stop: vi.fn(),
      getCurrentStage: vi.fn().mockReturnValue(null),
    } as unknown as HeadlessOrchestrator;

    outputWindow.setOrchestrator(mockOrchestrator);
  });

  it("should delegate to abortPipeline command when single pipeline is running", () => {
    vi.mocked(mockOrchestrator.getIsRunning).mockReturnValue(true);

    // Trigger the interrupt via the message handler callback
    // The OutputWindow constructor wires onInterrupt to handleInterrupt
    // We access it through the internal state by simulating the message
    (outputWindow as any).handleInterrupt();

    expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.abortPipeline");
  });

  it("should NOT call orchestrator.stop() directly", () => {
    vi.mocked(mockOrchestrator.getIsRunning).mockReturnValue(true);

    (outputWindow as any).handleInterrupt();

    expect(mockOrchestrator.stop).not.toHaveBeenCalled();
  });

  it("should show info message when no pipeline is running", () => {
    vi.mocked(mockOrchestrator.getIsRunning).mockReturnValue(false);

    (outputWindow as any).handleInterrupt();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "No pipeline is currently running"
    );
    expect(vscode.commands.executeCommand).not.toHaveBeenCalledWith("nightgauge.abortPipeline");
  });

  it("should handle missing orchestrator gracefully", () => {
    // Create a fresh OutputWindow without setting an orchestrator
    const windowNoOrchestrator = new OutputWindow(
      createMockExtensionUri(),
      createMockWorkspaceState()
    );

    // Should not throw
    (windowNoOrchestrator as any).handleInterrupt();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "No pipeline is currently running"
    );
    expect(vscode.commands.executeCommand).not.toHaveBeenCalledWith("nightgauge.abortPipeline");
  });

  it("should log warning message when interrupt is triggered", () => {
    vi.mocked(mockOrchestrator.getIsRunning).mockReturnValue(true);

    (outputWindow as any).handleInterrupt();

    // Verify the warning message was added to the output entries
    const entries = outputWindow.getState().getEntries();
    const warningEntry = entries.find(
      (e) => e.text.includes("Pipeline stop requested") && e.level === "warning"
    );
    expect(warningEntry).toBeDefined();
  });
});
