/**
 * CLI Output Formatting - Text and JSON output support
 *
 * Provides consistent output formatting for CLI commands.
 * Supports both human-readable text and machine-parseable JSON.
 */

import type { PipelineResult, StageResult } from "../orchestrator/PipelineOrchestrator.js";
import type { PipelineStage } from "../events/EventBus.js";
import type { WorkflowEvent } from "../cli/workflow/WorkflowEvent.js";

/**
 * Output format type
 */
export type OutputFormat = "text" | "json";

/**
 * Log level type
 */
export type LogLevel = "debug" | "info" | "warn" | "error";

/**
 * Log level priority (lower = more verbose)
 */
const LOG_LEVELS: Record<LogLevel, number> = {
  debug: 0,
  info: 1,
  warn: 2,
  error: 3,
};

/**
 * JSON output schema for pipeline results
 */
export interface PipelineJSONOutput {
  success: boolean;
  issueNumber: number;
  branch?: string;
  stagesCompleted: PipelineStage[];
  stagesFailed: PipelineStage[];
  totalDurationMs: number;
  usage: {
    inputTokens: number;
    outputTokens: number;
    cacheReadTokens: number;
    costUsd: number;
  };
  prUrl?: string;
  error?: string;
}

/**
 * JSON output schema for stage results
 */
export interface StageJSONOutput {
  success: boolean;
  stage: PipelineStage;
  issueNumber: number;
  durationMs: number;
  error?: string;
}

/**
 * JSON output schema for status
 */
export interface StatusJSONOutput {
  isRunning: boolean;
  currentStage: PipelineStage | null;
  issueNumber: number;
  contextFiles: string[];
  planFile?: string;
}

/**
 * OutputFormatter class for consistent CLI output
 *
 * @example
 * ```typescript
 * const formatter = new OutputFormatter('json', 'info');
 *
 * formatter.info('Starting pipeline...');
 * formatter.pipelineResult(result);
 * ```
 */
export class OutputFormatter {
  constructor(
    private format: OutputFormat = "text",
    private logLevel: LogLevel = "info"
  ) {}

  /**
   * Check if a log level should be output
   */
  private shouldLog(level: LogLevel): boolean {
    return LOG_LEVELS[level] >= LOG_LEVELS[this.logLevel];
  }

  /**
   * Log a debug message
   */
  debug(message: string, data?: unknown): void {
    if (!this.shouldLog("debug")) return;

    if (this.format === "json") {
      console.log(JSON.stringify({ level: "debug", message, data }));
    } else {
      console.log(`[DEBUG] ${message}`, data ?? "");
    }
  }

  /**
   * Log an info message
   */
  info(message: string, data?: unknown): void {
    if (!this.shouldLog("info")) return;

    if (this.format === "json") {
      console.log(JSON.stringify({ level: "info", message, data }));
    } else {
      console.log(message);
    }
  }

  /**
   * Log a warning message
   */
  warn(message: string, data?: unknown): void {
    if (!this.shouldLog("warn")) return;

    if (this.format === "json") {
      console.log(JSON.stringify({ level: "warn", message, data }));
    } else {
      console.warn(`Warning: ${message}`);
    }
  }

  /**
   * Log an error message
   */
  error(message: string, error?: Error): void {
    if (!this.shouldLog("error")) return;

    if (this.format === "json") {
      console.log(
        JSON.stringify({
          level: "error",
          message,
          error: error?.message,
          stack: error?.stack,
        })
      );
    } else {
      console.error(`Error: ${message}`);
      if (error?.stack && this.logLevel === "debug") {
        console.error(error.stack);
      }
    }
  }

  /**
   * Output pipeline result
   */
  pipelineResult(result: PipelineResult, prUrl?: string): void {
    if (this.format === "json") {
      const output: PipelineJSONOutput = {
        success: result.success,
        issueNumber: result.issueNumber,
        stagesCompleted: result.stagesCompleted,
        stagesFailed: result.stagesFailed,
        totalDurationMs: result.totalDurationMs,
        usage: {
          inputTokens: result.usage.inputTokens,
          outputTokens: result.usage.outputTokens,
          cacheReadTokens: result.usage.cacheReadTokens,
          costUsd: result.usage.costUsd,
        },
        prUrl,
      };
      console.log(JSON.stringify(output, null, 2));
    } else {
      console.log("");
      console.log(result.success ? "✓ Pipeline completed successfully" : "✗ Pipeline failed");
      console.log("");
      console.log(`Issue:     #${result.issueNumber}`);
      console.log(`Duration:  ${this.formatDuration(result.totalDurationMs)}`);
      console.log(`Completed: ${result.stagesCompleted.join(" → ")}`);
      if (result.stagesFailed.length > 0) {
        console.log(`Failed:    ${result.stagesFailed.join(", ")}`);
      }
      if (prUrl) {
        console.log(`PR:        ${prUrl}`);
      }
      console.log("");
      console.log("Token Usage:");
      console.log(`  Input:  ${result.usage.inputTokens.toLocaleString()}`);
      console.log(`  Output: ${result.usage.outputTokens.toLocaleString()}`);
      console.log(`  Cache:  ${result.usage.cacheReadTokens.toLocaleString()}`);
      console.log(`  Cost:   $${result.usage.costUsd.toFixed(4)}`);
    }
  }

