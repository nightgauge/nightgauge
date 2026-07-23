/**
 * Adapter preflight checks - Validates auth, branch, and docs before execution.
 *
 * Auth validation now delegates to the adapter instances via the registry.
 * Branch/docs checks remain here as shared infrastructure.
 *
 * @see Issue #627 - Extract ICliAdapter interface & unify types
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import { spawn } from "node:child_process";
import { resolveAdapter, type IncrediAdapter } from "./adapter.js";
import { defaultRegistry, isAgenticAdapter } from "./adapters/AdapterRegistry.js";
import { validateModelForAdapter } from "./adapters/modelPreflight.js";
import { AdapterError } from "./adapters/errors.js";

export interface PreflightCommandResult {
  code: number;
  stdout: string;
  stderr: string;
}

export type PreflightCommandRunner = (
  command: string,
  args: string[],
  cwd: string
) => Promise<PreflightCommandResult>;

export interface AdapterPreflightResult {
  adapterAuth: "passed";
  branchState: "passed";
  docsPreconditions: "passed";
  githubAuth?: "passed";
}

export class CodexPreflightError extends Error {
  constructor(
    message: string,
    public readonly check:
      "adapter-auth" | "gh-auth" | "branch-state" | "docs-preconditions" | "model-validation"
  ) {
    super(message);
    this.name = "CodexPreflightError";
  }
}

/**
 * Portable adapter preflight must not assume the consumer repository uses
 * Nightgauge's own documentation layout. Callers that enforce repository
 * documentation prerequisites can still pass `requiredDocs` explicitly.
 */
const DEFAULT_REQUIRED_DOCS: string[] = [];

/**
 * The `NIGHTGAUGE_*_MODEL` env var that configures each adapter's model.
 * Used by the model-validation preflight phase (#4021) to read the configured
 * model for the active adapter. Adapters with no model env (claude-*) fall back
 * to the generic NIGHTGAUGE_MODEL.
 */
const MODEL_ENV_BY_ADAPTER: Partial<Record<IncrediAdapter, string>> = {
  codex: "NIGHTGAUGE_CODEX_MODEL",
  gemini: "NIGHTGAUGE_GEMINI_MODEL",
  "gemini-sdk": "NIGHTGAUGE_GEMINI_MODEL",
  "lm-studio": "NIGHTGAUGE_LM_STUDIO_MODEL",
  ollama: "NIGHTGAUGE_OLLAMA_MODEL",
  copilot: "NIGHTGAUGE_COPILOT_MODEL",
};

/**
 * Model↔provider validation (#4021): fail fast when the configured model is
 * invalid for the active adapter, surfacing the actionable AdapterError message
 * during preflight rather than as an opaque CLI error at spawn time. A no-op for
 * adapters with OPEN model sets (claude-*, ollama, lm-studio, copilot).
 */
function validateModelEnvForAdapter(adapter: IncrediAdapter, env: NodeJS.ProcessEnv): void {
  const modelEnvVar = MODEL_ENV_BY_ADAPTER[adapter];
  const configuredModel = modelEnvVar ? env[modelEnvVar] : env.NIGHTGAUGE_MODEL;
  try {
    validateModelForAdapter(adapter, configuredModel);
  } catch (error) {
    if (error instanceof AdapterError) {
      throw new CodexPreflightError(error.format(), "model-validation");
    }
    throw error;
  }
}

/** Timeout for `claude auth status` to prevent hangs in non-interactive contexts. */
const CLAUDE_AUTH_TIMEOUT_MS = 10_000;

async function runCommand(
  command: string,
  args: string[],
  cwd: string,
  timeoutMs?: number
): Promise<PreflightCommandResult> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, stdio: "pipe" });
    let stdout = "";
    let stderr = "";
    let timedOut = false;
    let timer: ReturnType<typeof setTimeout> | undefined;

    if (timeoutMs && timeoutMs > 0) {
      timer = setTimeout(() => {
        timedOut = true;
        child.kill("SIGTERM");
      }, timeoutMs);
    }

    child.stdout.on("data", (data) => {
      stdout += String(data);
    });
    child.stderr.on("data", (data) => {
      stderr += String(data);
    });
    child.on("error", (err) => {
      if (timer) clearTimeout(timer);
      reject(err);
    });
    child.on("close", (code) => {
      if (timer) clearTimeout(timer);
      if (timedOut) {
        resolve({ code: 124, stdout, stderr: stderr || "Command timed out" });
      } else {
        resolve({ code: code ?? 1, stdout, stderr });
      }
    });
  });
}

/**
 * The default subprocess-based {@link PreflightCommandRunner}. Auth/preflight
 * callers that supply this runner perform REAL CLI probes (e.g.
 * `codex login status`, `claude auth status`); callers that omit a runner get
 * the no-op "passed" short-circuit each adapter implements (the documented
 * `validateAuth() resolves to passed without a runner` contract). Each spawn is
 * bounded by a SIGTERM timeout so a hung CLI cannot leak a child process.
 *
 * @see Issue #4031 — the Adapter Doctor and the pipeline auth gate inject this
 *   so CLI-adapter auth status is actually probed rather than silently passed.
 */
