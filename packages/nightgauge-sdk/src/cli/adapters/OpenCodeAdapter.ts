/**
 * OpenCode CLI adapter — `opencode run --format json`, one adapter reaching
 * local model servers (LM Studio, Ollama) and hosted providers, chosen per
 * dispatch by the provider-qualified model OpenCode's `-m` takes.
 *
 * The SDK twin of the Go adapter (internal/execution/adapters/opencode.go):
 * the same argv, the prompt on stdin, the same model check and credential
 * refusals, a child environment curated to the dispatched provider
 * (childEnv.ts), and the same stream parsing, failure detection, subagent
 * roll-up and cost (opencodeStream.ts).
 *
 * Every spawn runs under the per-run OpenCode config, isolation environment,
 * plugin handshake and vetted binary that `nightgauge opencode config` prints
 * ({@link OpenCodeRunConfigProvider}, opencodeRunConfig.ts, #1648): the Go
 * verb is the one authority, and no TypeScript builds any part of the config.
 * A spawn whose plugin handshake fails is killed and fails the stage
 * (opencodeHandshake.ts, #1804).
 *
 * @see docs/decisions/022-opencode-multi-provider-adapter.md
 * @see Issue #1637
 */

import type { spawn as nodeSpawn } from "node:child_process";
import { isAbsolute, relative, resolve } from "node:path";

import type { SDKQueryFunction } from "../../orchestrator/StageExecutor.js";
import { isLocalProvider, providerFor } from "../../eval/modelRegistry.js";
import type {
  ICliAdapter,
  OrchestrationCapability,
  QueryFunctionOptions,
  ValidateAuthOptions,
} from "./ICliAdapter.js";
import { verifyCLIInstalled } from "./validateCLIAuth.js";
import { AdapterError } from "./errors.js";
import { ADAPTER_COMPAT } from "./adapterCompat.generated.js";
import {
  OPENCODE_CONFIG_CONTENT_ENV,
  OPENCODE_DISABLE_FLAGS,
  OPENCODE_ISOLATION_XDG,
  OPENCODE_PLUGIN_NONCE_ENV,
  OPENCODE_PLUGIN_PATH_ENV,
  OPENCODE_PLUGIN_SENTINEL_ENV,
  isOpenCodeRunEnvAccepted,
} from "./childEnv.js";
import { createOpenCodeRunConfigProvider } from "./opencodeRunConfig.js";
import {
  OPENCODE_PLATFORM_PROVIDERS,
  openCodeProviderEnv,
  openCodeProviderKey,
} from "./opencodeCatalog.js";
import { createCliQueryFn } from "./cliQueryHelper.js";

const ADAPTER_NAME = "OpenCode";
const OPENCODE_DOCS_URL = "https://opencode.ai/docs/cli/";
/**
 * The remediation for a missing or refused opencode: the managed install of
 * the max-tested build and its pin, as the Go refusals word it
 * (`OpenCodeManagedInstall` in opencode_preflight.go). A global install of
 * the latest build would be refused again by the version policy once it is
 * newer than max-tested.
 */
const OPENCODE_INSTALL_CMD =
  `npm i --prefix ~/.nightgauge/tools/opencode opencode-ai@${ADAPTER_COMPAT.opencode.maxTested}, ` +
  "then pin it with opencode.binary: ~/.nightgauge/tools/opencode/node_modules/.bin/opencode " +
  "(as an absolute path) in ~/.nightgauge/config.yaml";
const ADR = "docs/decisions/022-opencode-multi-provider-adapter.md";

/**
 * The oldest opencode version Nightgauge supports, from the compat manifest
 * (internal/adaptercompat/manifests/opencode.json, the floor the Go doctor
 * and dispatch read). Below it the adapter fails closed, as the manifest's
 * `fail_closed` floor policy says, unlike the warn-only floors of the other
 * CLIs.
 */
export const OPENCODE_MIN_KNOWN_VERSION = ADAPTER_COMPAT.opencode.minVersion;