  /**
   * Output stage result
   */
  stageResult(result: StageResult): void {
    if (this.format === "json") {
      const output: StageJSONOutput = {
        success: result.success,
        stage: result.stage,
        issueNumber: result.issueNumber,
        durationMs: result.durationMs,
        error: result.error?.message,
      };
      console.log(JSON.stringify(output, null, 2));
    } else {
      const icon = result.success ? "✓" : "✗";
      console.log(`${icon} ${result.stage} completed in ${this.formatDuration(result.durationMs)}`);
      if (result.error) {
        console.error(`  Error: ${result.error.message}`);
      }
    }
  }

  /**
   * Output status information
   */
  status(status: StatusJSONOutput): void {
    if (this.format === "json") {
      console.log(JSON.stringify(status, null, 2));
    } else {
      console.log("");
      console.log(`Issue #${status.issueNumber} Pipeline Status`);
      console.log("");
      console.log(`Running:       ${status.isRunning ? "Yes" : "No"}`);
      if (status.currentStage) {
        console.log(`Current Stage: ${status.currentStage}`);
      }
      if (status.planFile) {
        console.log(`Plan:          ${status.planFile}`);
      }
      if (status.contextFiles.length > 0) {
        console.log("Context Files:");
        for (const file of status.contextFiles) {
          console.log(`  - ${file}`);
        }
      }
    }
  }

  /**
   * Output a workflow event node (for streaming).
   *
   * Renders the canonical node-tree emissions: phase nodes carry stage
   * lifecycle (running/succeeded/failed/skipped), agent nodes carry token usage
   * and the timeout terminal, and the root run node carries pipeline completion.
   */
  event(event: WorkflowEvent): void {
    if (this.format === "json") {
      console.log(JSON.stringify(event));
      return;
    }

    switch (event.kind) {
      case "run":
        if (event.status === "succeeded") {
          this.info(`Pipeline complete`);
        } else if (event.status === "failed") {
          this.error(`Pipeline failed`);
        } else if (event.status === "cancelled") {
          this.warn(`Pipeline cancelled`);
        }
        break;
      case "phase":
        if (event.status === "running") {
          this.info(`▶ Starting ${event.name}...`);
        } else if (event.status === "succeeded") {
          this.info(`✓ ${event.name} completed`);
        } else if (event.status === "failed") {
          this.error(`✗ ${event.name} failed`);
        } else if (event.status === "skipped") {
          this.info(`⏭ ${event.name} skipped`);
        }
        break;
      case "agent":
        if (event.status === "running") {
          const { inputTokens, outputTokens, costUsd } = event.usage;
          this.debug(`Tokens: ${inputTokens} in, ${outputTokens} out, $${costUsd.toFixed(4)}`);
        } else if (event.status === "failed" && event.terminalKind === "timeout") {
          this.error(`⏱ ${event.label ?? event.agentId} timed out`);
        }
        break;
      case "judge":
        this.debug(`Judge ${event.judgeId}: ${event.verdict}`);
        break;
    }
  }

  /**
   * Format duration in human-readable form
   */
  private formatDuration(ms: number): string {
    if (ms < 1000) return `${ms}ms`;
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
    const minutes = Math.floor(ms / 60000);
    const seconds = Math.floor((ms % 60000) / 1000);
    return `${minutes}m ${seconds}s`;
  }

  /**
   * Print a progress spinner (text mode only)
   */
  spinner(message: string): () => void {
    if (this.format === "json") {
      return () => {};
    }

    const frames = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
    let i = 0;
    const interval = setInterval(() => {
      process.stdout.write(`\r${frames[i]} ${message}`);
      i = (i + 1) % frames.length;
    }, 80);

    return () => {
      clearInterval(interval);
      process.stdout.write("\r" + " ".repeat(message.length + 3) + "\r");
    };
  }
}

/**
 * Create an output formatter from config
 */
export function createFormatter(format: OutputFormat, logLevel: LogLevel): OutputFormatter {
  return new OutputFormatter(format, logLevel);
}
