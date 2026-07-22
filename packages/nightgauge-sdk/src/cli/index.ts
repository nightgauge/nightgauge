#!/usr/bin/env node
/**
 * Nightgauge SDK CLI - Headless pipeline execution for CI/CD
 *
 * Usage:
 *   npx @nightgauge/sdk run <issue-number>
 *   npx @nightgauge/sdk stage <stage-name> <issue-number>
 *   npx @nightgauge/sdk status <issue-number>
 *
 * Environment Variables:
 *   NIGHTGAUGE_ADAPTER       - Adapter: claude, claude-headless, codex
 *   ANTHROPIC_API_KEY     - Required only when using claude-sdk adapter mode.
 *   NIGHTGAUGE_AUTO_APPROVE  - Auto-approve all stages (default: false)
 *   NIGHTGAUGE_OUTPUT_FORMAT - Output format: text or json (default: text)
 *   NIGHTGAUGE_LOG_LEVEL     - Log level: debug, info, warn, error (default: info)
 *   NIGHTGAUGE_TIMEOUT       - Global timeout in ms (default: 3600000 = 1 hour)
 *   NIGHTGAUGE_STAGE_TIMEOUT - Per-stage timeout in ms (default: 900000 = 15 min)
 *   NIGHTGAUGE_MODEL         - Model: sonnet, opus, haiku (default: sonnet)
 *
 * Exit Codes:
 *   0   - Pipeline completed successfully
 *   1   - Pipeline stage failed
 *   2   - Configuration/validation error
 *   3   - Timeout exceeded
 *   130 - User interrupt (SIGINT)
 *
 * @see docs/CI_INTEGRATION.md for complete documentation
 */

import cac from "cac";
import { loadConfigFromEnv, validateConfig, ConfigValidationError } from "./config.js";
import { registerRunCommand, EXIT_CODES } from "./commands/run.js";
import { registerStageCommand } from "./commands/stage.js";
import { registerStatusCommand } from "./commands/status.js";
import { registerQueryCommand } from "./commands/query.js";
import { registerPreflightCommand } from "./commands/preflight.js";

// Package version (will be replaced during build)
const VERSION = "0.2.0";

/**
 * Main CLI entry point
 */
async function main(): Promise<void> {
  const cli = cac("nightgauge-sdk");

  // Load and validate configuration from environment
  let config;
  try {
    config = loadConfigFromEnv();
    validateConfig(config);
  } catch (error) {
    if (error instanceof ConfigValidationError) {
      console.error(`Configuration error: ${error.message}`);
      console.error(`Field: ${error.field}`);
    } else if (error instanceof Error) {
      console.error(error.message);
    }
    process.exit(EXIT_CODES.CONFIG_ERROR);
  }

  // Register commands
  registerRunCommand(cli, config);
  registerStageCommand(cli, config);
  registerStatusCommand(cli, config);
  registerQueryCommand(cli, config);
  registerPreflightCommand(cli);

  // Global options
  cli.version(VERSION);
  cli.help();

  // Handle unknown commands. cac 7 dropped its EventEmitter base for
  // EventTarget, so `command:*` is now delivered via addEventListener rather
  // than `.on()`. It is still dispatched synchronously from cli.parse() below.
  cli.addEventListener("command:*", () => {
    console.error("Unknown command. Run --help for usage.");
    process.exit(EXIT_CODES.CONFIG_ERROR);
  });

  // Parse arguments
  try {
    cli.parse(process.argv, { run: false });

    // Run the matched command
    await cli.runMatchedCommand();
  } catch (error) {
    if (error instanceof Error) {
      // CAC throws for --help and --version, which is expected
      if (error.message.includes("Output help information")) {
        return;
      }
      console.error(`Error: ${error.message}`);
    }
    process.exit(EXIT_CODES.CONFIG_ERROR);
  }
}

// Run CLI
main().catch((error) => {
  console.error("Fatal error:", error);
  process.exit(EXIT_CODES.PIPELINE_FAILED);
});
