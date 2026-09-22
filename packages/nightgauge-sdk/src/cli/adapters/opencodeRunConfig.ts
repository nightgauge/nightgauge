/**
 * The SDK's OpenCode run config provider (#1648): each stage's per-run
 * OpenCode config, isolation environment, plugin handshake and vetted binary
 * come from `nightgauge opencode config`, the Go verb that runs the Go
 * adapter's own pre-dispatch checks and PrepareOpenCodeRun. No TypeScript here
 * builds any part of the config: this module runs the verb, validates what it
 * printed, and hands it on unchanged. There is no fallback: a verb that fails,
 * times out, prints something unreadable or a schema_version whose major this
 * module does not know fails the stage before any `opencode` process starts.
 *
 * The verb is run with `execFile` and an argv array (never a shell, so a
 * worktree path is one argv element whatever it holds), a 15 s timeout and a
 * bounded output. Its stderr reaches an error only redacted, and neither the
 * config nor an env value is ever logged.
 *
 * @see docs/decisions/022-opencode-multi-provider-adapter.md
 * @see cmd/nightgauge/opencode.go — the verb
 */

import { execFile as nodeExecFile } from "node:child_process";
import { basename, dirname, isAbsolute } from "node:path";
import { z } from "zod";

import { AdapterError, type AdapterErrorCategory } from "./errors.js";
import { openCodeRedactor } from "./opencodeStream.js";
import type {
  OpenCodeRunConfig,
  OpenCodeRunConfigProvider,
  OpenCodeRunConfigRequest,
} from "./OpenCodeAdapter.js";
import { isRunIdentity } from "../../context/runIdentity.js";

const ADAPTER_NAME = "OpenCode";
const ADR = "docs/decisions/022-opencode-multi-provider-adapter.md";

/** How long the verb may run. */
export const OPENCODE_CONFIG_VERB_TIMEOUT_MS = 15_000;
/** The most the verb may print on stdout or stderr. */
export const OPENCODE_CONFIG_VERB_MAX_BUFFER = 8 * 1024 * 1024;
/** The schema_version major this module reads (`OpenCodeConfigSchemaVersion` in Go). */
export const OPENCODE_CONFIG_SCHEMA_MAJOR = 1;
/** The most of the verb's stderr an error carries. */
const STDERR_TAIL = 4000;

/** The shape of `nightgauge opencode config --json` (`adapters.OpenCodeRun` in Go). */
const verbOutputSchema = z.object({
  schema_version: z.string(),
  config_content: z.string().min(1),
  env: z.record(z.string(), z.string()),
  env_withhold: z.object({
    prefixes: z.array(z.string()),
    names: z.array(z.string()),
  }),
  plugin_dir: z.string().min(1),
  run_dir: z.string().min(1),
  non_loopback: z.boolean(),
  binary: z.string().min(1),
  plugin_version: z.string().min(1),
  run_id: z.string().min(1),
});

/** The part of `node:child_process` execFile this module calls. */
export type OpenCodeConfigExecFile = (
  file: string,
  args: readonly string[],
  options: {
    env: NodeJS.ProcessEnv;
    cwd?: string;
    timeout: number;
    maxBuffer: number;
    encoding: "utf8";
    windowsHide: boolean;
  },
  callback: (error: ExecFileError | null, stdout: string, stderr: string) => void
) => unknown;

/** What execFile's callback error carries. */
export interface ExecFileError extends Error {
  code?: string | number | null;
  killed?: boolean;
  signal?: NodeJS.Signals | null;
}

export interface OpenCodeRunConfigProviderOptions {
  /** The environment the verb runs in and the binary is resolved from; default `process.env`. */
  env?: NodeJS.ProcessEnv;
  /** Runs the verb; default `node:child_process` execFile. */
  execFile?: OpenCodeConfigExecFile;
}

/**
 * The nightgauge binary: NIGHTGAUGE_BIN (the extension exports it), which
 * must be an absolute path, else `nightgauge` looked up on the environment's
 * PATH.
 */
export function resolveNightgaugeBinary(env: NodeJS.ProcessEnv): string {
  const bin = env.NIGHTGAUGE_BIN;
  if (bin === undefined || bin === "") return "nightgauge";
  if (!isAbsolute(bin)) {
    fail(
      `NIGHTGAUGE_BIN ${JSON.stringify(bin)} is not an absolute path; set it to the nightgauge ` +
        "binary's absolute path, or unset it to use the nightgauge on PATH",
      "CONFIG_INVALID"
    );
  }
  return bin;
}

/** `owner/name`, the form the verb's --repo takes. */
const REPO_RE = /^[A-Za-z0-9][A-Za-z0-9._-]*\/[A-Za-z0-9._-]+$/;

