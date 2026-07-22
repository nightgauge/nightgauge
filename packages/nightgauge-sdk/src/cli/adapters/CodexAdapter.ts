/**
 * Codex Adapter - Uses the Codex CLI for stage execution.
 *
 * Authenticates via `codex login status` (the only valid auth check in Codex CLI 0.98+).
 *
 * @see Issue #627 - Extract ICliAdapter interface & unify types
 * @see Issue #628 - Audit Codex CLI adapter against latest release
 */

import type { SDKQueryFunction } from "../../orchestrator/StageExecutor.js";
import type {
  ICliAdapter,
  OrchestrationCapability,
  ValidateAuthOptions,
  QueryFunctionOptions,
} from "./ICliAdapter.js";
import { validateCLIAuth, verifyCLIInstalled } from "./validateCLIAuth.js";
import { createCliQueryFn, parseCliArgs } from "./cliQueryHelper.js";
import { resolveAndValidateModel } from "./modelPreflight.js";

// Re-export the canonical alias resolver so existing import sites (and the
// adapter barrel) keep resolving it from CodexAdapter. The implementation now
// lives in the single-source-of-truth registry (#4018).
export { resolveCodexModelAlias } from "./codexModelRegistry.js";

const ADAPTER_NAME = "Codex";
const CODEX_DOCS_URL = "https://developers.openai.com/codex";
const CODEX_INSTALL_CMD = "npm install -g @openai/codex";

/**
 * Pipeline stages that do not benefit from persistent session state.
 *
 * These stages perform discrete, stateless operations (reading GitHub data,
 * validating output, creating PRs) and run faster without session overhead.
 *
 * Override with NIGHTGAUGE_CODEX_EPHEMERAL_STAGES (comma-separated list)
 * or set NIGHTGAUGE_CODEX_EPHEMERAL=true to make all stages ephemeral.
 */
const DEFAULT_EPHEMERAL_STAGES: ReadonlySet<string> = new Set([
  "issue-pickup",
  "feature-validate",
  "pr-create",
  "pr-merge",
]);

/**
 * Canonical base args for an autonomous, non-interactive Codex run.
 *
 * `--dangerously-bypass-approvals-and-sandbox` disables BOTH the filesystem
 * sandbox and approval prompts in a single flag — the documented mode for
 * "ephemeral, fully sandboxed environments like CI"
 * (https://developers.openai.com/codex/cli/reference). The pipeline runs in an
 * externally-isolated environment and must write outside the workspace (git ref
 * updates, context handoff files) without ever blocking on an approval prompt.
 * This replaces the now-deprecated `--full-auto` flag and matches the flag the
 * `exec resume` path already uses (see cliQueryHelper.ts).
 *
 * Returns a fresh array each call because callers mutate it (push `--model`,
 * spread `--ephemeral`). Single source of truth shared by createQueryFunction()
 * and getDefaultArgs() so the two cannot diverge.
 */
function getCodexBaseArgs(): string[] {
  return ["exec", "--dangerously-bypass-approvals-and-sandbox", "--json"];
}

/**
 * Returns true when the given stage should run with --ephemeral.
 *
 * Resolution order (first match wins):
 * 1. NIGHTGAUGE_CODEX_EPHEMERAL=true  → all stages ephemeral
 * 2. NIGHTGAUGE_CODEX_EPHEMERAL_STAGES=a,b,c → named stages ephemeral
 * 3. DEFAULT_EPHEMERAL_STAGES              → built-in set
 *
 * When stage is undefined, returns false (safe default: persistent).
 */
export function isEphemeralStage(stage: string | undefined): boolean {
  if (!stage) return false;

  const globalEphemeral = process.env.NIGHTGAUGE_CODEX_EPHEMERAL;
  if (globalEphemeral === "true" || globalEphemeral === "1") return true;

  const stageOverride = process.env.NIGHTGAUGE_CODEX_EPHEMERAL_STAGES;
  if (stageOverride) {
    const stages = stageOverride
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    return stages.includes(stage);
  }

  return DEFAULT_EPHEMERAL_STAGES.has(stage);
}

