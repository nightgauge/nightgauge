/**
 * Tests for the configureForgeInstance command wizard
 *
 * Covers:
 * - Happy-path wizard: full PAT flow → SecretStorage + config write
 * - CA validation failure: invalid PEM file → error shown, save blocked
 * - Connection-test failure: IPC error → user sees error, no forced save
 * - Secret rotation: re-running for existing ID updates secret
 * - Default-forge switch: forge-action:set-default writes to YAML (SettingsPanel)
 *
 * @see Issue #3364 - VSCode extension settings UI for managing forge instances
 */

import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import * as vscode from "vscode";

// ── Hoisted mock factories ────────────────────────────────────────────────────

const {
  mockRegisterCommand,
  mockShowInputBox,
  mockShowQuickPick,
  mockShowOpenDialog,
  mockShowInformationMessage,
  mockShowWarningMessage,
  mockShowErrorMessage,
  mockWithProgress,
  mockOpenExternal,
} = vi.hoisted(() => ({
  mockRegisterCommand: vi.fn(),
  mockShowInputBox: vi.fn(),
  mockShowQuickPick: vi.fn(),
  mockShowOpenDialog: vi.fn(),
  mockShowInformationMessage: vi.fn(),
  mockShowWarningMessage: vi.fn(),
  mockShowErrorMessage: vi.fn(),
  mockWithProgress: vi.fn(),
  mockOpenExternal: vi.fn(),
}));

// ── Module mocks ──────────────────────────────────────────────────────────────

vi.mock("vscode", () => ({
  commands: { registerCommand: mockRegisterCommand },
  window: {
    showInputBox: mockShowInputBox,
    showQuickPick: mockShowQuickPick,
    showOpenDialog: mockShowOpenDialog,
    showInformationMessage: mockShowInformationMessage,
    showWarningMessage: mockShowWarningMessage,
    showErrorMessage: mockShowErrorMessage,
    withProgress: mockWithProgress,
    createOutputChannel: vi.fn(() => ({ appendLine: vi.fn(), show: vi.fn(), dispose: vi.fn() })),
  },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }],
  },
  env: { openExternal: mockOpenExternal },
  Uri: { parse: vi.fn((s: string) => ({ toString: () => s })) },
  ProgressLocation: { Notification: 15 },
}));

const mockSecretSvc = {
  getForgeSecret: vi.fn(),
  setForgeSecret: vi.fn(),
  deleteForgeSecret: vi.fn(),
  setForgeLastTested: vi.fn(),
  getForgeLastTested: vi.fn(),
};

vi.mock("../../src/services/SecretStorageService", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../src/services/SecretStorageService")>();
  return {
    ...actual,
    SecretStorageService: {
      getInstance: vi.fn(() => mockSecretSvc),
      initialize: vi.fn(),
      resetInstance: vi.fn(),
    },
  };
});

const mockIpcInstance = {
  forgeConnectionTest: vi.fn(),
  authDeviceFlowStart: vi.fn(),
};

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: vi.fn(() => mockIpcInstance),
  },
}));

const mockYamlInstance = {
  read: vi.fn().mockResolvedValue({ config: {}, success: true }),
  write: vi.fn().mockResolvedValue({ success: true }),
  dispose: vi.fn(),
};

vi.mock("../../src/views/settings/IncrediYamlService", () => ({
  IncrediYamlService: vi.fn(function () {
    return mockYamlInstance;
  }),
}));

vi.mock("../../src/utils/pemValidator", () => ({
  validatePemFile: vi.fn().mockResolvedValue(null),
}));

// ── Helpers ───────────────────────────────────────────────────────────────────

import { registerConfigureForgeInstanceCommand } from "../../src/commands/configureForgeInstance";
import { validatePemFile } from "../../src/utils/pemValidator";

function getLastCommandHandler(): (...args: unknown[]) => Promise<void> {
  const calls = (mockRegisterCommand as ReturnType<typeof vi.fn>).mock.calls;
  return calls[calls.length - 1][1];
}

