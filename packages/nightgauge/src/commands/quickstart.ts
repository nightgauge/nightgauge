/**
 * Quickstart onboarding commands
 *
 * Surfaces a friendly first-run experience for repositories that have not yet
 * been initialized for the Nightgauge pipeline. These commands are wired
 * into the `nightgauge.pipelineView` welcome content (see package.json
 * `contributes.viewsWelcome`) so a user opening the extension in an unseen
 * repo sees action buttons instead of a bare empty state — and the extension
 * does not auto-scaffold `.nightgauge/` on their disk until they opt in.
 */

import { execFile } from "child_process";
import { promisify } from "util";
import * as vscode from "vscode";
import { BinaryResolver } from "../services/BinaryResolver";
import { resolveBinaryDiscoveryEnv } from "../utils/binaryDiscoveryEnv";
import type { Logger } from "../utils/logger";
import { isPluginEnabled } from "../utils/pluginHint";
import {
  GettingStartedPanel,
  type GettingStartedAction,
} from "../views/onboarding/GettingStartedPanel.js";
import { shouldAutoShowGettingStarted } from "../views/onboarding/onboardingGate.js";

export const REPO_INIT_SKILL = "/nightgauge:repo-init";
export const SMART_SETUP_SKILL = "/nightgauge:smart-setup";
const DOCS_URL = "https://github.com/nightgauge/nightgauge#readme";

/** globalState key tracking whether the Getting Started panel has already
 * auto-shown once for this VSCode installation (Issue #4155). */
const GETTING_STARTED_SHOWN_KEY = "nightgauge.gettingStarted.shown";

// Imported (not just re-exported) because `refreshRepoInitializedContext`
// below calls it: `export … from` creates no local binding.
import { isRepoInitialized } from "../utils/repoInitialized";
export { isRepoInitialized };

/**
 * Updates the `nightgauge.repoInitialized` VSCode context key so welcome
 * views and menus can react. Safe to call repeatedly.
 */
export async function refreshRepoInitializedContext(
  nightgaugeRoot: string | null,
  logger?: Logger
): Promise<boolean> {
  const initialized = nightgaugeRoot ? await isRepoInitialized(nightgaugeRoot) : false;
  await vscode.commands.executeCommand("setContext", "nightgauge.repoInitialized", initialized);
  logger?.info("Refreshed repoInitialized context", { nightgaugeRoot, initialized });
  return initialized;
}

function openSkillInTerminal(skill: string, cwd: string | undefined, logger: Logger): void {
  const terminal = vscode.window.createTerminal({
    name: `Nightgauge: ${skill}`,
    cwd,
    env: resolveBinaryDiscoveryEnv(),
  });
  terminal.show();
  terminal.sendText(`claude ${skill}`, true);
  logger.info("Opened skill in terminal", { skill, cwd });
}

/**
 * Writes `.nightgauge/config.yaml` with the bundled binary, so onboarding needs
 * neither the Claude plugin nor `nightgauge` on PATH (#2760, #1154). The config
 * has no board; linking one stays in the `repo-init` skill.
 */
async function initializeRepository(cwd: string, logger: Logger): Promise<void> {
  if (await refreshRepoInitializedContext(cwd, logger)) {
    vscode.window.showInformationMessage("Nightgauge: this repository is already initialized.");
    return;
  }

  const binary = await BinaryResolver.fromVSCode().resolve();
  if (!binary) {
    vscode.window.showErrorMessage(
      "Nightgauge: the bundled binary was not found. Reinstall the extension, " +
        "or set nightgauge.backend.binaryPath."
    );
    return;
  }

  try {
    await promisify(execFile)(binary, ["config", "init", "--detect", "--json"], { cwd });
  } catch (error) {
    const { stderr, message } = error as { stderr?: string; message?: string };
    const reason = stderr?.trim() || message || String(error);
    logger.warn("Repository initialization failed", { cwd, reason });
    vscode.window.showErrorMessage(`Nightgauge: could not initialize this repository. ${reason}`);
    return;
  }

  await refreshRepoInitializedContext(cwd, logger);
  logger.info("Initialized repository", { cwd });

  const created = "Nightgauge: created .nightgauge/config.yaml. No GitHub Project is linked yet.";
  if (!isPluginEnabled()) {
    vscode.window.showInformationMessage(created);
    return;
  }
  const linkBoard = "Link Project Board";
  if ((await vscode.window.showInformationMessage(created, linkBoard)) === linkBoard) {
    openSkillInTerminal(REPO_INIT_SKILL, cwd, logger);
  }
}

