/**
 * Copilot CLI Adapter - Uses the GitHub Copilot CLI for stage execution.
 *
 * Authenticates via env var cascade: GH_TOKEN → GITHUB_TOKEN →
 * COPILOT_GITHUB_TOKEN → `copilot auth status` CLI subcommand.
 * Prompts are delivered via stdin (default in createCliQueryFn).
 *
 * @see Issue #1942 - Implement CopilotCliAdapter for GitHub Copilot CLI
 */

import type { SDKQueryFunction } from "../../orchestrator/StageExecutor.js";
import type {
  ICliAdapter,
  OrchestrationCapability,
  ValidateAuthOptions,
  QueryFunctionOptions,
} from "./ICliAdapter.js";
import { verifyCLIInstalled, validateCLIAuth } from "./validateCLIAuth.js";
import { createCliQueryFn, parseCliArgs } from "./cliQueryHelper.js";
import { resolveAndValidateModel } from "./modelPreflight.js";

const ADAPTER_NAME = "GitHub Copilot";
const COPILOT_DOCS_URL =
  "https://docs.github.com/en/copilot/using-github-copilot/using-github-copilot-in-the-command-line";
const COPILOT_INSTALL_CMD = "npm install -g @github/copilot-cli";
const AUTH_ENV_VARS = ["GH_TOKEN", "GITHUB_TOKEN", "COPILOT_GITHUB_TOKEN"];

export class CopilotCliAdapter implements ICliAdapter {
  readonly name = "copilot" as const;
  readonly displayName = "GitHub Copilot";
  readonly cliCommand = "copilot";
  // copilot CLI — agentic coding-agent tool loop.
  readonly agentic = true;

  async validateAuth(options?: ValidateAuthOptions): Promise<"passed"> {
    const cwd = options?.cwd ?? process.cwd();
    const runner = options?.runner;

    if (!runner) {
      return "passed";
    }

    // 1. Verify CLI is installed
    await verifyCLIInstalled({
      command: this.cliCommand,
      runner,
      cwd,
      adapterName: ADAPTER_NAME,
      installCmd: COPILOT_INSTALL_CMD,
      docsUrl: COPILOT_DOCS_URL,
    });

    // 2. Check env vars first (cheapest, no subprocess)
    for (const envVar of AUTH_ENV_VARS) {
      if (process.env[envVar]?.trim()) {
        return "passed";
      }
    }

    // 3. CLI auth check fallback
    await validateCLIAuth({
      command: this.cliCommand,
      authSubcommands: [{ args: ["auth", "status"] }],
      runner,
      cwd,
      adapterName: ADAPTER_NAME,
      errorCategory: "AUTH_MISSING",
      loginHint: "gh auth login",
      docsUrl: COPILOT_DOCS_URL,
    });

    return "passed";
  }

  async createQueryFunction(_options?: QueryFunctionOptions): Promise<SDKQueryFunction> {
    const command = process.env.NIGHTGAUGE_COPILOT_CLI_COMMAND ?? this.cliCommand;
    const args = parseCliArgs(process.env.NIGHTGAUGE_COPILOT_CLI_ARGS, this.getDefaultArgs());

    // Inject model routing — set by skillRunner via NIGHTGAUGE_COPILOT_MODEL
    // (Issue #52). resolveAndValidateModel maps a Claude routing tier to a
    // concrete copilot-hosted model id (copilot is an OPEN set, so any concrete
    // id passes through). The prior adapter never sent --model, so model
    // selection was cosmetic; the Copilot CLI `--model` flag now actually
    // forces the model.
    const copilotModel = resolveAndValidateModel("copilot", process.env.NIGHTGAUGE_COPILOT_MODEL);
    if (copilotModel) {
      args.push("--model", copilotModel);
    }

    return createCliQueryFn({ command, args, adapter: this.name });
  }

  getDefaultArgs(): string[] {
    // `--allow-all-tools` grants unrestricted tool access for autonomous stage
    // execution — the documented Copilot CLI flag (the prior `--allow-all` was
    // not the tool-permission flag). `-s` is intentionally omitted so the stats
    // footer (premium-request count) is available for cost accounting (#52).
    return ["--allow-all-tools"];
  }

  getOrchestrationCapability(): OrchestrationCapability {
    return "sdk-fanout";
  }

  requiresDirectApiKey(): boolean {
    return false;
  }
}
