/**
 * Check Epic Completion command
 *
 * Runs the epic completion sweep to check ALL open epics for completion.
 * Closes epics that have all sub-issues completed and syncs project board status.
 *
 * @see docs/ARCHITECTURE.md - Epic Handling
 * @see Issue #520 - Epic lifecycle management bug fix
 */

import * as vscode from "vscode";
import { spawn, execSync } from "child_process";
import type { Logger } from "../utils/logger";
import type { OutputWindow } from "../views";
import { getWorkspaceRoot } from "../config/settings";
import { BinaryResolver } from "../services/BinaryResolver";

/**
 * Resolve GITHUB_TOKEN from environment or `gh auth token`.
 * The Go binary requires this env var but VSCode extensions don't
 * always inherit it from the user's shell.
 */
function resolveGitHubToken(): string | undefined {
  if (process.env.GITHUB_TOKEN) return process.env.GITHUB_TOKEN;
  try {
    return execSync("gh auth token", { timeout: 5_000 }).toString().trim();
  } catch {
    return undefined;
  }
}

/**
 * Result from `nightgauge epic check-completion --sweep --json`
 */
export interface EpicSweepResult {
  success: boolean;
  epics_checked: number;
  epics_closed: number;
  closed_epics: Array<{ number: number; title: string }>;
  error?: string;
}

/**
 * Result item returned by Go binary `epic check-completion --json`
 */
interface GoEpicCompletionResult {
  epicNumber: number;
  title: string;
  complete: boolean;
  total: number;
  closed: number;
  open: number;
}

/**
 * Result from Go binary `epic complete <N> --json`
 */
interface GoEpicCompleteResult {
  epicNumber: number;
  complete: boolean;
  total: number;
  closed: number;
  open: number;
  action: string; // "closed_and_merged", "closed_pr_created", "already_merged", "no_epic_branch", "not_complete"
  prUrl?: string;
  prNumber?: number;
  error?: string;
}

/**
 * Resolve the nightgauge Go binary path via BinaryResolver.
 * Uses the same 4-tier resolution as IpcClientBase (VSCode setting → env var →
 * extension-bundled binary → system PATH).
 */
async function resolveNightgaugeBinary(): Promise<string | null> {
  return BinaryResolver.fromVSCode().resolve();
}

/**
 * Run `epic complete <N> --json` to close an epic, create PR, and merge.
 */
function runEpicComplete(
  binary: string,
  epicNumber: number,
  workspaceRoot: string,
  logger: Logger
): Promise<GoEpicCompleteResult> {
  return new Promise((resolve, reject) => {
    const completeEnv = { ...process.env };
    const completeToken = resolveGitHubToken();
    if (completeToken && !completeEnv.GITHUB_TOKEN) {
      completeEnv.GITHUB_TOKEN = completeToken;
    }

    const proc = spawn(binary, ["epic", "complete", String(epicNumber), "--json"], {
      cwd: workspaceRoot,
      shell: false,
      env: completeEnv,
    });

    let stdout = "";
    let stderr = "";

    proc.stdout.on("data", (data: Buffer) => {
      stdout += data.toString();
    });
    proc.stderr.on("data", (data: Buffer) => {
      stderr += data.toString();
    });

    proc.on("error", (error: Error) => {
      reject(new Error(`Failed to spawn epic complete for #${epicNumber}: ${error.message}`));
    });

    proc.on("close", (code: number | null) => {
      if (stderr) {
        logger.debug(`Epic complete #${epicNumber} stderr`, {
          stderr: stderr.slice(0, 200),
        });
      }

      try {
        const result = JSON.parse(stdout.trim()) as GoEpicCompleteResult;
        resolve(result);
      } catch {
        if (code !== 0) {
          reject(new Error(stderr || `epic complete #${epicNumber} failed with exit code ${code}`));
        } else {
          reject(new Error(`Failed to parse epic complete result for #${epicNumber}`));
        }
      }
    });
  });
}

/**
 * Map Go binary `[]EpicCompletionResult` array → `EpicSweepResult`
 */
function mapGoResultToSweepResult(results: GoEpicCompletionResult[]): EpicSweepResult {
  const closedEpics = results
    .filter((r) => r.complete)
    .map((r) => ({ number: r.epicNumber, title: r.title }));
  return {
    success: true,
    epics_checked: results.length,
    epics_closed: closedEpics.length,
    closed_epics: closedEpics,
  };
}

/**
 * Run `epic check-completion --sweep --json` and return parsed results.
 */
