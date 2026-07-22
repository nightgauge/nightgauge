/**
 * Run Command - Execute the full pipeline for an issue
 *
 * Usage: nightgauge-sdk run <issue-number> [options]
 *
 * Runs the complete Issue-to-PR pipeline stages in order:
 * 1. issue-pickup - Extract requirements from GitHub issue
 * 2. feature-planning - Generate implementation plan
 * 3. feature-dev - Implement the feature
 * 4. feature-validate - Run quality gate checks before PR
 * 5. pr-create - Create pull request
 * 6. pr-merge - Wait for reviews and merge
 */

import type { CAC } from "cac";
import { PipelineOrchestrator } from "../../orchestrator/PipelineOrchestrator.js";
import type { PipelineStage } from "../../events/EventBus.js";
import { ContextManager } from "../../context/ContextManager.js";
import { PRContextSchema } from "../../context/schemas/index.js";
import type { CLIConfig } from "../config.js";
import { OutputFormatter } from "../output.js";
import { runAdapterPreflightChecks } from "../codexPreflight.js";
import { createAdapterQueryFunction } from "../adapterQuery.js";

/**
 * Exit codes for the CLI
 */
export const EXIT_CODES = {
  SUCCESS: 0,
  PIPELINE_FAILED: 1,
  CONFIG_ERROR: 2,
  TIMEOUT: 3,
  INTERRUPT: 130,
} as const;

/**
 * Options for the run command
 */
interface RunOptions {
  stages?: string;
  model?: string;
  autoApprove?: boolean;
  timeout?: number;
  stageTimeout?: number;
  format?: string;
  logLevel?: string;
}

/**
 * Register the run command
 */
export function registerRunCommand(cli: CAC, config: CLIConfig): void {
  cli
    .command("run <issue>", "Run the full pipeline for an issue")
    .option("--stages <stages>", "Comma-separated list of stages to run")
    .option("--model <model>", "Model to use (sonnet, opus, haiku)")
    .option("--auto-approve", "Auto-approve all stages (CI mode)")
    .option("--timeout <ms>", "Global timeout in milliseconds")
    .option("--stage-timeout <ms>", "Per-stage timeout in milliseconds")
    .option("--format <format>", "Output format (text, json)")
    .option("--log-level <level>", "Log level (debug, info, warn, error)")
    .action(async (issueArg: string, options: RunOptions) => {
      const issueNumber = parseInt(issueArg, 10);

      if (isNaN(issueNumber) || issueNumber <= 0) {
        console.error(`Error: Invalid issue number: ${issueArg}`);
        process.exit(EXIT_CODES.CONFIG_ERROR);
      }

      // Merge CLI options with config
      const finalConfig: CLIConfig = {
        ...config,
        autoApprove: options.autoApprove ?? config.autoApprove,
        outputFormat: (options.format as CLIConfig["outputFormat"]) ?? config.outputFormat,
        logLevel: (options.logLevel as CLIConfig["logLevel"]) ?? config.logLevel,
        globalTimeoutMs: options.timeout ?? config.globalTimeoutMs,
        stageTimeoutMs: options.stageTimeout ?? config.stageTimeoutMs,
        defaultModel: (options.model as CLIConfig["defaultModel"]) ?? config.defaultModel,
      };

      // Parse stages if provided
      const stages = options.stages
        ? (options.stages.split(",").map((s) => s.trim()) as PipelineStage[])
        : undefined;

      const formatter = new OutputFormatter(finalConfig.outputFormat, finalConfig.logLevel);

      if (finalConfig.adapter !== "claude-sdk") {
        try {
          const includesIssuePickup = !stages || stages.includes("issue-pickup");
          const includesGithubStage =
            !stages ||
            stages.includes("issue-pickup") ||
            stages.includes("pr-create") ||
            stages.includes("pr-merge");
          await runAdapterPreflightChecks({
            adapter: finalConfig.adapter,
            cwd: process.cwd(),
            allowMainBranch: includesIssuePickup,
            requireCleanWorkingTree: includesIssuePickup,
            requireGithubAuth: includesGithubStage,
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

      formatter.info(`Starting pipeline for issue #${issueNumber}...`);

      try {
        const queryFn = await createAdapterQueryFunction(finalConfig.adapter);
        const orchestrator = new PipelineOrchestrator(queryFn, {
          stages,
          autoApprove: finalConfig.autoApprove,
          globalTimeoutMs: finalConfig.globalTimeoutMs,
          stageTimeoutMs: finalConfig.stageTimeoutMs,
          defaultModel: finalConfig.defaultModel,
          // Thread the provider id so StageExecutor provisions provider-aware
          // steering (AGENTS.md/GEMINI.md) + the correct system-prompt preset on
          // this SDK-CLI path (#4038).
          adapter: finalConfig.adapter,
        });

        // Stream every workflow node emission (run / phase / agent / judge) to
        // the formatter, which renders the human-readable / JSON output.
        orchestrator.events.onAny((node) => {
          formatter.event(node);
        });

        // Handle SIGINT
        let interrupted = false;
        const sigintHandler = async () => {
          if (interrupted) {
            process.exit(EXIT_CODES.INTERRUPT);
          }
          interrupted = true;
          formatter.warn("Received SIGINT, stopping pipeline...");
          await orchestrator.stop();
        };
        process.on("SIGINT", sigintHandler);

        try {
          // Set up global timeout
          const globalTimeoutPromise = new Promise<never>((_, reject) => {
            if (finalConfig.globalTimeoutMs > 0) {
              setTimeout(() => {
                reject(new Error("Global timeout exceeded"));
              }, finalConfig.globalTimeoutMs);
            }
          });

          // Run pipeline with global timeout
          const result = await Promise.race([orchestrator.run(issueNumber), globalTimeoutPromise]);

          // Extract PR URL from context if available
          let prUrl: string | undefined;
          try {
            const prFilename = ContextManager.getFilename("pr", issueNumber);
            const prContext = await orchestrator.context.read(PRContextSchema, prFilename);
            prUrl = prContext?.pr_url;
          } catch {
            // PR context may not exist
          }

          formatter.pipelineResult(result, prUrl);

          process.exit(result.success ? EXIT_CODES.SUCCESS : EXIT_CODES.PIPELINE_FAILED);
        } catch (error) {
          if (error instanceof Error && error.message === "Global timeout exceeded") {
            formatter.error("Pipeline timed out");
            process.exit(EXIT_CODES.TIMEOUT);
          }

          formatter.error(
            "Pipeline execution failed",
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
