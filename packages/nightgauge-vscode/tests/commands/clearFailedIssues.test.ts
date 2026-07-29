/**
 * Tests for clearFailedIssues command
 *
 * Covers the confirmation-message consistency fix for #150: the message must
 * name exactly what was reset (the per-issue lifetime retry budget) and must
 * only offer "Resume Autonomous" when the separate, fleet-wide circuit
 * breaker is actually still tripped.
 *
 * @see src/commands/clearFailedIssues.ts
 * @see Issue #150
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { registerClearFailedIssuesCommand } from "../../src/commands/clearFailedIssues";
import { IpcClient } from "../../src/services/IpcClient";
import { CompletedIssuesService } from "../../src/services/CompletedIssuesService";

vi.mock("vscode", () => ({
  commands: {
    registerCommand: vi.fn((_id: string, handler: any) => ({
      dispose: vi.fn(),
      handler,
    })),
    executeCommand: vi.fn(),
  },
  window: {
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
  },
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: vi.fn(),
  },
}));

vi.mock("../../src/services/CompletedIssuesService", () => ({
  CompletedIssuesService: {
    getInstance: vi.fn(),
  },
}));

describe("clearFailedIssues Command", () => {
  let mockIpc: { autonomousClearIssueFailures: ReturnType<typeof vi.fn> };
  let mockService: { getFailed: ReturnType<typeof vi.fn>; clearFailed: ReturnType<typeof vi.fn> };
  let commandHandler: () => Promise<void>;

  beforeEach(() => {
    vi.clearAllMocks();

    mockIpc = {
      autonomousClearIssueFailures: vi.fn().mockResolvedValue({
        cleared: 0,
        circuitBreakerTripped: false,
      }),
    };
    vi.mocked(IpcClient.getInstance).mockReturnValue(mockIpc as any);

    mockService = {
      getFailed: vi.fn().mockReturnValue([]),
      clearFailed: vi.fn(),
    };
    vi.mocked(CompletedIssuesService.getInstance).mockReturnValue(mockService as any);

    const disposable = registerClearFailedIssuesCommand({} as any);
    commandHandler = (disposable as any).handler;
  });

  it("reports nothing to clear when both sources are empty", async () => {
    await commandHandler();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("No failed issues to clear.");
    expect(mockService.clearFailed).not.toHaveBeenCalled();
  });

  it("names the lifetime retry budget and omits the resume offer when the circuit breaker is not tripped", async () => {
    mockService.getFailed.mockReturnValue([{}, {}]);
    mockIpc.autonomousClearIssueFailures.mockResolvedValue({
      cleared: 2,
      circuitBreakerTripped: false,
    });

    await commandHandler();

    expect(mockService.clearFailed).toHaveBeenCalled();
    const message = vi.mocked(vscode.window.showInformationMessage).mock.calls[0][0];
    expect(message).toContain("lifetime retry budget");
    expect(message).toContain("was not tripped");
    // Single-arg call — no "Resume Autonomous" button offered.
    expect(vi.mocked(vscode.window.showInformationMessage).mock.calls[0]).toHaveLength(1);
  });

  it("offers Resume Autonomous only when the circuit breaker is still tripped", async () => {
    mockService.getFailed.mockReturnValue([]);
    mockIpc.autonomousClearIssueFailures.mockResolvedValue({
      cleared: 3,
      circuitBreakerTripped: true,
    });
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Resume Autonomous" as any);

    await commandHandler();

    const call = vi.mocked(vscode.window.showInformationMessage).mock.calls[0];
    expect(call[0]).toContain("still tripped");
    expect(call).toContain("Resume Autonomous");
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.autonomousResume");
  });

  it("falls back to not-cleared/not-tripped when the Go binary is unavailable", async () => {
    mockService.getFailed.mockReturnValue([{}]);
    mockIpc.autonomousClearIssueFailures.mockRejectedValue(new Error("IPC unavailable"));

    await commandHandler();

    expect(mockService.clearFailed).toHaveBeenCalled();
    const message = vi.mocked(vscode.window.showInformationMessage).mock.calls[0][0];
    expect(message).toBe("Cleared 1 failed issue.");
  });
});