export function createDefaultPreflightRunner(timeoutMs = 10_000): PreflightCommandRunner {
  return (command, args, cwd) => runCommand(command, args, cwd, timeoutMs);
}

async function exists(filePath: string): Promise<boolean> {
  try {
    await fs.access(filePath);
    return true;
  } catch {
    return false;
  }
}

async function runBranchAndDocsPreflight(options: {
  cwd: string;
  runner: PreflightCommandRunner;
  requiredDocs: string[];
  allowMainBranch?: boolean;
  requireCleanWorkingTree?: boolean;
}): Promise<Pick<AdapterPreflightResult, "branchState" | "docsPreconditions">> {
  const branch = await options.runner("git", ["branch", "--show-current"], options.cwd);
  if (branch.code !== 0) {
    throw new CodexPreflightError(
      "Adapter preflight failed: unable to determine current git branch.",
      "branch-state"
    );
  }

  const branchName = branch.stdout.trim();
  const isMainBranch = branchName === "main" || branchName === "master";
  if (!branchName || (isMainBranch && !options.allowMainBranch)) {
    throw new CodexPreflightError(
      "Adapter preflight failed: branch must be a feature branch, not main/master.",
      "branch-state"
    );
  }

  const gitStatus = await options.runner("git", ["status", "--porcelain"], options.cwd);
  if (gitStatus.code !== 0) {
    throw new CodexPreflightError(
      "Adapter preflight failed: unable to validate git working tree state.",
      "branch-state"
    );
  }

  if (options.requireCleanWorkingTree && gitStatus.stdout.trim().length > 0) {
    throw new CodexPreflightError(
      "Adapter preflight failed: working tree must be clean before stage execution.",
      "branch-state"
    );
  }

  const missingDocs: string[] = [];
  for (const doc of options.requiredDocs) {
    const docPath = path.join(options.cwd, doc);
    if (!(await exists(docPath))) {
      missingDocs.push(doc);
    }
  }

  if (missingDocs.length > 0) {
    throw new CodexPreflightError(
      `Adapter preflight failed: missing required documentation prerequisites: ${missingDocs.join(", ")}`,
      "docs-preconditions"
    );
  }

  return {
    branchState: "passed",
    docsPreconditions: "passed",
  };
}

/**
 * Validate GitHub auth for Codex adapter (needed for sandbox environments).
 *
 * Kept as a standalone function because it's Codex-specific (sandbox isolation).
 */
async function validateGithubAuthForCodex(
  runner: PreflightCommandRunner,
  cwd: string,
  env: NodeJS.ProcessEnv
): Promise<void> {
  const hasTokenAuth = Boolean(env.GH_TOKEN?.trim() || env.GITHUB_TOKEN?.trim());
  if (hasTokenAuth) {
    return;
  }

  let ghAuth: PreflightCommandResult;
  try {
    ghAuth = await runner("gh", ["auth", "status"], cwd);
  } catch (error) {
    const details = error instanceof Error ? `\nDetails: ${error.message}` : "";
    throw new CodexPreflightError(
      "Codex preflight failed: GitHub auth is unavailable in this execution environment. " +
        "If running in a sandbox, host `gh` keychain auth may be inaccessible. " +
        "Provide `GH_TOKEN` or `GITHUB_TOKEN`, or rerun with permissions that allow `gh auth status`." +
        details,
      "gh-auth"
    );
  }

  if (ghAuth.code !== 0) {
    const details = `${ghAuth.stderr}\n${ghAuth.stdout}`.trim();
    throw new CodexPreflightError(
      "Codex preflight failed: GitHub auth is unavailable in this execution environment. " +
        "If running in a sandbox, host `gh` keychain auth may be inaccessible. " +
        "Provide `GH_TOKEN` or `GITHUB_TOKEN`, or rerun with permissions that allow `gh auth status`." +
        (details ? `\nDetails: ${details}` : ""),
      "gh-auth"
    );
  }

  // Auth may be valid but API connectivity can still fail inside sandboxed execution.
  const ghApiProbe = await runner("gh", ["api", "rate_limit"], cwd);
  if (ghApiProbe.code !== 0) {
    const details = `${ghApiProbe.stderr}\n${ghApiProbe.stdout}`.trim();
    throw new CodexPreflightError(
      "Codex preflight failed: GitHub API is unreachable from this execution environment. " +
        "Retry with network-enabled/elevated execution, or provide a reachable environment for `gh api` calls." +
        (details ? `\nDetails: ${details}` : ""),
      "gh-auth"
    );
  }
}