/**
 * The shape a model must have before it reaches argv: `<provider>/<model>`,
 * with no whitespace or control character anywhere. The adapter also holds it
 * to the Go adapter's rule ({@link validateOpenCodeModel}).
 */
// eslint-disable-next-line no-control-regex -- the control range is what the check refuses
export const OPENCODE_MODEL_ID_RE = /^[a-z0-9][a-z0-9._-]*\/[^\s\x00-\x1f]+$/;

/**
 * The shape of an OpenCode provider key, and of an operator endpoint id: no
 * leading dash, so it never reads as a flag, and no dot, so a host name or an
 * address never becomes one (ADR-022 § 1; `openCodeProviderKeyRE` in Go).
 */
const OPENCODE_PROVIDER_KEY_RE = /^[a-z0-9][a-z0-9-]*$/;

/** Go's `unicode.IsSpace || unicode.IsControl`. */
const SPACE_OR_CONTROL_RE = /[\p{White_Space}\p{Cc}]/u;

/**
 * Flags the adapter never emits (ADR-022 § The command, § 9, § 18): each one
 * approves tool calls without the permission map, publishes or resumes a
 * session, or opens a listener.
 */
export const OPENCODE_FORBIDDEN_FLAGS: readonly string[] = Object.freeze([
  "--auto",
  "--yolo",
  "--dangerously-skip-permissions",
  "--share",
  "--continue",
  "--port",
  "--mdns",
  "--cors",
]);

/**
 * The `opencode run` argv for one stage, identical to the Go adapter's
 * `BuildCommand`: `run --format json --print-logs --log-level ERROR -m
 * <provider/model> --dir <worktree>`. The prompt is never on argv: it goes on
 * stdin (ADR-022 § 19).
 */
export function buildOpenCodeArgv(model: string, worktree: string): string[] {
  return [
    "run",
    "--format",
    "json",
    "--print-logs",
    "--log-level",
    "ERROR",
    "-m",
    model,
    "--dir",
    worktree,
  ];
}

/**
 * Refuse a model that cannot safely reach `-m`, with `CONFIG_INVALID`: it must
 * match {@link OPENCODE_MODEL_ID_RE}, its provider key must be a provider-key
 * shape, and its model id must not start with `-` or hold whitespace or a
 * control character (`OpenCodeModelArg` in Go). A bare id or a tier band is
 * refused too: the adapter never infers a provider.
 */
export function validateOpenCodeModel(model: string): void {
  const slash = model.indexOf("/");
  const key = slash < 0 ? "" : model.slice(0, slash);
  const id = slash < 0 ? "" : model.slice(slash + 1);
  if (
    !OPENCODE_MODEL_ID_RE.test(model) ||
    !OPENCODE_PROVIDER_KEY_RE.test(key) ||
    id.startsWith("-") ||
    SPACE_OR_CONTROL_RE.test(id)
  ) {
    throw new AdapterError(
      `model ${JSON.stringify(model)} is not valid for the opencode adapter: set the stage model to ` +
        `<provider>/<model>, such as lmstudio/<model-id> or anthropic/<model-id>. The provider key is ` +
        `lowercase letters, digits and '-', and the model id must not start with '-' or contain ` +
        `whitespace or a control character. See ${ADR} § 1`,
      "CONFIG_INVALID",
      ADAPTER_NAME
    );
  }
}

/**
 * ADR-022 § 17 before spawn (`openCodeCredentialRefusal` in Go): a model on a
 * platform provider is refused; an `anthropic/` model needs
 * `ANTHROPIC_API_KEY`, and is never served by a subscription or OAuth login; a
 * local provider needs no key; another hosted provider OpenCode's catalog
 * knows needs one of the variables the catalog binds to it. A provider key the
 * catalog does not know, such as a declared endpoint's id, gets its provider
 * block from the per-run config.
 */
