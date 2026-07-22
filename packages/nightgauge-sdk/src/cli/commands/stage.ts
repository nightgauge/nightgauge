/**
 * Stage Command - Execute a single pipeline stage
 *
 * Usage: nightgauge-sdk stage <stage-name> <issue-number> [options]
 *
 * Runs a single stage of the pipeline, useful for:
 * - Retrying a failed stage
 * - Running stages individually for debugging
 * - Custom pipeline flows
 */

import type { CAC } from "cac";
import { resolveAdapter } from "../adapter.js";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { spawn } from "node:child_process";
import { PipelineOrchestrator, DEFAULT_STAGES } from "../../orchestrator/PipelineOrchestrator.js";
import type { PipelineStage } from "../../events/EventBus.js";
import type { CLIConfig } from "../config.js";
import { OutputFormatter } from "../output.js";
import { EXIT_CODES } from "./run.js";
import { runAdapterPreflightChecks } from "../codexPreflight.js";
import { createAdapterQueryFunction } from "../adapterQuery.js";

/**
 * Options for the stage command
 */
interface StageOptions {
  model?: string;
  timeout?: number;
  format?: string;
  logLevel?: string;
}

interface CommandResult {
  code: number;
  stdout: string;
  stderr: string;
}

async function runCommand(command: string, args: string[], cwd: string): Promise<CommandResult> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd,
      stdio: "pipe",
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      stdout += String(chunk);
    });
    child.stderr.on("data", (chunk) => {
      stderr += String(chunk);
    });
    child.on("error", reject);
    child.on("close", (code) => {
      resolve({ code: code ?? 1, stdout, stderr });
    });
  });
}

async function fileExists(filePath: string): Promise<boolean> {
  try {
    await readFile(filePath);
    return true;
  } catch {
    return false;
  }
}

function getContextPath(cwd: string, stageFile: string, issueNumber: number): string {
  return path.join(cwd, ".nightgauge", "pipeline", `${stageFile}-${issueNumber}.json`);
}

async function getCurrentBranch(cwd: string): Promise<string | null> {
  const result = await runCommand("git", ["branch", "--show-current"], cwd);
  if (result.code !== 0) {
    return null;
  }
  const branch = result.stdout.trim();
  return branch.length > 0 ? branch : null;
}

function parseExpectedBranchFromIssueContext(raw: string): string | null {
  try {
    const parsed = JSON.parse(raw) as { branch?: unknown };
    if (typeof parsed.branch !== "string") {
      return null;
    }
    const branch = parsed.branch.trim();
    return branch.length > 0 ? branch : null;
  } catch {
    return null;
  }
}

export async function validateCodexStagePostconditions(options: {
  stage: PipelineStage;
  issueNumber: number;
  cwd: string;
}): Promise<void> {
  const { stage, issueNumber, cwd } = options;

  const requiredContextFileByStage: Partial<Record<PipelineStage, string>> = {
    "issue-pickup": "issue",
    "feature-planning": "planning",
    "feature-dev": "dev",
    "feature-validate": "validate",
    "pr-create": "pr",
  };

  const requiredContextPrefix = requiredContextFileByStage[stage];
  if (requiredContextPrefix) {
    const requiredPath = getContextPath(cwd, requiredContextPrefix, issueNumber);
    if (!(await fileExists(requiredPath))) {
      throw new Error(
        `Codex postcondition failed: stage '${stage}' reported success but required context file is missing: ${requiredPath}`
      );
    }
  }

  if (stage === "issue-pickup") {
    const issueContextPath = getContextPath(cwd, "issue", issueNumber);
    const rawIssueContext = await readFile(issueContextPath, "utf-8");
    const expectedBranch = parseExpectedBranchFromIssueContext(rawIssueContext);
    if (!expectedBranch) {
      throw new Error(
        `Codex postcondition failed: issue context is missing a valid branch field: ${issueContextPath}`
      );
    }

    const currentBranch = await getCurrentBranch(cwd);
    if (!currentBranch) {
      throw new Error(
        "Codex postcondition failed: unable to determine current git branch after issue-pickup."
      );
    }

    if (currentBranch === "main" || currentBranch === "master") {
      throw new Error(
        `Codex postcondition failed: still on protected branch '${currentBranch}' after issue-pickup. Expected '${expectedBranch}'.`
      );
    }

    if (currentBranch !== expectedBranch) {
      throw new Error(
        `Codex postcondition failed: current branch '${currentBranch}' does not match issue context branch '${expectedBranch}'.`
      );
    }
  }

  if (stage === "pr-merge") {
    const residualContextFiles = ["issue", "planning", "dev", "pr"].map((prefix) =>
      getContextPath(cwd, prefix, issueNumber)
    );
    const leftovers: string[] = [];
    for (const candidate of residualContextFiles) {
      if (await fileExists(candidate)) {
        leftovers.push(candidate);
      }
    }

    if (leftovers.length > 0) {
      throw new Error(
        `Codex postcondition failed: pr-merge reported success but context cleanup is incomplete: ${leftovers.join(", ")}`
      );
    }
  }
}

