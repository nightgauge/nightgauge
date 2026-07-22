/**
 * Select Target Branch command
 *
 * Shows a Quick Pick dialog for selecting the target branch for PR creation.
 * The selected branch is stored in PipelineStateService and displayed in the status bar.
 *
 * @see Issue #101 - Add target branch selection UI
 * @see Issue #433 - config.yaml (formerly nightgauge.yaml)
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger";
import type { PipelineStateService } from "../services/PipelineStateService";
import {
  listRemoteBranches,
  getSortedBranches,
  filterTargetBranches,
  getBranchLabel,
  getBranchDescription,
  isValidBranchName,
  type BranchInfo,
} from "../utils/branchUtils";
import { resolveConfigPath, logDeprecationWarning } from "../utils/configPathResolver";

/**
 * Read branch suggestions from nightgauge config file
 */
async function getBranchSuggestions(cwd: string): Promise<string[]> {
  try {
    // Resolve config path with fallback to legacy
    const pathResult = await resolveConfigPath(cwd);
    if (!pathResult.exists) {
      return [];
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const incrediFile = vscode.Uri.file(pathResult.path);
    const content = await vscode.workspace.fs.readFile(incrediFile);
    const text = new TextDecoder().decode(content);

    // Simple YAML parsing for branch.suggestions array
    // Note: This is a basic parser; could use a proper YAML library if needed
    const suggestionsMatch = text.match(
      /branch:\s*\n(?:.*\n)*?\s*suggestions:\s*\n((?:\s*-\s*.+\n?)+)/
    );

    if (suggestionsMatch) {
      const suggestionsBlock = suggestionsMatch[1];
      return suggestionsBlock
        .split("\n")
        .map((line) => line.replace(/^\s*-\s*/, "").trim())
        .filter((line) => line && !line.startsWith("#"));
    }
  } catch {
    // No nightgauge config or no suggestions section
  }

  return [];
}

/**
 * Read protected branches from nightgauge config file
 */
export async function getProtectedBranches(cwd: string): Promise<string[]> {
  try {
    // Resolve config path with fallback to legacy
    const pathResult = await resolveConfigPath(cwd);
    if (!pathResult.exists) {
      return [];
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const incrediFile = vscode.Uri.file(pathResult.path);
    const content = await vscode.workspace.fs.readFile(incrediFile);
    const text = new TextDecoder().decode(content);

    // Simple YAML parsing for branch.protected array
    const protectedMatch = text.match(
      /branch:\s*\n(?:.*\n)*?\s*protected:\s*\n((?:\s*-\s*.+\n?)+)/
    );

    if (protectedMatch) {
      const protectedBlock = protectedMatch[1];
      return protectedBlock
        .split("\n")
        .map((line) => line.replace(/^\s*-\s*/, "").trim())
        .filter((line) => line && !line.startsWith("#"));
    }
  } catch {
    // No nightgauge config or no protected section
  }

  return [];
}

/**
 * Read default base branch from nightgauge config file
 */
async function getDefaultBaseBranch(cwd: string): Promise<string> {
  try {
    // Resolve config path with fallback to legacy
    const pathResult = await resolveConfigPath(cwd);
    if (!pathResult.exists) {
      return "main";
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const incrediFile = vscode.Uri.file(pathResult.path);
    const content = await vscode.workspace.fs.readFile(incrediFile);
    const text = new TextDecoder().decode(content);

    // Look for branch.base setting
    const baseMatch = text.match(/branch:\s*\n(?:.*\n)*?\s*base:\s*(\S+)/);
    if (baseMatch) {
      return baseMatch[1].trim();
    }
  } catch {
    // No nightgauge config
  }

  return "main";
}

/**
 * Show the target branch selection Quick Pick
 *
 * @param pipelineStateService - Service to store selection
 * @param logger - Logger instance
 * @param workspaceRoot - Workspace root path
 * @returns Selected branch name or undefined if cancelled
 */
export async function showBranchPicker(
  pipelineStateService: PipelineStateService,
  logger: Logger,
  workspaceRoot: string
): Promise<string | undefined> {
  logger.debug("Opening branch selection picker");

  // Get current selection
  const currentBranch = await pipelineStateService.getBaseBranch();
  const defaultBranch = await getDefaultBaseBranch(workspaceRoot);

  // Show loading indicator while fetching branches
  return vscode.window.withProgress(
    {
      location: vscode.ProgressLocation.Notification,
      title: "Loading branches...",
      cancellable: false,
    },
    async () => {
      // Fetch remote branches and protected branches config
      const remoteBranches = await listRemoteBranches(workspaceRoot);
      const configSuggestions = await getBranchSuggestions(workspaceRoot);
      const protectedBranches = await getProtectedBranches(workspaceRoot);

      // Process branches
      const sortedBranches = getSortedBranches(remoteBranches, configSuggestions);
      const filteredBranches = filterTargetBranches(sortedBranches);

      // Create Quick Pick items
      const items: vscode.QuickPickItem[] = filteredBranches.map(
        (branch) =>
          ({
            label: getBranchLabel(branch, currentBranch ?? defaultBranch, protectedBranches),
            description: getBranchDescription(branch, protectedBranches),
            detail: branch.name === currentBranch ? "Currently selected" : undefined,
            picked: branch.name === (currentBranch ?? defaultBranch),
            // Store the actual branch name for retrieval
            branch: branch.name,
          }) as vscode.QuickPickItem & { branch: string }
      );

      // Add "Enter custom branch" option
      items.push({
        label: "$(edit) Enter custom branch name...",
        description: "Type a branch name manually",
        alwaysShow: true,
      } as vscode.QuickPickItem);

      // Show Quick Pick
      const selected = await vscode.window.showQuickPick(items, {
        title: "Select Target Branch",
        placeHolder: `Current: ${currentBranch ?? defaultBranch}`,
        matchOnDescription: true,
      });

      if (!selected) {
        logger.debug("Branch selection cancelled");
        return undefined;
      }

      // Handle custom branch entry
      if (selected.label.includes("Enter custom branch")) {
        const customBranch = await vscode.window.showInputBox({
          prompt: "Enter target branch name",
          placeHolder: "e.g., release/v2.0, epic/auth-system",
          value: currentBranch ?? defaultBranch,
          validateInput: (value) => {
            if (!value) {
              return "Branch name is required";
            }
            if (!isValidBranchName(value)) {
              return "Invalid branch name";
            }
            return null;
          },
        });

        if (!customBranch) {
          return undefined;
        }

        // Store selection
        await pipelineStateService.setBaseBranch(customBranch);
        logger.info("Target branch set (custom)", { branch: customBranch });
        return customBranch;
      }

      // Get selected branch name
      const selectedBranch = (selected as vscode.QuickPickItem & { branch?: string }).branch;

      if (!selectedBranch) {
        return undefined;
      }

      // Store selection
      await pipelineStateService.setBaseBranch(selectedBranch);
      logger.info("Target branch set", { branch: selectedBranch });

      return selectedBranch;
    }
  );
}

/**
 * Register the Select Target Branch command
 *
 * This command can be invoked from:
 * 1. Status bar click
 * 2. Command palette
 * 3. Before issue pickup (if no branch set)
 */
export function registerSelectTargetBranchCommand(
  logger: Logger,
  pipelineStateService: PipelineStateService
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.selectTargetBranch", async () => {
    const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;

    if (!workspaceRoot) {
      vscode.window.showWarningMessage("No workspace folder open. Please open a repository first.");
      return;
    }

    try {
      const selected = await showBranchPicker(pipelineStateService, logger, workspaceRoot);

      if (selected) {
        vscode.window.showInformationMessage(`Target branch set to: ${selected}`);
      }
    } catch (error) {
      logger.error("Failed to select target branch", { error });
      vscode.window.showErrorMessage(
        `Failed to select target branch: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  });
}
