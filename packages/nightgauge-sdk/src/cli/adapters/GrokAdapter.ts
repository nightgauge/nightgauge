/**
 * Grok Build CLI adapter — agentic headless `grok -p` / `--prompt-file`.
 *
 * Auth: SuperGrok / grok.com session (`~/.grok/auth.json`) or `XAI_API_KEY`.
 * Prompt is never piped on stdin (Grok headless ignores it).
 *
 * @see Issue #524
 * @see Issue #526
 */

import { homedir } from "node:os";
import { join } from "node:path";
import { access } from "node:fs/promises";
import { constants as fsConstants } from "node:fs";

import type { SDKQueryFunction } from "../../orchestrator/StageExecutor.js";
import type {
  ICliAdapter,
  OrchestrationCapability,
  ValidateAuthOptions,
  QueryFunctionOptions,
} from "./ICliAdapter.js";
import { verifyCLIInstalled } from "./validateCLIAuth.js";
import { createCliQueryFn, parseCliArgs } from "./cliQueryHelper.js";
import { AdapterError } from "./errors.js";
import { resolveAndValidateModel } from "./modelPreflight.js";
import { grokCliEffortFlag } from "./grokEffort.js";

const ADAPTER_NAME = "Grok";
const GROK_DOCS_URL = "https://docs.x.ai/build/overview";
const GROK_INSTALL_CMD = "curl -fsSL https://x.ai/cli/install.sh | bash";
export const GROK_MIN_KNOWN_VERSION = "1.0.0";

function compareVersions(a: string, b: string): number {
  const pa = a.split(".").map(Number);
  const pb = b.split(".").map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const na = pa[i] ?? 0;
    const nb = pb[i] ?? 0;
    if (na !== nb) return na - nb;
  }
  return 0;
}

function grokHome(): string {
  return process.env.GROK_HOME?.trim() || join(homedir(), ".grok");
}

async function sessionFileExists(): Promise<boolean> {
  try {
    await access(join(grokHome(), "auth.json"), fsConstants.R_OK);
    return true;
  } catch {
    return false;
  }
}

export class GrokAdapter implements ICliAdapter {
  readonly name = "grok" as const;
  readonly displayName = "Grok";
  readonly cliCommand = "grok";
  readonly agentic = true;

  async validateAuth(options?: ValidateAuthOptions): Promise<"passed"> {
    const cwd = options?.cwd ?? process.cwd();
    const runner = options?.runner;

    if (!runner) {
      return "passed";
    }

    const versionResult = await verifyCLIInstalled({
      command: this.cliCommand,
      runner,
      cwd,
      adapterName: ADAPTER_NAME,
      installCmd: GROK_INSTALL_CMD,
      docsUrl: GROK_DOCS_URL,
    });

    const versionMatch = versionResult.stdout.trim().match(/(\d+\.\d+\.\d+)/);
    if (versionMatch && compareVersions(versionMatch[1], GROK_MIN_KNOWN_VERSION) < 0) {
      console.warn(
        `[grok-adapter] WARNING: Grok CLI version ${versionMatch[1]} is older than ` +
          `minimum known compatible version ${GROK_MIN_KNOWN_VERSION}.`
      );
    }

    if (process.env.XAI_API_KEY?.trim()) {
      return "passed";
    }

    if (await sessionFileExists()) {
      return "passed";
    }

    // Cheap probe: inspect exits 0 when a session can be loaded.
    const inspect = await runner(this.cliCommand, ["inspect", "--json"], cwd);
    if (inspect.code === 0) {
      return "passed";
    }

    throw new AdapterError(
      "Grok CLI is not authenticated.\n" +
        "Run `grok login` (SuperGrok / grok.com session) or set XAI_API_KEY.\n" +
        `Docs: ${GROK_DOCS_URL}`,
      "AUTH_MISSING",
      ADAPTER_NAME,
      GROK_DOCS_URL
    );
  }

  async createQueryFunction(_options?: QueryFunctionOptions): Promise<SDKQueryFunction> {
    const command = process.env.NIGHTGAUGE_GROK_CLI_COMMAND ?? this.cliCommand;
    const args = parseCliArgs(process.env.NIGHTGAUGE_GROK_CLI_ARGS, this.getDefaultArgs());

    const grokModel = resolveAndValidateModel(
      "grok",
      process.env.NIGHTGAUGE_GROK_MODEL ?? process.env.NIGHTGAUGE_MODEL
    );
    if (grokModel) {
      args.push("--model", grokModel);
    }

    const effort = grokCliEffortFlag(process.env.NIGHTGAUGE_GROK_EFFORT);
    if (effort) {
      args.push("--effort", effort);
    }

    return createCliQueryFn({
      command,
      args,
      adapter: this.name,
      promptDelivery: "prompt-file",
    });
  }

  getDefaultArgs(): string[] {
    return [
      "--output-format",
      "streaming-json",
      "--always-approve",
      "--no-auto-update",
      "--max-turns",
      "200",
    ];
  }

  getOrchestrationCapability(): OrchestrationCapability {
    return "sdk-fanout";
  }

  requiresDirectApiKey(): boolean {
    return false;
  }
}