/**
 * Register the stage command
 */
export function registerStageCommand(cli: CAC, config: CLIConfig): void {
  const stageNames = DEFAULT_STAGES.join(", ");

  cli
    .command("stage <stage> <issue>", `Run a single pipeline stage. Stages: ${stageNames}`)
    .option("--model <model>", "Model to use (sonnet, opus, haiku)")
    .option("--timeout <ms>", "Stage timeout in milliseconds")
    .option("--format <format>", "Output format (text, json)")
    .option("--log-level <level>", "Log level (debug, info, warn, error)")
    .action(async (stageArg: string, issueArg: string, options: StageOptions) => {
      // Validate stage name
      const stage = stageArg as PipelineStage;
      if (!DEFAULT_STAGES.includes(stage)) {
        console.error(`Error: Unknown stage '${stageArg}'`);
        console.error(`Valid stages: ${stageNames}`);
        process.exit(EXIT_CODES.CONFIG_ERROR);
      }

      // Validate issue number
      const issueNumber = parseInt(issueArg, 10);
      if (isNaN(issueNumber) || issueNumber <= 0) {
        console.error(`Error: Invalid issue number: ${issueArg}`);
        process.exit(EXIT_CODES.CONFIG_ERROR);
      }

      // Merge CLI options with config. The adapter re-resolves with the
      // stage so pipeline.stage_adapters.<stage> / the per-stage env rung
      // apply (#54) — a single-stage invocation is exactly the per-stage case.
      const finalConfig: CLIConfig = {
        ...config,
        adapter: resolveAdapter(process.env, { stage, cwd: process.cwd() }),
        outputFormat: (options.format as CLIConfig["outputFormat"]) ?? config.outputFormat,
        logLevel: (options.logLevel as CLIConfig["logLevel"]) ?? config.logLevel,
        stageTimeoutMs: options.timeout ?? config.stageTimeoutMs,
        defaultModel: (options.model as CLIConfig["defaultModel"]) ?? config.defaultModel,
      };

      const formatter = new OutputFormatter(finalConfig.outputFormat, finalConfig.logLevel);

      if (finalConfig.adapter !== "claude-sdk") {
        try {
          await runAdapterPreflightChecks({
            adapter: finalConfig.adapter,
            cwd: process.cwd(),
            stage,
          });
          formatter.info(`${finalConfig.adapter} preflight checks passed.`);
        } catch (error) {
          formatter.error(
            error instanceof Error
              ? error.message
              : `${finalConfig.adapter} preflight checks failed.`
          );
          process.exit(EXIT_CODES.CONFIG_ERROR);
        }
      }

      formatter.info(`Running stage '${stage}' for issue #${issueNumber}...`);

      try {
        const queryFn = await createAdapterQueryFunction(finalConfig.adapter);
        const orchestrator = new PipelineOrchestrator(queryFn, {
          stageTimeoutMs: finalConfig.stageTimeoutMs,
          defaultModel: finalConfig.defaultModel,
          // Provider id → provider-aware steering + preset on the SDK-CLI path (#4038).
          adapter: finalConfig.adapter,
        });

        // Stream every workflow node emission (run / phase / agent / judge) to
        // the formatter, which renders the human-readable / JSON output.
        orchestrator.events.onAny((node) => {
          formatter.event(node);
        });

        // Handle SIGINT
        const sigintHandler = async () => {
          formatter.warn("Received SIGINT, stopping...");
          await orchestrator.stop();
          process.exit(EXIT_CODES.INTERRUPT);
        };
        process.on("SIGINT", sigintHandler);

        try {
          const result = await orchestrator.runStage(stage, issueNumber);

          if (result.success && finalConfig.adapter === "codex") {
            try {
              await validateCodexStagePostconditions({
                stage,
                issueNumber,
                cwd: process.cwd(),
              });
            } catch (error) {
              formatter.error(
                error instanceof Error
                  ? error.message
                  : "Codex stage postcondition validation failed."
              );
              process.exit(EXIT_CODES.PIPELINE_FAILED);
            }
          }

          formatter.stageResult(result);

          process.exit(result.success ? EXIT_CODES.SUCCESS : EXIT_CODES.PIPELINE_FAILED);
        } catch (error) {
          formatter.error(
            "Stage execution failed",
            error instanceof Error ? error : new Error(String(error))
          );
          process.exit(EXIT_CODES.PIPELINE_FAILED);
        } finally {
          process.off("SIGINT", sigintHandler);
        }
      } catch (error) {
        formatter.error(
          `Unable to initialize adapter '${finalConfig.adapter}'.`,
          error instanceof Error ? error : new Error(String(error))
        );
        process.exit(EXIT_CODES.CONFIG_ERROR);
      }
    });
}
