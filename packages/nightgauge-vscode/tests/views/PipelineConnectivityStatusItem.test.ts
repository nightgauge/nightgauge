/**
 * PipelineConnectivityStatusItem.test.ts (Issue #3203)
 *
 * Verifies visibility, label, and click behavior of the pipeline-aware
 * connectivity status bar item.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// ---------------------------------------------------------------------------
// vscode mock with createStatusBarItem + registerCommand
// ---------------------------------------------------------------------------
vi.mock("vscode", () => {
  class InternalEventEmitter<T> {
    listeners: Array<(e: T) => void> = [];
    event = (listener: (e: T) => void) => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          const idx = this.listeners.indexOf(listener);
          if (idx >= 0) this.listeners.splice(idx, 1);
        },
      };
    };
    fire = (data: T) => {
      for (const l of [...this.listeners]) l(data);
    };
    dispose = () => {};
  }

  const showQuickPick = vi.fn();
  const showInformationMessage = vi.fn();

  return {
    EventEmitter: InternalEventEmitter,
    StatusBarAlignment: { Left: 1, Right: 2 },
    ThemeColor: class {
      constructor(public readonly id: string) {}
    },
    window: {
      createStatusBarItem: vi.fn(() => ({
        text: "",
        tooltip: "",
        backgroundColor: undefined as unknown,
        command: undefined as unknown,
        show: vi.fn(),
        hide: vi.fn(),
        dispose: vi.fn(),
      })),
      showQuickPick,
      showInformationMessage,
    },
    commands: {
      registerCommand: vi.fn((_id: string, _fn: () => unknown) => ({
        dispose: vi.fn(),
      })),
    },
  };
});

// Mock skillRunner so we can control hasActiveProcess() and assert
// killAllActiveProcesses() is invoked.
const hasActiveProcessMock = vi.fn<() => boolean>();
const killAllActiveProcessesMock = vi.fn();
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: () => hasActiveProcessMock(),
  killAllActiveProcesses: () => killAllActiveProcessesMock(),
}));

import * as vscode from "vscode";
import { ConnectivityStateBus } from "../../src/platform/ConnectivityStateBus";
import { PipelineConnectivityStatusItem } from "../../src/views/PipelineConnectivityStatusItem";

type MockItem = {
  text: string;
  tooltip: string;
  backgroundColor: unknown;
  command: unknown;
  show: ReturnType<typeof vi.fn>;
  hide: ReturnType<typeof vi.fn>;
  dispose: ReturnType<typeof vi.fn>;
};

function lastCreatedItem(): MockItem {
  const calls = (
    vscode.window.createStatusBarItem as unknown as {
      mock: { results: Array<{ value: MockItem }> };
    }
  ).mock.results;
  return calls[calls.length - 1].value;
}

describe("PipelineConnectivityStatusItem (#3203)", () => {
  let registeredCommand: { id: string; handler: () => unknown } | null = null;

  beforeEach(() => {
    ConnectivityStateBus.resetForTests();
    hasActiveProcessMock.mockReset();
    killAllActiveProcessesMock.mockReset();
    (vscode.window.createStatusBarItem as unknown as { mockClear: () => void }).mockClear();
    (vscode.window.showQuickPick as unknown as { mockReset: () => void }).mockReset();
    (vscode.window.showInformationMessage as unknown as { mockReset: () => void }).mockReset();
    (vscode.commands.registerCommand as unknown as { mockClear: () => void }).mockClear();
    (
      vscode.commands.registerCommand as unknown as {
        mockImplementation: (fn: (id: string, h: () => unknown) => unknown) => void;
      }
    ).mockImplementation((id: string, handler: () => unknown) => {
      registeredCommand = { id, handler };
      return { dispose: vi.fn() };
    });
    registeredCommand = null;
  });

  afterEach(() => {
    ConnectivityStateBus.resetForTests();
  });

  it("hides the item while connectivity is online (regardless of pipeline activity)", () => {
    hasActiveProcessMock.mockReturnValue(true);
    const sut = new PipelineConnectivityStatusItem();
    const item = lastCreatedItem();

    expect(item.show).not.toHaveBeenCalled();
    expect(item.hide).toHaveBeenCalled();
    sut.dispose();
  });

  it("hides the item when offline but no pipeline is running", () => {
    hasActiveProcessMock.mockReturnValue(false);
    const sut = new PipelineConnectivityStatusItem();
    ConnectivityStateBus.set("offline");
    const item = lastCreatedItem();

    expect(item.show).not.toHaveBeenCalled();
    expect(item.hide).toHaveBeenCalled();
    sut.dispose();
  });

  it("shows the offline label when both offline and a pipeline is running", () => {
    hasActiveProcessMock.mockReturnValue(true);
    const sut = new PipelineConnectivityStatusItem();
    ConnectivityStateBus.set("offline");
    const item = lastCreatedItem();

    expect(item.show).toHaveBeenCalled();
    expect(item.text).toContain("Pipeline paused");
    expect(item.text).toContain("offline");
    sut.dispose();
  });

  it("shows the degraded label when connectivity is unstable", () => {
    hasActiveProcessMock.mockReturnValue(true);
    const sut = new PipelineConnectivityStatusItem();
    ConnectivityStateBus.set("degraded");
    const item = lastCreatedItem();

    expect(item.show).toHaveBeenCalled();
    expect(item.text).toContain("connectivity unstable");
    sut.dispose();
  });

  it("kills active pipelines when the user picks 'Cancel running pipelines'", async () => {
    hasActiveProcessMock.mockReturnValue(true);
    const sut = new PipelineConnectivityStatusItem();
    ConnectivityStateBus.set("offline");

    (
      vscode.window.showQuickPick as unknown as { mockResolvedValue: (v: unknown) => void }
    ).mockResolvedValue({
      label: "$(stop) Cancel running pipelines",
      value: "Cancel running pipelines",
    });

    expect(registeredCommand).not.toBeNull();
    await registeredCommand!.handler();

    expect(killAllActiveProcessesMock).toHaveBeenCalledTimes(1);
    sut.dispose();
  });

  it("does NOT kill anything when the user picks 'Keep waiting'", async () => {
    hasActiveProcessMock.mockReturnValue(true);
    const sut = new PipelineConnectivityStatusItem();
    ConnectivityStateBus.set("offline");

    (
      vscode.window.showQuickPick as unknown as { mockResolvedValue: (v: unknown) => void }
    ).mockResolvedValue({
      label: "$(clock) Keep waiting",
      value: "Keep waiting",
    });

    await registeredCommand!.handler();

    expect(killAllActiveProcessesMock).not.toHaveBeenCalled();
    sut.dispose();
  });

  it("shows an info message when clicked while no pipeline is running", async () => {
    hasActiveProcessMock.mockReturnValue(false);
    const sut = new PipelineConnectivityStatusItem();
    ConnectivityStateBus.set("offline");

    await registeredCommand!.handler();

    expect(killAllActiveProcessesMock).not.toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(1);
    sut.dispose();
  });
});
