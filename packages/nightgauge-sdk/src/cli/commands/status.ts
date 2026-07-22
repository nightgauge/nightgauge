/**
 * Status Command - Check pipeline status for an issue
 *
 * Usage: nightgauge-sdk status <issue-number> [options]
 *
 * Shows the current pipeline status including:
 * - Current stage (if running)
 * - Context files present
 * - Plan file location
 */

import type { CAC } from "cac";
import * as fs from "fs/promises";
import * as path from "path";
import type { CLIConfig } from "../config.js";
import { OutputFormatter, type StatusJSONOutput } from "../output.js";
import { EXIT_CODES } from "./run.js";

/**
 * Options for the status command
 */
interface StatusOptions {
  format?: string;
  logLevel?: string;
  contextPath?: string;
  plansPath?: string;
}

/**
 * Check if a file exists
 */
async function fileExists(filePath: string): Promise<boolean> {
  try {
    await fs.access(filePath);
    return true;
  } catch {
    return false;
  }
}

/**
 * Find context files for an issue
 */
async function findContextFiles(issueNumber: number, contextPath: string): Promise<string[]> {
  const files: string[] = [];
  const contextTypes = ["issue", "planning", "dev", "pr"];

  for (const type of contextTypes) {
    const filePath = path.join(contextPath, `${type}-${issueNumber}.json`);
    if (await fileExists(filePath)) {
      files.push(filePath);
    }
  }

  return files;
}

/**
 * Find plan file for an issue
 */
async function findPlanFile(issueNumber: number, plansPath: string): Promise<string | undefined> {
  try {
    const files = await fs.readdir(plansPath);
    const planFile = files.find((f) => f.startsWith(`${issueNumber}-`) && f.endsWith(".md"));
    return planFile ? path.join(plansPath, planFile) : undefined;
  } catch {
    return undefined;
  }
}

/**
 * Determine current stage from context files
 */
function inferCurrentStage(contextFiles: string[]): string | null {
  const types = contextFiles.map((f) => {
    const basename = path.basename(f);
    return basename.split("-")[0];
  });

  // Infer based on which context files exist
  if (types.includes("pr")) return "pr-merge";
  if (types.includes("dev")) return "pr-create";
  if (types.includes("planning")) return "feature-dev";
  if (types.includes("issue")) return "feature-planning";
  return null;
}

/**
 * Register the status command
 */
export function registerStatusCommand(cli: CAC, config: CLIConfig): void {
  cli
    .command("status <issue>", "Check pipeline status for an issue")
    .option("--format <format>", "Output format (text, json)")
    .option("--log-level <level>", "Log level (debug, info, warn, error)")
    .option("--context-path <path>", "Path to context files")
    .option("--plans-path <path>", "Path to plan files")
    .action(async (issueArg: string, options: StatusOptions) => {
      // Validate issue number
      const issueNumber = parseInt(issueArg, 10);
      if (isNaN(issueNumber) || issueNumber <= 0) {
        console.error(`Error: Invalid issue number: ${issueArg}`);
        process.exit(EXIT_CODES.CONFIG_ERROR);
      }

      const outputFormat = (options.format as CLIConfig["outputFormat"]) ?? config.outputFormat;
      const logLevel = (options.logLevel as CLIConfig["logLevel"]) ?? config.logLevel;
      const contextPath = options.contextPath ?? ".nightgauge/pipeline";
      const plansPath = options.plansPath ?? ".nightgauge/plans";

      const formatter = new OutputFormatter(outputFormat, logLevel);

      try {
        const contextFiles = await findContextFiles(issueNumber, contextPath);
        const planFile = await findPlanFile(issueNumber, plansPath);
        const currentStage = inferCurrentStage(contextFiles);

        const status: StatusJSONOutput = {
          isRunning: false, // CLI status check cannot detect running state
          currentStage:
            currentStage as unknown as import("../output.js").StatusJSONOutput["currentStage"],
          issueNumber,
          contextFiles,
          planFile,
        };

        formatter.status(status);
        process.exit(EXIT_CODES.SUCCESS);
      } catch (error) {
        formatter.error(
          "Failed to get status",
          error instanceof Error ? error : new Error(String(error))
        );
        process.exit(EXIT_CODES.PIPELINE_FAILED);
      }
    });
}