/**
 * Minimum Codex CLI version known to be compatible.
 * Used for a preflight warning (not a hard block) when the version is unrecognized.
 */
const MIN_KNOWN_VERSION = "0.111.0";

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

export class CodexAdapter implements ICliAdapter {
  readonly name = "codex" as const;
  readonly displayName = "Codex";
  readonly cliCommand = "codex";
  // codex exec — sandbox-scoped agentic tool loop (#4026).
  readonly agentic = true;

  async validateAuth(options?: ValidateAuthOptions): Promise<"passed"> {
    const cwd = options?.cwd ?? process.cwd();
    const runner = options?.runner;

    if (!runner) {
      return "passed";
    }

    // Verify Codex CLI is installed and check version (warning only)
    const versionResult = await verifyCLIInstalled({
      command: "codex",
      runner,
      cwd,
      adapterName: ADAPTER_NAME,
      installCmd: CODEX_INSTALL_CMD,
      docsUrl: CODEX_DOCS_URL,
    });

    const versionMatch = versionResult.stdout.trim().match(/(\d+\.\d+\.\d+)/);
    if (versionMatch) {
      const detected = versionMatch[1];
      if (compareVersions(detected, MIN_KNOWN_VERSION) < 0) {
        // Warning only — do not block, as newer compatible versions may also work
        console.warn(
          `[codex-adapter] WARNING: Codex CLI version ${detected} is older than ` +
            `minimum known compatible version ${MIN_KNOWN_VERSION}. ` +
            `Some features may not work as expected.`
        );
      }
    }

    // Issue #628: Only `codex login status` is valid in Codex CLI 0.98+.
    // `codex auth status` and `codex login --status` are invalid commands.
    return validateCLIAuth({
      command: "codex",
      authSubcommands: [{ args: ["login", "status"] }],
      runner,
      cwd,
      adapterName: ADAPTER_NAME,
      errorCategory: "AUTH_MISSING",
      loginHint: "codex login",
      docsUrl: CODEX_DOCS_URL,
    });
  }

  async createQueryFunction(options?: QueryFunctionOptions): Promise<SDKQueryFunction> {
    const command = process.env.NIGHTGAUGE_CODEX_CLI_COMMAND ?? "codex";
    const baseArgs = parseCliArgs(process.env.NIGHTGAUGE_CODEX_CLI_ARGS, getCodexBaseArgs());

    // Append --ephemeral for stateless stages. --ephemeral is mutually exclusive
    // with session resume flags; guard against combining them.
    const ephemeral = isEphemeralStage(options?.stage);
    const hasResume = baseArgs.includes("--resume") || baseArgs.includes("resume");
    if (ephemeral && hasResume) {
      throw new Error(
        `[codex-adapter] --ephemeral and session resume cannot be used together ` +
          `(stage: ${options?.stage ?? "unknown"}). Remove --resume from ` +
          `NIGHTGAUGE_CODEX_CLI_ARGS or disable ephemeral for this stage.`
      );
    }

    const args = ephemeral ? [...baseArgs, "--ephemeral"] : baseArgs;

    // Inject model routing — set by skillRunner via NIGHTGAUGE_CODEX_MODEL
    // (Issue #1656). resolveAndValidateModel (#4021) resolves tier aliases AND
    // fails fast with an actionable AdapterError if the configured model is not
    // a valid Codex model, before it ever reaches the CLI as --model.
    const codexModel = resolveAndValidateModel("codex", process.env.NIGHTGAUGE_CODEX_MODEL);
    if (codexModel) {
      args.push("--model", codexModel);
    }

    return createCliQueryFn({ command, args, adapter: this.name });
  }

  getDefaultArgs(): string[] {
    return getCodexBaseArgs();
  }

  getOrchestrationCapability(): OrchestrationCapability {
    // Codex is a first-class fan-out participant driven by the engine over
    // `codex exec --ephemeral` (#3911). It has no native token usage in its
    // JSONL output (spike #2587), so its WorkflowAgentUsage is `estimated:true`.
    return "sdk-fanout";
  }

  requiresDirectApiKey(): boolean {
    return false;
  }
}