export function openCodeCredentialRefusal(model: string, env: NodeJS.ProcessEnv): void {
  const key = openCodeProviderKey(model);
  if (OPENCODE_PLATFORM_PROVIDERS.includes(key)) {
    throw new AdapterError(
      `model ${JSON.stringify(model)} is refused: provider "${key}" authenticates with the credentials ` +
        `of a platform account the stage keeps for its own tools, which can be a subscription or OAuth ` +
        `login, and an OpenCode stage authenticates only with a model provider's own API-key variable. ` +
        `Name a provider that has one, such as anthropic/<model> with ANTHROPIC_API_KEY. See ${ADR} § 17`,
      "CONFIG_INVALID",
      ADAPTER_NAME
    );
  }
  if (key === "anthropic") {
    if (env.ANTHROPIC_API_KEY) return;
    throw new AdapterError(
      `model ${JSON.stringify(model)} is refused: ANTHROPIC_API_KEY is not set, and an anthropic/ stage ` +
        `through OpenCode authenticates only with that variable, never with a subscription or OAuth ` +
        `login. Set ANTHROPIC_API_KEY in the environment that runs nightgauge, or run the Anthropic ` +
        `model on the claude-headless adapter (NIGHTGAUGE_ADAPTER=claude-headless), which serves a ` +
        `Claude subscription. See ${ADR} § 17`,
      "AUTH_MISSING",
      ADAPTER_NAME
    );
  }
  if (isLocalProvider(providerFor("opencode", model))) return;
  // No OPENCODE_* variable reaches the child (childEnv.ts), OpenCode's own
  // OPENCODE_API_KEY included, so it is never the key a dispatch relies on.
  const vars = openCodeProviderEnv(model).filter((name) => !name.startsWith("OPENCODE_"));
  if (vars.length === 0 || vars.some((name) => env[name])) return;
  throw new AdapterError(
    `model ${JSON.stringify(model)} is refused: provider "${key}" needs its API-key variable, ` +
      `${vars.join(" or ")}, and none is set. An OpenCode stage authenticates only with the ` +
      `provider's own variable. See ${ADR} § 17`,
    "AUTH_MISSING",
    ADAPTER_NAME
  );
}

/** Semver-ish order of two `x.y.z[-pre]` versions; a pre-release sorts below its release. */
function compareVersions(a: string, b: string): number {
  const [coreA, preA] = a.split("+")[0].split(/-(.*)/s);
  const [coreB, preB] = b.split("+")[0].split(/-(.*)/s);
  const pa = coreA.split(".").map(Number);
  const pb = coreB.split(".").map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] ?? 0) - (pb[i] ?? 0);
    if (d !== 0) return d;
  }
  if (preA && !preB) return -1;
  if (!preA && preB) return 1;
  return 0;
}

/** The version `opencode --version` prints: its first line, as the Go fold reads it. */
const OPENCODE_VERSION_RE = /^[0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.+-]*$/;

// ---------------------------------------------------------------------------
// The per-run config (#1648)
// ---------------------------------------------------------------------------

/** What a stage's opencode spawn is given, as `nightgauge opencode config` prints it. */
export interface OpenCodeRunConfig {
  /** OPENCODE_CONFIG_CONTENT, the per-run OpenCode config (`config_content`). */
  readonly configContent: string;
  /**
   * The isolation variables the spawn sets from the run (`env`): the run
   * root's four XDG directories, the OPENCODE_DISABLE_* switches and the
   * tool pins. No credential.
   */
  readonly env: Readonly<Record<string, string>>;
  /** The run's private root (`run_dir`), where the post-run helpers run. */
  readonly runDir: string;
  /**
   * The `plugin_version` the Nightgauge plugin's handshake sentinel must
   * carry (`plugin_version`); the nonce and the sentinel path are in `env`.
   */
  readonly pluginVersion: string;
  /**
   * The absolute path of the opencode binary the verb's version policy vetted
   * (`binary`), spawned instead of whatever `opencode` PATH finds.
   */
  readonly binary?: string;
  /**
   * The inherited variables the spawn must not get (`env_withhold`): every
   * name starting with one of `prefixes` or listed in `names`.
   */
  readonly envWithhold?: {
    readonly prefixes: readonly string[];
    readonly names: readonly string[];
  };
}