/**
 * The verb's --skills-root for a stage whose SKILL.md the SDK resolved in
 * `skillDir` (`<root>/skills/<stage skill>`, loadStageSkill): `<root>`, the
 * convention skillrender.DefaultRoots searches, so the verb builds the
 * permission map from the SKILL.md the stage's prompt came from. Refused
 * when the stage's skill directory is unknown or not under a `skills`
 * directory: the verb would otherwise resolve skills from wherever this
 * process started.
 */
export function openCodeSkillsRoot(skillDir: string | undefined): string {
  if (skillDir === undefined || skillDir === "") {
    fail(
      "the stage's skill directory is not known, so the verb could not build the stage's " +
        "permission map from its SKILL.md; run the stage through PipelineOrchestrator, which " +
        "resolves it",
      "CONFIG_INVALID"
    );
  }
  if (!isAbsolute(skillDir) || basename(dirname(skillDir)) !== "skills") {
    fail(
      `the stage's skill directory ${JSON.stringify(skillDir)} is not an absolute ` +
        "<root>/skills/<skill> path, the layout the verb's --skills-root searches",
      "CONFIG_INVALID"
    );
  }
  return dirname(dirname(skillDir));
}

/** The verb's argv for one request: every value its own element. */
export function openCodeConfigVerbArgs(request: OpenCodeRunConfigRequest): string[] {
  const args = [
    "opencode",
    "config",
    "--stage",
    request.stage ?? "",
    "--worktree",
    request.worktree,
    "--model",
    request.model,
    "--skills-root",
    openCodeSkillsRoot(request.skillDir),
  ];
  if (request.repo !== undefined && REPO_RE.test(request.repo)) args.push("--repo", request.repo);
  // The stage's turn budget, the Go dispatch's RunOptions.MaxTurns: the verb
  // makes it the steps cap of the build agent and each subagent.
  if (
    request.maxTurns !== undefined &&
    Number.isInteger(request.maxTurns) &&
    request.maxTurns > 0
  ) {
    args.push("--max-turns", String(request.maxTurns));
  }
  // The run's identity, the Go dispatch's RunOptions.RunID: the stages of one
  // run share its root. Like the Go manager, a value that is not a run
  // identity is not passed, and the verb mints a root of its own.
  if (request.runId !== undefined && isRunIdentity(request.runId)) {
    args.push("--run-id", request.runId);
  }
  args.push("--json");
  return args;
}

function fail(reason: string, code: AdapterErrorCategory): never {
  throw new AdapterError(
    `the per-run OpenCode config could not be obtained from \`nightgauge opencode config\`, so no ` +
      `opencode process is started: ${reason}. See ${ADR} § 8`,
    code,
    ADAPTER_NAME
  );
}

/** A redactor for the verb's stderr: every credential-named variable's value, then every credential shape. */
function stderrRedactor(env: NodeJS.ProcessEnv): (s: string) => string {
  const secrets: Record<string, string | undefined> = {};
  for (const [name, value] of Object.entries(env)) {
    if (/KEY|TOKEN|SECRET|PASSWORD/i.test(name)) secrets[name] = value;
  }
  return openCodeRedactor(secrets);
}

function stderrTail(stderr: string, redact: (s: string) => string): string {
  const text = redact(stderr.trim());
  if (text === "") return "it printed nothing on stderr";
  const tail = text.length > STDERR_TAIL ? `…${text.slice(-STDERR_TAIL)}` : text;
  return `its stderr:\n${tail}`;
}

/**
 * Validate the verb's stdout and turn it into the adapter's run config. An
 * unknown schema_version major is refused before the rest is read.
 */
export function parseOpenCodeConfigVerbOutput(stdout: string): OpenCodeRunConfig {
  let raw: unknown;
  try {
    raw = JSON.parse(stdout);
  } catch {
    fail("its output is not JSON", "CONFIG_INVALID");
  }
  const version =
    typeof raw === "object" && raw !== null
      ? (raw as { schema_version?: unknown }).schema_version
      : undefined;
  if (typeof version !== "string" || !/^[0-9]+\.[0-9]+$/.test(version)) {
    fail("its output has no schema_version of the form <major>.<minor>", "CONFIG_INVALID");
  }
  const major = Number(version.split(".")[0]);
  if (major !== OPENCODE_CONFIG_SCHEMA_MAJOR) {
    fail(
      `its schema_version ${version} has major ${major}, and this SDK reads major ` +
        `${OPENCODE_CONFIG_SCHEMA_MAJOR} only: use a nightgauge binary and SDK from the same release`,
      "VERSION_MISMATCH"
    );
  }
  const parsed = verbOutputSchema.safeParse(raw);
  if (!parsed.success) {
    const fields = [...new Set(parsed.error.issues.map((i) => i.path.join(".") || "(root)"))];
    fail(`its output is missing or mistypes ${fields.join(", ")}`, "CONFIG_INVALID");
  }
  const out = parsed.data;
  return {
    configContent: out.config_content,
    env: out.env,
    runDir: out.run_dir,
    binary: out.binary,
    pluginVersion: out.plugin_version,
    envWithhold: out.env_withhold,
    runId: out.run_id,
  };
}

