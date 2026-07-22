/**
 * Gemini Adapter - Uses the Gemini CLI for stage execution.
 *
 * Authenticates via a three-method cascade: GEMINI_API_KEY env var,
 * Vertex AI env vars, or `gcloud auth print-access-token` fallback.
 * Gemini CLI has no `auth status` subcommand, so auth uses indirect detection.
 * Output is structured stream-json (NDJSON events) via `--output-format stream-json`.
 * Prompts are delivered as positional arguments (`gemini "prompt" --flags`).
 *
 * @see Issue #629 - Audit and fix Gemini CLI adapter against latest release
 * @see Issue #1051 - Upgrade CLI flags and stream-json output parsing
 * @see Issue #1052 - Add Gemini auth validation cascade
 * @see Issue #2596 - Standardize adapter error messages
 */

import type { SDKQueryFunction } from "../../orchestrator/StageExecutor.js";
import type {
  ICliAdapter,
  OrchestrationCapability,
  ValidateAuthOptions,
  QueryFunctionOptions,
} from "./ICliAdapter.js";
import { verifyCLIInstalled } from "./validateCLIAuth.js";
import { createCliQueryFn, parseCliArgs } from "./cliQueryHelper.js";
import { AdapterError, throwTimeoutError } from "./errors.js";
import { resolveAndValidateModel } from "./modelPreflight.js";

const ADAPTER_NAME = "Gemini";
const GEMINI_DOCS_URL = "https://ai.google.dev/gemini-api/docs";
const GEMINI_INSTALL_CMD = "npm install -g @google/gemini-cli";

/** Timeout for gcloud auth check to prevent hangs (matches Claude adapter pattern). */
const GCLOUD_AUTH_TIMEOUT_MS = 10_000;

/**
 * Minimum Gemini CLI version known to be compatible with stream-json output.
 * Used for a preflight warning (not a hard block) when the version is unrecognized.
 */
const MIN_KNOWN_VERSION = "0.29.0";

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

export class GeminiAdapter implements ICliAdapter {
  readonly name = "gemini" as const;
  readonly displayName = "Gemini";
  readonly cliCommand = "gemini";
  // gemini CLI — agentic tool loop (yolo mode).
  readonly agentic = true;

  async validateAuth(options?: ValidateAuthOptions): Promise<"passed"> {
    const cwd = options?.cwd ?? process.cwd();
    const runner = options?.runner;

    if (!runner) {
      return "passed";
    }

    // Verify Gemini CLI is installed and check version (warning only)
    const versionResult = await verifyCLIInstalled({
      command: this.cliCommand,
      runner,
      cwd,
      adapterName: ADAPTER_NAME,
      installCmd: GEMINI_INSTALL_CMD,
      docsUrl: GEMINI_DOCS_URL,
    });

    const versionMatch = versionResult.stdout.trim().match(/(\d+\.\d+\.\d+)/);
    if (versionMatch) {
      const detected = versionMatch[1];
      if (compareVersions(detected, MIN_KNOWN_VERSION) < 0) {
        console.warn(
          `[gemini-adapter] WARNING: Gemini CLI version ${detected} is older than ` +
            `minimum known compatible version ${MIN_KNOWN_VERSION}. ` +
            `Some features may not work as expected.`
        );
      }
    }

    // Auth cascade: check cheapest methods first (env vars), then gcloud fallback.
    // Gemini CLI has no `auth status` subcommand, so we use indirect detection.

    // 1. GEMINI_API_KEY — instant, deterministic
    if (process.env.GEMINI_API_KEY?.trim()) {
      return "passed";
    }

    // 2. Vertex AI — GOOGLE_API_KEY + GOOGLE_GENAI_USE_VERTEXAI=true
    if (process.env.GOOGLE_GENAI_USE_VERTEXAI === "true" && process.env.GOOGLE_API_KEY?.trim()) {
      return "passed";
    }

    // 3. Google OAuth via gcloud — requires spawning a process
    const gcloudResult = await runner("gcloud", ["auth", "print-access-token"], cwd);
    if (gcloudResult.code === 0) {
      return "passed";
    }

    if (gcloudResult.code === 124) {
      throwTimeoutError(
        ADAPTER_NAME,
        "`gcloud auth print-access-token`",
        GCLOUD_AUTH_TIMEOUT_MS,
        "Verify gcloud auth works manually, or set GEMINI_API_KEY to bypass."
      );
    }

    // 4. All methods failed — actionable error message listing all auth options
    throw new AdapterError(
      "No Gemini authentication detected.\n\n" +
        "Configure one of the following:\n" +
        "  1. Set GEMINI_API_KEY environment variable (get key: aistudio.google.com/apikey)\n" +
        "  2. Set GOOGLE_API_KEY + GOOGLE_GENAI_USE_VERTEXAI=true for Vertex AI\n" +
        "  3. Run `gcloud auth login` for Google OAuth (free tier: 60 req/min)\n" +
        `Docs: ${GEMINI_DOCS_URL}`,
      "AUTH_MISSING",
      ADAPTER_NAME,
      GEMINI_DOCS_URL
    );
  }

  async createQueryFunction(_options?: QueryFunctionOptions): Promise<SDKQueryFunction> {
    const command = process.env.NIGHTGAUGE_GEMINI_CLI_COMMAND ?? this.cliCommand;
    const args = parseCliArgs(process.env.NIGHTGAUGE_GEMINI_CLI_ARGS, [
      "--output-format",
      "stream-json",
    ]);

    // Inject model routing — set by skillRunner via NIGHTGAUGE_GEMINI_MODEL
    // (mirrors CodexAdapter/GeminiSdkAdapter). resolveAndValidateModel (#4021)
    // resolves tier aliases and fails fast on an invalid Gemini model before
    // it reaches the CLI. Without this the CLI silently fell back to its own
    // configured default, ignoring pipeline model routing entirely (#53).
    const geminiModel = resolveAndValidateModel(
      "gemini",
      process.env.NIGHTGAUGE_GEMINI_MODEL ?? process.env.NIGHTGAUGE_MODEL
    );
    if (geminiModel) {
      args.push("--model", geminiModel);
    }

    return createCliQueryFn({
      command,
      args,
      adapter: this.name,
      promptDelivery: "positional",
    });
  }

  getDefaultArgs(): string[] {
    return ["--output-format", "stream-json"];
  }

  getOrchestrationCapability(): OrchestrationCapability {
    return "sdk-fanout";
  }

  requiresDirectApiKey(): boolean {
    return false;
  }
}