/** What the config is built for. */
export interface OpenCodeRunConfigRequest {
  /** The `<provider>/<model>` the stage is dispatched to. */
  readonly model: string;
  /** The absolute worktree the stage runs in. */
  readonly worktree: string;
  /** The pipeline stage; the verb needs it. */
  readonly stage?: string;
  /** The repository the stage works on, as `owner/name`: its MCP servers come from its default branch. */
  readonly repo?: string;
}

/** Builds a stage's per-run config and isolation environment (opencodeRunConfig.ts). */
export type OpenCodeRunConfigProvider = (
  request: OpenCodeRunConfigRequest
) => Promise<OpenCodeRunConfig>;

function invalidRunConfig(reason: string): never {
  throw new AdapterError(
    `the run config provider returned an unusable OpenCode run config: ${reason}. See ${ADR} § 8`,
    "CONFIG_INVALID",
    ADAPTER_NAME
  );
}

/**
 * Hold a provider's run config to what an isolated spawn needs: a config, the
 * run's root, the four XDG directories at absolute paths, every
 * OPENCODE_DISABLE_* switch set to "1", and no variable outside the run's own
 * set, so a provider can neither pass a credential nor an operator OpenCode
 * variable to the child.
 */
function checkRunConfig(config: unknown): OpenCodeRunConfig {
  if (typeof config !== "object" || config === null) invalidRunConfig("it is not an object");
  const c = config as Partial<OpenCodeRunConfig>;
  if (typeof c.configContent !== "string" || c.configContent.trim() === "") {
    invalidRunConfig(`it has no ${OPENCODE_CONFIG_CONTENT_ENV}`);
  }
  if (typeof c.runDir !== "string" || !isAbsolute(c.runDir)) {
    invalidRunConfig("its run directory is not an absolute path");
  }
  if (typeof c.env !== "object" || c.env === null) invalidRunConfig("it has no environment");
  const env = c.env as Record<string, unknown>;
  for (const [name, value] of Object.entries(env)) {
    if (!isOpenCodeRunEnvAccepted(name))
      invalidRunConfig(`it sets ${name}, which is not a run variable`);
    if (typeof value !== "string") invalidRunConfig(`its ${name} is not a string`);
  }
  for (const name of OPENCODE_ISOLATION_XDG) {
    const v = env[name];
    if (typeof v !== "string" || !isAbsolute(v)) {
      invalidRunConfig(`its ${name} is not an absolute path in the run's root`);
    }
  }
  for (const name of OPENCODE_DISABLE_FLAGS) {
    if (env[name] !== "1") invalidRunConfig(`it does not set ${name}=1`);
  }
  // NIGHTGAUGE_OPENCODE_PLUGIN_PATH is `run.PluginDir`-rooted
  // (`filepath.Join(root, "config", "opencode", "nightgauge-plugin")`,
  // opencode_config.go), and NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL is
  // `opencodeplugin.SentinelPath`'s `runDir` branch — both always under the
  // run's own root for a config this provider builds. The plugin's own init
  // writes the sentinel file verbatim at this path (fs.writeFileSync,
  // plugin/nightgauge.js), truncating whatever is already there, so an
  // out-of-root or relative value here is refused rather than trusted.
  for (const name of [OPENCODE_PLUGIN_PATH_ENV, OPENCODE_PLUGIN_SENTINEL_ENV]) {
    const v = env[name];
    if (v === undefined) continue;
    const rel = relative(c.runDir!, v as string);
    if (!isAbsolute(v as string) || rel === "" || rel.startsWith("..") || isAbsolute(rel)) {
      invalidRunConfig(`its ${name} is not an absolute path in the run's root`);
    }
  }
  if (
    env[OPENCODE_CONFIG_CONTENT_ENV] !== undefined &&
    env[OPENCODE_CONFIG_CONTENT_ENV] !== c.configContent
  ) {
    invalidRunConfig(`its env ${OPENCODE_CONFIG_CONTENT_ENV} differs from its config`);
  }
  // The plugin handshake (#1804): without it a spawn could run with the
  // careful-gate plugin never loaded and nothing would notice.
  for (const name of [
    OPENCODE_PLUGIN_PATH_ENV,
    OPENCODE_PLUGIN_NONCE_ENV,
    OPENCODE_PLUGIN_SENTINEL_ENV,
  ]) {
    if (typeof env[name] !== "string" || env[name] === "") {
      invalidRunConfig(`it sets no ${name}, so the plugin handshake cannot be verified`);
    }
  }
  if (typeof c.pluginVersion !== "string" || c.pluginVersion === "") {
    invalidRunConfig("it names no plugin version, so the plugin handshake cannot be verified");
  }
  if (c.binary !== undefined && (typeof c.binary !== "string" || !isAbsolute(c.binary))) {
    invalidRunConfig("its opencode binary is not an absolute path");
  }
  const withhold = c.envWithhold;
  if (
    withhold !== undefined &&
    (typeof withhold !== "object" ||
      withhold === null ||
      !Array.isArray(withhold.prefixes) ||
      !Array.isArray(withhold.names) ||
      ![...withhold.prefixes, ...withhold.names].every((n) => typeof n === "string"))
  ) {
    invalidRunConfig("its withheld-variable set is not two lists of names");
  }
  return {
    configContent: c.configContent!,
    env: env as Record<string, string>,
    runDir: c.runDir!,
    pluginVersion: c.pluginVersion!,
    ...(c.binary !== undefined && { binary: c.binary }),
    ...(withhold !== undefined && { envWithhold: withhold }),
  };
}