export async function runAdapterPreflightChecks(options?: {
  adapter?: IncrediAdapter;
  cwd?: string;
  runner?: PreflightCommandRunner;
  env?: NodeJS.ProcessEnv;
  requiredDocs?: string[];
  stage?: string;
  allowMainBranch?: boolean;
  requireCleanWorkingTree?: boolean;
  requireGithubAuth?: boolean;
}): Promise<AdapterPreflightResult> {
  const cwd = options?.cwd ?? process.cwd();
  const runner = options?.runner ?? runCommand;
  const requiredDocs = options?.requiredDocs ?? DEFAULT_REQUIRED_DOCS;
  const env = options?.env ?? process.env;
  const adapter = options?.adapter ?? resolveAdapter();
  const allowMainBranch = options?.allowMainBranch ?? options?.stage === "issue-pickup";
  const requireCleanWorkingTree =
    options?.requireCleanWorkingTree ?? (options?.stage ? options.stage === "issue-pickup" : true);
  const requireGithubAuth =
    options?.requireGithubAuth ??
    (options?.stage
      ? options.stage === "issue-pickup" ||
        options.stage === "pr-create" ||
        options.stage === "pr-merge"
      : true);

  // Agentic truth-gate (#57): chat-completion-only adapters have no tool
  // loop — a pipeline stage dispatched to them emits prose instead of
  // commits. Hard-fail before any other check. Eval/judge/summarization
  // surfaces do not run this preflight and keep chat-only adapters.
  if (!isAgenticAdapter(adapter)) {
    throw new AdapterError(
      `The ${adapter} adapter is chat-completion-only (no agentic tool loop): ` +
        `pipeline stages cannot edit files, run shell commands, or call gh through it. ` +
        `Set NIGHTGAUGE_ADAPTER to an agentic adapter (claude-sdk, claude-headless, codex, gemini, copilot). ` +
        `Chat-only adapters remain available for eval/judge surfaces.`,
      "CONFIG_INVALID",
      adapter
    );
  }

  const commonChecks = await runBranchAndDocsPreflight({
    cwd,
    runner,
    requiredDocs,
    allowMainBranch,
    requireCleanWorkingTree,
  });

  // Model↔provider validation (#4021): runs for every adapter, before auth, so
  // an invalid (adapter, model) pair fails preflight with a clear remediation
  // message instead of an opaque CLI error at spawn time.
  validateModelEnvForAdapter(adapter, env);

  // Delegate auth validation to adapter instance via registry
  const adapterInstance = defaultRegistry.get(adapter);

  if (adapter === "codex") {
    // Codex adapter: validate CLI auth via adapter, then GitHub auth separately
    await adapterInstance.validateAuth({ runner, cwd });
    if (requireGithubAuth) {
      await validateGithubAuthForCodex(runner, cwd, env);
    }
    return {
      adapterAuth: "passed",
      githubAuth: "passed",
      ...commonChecks,
    };
  }

  if (adapter === "claude-headless") {
    // When using the default runner, wrap with timeout for claude auth status
    // to prevent hangs in non-interactive contexts (Issue #626)
    const claudeRunner: PreflightCommandRunner = options?.runner
      ? runner
      : (cmd, args, runCwd) => runCommand(cmd, args, runCwd, CLAUDE_AUTH_TIMEOUT_MS);
    await adapterInstance.validateAuth({ runner: claudeRunner, cwd });
    return {
      adapterAuth: "passed",
      ...commonChecks,
    };
  }

  if (adapter === "gemini") {
    // Gemini adapter: validate CLI + auth cascade (env vars, then gcloud fallback).
    // Wrap runner with timeout for gcloud auth check (Issue #1052).
    const geminiRunner: PreflightCommandRunner = options?.runner
      ? runner
      : (cmd, args, runCwd) => runCommand(cmd, args, runCwd, CLAUDE_AUTH_TIMEOUT_MS);
    await adapterInstance.validateAuth({ runner: geminiRunner, cwd });
    return {
      adapterAuth: "passed",
      ...commonChecks,
    };
  }

  // claude-sdk: minimal checks (API key validated elsewhere)
  return {
    adapterAuth: "passed",
    ...commonChecks,
  };
}

export async function runCodexPreflightChecks(options?: {
  cwd?: string;
  runner?: PreflightCommandRunner;
  env?: NodeJS.ProcessEnv;
  requiredDocs?: string[];
  stage?: string;
  allowMainBranch?: boolean;
  requireCleanWorkingTree?: boolean;
  requireGithubAuth?: boolean;
}): Promise<AdapterPreflightResult> {
  return runAdapterPreflightChecks({
    adapter: "codex",
    cwd: options?.cwd,
    runner: options?.runner,
    env: options?.env,
    requiredDocs: options?.requiredDocs,
    stage: options?.stage,
    allowMainBranch: options?.allowMainBranch,
    requireCleanWorkingTree: options?.requireCleanWorkingTree,
    requireGithubAuth: options?.requireGithubAuth,
  });
}