function resolveWorkspaceCwd(): string | undefined {
  const folders = vscode.workspace.workspaceFolders;
  if (!folders || folders.length === 0) return undefined;
  return folders[0].uri.fsPath;
}

/**
 * Dispatches a button click from the Getting Started webview to the real
 * command it represents, so the panel has no direct knowledge of terminal
 * spawning or skill names — it only knows the three abstract steps.
 */
function dispatchGettingStartedAction(action: GettingStartedAction): void {
  switch (action) {
    case "init":
      void vscode.commands.executeCommand("nightgauge.quickstartRepoInit");
      break;
    case "pickup":
      void vscode.commands.executeCommand("nightgauge.pickupIssue");
      break;
    case "docs":
      void vscode.env.openExternal(vscode.Uri.parse(DOCS_URL));
      break;
  }
}

/** Opens (or reveals) the Getting Started onboarding panel. */
export function showGettingStartedPanel(): void {
  GettingStartedPanel.show(dispatchGettingStartedAction);
}

/**
 * Auto-opens the Getting Started panel exactly once per VSCode installation,
 * the first time a workspace has not yet been Nightgauge-initialized.
 * Called from `bootstrap/services.ts` right after `repoInitialized` is
 * resolved, so it reuses that signal instead of re-deriving it — see
 * `onboardingGate.ts` for the pure decision logic this wraps.
 */
export async function maybeShowGettingStartedOnActivate(
  context: vscode.ExtensionContext,
  repoInitialized: boolean,
  logger?: Logger
): Promise<void> {
  const alreadyShown = context.globalState.get<boolean>(GETTING_STARTED_SHOWN_KEY, false);
  if (!shouldAutoShowGettingStarted({ repoInitialized, alreadyShown })) {
    return;
  }
  await context.globalState.update(GETTING_STARTED_SHOWN_KEY, true);
  logger?.info("Auto-showing Getting Started onboarding panel (first run)");
  showGettingStartedPanel();
}

export function registerQuickstartCommands(
  context: vscode.ExtensionContext,
  nightgaugeRoot: string | null,
  logger: Logger
): void {
  const repoInit = vscode.commands.registerCommand("nightgauge.quickstartRepoInit", async () => {
    const cwd = resolveWorkspaceCwd();
    if (!cwd) {
      vscode.window.showWarningMessage(
        "Nightgauge: Open a folder or repository first, then try again."
      );
      return;
    }
    await initializeRepository(cwd, logger);
  });

  const smartSetup = vscode.commands.registerCommand(
    "nightgauge.quickstartSmartSetup",
    async () => {
      const cwd = resolveWorkspaceCwd();
      if (!cwd) {
        vscode.window.showWarningMessage(
          "Nightgauge: Open a folder or repository first, then try again."
        );
        return;
      }
      if (!isPluginEnabled()) {
        await vscode.commands.executeCommand("nightgauge.setupPlugins");
        return;
      }
      openSkillInTerminal(SMART_SETUP_SKILL, cwd, logger);
    }
  );

  const learnMore = vscode.commands.registerCommand("nightgauge.quickstartLearnMore", async () => {
    await vscode.env.openExternal(vscode.Uri.parse(DOCS_URL));
  });

  const refreshContext = vscode.commands.registerCommand(
    "nightgauge.refreshRepoInitializedContext",
    async () => {
      await refreshRepoInitializedContext(nightgaugeRoot, logger);
    }
  );

  const showGettingStarted = vscode.commands.registerCommand(
    "nightgauge.showGettingStarted",
    async () => {
      showGettingStartedPanel();
    }
  );

  context.subscriptions.push(repoInit, smartSetup, learnMore, refreshContext, showGettingStarted);
}