/**
 * The run config provider the SDK's OpenCode adapter uses by default: one run
 * of `nightgauge opencode config` per stage.
 */
export function createOpenCodeRunConfigProvider(
  options: OpenCodeRunConfigProviderOptions = {}
): OpenCodeRunConfigProvider {
  const env = options.env ?? process.env;
  const execFile = options.execFile ?? (nodeExecFile as unknown as OpenCodeConfigExecFile);
  return async (request) => {
    if (request.stage === undefined || request.stage.trim() === "") {
      fail(
        "the stage is not known, and the verb builds the config for a stage (its permission map " +
          "comes from the stage's allowed tools)",
        "CONFIG_INVALID"
      );
    }
    if (request.repo !== undefined && !REPO_RE.test(request.repo)) {
      console.warn(
        `[opencode-adapter] the stage's repository ${JSON.stringify(request.repo)} is not ` +
          "owner/name, so it is not passed to `nightgauge opencode config` and the stage gets no " +
          "MCP server from it"
      );
    }
    const bin = resolveNightgaugeBinary(env);
    const args = openCodeConfigVerbArgs(request);
    const redact = stderrRedactor(env);
    const stdout = await new Promise<string>((resolvePromise, reject) => {
      try {
        execFile(
          bin,
          args,
          {
            env,
            // The worktree, never wherever this process started: the verb
            // resolves the forge identity for the MCP read from its cwd.
            cwd: request.worktree,
            timeout: OPENCODE_CONFIG_VERB_TIMEOUT_MS,
            maxBuffer: OPENCODE_CONFIG_VERB_MAX_BUFFER,
            encoding: "utf8",
            windowsHide: true,
          },
          (error, out, errOut) => {
            if (error === null) {
              resolvePromise(out);
              return;
            }
            try {
              if (error.code === "ENOENT") {
                fail(
                  `${bin} was not found: install nightgauge, or set NIGHTGAUGE_BIN to its absolute path`,
                  "BINARY_NOT_FOUND"
                );
              }
              if (error.code === "ERR_CHILD_PROCESS_STDIO_MAXBUFFER") {
                fail(
                  `it printed more than ${OPENCODE_CONFIG_VERB_MAX_BUFFER} bytes`,
                  "CONFIG_INVALID"
                );
              }
              if (error.killed) {
                fail(
                  `it did not finish within ${OPENCODE_CONFIG_VERB_TIMEOUT_MS / 1000} s and was killed; ` +
                    stderrTail(errOut ?? "", redact),
                  "TIMEOUT"
                );
              }
              fail(
                `it refused the dispatch (exit code ${String(error.code)}); ${stderrTail(errOut ?? "", redact)}`,
                "CONFIG_INVALID"
              );
            } catch (e) {
              reject(e);
            }
          }
        );
      } catch (e) {
        reject(e);
      }
    });
    return parseOpenCodeConfigVerbOutput(stdout);
  };
}

/**
 * Deletes a run's per-run root with `nightgauge opencode cleanup --run-id`,
 * the SDK twin of the Go scheduler's CleanupOpenCodeRunRoot (ADR-022 § 22).
 * The root's location is Go's alone; this only names the run.
 */
export type OpenCodeRunRootCleaner = (runId: string) => Promise<void>;

export function createOpenCodeRunRootCleaner(
  options: OpenCodeRunConfigProviderOptions = {}
): OpenCodeRunRootCleaner {
  const env = options.env ?? process.env;
  const execFile = options.execFile ?? (nodeExecFile as unknown as OpenCodeConfigExecFile);
  return (runId) =>
    new Promise<void>((resolvePromise, reject) => {
      try {
        const bin = resolveNightgaugeBinary(env);
        const redact = stderrRedactor(env);
        execFile(
          bin,
          ["opencode", "cleanup", "--run-id", runId],
          {
            env,
            timeout: OPENCODE_CONFIG_VERB_TIMEOUT_MS,
            maxBuffer: OPENCODE_CONFIG_VERB_MAX_BUFFER,
            encoding: "utf8",
            windowsHide: true,
          },
          (error, _out, errOut) => {
            if (error === null) resolvePromise();
            else
              reject(
                new Error(
                  `nightgauge opencode cleanup --run-id ${runId} failed; ${stderrTail(errOut ?? "", redact)}`
                )
              );
          }
        );
      } catch (e) {
        reject(e);
      }
    });
}