function runCheckCompletionSweep(
  binary: string,
  workspaceRoot: string,
  logger: Logger
): Promise<GoEpicCompletionResult[]> {
  return new Promise((resolve, reject) => {
    logger.debug("Running epic completion sweep via Go binary", { binary });

    const env = { ...process.env };
    const token = resolveGitHubToken();
    if (token && !env.GITHUB_TOKEN) {
      env.GITHUB_TOKEN = token;
    }

    const proc = spawn(binary, ["epic", "check-completion", "--sweep", "--json"], {
      cwd: workspaceRoot,
      shell: false,
      env,
    });

    let stdout = "";
    let stderr = "";

    proc.stdout.on("data", (data: Buffer) => {
      stdout += data.toString();
    });
    proc.stderr.on("data", (data: Buffer) => {
      stderr += data.toString();
    });

    proc.on("error", (error: Error) => {
      reject(new Error(`Failed to spawn epic sweep process: ${error.message}`));
    });

    proc.on("close", (code: number | null) => {
      if (code !== 0) {
        try {
          const result = JSON.parse(stdout || stderr);
          if (result.error) {
            reject(new Error(result.error));
            return;
          }
        } catch {
          // Not JSON, use raw stderr
        }
        reject(new Error(stderr || `Epic sweep failed with exit code ${code}`));
        return;
      }

      try {
        const raw = JSON.parse(stdout.trim());
        // Go binary returns null when there are no epics to check
        const parsed = (Array.isArray(raw) ? raw : []) as GoEpicCompletionResult[];
        const complete = parsed.filter((e) => e.complete);
        const incomplete = parsed.filter((e) => !e.complete);
        logger.debug("Epic sweep summary", {
          total: parsed.length,
          complete: complete.map((e) => `#${e.epicNumber}`).join(", ") || "none",
          incomplete:
            incomplete.map((e) => `#${e.epicNumber} (${e.closed}/${e.total})`).join(", ") || "none",
        });
        resolve(parsed);
      } catch {
        logger.error("Failed to parse epic sweep result", {
          stdout: stdout.slice(0, 200),
        });
        reject(new Error("Failed to parse epic sweep result"));
      }
    });
  });
}

/**
 * Run the epic completion sweep
 *
 * 1. Invokes `epic check-completion --sweep --json` to find completed epics.
 * 2. For each completed epic, runs `epic complete <N> --json` to close the
 *    epic issue, create the epic→main PR, merge it, and clean up branches.
 */
export async function runEpicCompletionSweep(
  workspaceRoot: string,
  logger: Logger
): Promise<EpicSweepResult> {
  const binary = await resolveNightgaugeBinary();
  if (!binary) {
    // The sweep is a best-effort safety net, not a required step. When the
    // binary can't be resolved (e.g. a transient extension-update window),
    // skip quietly instead of surfacing a scary WARN — matching the
    // HeadlessOrchestrator's epic-sweep skip. The binary resolver already
    // self-heals past a GC'd extension dir (#3883); a genuine miss here just
    // defers the sweep to the next completion.
    logger.debug("Epic completion sweep skipped — binary not found");
    return { success: true, epics_checked: 0, epics_closed: 0, closed_epics: [] };
  }

  const results = await runCheckCompletionSweep(binary, workspaceRoot, logger);
  const sweepResult = mapGoResultToSweepResult(results);

  // Run `epic complete` for each completed epic to close, create PR, and merge
  const completedEpics = results.filter((r) => r.complete);
  for (const epic of completedEpics) {
    try {
      const completeResult = await runEpicComplete(binary, epic.epicNumber, workspaceRoot, logger);
      logger.info("Epic completion flow finished", {
        epicNumber: epic.epicNumber,
        action: completeResult.action,
        prUrl: completeResult.prUrl,
        error: completeResult.error,
      });
    } catch (completeErr) {
      logger.warn("Epic completion flow failed for epic", {
        epicNumber: epic.epicNumber,
        error: completeErr instanceof Error ? completeErr.message : String(completeErr),
      });
    }
  }

  return sweepResult;
}

/**
 * Register the Check Epic Completion command
 *
 * This command can be invoked from:
 * 1. Command palette (Nightgauge: Check Epic Completion)
 */
export function registerCheckEpicCompletionCommand(
  logger: Logger,
  outputWindow?: OutputWindow
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.checkEpicCompletion", async () => {
    const workspaceRoot = getWorkspaceRoot();
    if (!workspaceRoot) {
      vscode.window.showErrorMessage("No workspace folder open");
      return;
    }

    outputWindow?.appendLine("Running epic completion sweep...", "info");

    try {
      const result = await runEpicCompletionSweep(workspaceRoot, logger);

      logger.info("Epic completion sweep completed", {
        epicsChecked: result.epics_checked,
        epicsClosed: result.epics_closed,
        closedEpics: result.closed_epics.map((e) => e.number),
      });

      if (result.epics_closed > 0) {
        // Build message with closed epic numbers
        const epicNumbers = result.closed_epics.map((e) => `#${e.number}`).join(", ");

        const message = `Closed ${result.epics_closed} epic(s) with all sub-issues complete: ${epicNumbers}`;

        outputWindow?.appendLine(`\n${message}`, "info");
        vscode.window.showInformationMessage(message);
      } else {
        const message = `No epics ready to close (checked ${result.epics_checked} epics)`;
        outputWindow?.appendLine(`\n${message}`, "info");
        vscode.window.showInformationMessage(message);
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error";
      logger.error("Epic completion sweep failed", { error: message });
      outputWindow?.appendLine(`\nEpic sweep failed: ${message}`, "error");
      vscode.window.showErrorMessage(`Epic completion sweep failed: ${message}`);
    }
  });
}
