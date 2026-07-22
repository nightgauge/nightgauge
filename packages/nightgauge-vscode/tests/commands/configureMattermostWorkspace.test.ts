/**
 * Tests for the configureMattermostWorkspace command
 *
 * @see Issue #3378 - VSCode command: Nightgauge: Configure Mattermost Workspace
 */

import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import * as vscode from "vscode";
import { registerConfigureMattermostWorkspaceCommand } from "../../src/commands/configureMattermostWorkspace";
import {
  SecretStorageService,
  SECRET_KEYS,
  mattermostSigningKey,
} from "../../src/services/SecretStorageService";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("../../src/services/SecretStorageService", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../src/services/SecretStorageService")>();
  return {
    ...actual,
    SecretStorageService: {
      getInstance: vi.fn(),
      initialize: vi.fn(),
      resetInstance: vi.fn(),
    },
  };
});

vi.mock("../../src/views/settings/IncrediYamlService", () => ({
  IncrediYamlService: vi.fn(function () {
    return {
      read: vi.fn().mockResolvedValue({ config: {}, success: true }),
      write: vi.fn().mockResolvedValue({ success: true }),
      getPrimaryConfigPath: vi.fn().mockReturnValue("/mock/.nightgauge/config.yaml"),
      dispose: vi.fn(),
    };
  }),
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function getLastCommandHandler(): (...args: any[]) => Promise<void> {
  const calls = (vscode.commands.registerCommand as any).mock.calls;
  return calls[calls.length - 1][1];
}

/** Sequence of showInputBox return values for the happy-path multi-step flow */
function setupHappyPathInputs(channels: Array<{ id: string; token: string }> = []) {
  const responses = [
    "https://mattermost.example.com", // server URL
    "xoxb-bot-token", // bot token
    "https://mattermost.example.com/hooks/abc123", // webhook URL
    ...channels.flatMap(({ id, token }) => [id, token]),
    "", // blank channel ID → done
  ];
  let idx = 0;
  (vscode.window.showInputBox as any).mockImplementation(() => Promise.resolve(responses[idx++]));
}

function createMockSecretService() {
  return {
    getSecret: vi.fn().mockResolvedValue(undefined),
    setSecret: vi.fn().mockResolvedValue(undefined),
    deleteSecret: vi.fn().mockResolvedValue(undefined),
  };
}

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

describe("registerConfigureMattermostWorkspaceCommand", () => {
  let mockSecretService: ReturnType<typeof createMockSecretService>;
  let commandHandler: () => Promise<void>;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.clearAllMocks();

    mockSecretService = createMockSecretService();
    (SecretStorageService.getInstance as any).mockReturnValue(mockSecretService);

    // withProgress immediately executes the task
    (vscode.window as any).withProgress = vi.fn(async (_opts: any, task: () => Promise<any>) => {
      await task();
    });

    // Default: showInputBox returns undefined (cancel) unless overridden
    (vscode.window as any).showInputBox = vi.fn().mockResolvedValue(undefined);
    (vscode.window as any).showQuickPick = vi.fn().mockResolvedValue(undefined);

    // Default workspace
    (vscode.workspace as any).workspaceFolders = [{ uri: { fsPath: "/mock/workspace" } }];
    (vscode.workspace as any).fs = {
      readFile: vi.fn().mockResolvedValue(Buffer.from("")),
      writeFile: vi.fn().mockResolvedValue(undefined),
    };

    // Default fetch: webhook returns 200, inbound HEAD times out (AbortError)
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const abortErr = Object.assign(new Error("aborted"), { name: "AbortError" });
    fetchMock.mockImplementation((url: string, opts: any) => {
      if (opts?.method === "HEAD") return Promise.reject(abortErr);
      return Promise.resolve({ ok: true, status: 200, statusText: "OK" });
    });

    registerConfigureMattermostWorkspaceCommand();
    commandHandler = getLastCommandHandler();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  // ── Case 15: SecretStorageService unavailable ───────────────────────────
  it("shows error and exits early when SecretStorageService is unavailable", async () => {
    (SecretStorageService.getInstance as any).mockReturnValue(null);

    await commandHandler();

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("SecretStorage is not available")
    );
    expect(vscode.window.showInputBox).not.toHaveBeenCalled();
  });

  // ── Case 3: Cancel on server URL step ──────────────────────────────────
  it("does not write any secrets when user cancels on server URL step", async () => {
    (vscode.window.showInputBox as any).mockResolvedValue(undefined);

    await commandHandler();

    expect(mockSecretService.setSecret).not.toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
  });

  // ── Case 4: Cancel on bot token step ───────────────────────────────────
  it("does not write any secrets when user cancels on bot token step", async () => {
    (vscode.window.showInputBox as any)
      .mockResolvedValueOnce("https://mattermost.example.com") // server URL
      .mockResolvedValueOnce(undefined); // cancel on bot token

    await commandHandler();

    expect(mockSecretService.setSecret).not.toHaveBeenCalled();
  });

  // ── Case 5: Cancel on webhook URL step ─────────────────────────────────
  it("does not write any secrets when user cancels on webhook URL step", async () => {
    (vscode.window.showInputBox as any)
      .mockResolvedValueOnce("https://mattermost.example.com")
      .mockResolvedValueOnce("bot-token")
      .mockResolvedValueOnce(undefined); // cancel on webhook

    await commandHandler();

    expect(mockSecretService.setSecret).not.toHaveBeenCalled();
  });

  // ── Case 6: Empty channel ID ends loop without aborting ─────────────────
  it("proceeds when user leaves channel ID blank (no channels)", async () => {
    setupHappyPathInputs([]); // blank channel ID → finish immediately

    await commandHandler();

    // Secrets written for server URL, bot token, webhook URL
    expect(mockSecretService.setSecret).toHaveBeenCalledWith(
      SECRET_KEYS.mattermostWebhookUrl,
      "https://mattermost.example.com/hooks/abc123"
    );
    expect(mockSecretService.setSecret).toHaveBeenCalledWith(
      SECRET_KEYS.mattermostBotToken,
      "xoxb-bot-token"
    );
  });

  // ── Case 7: Cancel during token input in channel loop ───────────────────
  it("aborts entire command when user cancels token input during channel loop", async () => {
    (vscode.window.showInputBox as any)
      .mockResolvedValueOnce("https://mattermost.example.com")
      .mockResolvedValueOnce("bot-token")
      .mockResolvedValueOnce("https://mattermost.example.com/hooks/abc123")
      .mockResolvedValueOnce("town-square") // channel ID provided
      .mockResolvedValueOnce(undefined); // cancel on token input → abort

    await commandHandler();

    expect(mockSecretService.setSecret).not.toHaveBeenCalled();
  });

  // ── Case 8: Server URL validation ──────────────────────────────────────
  it("validateInput on server URL rejects non-https URLs", async () => {
    registerConfigureMattermostWorkspaceCommand();
    const calls = (vscode.commands.registerCommand as any).mock.calls;
    const handler = calls[calls.length - 1][1];

    // Capture the validateInput function from the last showInputBox call
    let capturedValidate: ((v: string) => string | null) | undefined;
    (vscode.window.showInputBox as any).mockImplementation((opts: any) => {
      capturedValidate = opts?.validateInput;
      return Promise.resolve(undefined); // cancel after capturing
    });

    await handler();

    expect(capturedValidate).toBeDefined();
    expect(capturedValidate!("")).toBe("Server URL cannot be empty");
    expect(capturedValidate!("http://mattermost.example.com")).toMatch(/https/);
    expect(capturedValidate!("https://mattermost.example.com")).toBeNull();
  });

  // ── Case 9: Webhook URL validation ──────────────────────────────────────
  it("validateInput on webhook URL rejects non-hook URLs", async () => {
    let capturedValidate: ((v: string) => string | null) | undefined;
    let callIdx = 0;
    (vscode.window.showInputBox as any).mockImplementation((opts: any) => {
      callIdx++;
      if (callIdx === 1) return Promise.resolve("https://mattermost.example.com"); // server
      if (callIdx === 2) return Promise.resolve("bot-token"); // bot token
      if (callIdx === 3) {
        capturedValidate = opts?.validateInput;
        return Promise.resolve(undefined); // cancel after capturing
      }
      return Promise.resolve(undefined);
    });

    await commandHandler();

    expect(capturedValidate).toBeDefined();
    expect(capturedValidate!("")).toBe("Webhook URL cannot be empty");
    expect(capturedValidate!("https://mattermost.example.com/not-a-hook")).toMatch(
      /incoming-webhook/
    );
    expect(capturedValidate!("https://mattermost.example.com/hooks/abc123")).toBeNull();
  });

  // ── Case 10: Webhook POST returns non-200 ──────────────────────────────
  it("shows error and does not save when webhook returns non-200", async () => {
    setupHappyPathInputs([]);
    fetchMock.mockImplementation((url: string, opts: any) => {
      if (opts?.method === "HEAD")
        return Promise.reject(Object.assign(new Error("aborted"), { name: "AbortError" }));
      return Promise.resolve({ ok: false, status: 401, statusText: "Unauthorized" });
    });

    await commandHandler();

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("Webhook connection failed")
    );
    expect(mockSecretService.setSecret).not.toHaveBeenCalled();
  });

  // ── Case 11: Webhook POST throws network error ──────────────────────────
  it("shows error and does not save when webhook fetch throws", async () => {
    setupHappyPathInputs([]);
    fetchMock.mockImplementation((url: string, opts: any) => {
      if (opts?.method === "HEAD")
        return Promise.reject(Object.assign(new Error("aborted"), { name: "AbortError" }));
      return Promise.reject(new Error("ECONNREFUSED"));
    });

    await commandHandler();

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("ECONNREFUSED")
    );
    expect(mockSecretService.setSecret).not.toHaveBeenCalled();
  });

  // ── Case 1: Happy path — no channels, receiver not running ─────────────
  it("saves secrets and shows warning when webhook passes but receiver is not running", async () => {
    setupHappyPathInputs([]);

    await commandHandler();

    expect(mockSecretService.setSecret).toHaveBeenCalledWith(
      SECRET_KEYS.mattermostWebhookUrl,
      "https://mattermost.example.com/hooks/abc123"
    );
    expect(mockSecretService.setSecret).toHaveBeenCalledWith(
      SECRET_KEYS.mattermostBotToken,
      "xoxb-bot-token"
    );
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("receiver ⚠ (not running)")
    );
  });

  // ── Case 2: Happy path — receiver running and accepts signed request ────
  it("shows success info when webhook passes and receiver accepts signed request", async () => {
    setupHappyPathInputs([{ id: "town-square", token: "my-signing-token" }]);

    fetchMock.mockImplementation((url: string, opts: any) => {
      // HEAD → receiver is up
      if (opts?.method === "HEAD") return Promise.resolve({ ok: true, status: 200 });
      // POST (webhook test) → 200
      return Promise.resolve({ ok: true, status: 200 });
    });

    await commandHandler();

    expect(mockSecretService.setSecret).toHaveBeenCalledWith(
      mattermostSigningKey("town-square"),
      "my-signing-token"
    );
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("webhook ✓, receiver ✓")
    );
  });

  // ── Case 12: Receiver returns 403 (bad signing token) ──────────────────
  it("shows warning but still saves credentials when receiver rejects signing token", async () => {
    setupHappyPathInputs([{ id: "town-square", token: "bad-token" }]);

    fetchMock.mockImplementation((url: string, opts: any) => {
      if (opts?.method === "HEAD") return Promise.resolve({ ok: true, status: 200 });
      if (opts?.method === "POST" && url.includes("8765")) {
        return Promise.resolve({ ok: false, status: 403, statusText: "Forbidden" });
      }
      // Outbound webhook POST → 200
      return Promise.resolve({ ok: true, status: 200 });
    });

    await commandHandler();

    expect(mockSecretService.setSecret).toHaveBeenCalled();
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("signing")
    );
  });

  // ── Case 13: Multiple channels written correctly ─────────────────────────
  it("writes signing tokens for all collected channels", async () => {
    setupHappyPathInputs([
      { id: "general", token: "token-a" },
      { id: "alerts", token: "token-b" },
    ]);

    await commandHandler();

    expect(mockSecretService.setSecret).toHaveBeenCalledWith(
      mattermostSigningKey("general"),
      "token-a"
    );
    expect(mockSecretService.setSecret).toHaveBeenCalledWith(
      mattermostSigningKey("alerts"),
      "token-b"
    );
  });

  // ── Case 14: YAML round-trip preserves existing config ──────────────────
  it("merges notifications block with existing config when writing YAML", async () => {
    const { IncrediYamlService } = await import("../../src/views/settings/IncrediYamlService");
    const mockWrite = vi.fn().mockResolvedValue({ success: true });
    const mockRead = vi.fn().mockResolvedValue({
      config: { project: { number: 42 } },
      success: true,
    });
    (IncrediYamlService as any).mockImplementation(function () {
      return {
        read: mockRead,
        write: mockWrite,
        getPrimaryConfigPath: vi.fn().mockReturnValue("/mock/.nightgauge/config.yaml"),
        dispose: vi.fn(),
      };
    });

    setupHappyPathInputs([]);

    await commandHandler();

    expect(mockWrite).toHaveBeenCalledWith(
      expect.objectContaining({
        project: { number: 42 },
        notifications: expect.objectContaining({
          mattermost: expect.objectContaining({ enabled: true }),
        }),
      }),
      "project"
    );
  });

  // ── Registers the correct command ID ────────────────────────────────────
  it("registers command with ID nightgauge.configureMattermostWorkspace", () => {
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.configureMattermostWorkspace",
      expect.any(Function)
    );
  });
});