/** Set up the happy-path multi-step inputs for a PAT GitHub forge. */
function setupHappyPathInputs(instanceId = "test-forge") {
  // Step 1: instance ID
  mockShowInputBox.mockResolvedValueOnce(instanceId);
  // Step 2: URL (blank = github.com default)
  mockShowInputBox.mockResolvedValueOnce("");
  // Steps 3/4: kind + auth method pickers
  mockShowQuickPick.mockResolvedValueOnce({ label: "GitHub", value: "github" });
  mockShowQuickPick.mockResolvedValueOnce({ label: "PAT", value: "pat" });
  // Step 5: PAT token
  mockShowInputBox.mockResolvedValueOnce("ghp_test_token");
  // Step 6: CA bundle — skip
  mockShowQuickPick.mockResolvedValueOnce({ label: "No CA bundle needed", value: "skip" });
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("registerConfigureForgeInstanceCommand", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockSecretSvc.setForgeSecret.mockResolvedValue(undefined);
    mockSecretSvc.setForgeLastTested.mockResolvedValue(undefined);
    mockSecretSvc.getForgeSecret.mockResolvedValue(undefined);
    mockYamlInstance.read.mockResolvedValue({ config: {}, success: true });
    mockYamlInstance.write.mockResolvedValue({ success: true });
    mockIpcInstance.forgeConnectionTest.mockResolvedValue({
      ok: true,
      latency_ms: 45,
    });
    // withProgress executes the callback immediately
    mockWithProgress.mockImplementation(
      (_opts: unknown, cb: (progress: unknown) => Promise<unknown>) => cb({})
    );
  });

  it("registers the nightgauge.configureForgeInstance command", () => {
    registerConfigureForgeInstanceCommand();
    expect(mockRegisterCommand).toHaveBeenCalledWith(
      "nightgauge.configureForgeInstance",
      expect.any(Function)
    );
  });

  describe("happy path — PAT GitHub forge", () => {
    it("saves credential to SecretStorage and writes config.yaml", async () => {
      registerConfigureForgeInstanceCommand();
      setupHappyPathInputs("my-github");
      const handler = getLastCommandHandler();
      await handler();

      // SecretStorage updated
      expect(mockSecretSvc.setForgeSecret).toHaveBeenCalledWith("my-github", "ghp_test_token");
      expect(mockSecretSvc.setForgeLastTested).toHaveBeenCalledWith(
        "my-github",
        expect.stringMatching(/^\d{4}-\d{2}-\d{2}/)
      );

      // config.yaml written with forge metadata
      expect(mockYamlInstance.write).toHaveBeenCalled();
      const writtenCfg = mockYamlInstance.write.mock.calls.at(-1)![0];
      expect(writtenCfg.forges?.["my-github"]).toMatchObject({
        kind: "github",
        auth_method: "pat",
      });

      expect(mockShowInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("added successfully")
      );
    });

    it("shows success notification with latency", async () => {
      registerConfigureForgeInstanceCommand();
      setupHappyPathInputs("fast-forge");
      const handler = getLastCommandHandler();
      await handler();

      expect(mockShowInformationMessage).toHaveBeenCalledWith(expect.stringContaining("45ms"));
    });
  });

  describe("CA validation failure", () => {
    it("shows error and does not save when PEM file is invalid", async () => {
      vi.mocked(validatePemFile).mockResolvedValueOnce(
        "File does not appear to be a PEM certificate bundle"
      );

      registerConfigureForgeInstanceCommand();
      // Step 1-5 same as happy path
      mockShowInputBox.mockResolvedValueOnce("secure-forge");
      mockShowInputBox.mockResolvedValueOnce("https://gitlab.example.com");
      mockShowQuickPick.mockResolvedValueOnce({ label: "GitLab", value: "gitlab" });
      mockShowQuickPick.mockResolvedValueOnce({ label: "PAT", value: "pat" });
      mockShowInputBox.mockResolvedValueOnce("glpat-token");
      // Step 6: choose to pick a file
      mockShowQuickPick.mockResolvedValueOnce({ label: "Select CA bundle file…", value: "pick" });
      mockShowOpenDialog.mockResolvedValueOnce([{ fsPath: "/path/to/bad.pem" }]);

      const handler = getLastCommandHandler();
      await handler();

      expect(mockShowErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Invalid CA bundle")
      );
      // Should not reach save step
      expect(mockSecretSvc.setForgeSecret).not.toHaveBeenCalled();
      expect(mockYamlInstance.write).not.toHaveBeenCalled();
    });
  });

  describe("connection test failure", () => {
    it("shows warning and proceeds only if user confirms Save Anyway", async () => {
      mockIpcInstance.forgeConnectionTest.mockResolvedValue({
        ok: false,
        latency_ms: 200,
        error: "authentication failed (HTTP 401)",
      });
      mockShowWarningMessage.mockResolvedValueOnce("Save Anyway");

      registerConfigureForgeInstanceCommand();
      setupHappyPathInputs("failing-forge");
      const handler = getLastCommandHandler();
      await handler();

      expect(mockShowWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("Connection test failed"),
        "Save Anyway",
        "Cancel"
      );
      // User chose "Save Anyway" so secret should be stored
      expect(mockSecretSvc.setForgeSecret).toHaveBeenCalledWith("failing-forge", "ghp_test_token");
    });

    it("does not save when user cancels on connection test failure", async () => {
      mockIpcInstance.forgeConnectionTest.mockResolvedValue({
        ok: false,
        latency_ms: 200,
        error: "authentication failed (HTTP 401)",
      });
      mockShowWarningMessage.mockResolvedValueOnce(undefined); // dismissed

      registerConfigureForgeInstanceCommand();
      setupHappyPathInputs("cancelled-forge");
      const handler = getLastCommandHandler();
      await handler();

      expect(mockSecretSvc.setForgeSecret).not.toHaveBeenCalled();
    });
  });

  describe("secret rotation — editing existing instance", () => {
    it("updates the secret for an existing forge ID when called with instanceId arg", async () => {
      registerConfigureForgeInstanceCommand();
      // When editing, step 1 (ID input) is skipped; handler receives instanceId arg
      mockShowInputBox.mockResolvedValueOnce(""); // URL
      mockShowQuickPick.mockResolvedValueOnce({ label: "GitHub", value: "github" });
      mockShowQuickPick.mockResolvedValueOnce({ label: "PAT", value: "pat" });
      mockShowInputBox.mockResolvedValueOnce("ghp_new_rotated_token");
      mockShowQuickPick.mockResolvedValueOnce({ label: "No CA bundle needed", value: "skip" });

      const handler = getLastCommandHandler();
      await handler("existing-forge"); // pre-filled instanceId

      expect(mockSecretSvc.setForgeSecret).toHaveBeenCalledWith(
        "existing-forge",
        "ghp_new_rotated_token"
      );
      expect(mockShowInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("updated successfully")
      );
    });
  });

  describe("CI job token — no credential needed", () => {
    it("shows info message and skips credential storage for ci_job_token", async () => {
      registerConfigureForgeInstanceCommand();
      mockShowInputBox.mockResolvedValueOnce("ci-forge");
      mockShowInputBox.mockResolvedValueOnce("https://gitlab.corp.com");
      mockShowQuickPick.mockResolvedValueOnce({ label: "GitLab", value: "gitlab" });
      mockShowQuickPick.mockResolvedValueOnce({ label: "CI Job Token", value: "ci_job_token" });
      mockShowQuickPick.mockResolvedValueOnce({ label: "No CA bundle needed", value: "skip" });

      const handler = getLastCommandHandler();
      await handler();

      // No credential stored — ci_job_token uses environment variable
      expect(mockSecretSvc.setForgeSecret).not.toHaveBeenCalled();
      expect(mockShowInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("CI_JOB_TOKEN")
      );
    });
  });

  describe("dismissal at each step", () => {
    it("returns early when instance ID input is dismissed", async () => {
      registerConfigureForgeInstanceCommand();
      mockShowInputBox.mockResolvedValueOnce(undefined);
      const handler = getLastCommandHandler();
      await handler();
      expect(mockSecretSvc.setForgeSecret).not.toHaveBeenCalled();
    });

    it("returns early when kind picker is dismissed", async () => {
      registerConfigureForgeInstanceCommand();
      mockShowInputBox.mockResolvedValueOnce("my-forge");
      mockShowInputBox.mockResolvedValueOnce("");
      mockShowQuickPick.mockResolvedValueOnce(undefined);
      const handler = getLastCommandHandler();
      await handler();
      expect(mockSecretSvc.setForgeSecret).not.toHaveBeenCalled();
    });
  });

  describe("error handling — no workspace", () => {
    it("shows error when no workspace folder is open", async () => {
      vi.mocked(vscode.workspace).workspaceFolders =
        undefined as unknown as (typeof vscode.workspace)["workspaceFolders"];

      registerConfigureForgeInstanceCommand();
      const handler = getLastCommandHandler();
      await handler();

      expect(mockShowErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("No workspace folder open")
      );

      // Restore
      vi.mocked(vscode.workspace).workspaceFolders = [
        { uri: { fsPath: "/mock/workspace" } },
      ] as unknown as (typeof vscode.workspace)["workspaceFolders"];
    });
  });
});
