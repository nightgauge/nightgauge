/**
 * Tests for Quickstart onboarding commands and repo-init detection.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as vscode from "vscode";
import * as fsp from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import {
  isRepoInitialized,
  refreshRepoInitializedContext,
  registerQuickstartCommands,
  maybeShowGettingStartedOnActivate,
  REPO_INIT_SKILL,
  SMART_SETUP_SKILL,
} from "../../src/commands/quickstart";
import { setPluginState } from "../../src/utils/pluginHint";
import type { Logger } from "../../src/utils/logger";
import { GettingStartedPanel } from "../../src/views/onboarding/GettingStartedPanel";

// GettingStartedPanel opens a real vscode.window.createWebviewPanel, which
// isn't part of the shared tests/setup.ts vscode mock. Stub it here so
// quickstart tests can assert *whether* the panel was asked to show without
// exercising webview creation — that's covered separately by
// tests/views/onboarding/GettingStartedPanel.test.ts.
vi.mock("../../src/views/onboarding/GettingStartedPanel", () => ({
  GettingStartedPanel: { show: vi.fn() },
}));

describe("quickstart helpers", () => {
  let tmpRoot: string;

  beforeEach(async () => {
    tmpRoot = await fsp.mkdtemp(path.join(os.tmpdir(), "nightgauge-quickstart-"));
    vi.clearAllMocks();
  });

  afterEach(async () => {
    await fsp.rm(tmpRoot, { recursive: true, force: true });
  });

  describe("isRepoInitialized", () => {
    it("returns false when .nightgauge/config.yaml does not exist", async () => {
      expect(await isRepoInitialized(tmpRoot)).toBe(false);
    });

    it("returns true when .nightgauge/config.yaml exists", async () => {
      const configDir = path.join(tmpRoot, ".nightgauge");
      await fsp.mkdir(configDir, { recursive: true });
      await fsp.writeFile(path.join(configDir, "config.yaml"), "version: 1\n");

      expect(await isRepoInitialized(tmpRoot)).toBe(true);
    });

    it("returns false when config.yaml is actually a directory", async () => {
      const configDir = path.join(tmpRoot, ".nightgauge", "config.yaml");
      await fsp.mkdir(configDir, { recursive: true });

      expect(await isRepoInitialized(tmpRoot)).toBe(false);
    });
  });

  describe("refreshRepoInitializedContext", () => {
    it("sets context to false when nightgaugeRoot is null", async () => {
      const result = await refreshRepoInitializedContext(null);
      expect(result).toBe(false);
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.repoInitialized",
        false
      );
    });

    it("sets context to true when config.yaml exists", async () => {
      const configDir = path.join(tmpRoot, ".nightgauge");
      await fsp.mkdir(configDir, { recursive: true });
      await fsp.writeFile(path.join(configDir, "config.yaml"), "");

      const result = await refreshRepoInitializedContext(tmpRoot);
      expect(result).toBe(true);
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.repoInitialized",
        true
      );
    });
  });

  describe("registerQuickstartCommands", () => {
    let logger: Logger;
    let context: vscode.ExtensionContext;

    beforeEach(() => {
      logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn() } as unknown as Logger;
      context = {
        subscriptions: [],
      } as unknown as vscode.ExtensionContext;

      // vscode mock in setup.ts doesn't include createTerminal or env.openExternal —
      // patch them here.
      (vscode.window as any).createTerminal = vi.fn(() => ({
        show: vi.fn(),
        sendText: vi.fn(),
      }));
      (vscode as any).env = {
        openExternal: vi.fn(() => Promise.resolve(true)),
      };
    });

    it("registers the five quickstart commands on the extension context", () => {
      registerQuickstartCommands(context, tmpRoot, logger);

      const registered = (vscode.commands.registerCommand as any).mock.calls.map(
        (c: unknown[]) => c[0]
      );
      expect(registered).toContain("nightgauge.quickstartRepoInit");
      expect(registered).toContain("nightgauge.quickstartSmartSetup");
      expect(registered).toContain("nightgauge.quickstartLearnMore");
      expect(registered).toContain("nightgauge.refreshRepoInitializedContext");
      expect(registered).toContain("nightgauge.showGettingStarted");
      expect(context.subscriptions.length).toBe(5);
    });

    it("opens the Getting Started panel when nightgauge.showGettingStarted fires", async () => {
      registerQuickstartCommands(context, tmpRoot, logger);

      const calls = (vscode.commands.registerCommand as any).mock.calls;
      const handler = calls.find(
        (c: unknown[]) => c[0] === "nightgauge.showGettingStarted"
      )![1] as () => Promise<void>;

      await handler();

      expect(GettingStartedPanel.show).toHaveBeenCalledTimes(1);
    });

    describe("Initialize Repository (#2760)", () => {
      const savedBinaryPath = process.env.NIGHTGAUGE_GO_BINARY_PATH;
      const savedPath = process.env.PATH;
      let argsFile: string;

      /** A binary outside PATH, standing in for the VSIX's bundled one. */
      async function bundleBinary(script: string): Promise<void> {
        const binary = path.join(tmpRoot, "bundled-nightgauge");
        await fsp.writeFile(binary, `#!/bin/sh\n${script}\n`, { mode: 0o755 });
        process.env.NIGHTGAUGE_GO_BINARY_PATH = binary;
      }

      const writesConfig = () =>
        bundleBinary(
          `echo "$@" > "${argsFile}"\nmkdir -p .nightgauge\necho "owner: acme" > .nightgauge/config.yaml`
        );

      async function fire(command: string): Promise<void> {
        registerQuickstartCommands(context, tmpRoot, logger);
        const calls = (vscode.commands.registerCommand as any).mock.calls;
        await (calls.find((c: unknown[]) => c[0] === command)![1] as () => Promise<void>)();
      }

      beforeEach(() => {
        argsFile = path.join(tmpRoot, "args");
        (vscode.workspace as any).workspaceFolders = [
          { uri: { fsPath: tmpRoot }, name: "test", index: 0 },
        ];
        setPluginState("absent");
        process.env.PATH = path.dirname(process.execPath) + ":/usr/bin:/bin";
      });

      afterEach(() => {
        (vscode.workspace as any).workspaceFolders = undefined;
        setPluginState("unknown");
        process.env.PATH = savedPath;
        if (savedBinaryPath === undefined) delete process.env.NIGHTGAUGE_GO_BINARY_PATH;
        else process.env.NIGHTGAUGE_GO_BINARY_PATH = savedBinaryPath;
      });

      it("writes the config through the bundled binary with no plugin and no terminal", async () => {
        await writesConfig();

        await fire("nightgauge.quickstartRepoInit");

        expect((await fsp.readFile(argsFile, "utf8")).trim()).toBe("config init --detect --json");
        expect(await isRepoInitialized(tmpRoot)).toBe(true);
        expect(vscode.commands.executeCommand).toHaveBeenLastCalledWith(
          "setContext",
          "nightgauge.repoInitialized",
          true
        );
        expect(vscode.window.createTerminal).not.toHaveBeenCalled();
        expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
          expect.stringContaining("created .nightgauge/config.yaml")
        );
      });

      it("offers board linking through repo-init only when the plugin is enabled", async () => {
        await writesConfig();
        setPluginState("enabled");
        (vscode.window.showInformationMessage as any).mockResolvedValueOnce("Link Project Board");

        await fire("nightgauge.quickstartRepoInit");

        const terminal = (vscode.window.createTerminal as any).mock.results[0].value;
        expect(terminal.sendText).toHaveBeenCalledWith(`claude ${REPO_INIT_SKILL}`, true);
      });

      it("passes the binary's environment to the terminal it opens", async () => {
        await writesConfig();
        setPluginState("enabled");
        (vscode.window.showInformationMessage as any).mockResolvedValueOnce("Link Project Board");

        await fire("nightgauge.quickstartRepoInit");

        const options = (vscode.window.createTerminal as any).mock.calls[0][0];
        expect(options.env.NIGHTGAUGE_BIN).toBe(process.env.NIGHTGAUGE_GO_BINARY_PATH);
        expect(options.env.PATH.split(":")[0]).toBe(tmpRoot);
      });

      it("shows the binary's own error and leaves the repository uninitialized", async () => {
        await bundleBinary(`echo "Error: --detect needs a GitHub origin remote" >&2\nexit 1`);

        await fire("nightgauge.quickstartRepoInit");

        expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
          expect.stringContaining("--detect needs a GitHub origin remote")
        );
        expect(await isRepoInitialized(tmpRoot)).toBe(false);
      });

      it("treats an existing config as success without running the binary", async () => {
        await writesConfig();
        await fsp.mkdir(path.join(tmpRoot, ".nightgauge"), { recursive: true });
        await fsp.writeFile(path.join(tmpRoot, ".nightgauge", "config.yaml"), "owner: mine\n");

        await fire("nightgauge.quickstartRepoInit");

        await expect(fsp.stat(argsFile)).rejects.toThrow();
        expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
      });

      it("runs plugin setup, not a dead terminal, for AI-Ready without the plugin", async () => {
        await fire("nightgauge.quickstartSmartSetup");

        expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.setupPlugins");
        expect(vscode.window.createTerminal).not.toHaveBeenCalled();
      });

      it("opens smart-setup in a terminal when the plugin is enabled", async () => {
        setPluginState("enabled");

        await fire("nightgauge.quickstartSmartSetup");

        const terminal = (vscode.window.createTerminal as any).mock.results[0].value;
        expect(terminal.sendText).toHaveBeenCalledWith("claude /nightgauge:smart-setup", true);
      });
    });

    it("names only skills the plugin ships", async () => {
      const skillsDir = path.resolve(__dirname, "../../../../claude-plugins/nightgauge/skills");
      const shipped = await fsp.readdir(skillsDir);

      for (const skill of [REPO_INIT_SKILL, SMART_SETUP_SKILL]) {
        expect(skill).toMatch(/^\/nightgauge:/);
        expect(shipped).toContain(skill.replace("/nightgauge:", ""));
      }
    });

    it("warns the user when no workspace folder is open", async () => {
      (vscode.workspace as any).workspaceFolders = undefined;

      registerQuickstartCommands(context, null, logger);

      const calls = (vscode.commands.registerCommand as any).mock.calls;
      const handler = calls.find(
        (c: unknown[]) => c[0] === "nightgauge.quickstartSmartSetup"
      )![1] as () => Promise<void>;

      await handler();

      expect(vscode.window.showWarningMessage).toHaveBeenCalled();
      expect(vscode.window.createTerminal).not.toHaveBeenCalled();
    });

    it("opens the docs URL externally for the learnMore command", async () => {
      registerQuickstartCommands(context, tmpRoot, logger);

      const calls = (vscode.commands.registerCommand as any).mock.calls;
      const handler = calls.find(
        (c: unknown[]) => c[0] === "nightgauge.quickstartLearnMore"
      )![1] as () => Promise<void>;

      await handler();

      expect(vscode.env.openExternal).toHaveBeenCalled();
    });
  });

  describe("maybeShowGettingStartedOnActivate", () => {
    let logger: Logger;

    function buildContext(alreadyShown: boolean) {
      return {
        globalState: {
          get: vi.fn().mockReturnValue(alreadyShown),
          update: vi.fn(),
        },
      } as unknown as vscode.ExtensionContext;
    }

    beforeEach(() => {
      logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn() } as unknown as Logger;
    });

    it("shows the panel and marks it shown when the repo is uninitialized and never shown before", async () => {
      const context = buildContext(false);

      await maybeShowGettingStartedOnActivate(context, false, logger);

      expect(context.globalState.get).toHaveBeenCalledWith(
        "nightgauge.gettingStarted.shown",
        false
      );
      expect(context.globalState.update).toHaveBeenCalledWith(
        "nightgauge.gettingStarted.shown",
        true
      );
      expect(GettingStartedPanel.show).toHaveBeenCalledTimes(1);
    });

    it("does nothing when the repo is already initialized", async () => {
      const context = buildContext(false);

      await maybeShowGettingStartedOnActivate(context, true, logger);

      expect(context.globalState.update).not.toHaveBeenCalled();
      expect(GettingStartedPanel.show).not.toHaveBeenCalled();
    });

    it("does nothing when the panel has already auto-shown once", async () => {
      const context = buildContext(true);

      await maybeShowGettingStartedOnActivate(context, false, logger);

      expect(context.globalState.update).not.toHaveBeenCalled();
      expect(GettingStartedPanel.show).not.toHaveBeenCalled();
    });
  });
});