// ---------------------------------------------------------------------------
// The adapter
// ---------------------------------------------------------------------------

/** What an {@link OpenCodeAdapter} is built from; every field has a production default. */
export interface OpenCodeAdapterOptions {
  /**
   * Builds each stage's per-run config; default: `nightgauge opencode config`
   * (createOpenCodeRunConfigProvider) run in {@link env}.
   */
  runConfigProvider?: OpenCodeRunConfigProvider;
  /** The environment the adapter reads and curates for the child; default `process.env`. */
  env?: NodeJS.ProcessEnv;
  /**
   * The dispatched `<provider>/<model>`; default NIGHTGAUGE_MODEL. A tier band
   * is refused. The same model is passed to `nightgauge opencode config` as
   * --model, so the per-run config and `-m` always name the same model.
   */
  model?: string;
  /** Spawns processes; default `node:child_process` spawn. */
  spawn?: typeof nodeSpawn;
}

export class OpenCodeAdapter implements ICliAdapter {
  readonly name = "opencode" as const;
  readonly displayName = "OpenCode";
  readonly cliCommand = "opencode";
  /** `opencode run` drives a real tool loop (bash, edit, read, grep, task, …). */
  readonly agentic = true;

  private readonly runConfigProvider: OpenCodeRunConfigProvider;
  private readonly env: NodeJS.ProcessEnv;
  private readonly model?: string;
  private readonly spawn?: typeof nodeSpawn;

  constructor(options: OpenCodeAdapterOptions = {}) {
    this.env = options.env ?? process.env;
    this.runConfigProvider =
      options.runConfigProvider ?? createOpenCodeRunConfigProvider({ env: this.env });
    this.model = options.model;
    this.spawn = options.spawn;
  }

  /** The dispatched model, or `undefined` when none is configured. */
  private dispatchModel(): string | undefined {
    const model = this.model ?? this.env.NIGHTGAUGE_MODEL;
    return model === undefined || model === "" ? undefined : model;
  }

