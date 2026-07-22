/**
 * Tests for switchPlatformEnvironment command
 *
 * @see Issue #3720 — Switch Platform Environment command
 * @see packages/nightgauge-vscode/src/commands/switchPlatformEnvironment.ts
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { QuickPickItem } from "vscode";

type EnvironmentOption = QuickPickItem & { value: string };

let quickPickResponses: Array<EnvironmentOption | undefined> = [];
let quickPickCallIndex = 0;
let inputBoxResponse: string | undefined = undefined;

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }],
  },
  window: {
    showQuickPick: vi.fn((_items: unknown[], _opts: unknown) => {
      const response = quickPickResponses[quickPickCallIndex];
      quickPickCallIndex++;
      return Promise.resolve(response);
    }),
    showInputBox: vi.fn((_opts: unknown) => {
      return Promise.resolve(inputBoxResponse);
    }),
    showErrorMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showWarningMessage: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  commands: {
    registerCommand: vi.fn((_id: string, callback: (...args: unknown[]) => unknown) => ({
      dispose: vi.fn(),
      _callback: callback,
    })),
  },
}));

const mockWriteLocal = vi.fn(() => Promise.resolve({ success: true }));
const mockDispose = vi.fn();

vi.mock("../../src/views/settings/IncrediYamlService", () => ({
  IncrediYamlService: vi.fn(function (this: any) {
    this.writeLocal = mockWriteLocal;
    this.dispose = mockDispose;
  }),
}));

const mockReload = vi.fn(() => Promise.resolve());
const mockGetPlatform = vi.fn(() => ({ environment: "production" as const }));

vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(() => ({
      reload: mockReload,
      getPlatform: mockGetPlatform,
    })),
  },
}));

const mockDisconnect = vi.fn();
const mockConnect = vi.fn();
const mockIsConnected = vi.fn(() => false);

vi.mock("../../src/services/ProjectEventSubscriber", () => ({
  ProjectEventSubscriber: {
    // Access to the mock vars stays lazy (inside the arrow) — the factory is
    // hoisted above their const declarations (TDZ), so eager access throws.
    getInstance: vi.fn(() => ({
      isConnected: mockIsConnected,
      disconnect: mockDisconnect,
      connect: mockConnect,
    })),
    // #3925 — the command now uses the non-throwing accessor.
    getInstanceOrNull: vi.fn(() => ({
      isConnected: mockIsConnected,
      disconnect: mockDisconnect,
      connect: mockConnect,
    })),
  },
}));

const mockRetrieve = vi.fn(() => Promise.resolve("mock-access-token"));

vi.mock("../../src/platform/TokenStorage", () => ({
  TokenStorage: {
    getInstance: vi.fn(() => ({
      retrieve: mockRetrieve,
    })),
  },
}));

import { registerSwitchPlatformEnvironmentCommand } from "../../src/commands/switchPlatformEnvironment";

async function invokeCommand(): Promise<void> {
  const vscode = await import("vscode");
  const registerCall = vi.mocked(vscode.commands.registerCommand).mock.calls.at(-1);
  const callback = registerCall![1] as () => Promise<void>;
  await callback();
}

describe("switchPlatformEnvironment command", () => {
  const mockLogger = {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  };

  beforeEach(() => {
    vi.clearAllMocks();
    quickPickResponses = [];
    quickPickCallIndex = 0;
    inputBoxResponse = undefined;
    mockWriteLocal.mockResolvedValue({ success: true });
    mockGetPlatform.mockReturnValue({ environment: "production" as const });
    mockIsConnected.mockReturnValue(false);
  });

  it("writes production environment when selected", async () => {
    quickPickResponses = [{ label: "Production", value: "production" }];

    registerSwitchPlatformEnvironmentCommand(mockLogger as never, null);
    await invokeCommand();

    expect(mockWriteLocal).toHaveBeenCalledWith(
      expect.objectContaining({
        platform: expect.objectContaining({ environment: "production" }),
      })
    );
    expect(mockReload).toHaveBeenCalled();
    expect(vi.mocked((await import("vscode")).window.showInformationMessage)).toHaveBeenCalledWith(
      expect.stringContaining("production")
    );
  });

  it("writes canary environment when selected", async () => {
    quickPickResponses = [{ label: "Canary", value: "canary" }];

    registerSwitchPlatformEnvironmentCommand(mockLogger as never, null);
    await invokeCommand();

    expect(mockWriteLocal).toHaveBeenCalledWith(
      expect.objectContaining({
        platform: expect.objectContaining({ environment: "canary" }),
      })
    );
    expect(mockReload).toHaveBeenCalled();
    expect(vi.mocked((await import("vscode")).window.showInformationMessage)).toHaveBeenCalledWith(
      expect.stringContaining("canary")
    );
  });

  it("writes custom environment with api_url when valid URL entered", async () => {
    quickPickResponses = [{ label: "Custom", value: "custom" }];
    inputBoxResponse = "https://my.custom.host.com";

    registerSwitchPlatformEnvironmentCommand(mockLogger as never, null);
    await invokeCommand();

    expect(mockWriteLocal).toHaveBeenCalledWith(
      expect.objectContaining({
        platform: expect.objectContaining({
          environment: "custom",
          api_url: "https://my.custom.host.com",
        }),
      })
    );
    expect(vi.mocked((await import("vscode")).window.showInformationMessage)).toHaveBeenCalledWith(
      expect.stringContaining("custom")
    );
    expect(vi.mocked((await import("vscode")).window.showInformationMessage)).toHaveBeenCalledWith(
      expect.stringContaining("https://my.custom.host.com")
    );
  });

  it("accepts localhost URL for custom environment", async () => {
    quickPickResponses = [{ label: "Custom", value: "custom" }];
    inputBoxResponse = "http://localhost:9000";

    registerSwitchPlatformEnvironmentCommand(mockLogger as never, null);
    await invokeCommand();

    expect(mockWriteLocal).toHaveBeenCalledWith(
      expect.objectContaining({
        platform: expect.objectContaining({
          environment: "custom",
          api_url: "http://localhost:9000",
        }),
      })
    );
  });

  it("does not write config when quick pick is cancelled", async () => {
    quickPickResponses = [undefined];

    registerSwitchPlatformEnvironmentCommand(mockLogger as never, null);
    await invokeCommand();

    expect(mockWriteLocal).not.toHaveBeenCalled();
    expect(mockReload).not.toHaveBeenCalled();
  });

  it("does not write config when custom URL input is cancelled", async () => {
    quickPickResponses = [{ label: "Custom", value: "custom" }];
    inputBoxResponse = undefined;

    registerSwitchPlatformEnvironmentCommand(mockLogger as never, null);
    await invokeCommand();

    expect(mockWriteLocal).not.toHaveBeenCalled();
    expect(mockReload).not.toHaveBeenCalled();
  });

  it("shows error and does not reload on write failure", async () => {
    quickPickResponses = [{ label: "Production", value: "production" }];
    mockWriteLocal.mockResolvedValue({ success: false, error: "disk full" });

    registerSwitchPlatformEnvironmentCommand(mockLogger as never, null);
    await invokeCommand();

    expect(vi.mocked((await import("vscode")).window.showErrorMessage)).toHaveBeenCalledWith(
      expect.stringContaining("disk full")
    );
    expect(mockReload).not.toHaveBeenCalled();
  });

  it("triggers SSE disconnect and reconnect when subscriber is connected", async () => {
    quickPickResponses = [{ label: "Canary", value: "canary" }];
    mockIsConnected.mockReturnValue(true);
    mockGetPlatform.mockReturnValue({ environment: "canary" as const });

    registerSwitchPlatformEnvironmentCommand(mockLogger as never, null);
    await invokeCommand();

    expect(mockDisconnect).toHaveBeenCalled();
    expect(mockConnect).toHaveBeenCalledWith(
      expect.stringContaining("canary"),
      "mock-access-token"
    );
  });

  it("does not trigger SSE reconnect when subscriber is not connected", async () => {
    quickPickResponses = [{ label: "Production", value: "production" }];
    mockIsConnected.mockReturnValue(false);

    registerSwitchPlatformEnvironmentCommand(mockLogger as never, null);
    await invokeCommand();

    expect(mockDisconnect).not.toHaveBeenCalled();
    expect(mockConnect).not.toHaveBeenCalled();
  });

  it("marks current environment with check icon in quick pick items", async () => {
    mockGetPlatform.mockReturnValue({ environment: "canary" as const });
    quickPickResponses = [undefined]; // cancel after inspecting

    const vscode = await import("vscode");
    registerSwitchPlatformEnvironmentCommand(mockLogger as never, null);
    await invokeCommand();

    const showQuickPickMock = vi.mocked(vscode.window.showQuickPick);
    const items = showQuickPickMock.mock.calls[0][0] as EnvironmentOption[];
    const canaryItem = items.find((i) => i.value === "canary");
    const productionItem = items.find((i) => i.value === "production");

    expect(canaryItem?.description).toContain("Current");
    expect(productionItem?.description ?? "").not.toContain("Current");
  });
});
