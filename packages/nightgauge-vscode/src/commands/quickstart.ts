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

import * as vscode from "vscode";
import * as path from "node:path";
import * as fs from "node:fs/promises";
import type { Logger } from "../utils/logger";
import {
  GettingStartedPanel,
  type GettingStartedAction,
} from "../views/onboarding/GettingStartedPanel.js";
import { shouldAutoShowGettingStarted } from "../views/onboarding/onboardingGate.js";

const REPO_INIT_SKILL = "/nightgauge:repo-init";
const SMART_SETUP_SKILL = "/smart-setup";
const DOCS_URL = "https://github.com/nightgauge/nightgauge#readme";

/** globalState key tracking whether the Getting Started panel has already
 * auto-shown once for this VSCode installation (Issue #4155). */
const GETTING_STARTED_SHOWN_KEY = "nightgauge.gettingStarted.shown";

/**
 * Checks whether `.nightgauge/config.yaml` exists at `incrediRoot`.
 * This is the canonical signal that `/nightgauge:repo-init` has run.
 */
export async function isRepoInitialized(incrediRoot: string): Promise<boolean> {
  const configPath = path.join(incrediRoot, ".nightgauge", "config.yaml");
  try {
    const stat = await fs.stat(configPath);
    return stat.isFile();
  } catch {
    return false;
  }
}

/**
 * Updates the `nightgauge.repoInitialized` VSCode context key so welcome
 * views and menus can react. Safe to call repeatedly.
 */
export async function refreshRepoInitializedContext(
  incrediRoot: string | null,
  logger?: Logger
): Promise<boolean> {
  const initialized = incrediRoot ? await isRepoInitialized(incrediRoot) : false;
  await vscode.commands.executeCommand("setContext", "nightgauge.repoInitialized", initialized);
  logger?.info("Refreshed repoInitialized context", { incrediRoot, initialized });
  return initialized;
}

function openSkillInTerminal(skill: string, cwd: string | undefined, logger: Logger): void {
  const terminal = vscode.window.createTerminal({
    name: `Nightgauge: ${skill}`,
    cwd,
  });
  terminal.show();
  terminal.sendText(`claude ${skill}`, true);
  logger.info("Opened skill in terminal", { skill, cwd });
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
  incrediRoot: string | null,
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
    openSkillInTerminal(REPO_INIT_SKILL, cwd, logger);
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
      openSkillInTerminal(SMART_SETUP_SKILL, cwd, logger);
    }
  );

  const learnMore = vscode.commands.registerCommand("nightgauge.quickstartLearnMore", async () => {
    await vscode.env.openExternal(vscode.Uri.parse(DOCS_URL));
  });

  const refreshContext = vscode.commands.registerCommand(
    "nightgauge.refreshRepoInitializedContext",
    async () => {
      await refreshRepoInitializedContext(incrediRoot, logger);
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