  /**
   * With a runner: the binary is on PATH and `opencode --version` is at least
   * {@link OPENCODE_MIN_KNOWN_VERSION}, failing closed below it or when no
   * version can be read. Always, when a model is configured: the model check
   * and the credential refusals of ADR-022 § 17.
   */
  async validateAuth(options?: ValidateAuthOptions): Promise<"passed"> {
    const runner = options?.runner;
    if (runner) {
      const cwd = options?.cwd ?? process.cwd();
      const result = await verifyCLIInstalled({
        command: this.cliCommand,
        runner,
        cwd,
        adapterName: ADAPTER_NAME,
        installCmd: OPENCODE_INSTALL_CMD,
        docsUrl: OPENCODE_DOCS_URL,
      });
      const version = result.stdout.trim().split("\n")[0].trim();
      if (!OPENCODE_VERSION_RE.test(version)) {
        throw new AdapterError(
          `opencode --version printed no version, so it cannot be checked against the minimum ` +
            `supported version ${OPENCODE_MIN_KNOWN_VERSION}; the opencode adapter fails closed.\n` +
            `Fix: ${OPENCODE_INSTALL_CMD}`,
          "VERSION_MISMATCH",
          ADAPTER_NAME,
          OPENCODE_DOCS_URL
        );
      }
      if (compareVersions(version, OPENCODE_MIN_KNOWN_VERSION) < 0) {
        const message =
          `opencode ${version} is below the minimum supported version ${OPENCODE_MIN_KNOWN_VERSION} ` +
          `(internal/adaptercompat/manifests/opencode.json).\nFix: ${OPENCODE_INSTALL_CMD}`;
        if (ADAPTER_COMPAT.opencode.floorPolicy === "fail_closed") {
          throw new AdapterError(message, "VERSION_MISMATCH", ADAPTER_NAME, OPENCODE_DOCS_URL);
        }
        console.warn(`[opencode-adapter] WARNING: ${message}`);
      }
    }
    const model = this.dispatchModel();
    if (model !== undefined) {
      validateOpenCodeModel(model);
      openCodeCredentialRefusal(model, this.env);
    }
    return "passed";
  }

  /**
   * Check the model and its credentials, then obtain the stage's per-run config
   * through the run config provider (`nightgauge opencode config` by default)
   * and return the query function. No opencode process is spawned here, and
   * none at all when a check or the provider refuses.
   */
  async createQueryFunction(options?: QueryFunctionOptions): Promise<SDKQueryFunction> {
    const model = this.dispatchModel();
    if (model === undefined) {
      throw new AdapterError(
        "the opencode adapter needs a model: set NIGHTGAUGE_MODEL to " +
          "<provider>/<model>, such as lmstudio/<model-id> or anthropic/<model-id>. Without -m " +
          `OpenCode would run the model its own config names. See ${ADR} § The command`,
        "CONFIG_INVALID",
        ADAPTER_NAME
      );
    }
    validateOpenCodeModel(model);
    openCodeCredentialRefusal(model, this.env);
    const worktree = resolve(options?.cwd ?? process.cwd());
    const stage = options?.stage;
    const repo = this.env.NIGHTGAUGE_TARGET_REPO || this.env.NIGHTGAUGE_REPO || undefined;
    const runConfig = checkRunConfig(
      await this.runConfigProvider({ model, worktree, stage, ...(repo && { repo }) })
    );
    return createCliQueryFn({
      command: runConfig.binary ?? this.cliCommand,
      args: buildOpenCodeArgv(model, worktree),
      adapter: this.name,
      promptDelivery: "stdin",
      openCode: { model, worktree, stage, runConfig, parentEnv: this.env, spawn: this.spawn },
    });
  }

  getDefaultArgs(): string[] {
    return ["run", "--format", "json", "--print-logs", "--log-level", "ERROR"];
  }

  /** ADR-022 § 16: OpenCode's `task` subagents are not a workflow backend; fan-out is portable. */
  getOrchestrationCapability(): OrchestrationCapability {
    return "sdk-fanout";
  }

  /** The key a hosted provider needs is checked per model (validateAuth), not demanded up front. */
  requiresDirectApiKey(): boolean {
    return false;
  }
}
