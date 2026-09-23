/**
 * Least-privilege child-process environment curation (#4094, fan-out F4).
 *
 * Every spawned CLI agent — including the up-to-1000x fan-out workers — used to
 * inherit the FULL parent `process.env` (see cliQueryHelper.ts), handing each
 * sub-process every secret the orchestrator happened to hold (database URLs,
 * cloud keys, unrelated service tokens), in direct contradiction of
 * standards/security.md's least-privilege principle.
 *
 * `curateChildEnv` is the single choke point: it returns a NEW environment
 * containing ONLY the variables a CLI adapter legitimately needs to run and
 * authenticate — system/runtime essentials, the provider auth + routing vars,
 * and the project's own `NIGHTGAUGE_*` / `CLAUDE_CODE_*` config namespaces.
 * Everything else (a Stripe key, an AWS secret, a DB password) is withheld.
 *
 * Deny-by-default: a variable is forwarded only if it matches the allowlist.
 *
 * @see docs/security/WORKFLOW_FANOUT_SECURITY.md — F4 scoped permissions
 * @see Issue #4094 - Scope spawned/fanned-out agent env to a least-privilege allowlist
 * @see Issue #1637 - the OpenCode curation, {@link curateOpenCodeChildEnv}
 */

import {
  OPENCODE_NIGHTGAUGE_ALLOW,
  openCodeProviderEnv,
  openCodeWithholdsNightgaugeEnv,
} from "./opencodeCatalog.js";

export { OPENCODE_NIGHTGAUGE_ALLOW, openCodeWithholdsNightgaugeEnv };

/**
 * System/runtime variables a spawned CLI needs to function. Notably PATH (to
 * find the CLI binary) and HOME (to resolve `~/.claude`, `~/.codex`, `gh`
 * config) — stripping these breaks every adapter.
 */
const SYSTEM_ALLOW = new Set<string>([
  "PATH",
  "HOME",
  "USER",
  "LOGNAME",
  "SHELL",
  "TERM",
  "TMPDIR",
  "TMP",
  "TEMP",
  "LANG",
  "LANGUAGE",
  "LC_ALL",
  "LC_CTYPE",
  "TZ",
  "PWD",
  "COLUMNS",
  "LINES",
  "XDG_CONFIG_HOME",
  "XDG_CACHE_HOME",
  "XDG_DATA_HOME",
  // Windows essentials (no-ops on POSIX hosts).
  "SystemRoot",
  "SystemDrive",
  "ComSpec",
  "PATHEXT",
  "WINDIR",
  "APPDATA",
  "LOCALAPPDATA",
  "USERPROFILE",
  "ProgramFiles",
  "ProgramData",
]);

/**
 * Provider auth + routing variables the CLI adapters legitimately read. This is
 * the union of every `process.env.*` auth/routing read across the adapters
 * (ClaudeHeadless/ClaudeSdk, Codex, Copilot, Gemini) — the childEnv guard test
 * asserts no adapter reads a name absent from the allowlist, so this set cannot
 * silently drift out of sync.
 */
const PROVIDER_ALLOW = new Set<string>([
  // Anthropic / Claude CLI
  "ANTHROPIC_API_KEY",
  "ANTHROPIC_AUTH_TOKEN",
  "ANTHROPIC_BASE_URL",
  // GitHub / Copilot
  "GH_TOKEN",
  "GITHUB_TOKEN",
  "COPILOT_GITHUB_TOKEN",
  "GH_HOST",
  // OpenAI / Codex
  "OPENAI_API_KEY",
  "CODEX_HOME",
  // Google / Gemini
  "GEMINI_API_KEY",
  "GOOGLE_API_KEY",
  "GOOGLE_GENAI_USE_VERTEXAI",
  "GOOGLE_APPLICATION_CREDENTIALS",
  // xAI / Grok Build
  "XAI_API_KEY",
  "GROK_HOME",
]);

/**
 * Variable-name prefixes that are forwarded wholesale: the project's own config
 * namespace and the Claude CLI's own namespace. These are not secrets the way a
 * provider token is — they are pipeline configuration the spawned stage needs.
 */
const ALLOW_PREFIXES = ["NIGHTGAUGE_", "CLAUDE_CODE_"] as const;

/**
 * Whether a single environment variable name passes the least-privilege
 * allowlist. Exported so the guard test can assert every adapter env read is
 * covered.
 */
export function isChildEnvAllowed(key: string, extraAllow?: ReadonlySet<string>): boolean {
  return (
    SYSTEM_ALLOW.has(key) ||
    PROVIDER_ALLOW.has(key) ||
    (extraAllow?.has(key) ?? false) ||
    ALLOW_PREFIXES.some((p) => key.startsWith(p))
  );
}

/**
 * Return a curated copy of `parentEnv` containing only allowlisted variables.
 * Pure: does not mutate the input or `process.env`.
 *
 * @param parentEnv - Source environment (defaults to `process.env`).
 * @param extraAllow - Additional exact variable names to permit for this call.
 */
export function curateChildEnv(
  parentEnv: NodeJS.ProcessEnv = process.env,
  extraAllow: readonly string[] = []
): NodeJS.ProcessEnv {
  const extra = new Set(extraAllow);
  const curated: NodeJS.ProcessEnv = {};
  for (const [key, value] of Object.entries(parentEnv)) {
    if (value === undefined) continue;
    if (isChildEnvAllowed(key, extra)) {
      curated[key] = value;
    }
  }
  return curated;
}

// ---------------------------------------------------------------------------
// OpenCode (#1637, ADR-022 § 8, § 17)
// ---------------------------------------------------------------------------
//
// `curateChildEnv` is one allowlist for every adapter, so a local OpenCode run
// would still see OPENAI_API_KEY, and OpenCode loads every catalog provider
// whose variable is set. OpenCode also reads its own state from the XDG
// directories and takes config, logins and a session-sharing switch from
// OPENCODE_* variables. So an opencode child gets its own curation, the
// allowlist twin of the Go adapter's withhold rule (OpenCodeWithholdsEnv in
// internal/execution/adapters/opencode_isolation.go):
//
//   - the system essentials, without the operator's XDG directories: the run's
//     own four replace them, so OpenCode reads none of the operator's config,
//     logins, plugins or sessions;
//   - the forge variables the bash tool's `gh` and `git` need;
//   - the variables OpenCode's catalog binds to the dispatched provider, and
//     no other provider's;
//   - the NIGHTGAUGE_* variables the stage and its plugin read
//     (OPENCODE_NIGHTGAUGE_ALLOW), and no other: the namespace also holds
//     operator secrets such as NIGHTGAUGE_LM_STUDIO_API_KEY and
//     NIGHTGAUGE_JIRA_TOKEN;
//   - no inherited OPENCODE_* variable at all. The OPENCODE_* names a spawn
//     carries are the ones Nightgauge sets, from the run's config or the
//     adapter, never the operator's OPENCODE_PERMISSION, OPENCODE_AUTO_SHARE,
//     OPENCODE_CONFIG*, OPENCODE_SERVER_PASSWORD or OPENCODE_AUTH_CONTENT.
//
// CLAUDE_CODE_* is not forwarded: it is the Claude CLI's namespace, which
// holds a subscription login, and an OpenCode stage authenticates with a
// provider's API-key variable only (ADR-022 § 17).

/** The XDG base directories the per-run root replaces (ADR-022 § 8). */
export const OPENCODE_ISOLATION_XDG = Object.freeze([
  "XDG_CONFIG_HOME",
  "XDG_DATA_HOME",
  "XDG_CACHE_HOME",
  "XDG_STATE_HOME",
] as const);

/**
 * The OpenCode switches every spawn sets to "1" (ADR-022 § 10, § 11, § 15), a
 * copy of `openCodeDisableFlags` in internal/execution/adapters/opencode_isolation.go.
 */
export const OPENCODE_DISABLE_FLAGS = Object.freeze([
  "OPENCODE_DISABLE_MODELS_FETCH",
  "OPENCODE_DISABLE_AUTOUPDATE",
  "OPENCODE_DISABLE_LSP_DOWNLOAD",
  "OPENCODE_DISABLE_DEFAULT_PLUGINS",
  "OPENCODE_DISABLE_SHARE",
  "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT",
  "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS",
  "OPENCODE_DISABLE_EXTERNAL_SKILLS",
] as const);

/**
 * The per-spawn server password (ADR-022 § 18). The adapter mints a fresh
 * value for every spawn; an inherited one never reaches the child.
 */
export const OPENCODE_SERVER_PASSWORD_ENV = "OPENCODE_SERVER_PASSWORD";

/** The per-run OpenCode config, as the run's config provider builds it (ADR-022 § 8). */
export const OPENCODE_CONFIG_CONTENT_ENV = "OPENCODE_CONFIG_CONTENT";

/**
 * The nightgauge plugin's path and handshake variables, the TS twin of
 * `opencodeplugin.EnvPluginPath` / `EnvNonce` / `EnvSentinel`
 * (internal/execution/opencodeplugin/plugin.go): `InstallNightgaugePlugin`
 * (internal/execution/adapters/opencode.go) sets all three on every run with
 * a run identity, and `nightgauge opencode config --json` prints the same
 * three for parity with the Go adapter's own spawn path (#1804). Their
 * values are a per-run plugin file path, a minted nonce and a per-run
 * sentinel file path — none of them an operator path — so forwarding them to
 * an opencode child carries no leak.
 */
export const OPENCODE_PLUGIN_PATH_ENV = "NIGHTGAUGE_OPENCODE_PLUGIN_PATH";
export const OPENCODE_PLUGIN_NONCE_ENV = "NIGHTGAUGE_OPENCODE_PLUGIN_NONCE";
export const OPENCODE_PLUGIN_SENTINEL_ENV = "NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL";

/**
 * `OPENCODE_DISABLE_PROJECT_CONFIG` is set by `InstallNightgaugePlugin`
 * itself (opencode.go, literal at the call site, not one of
 * `opencodeplugin`'s exported `Env*` constants), not by the blanket
 * `openCodeDisableFlags` every spawn sets regardless (opencode_isolation.go's
 * doc comment on that slice says so explicitly) — but
 * `InstallNightgaugePlugin` runs on every OpenCode dispatch today, so it is
 * every spawn all the same (ADR-022 amendment 2026-09-14). Its value is
 * always the literal `"1"`, forwarded the same as the blanket disable flags.
 */
export const OPENCODE_DISABLE_PROJECT_CONFIG_ENV = "OPENCODE_DISABLE_PROJECT_CONFIG";

/**
 * The isolated per-run HOME `OpenCodeIsolationEnv` sets whenever
 * `opencode.inherit_user_config` is false — the default
 * (internal/execution/adapters/opencode_isolation.go, `env["HOME"] =
 * filepath.Join(in.Root, "home")`) — carried in `OpenCodeRun.Env` alongside
 * the XDG directories (opencode_config.go). Forwarding it is not optional:
 * `SYSTEM_ALLOW` already passes an *inherited* HOME through to every child
 * ({@link isOpenCodeChildEnvAllowed}), so without this run variable able to
 * override it, an opencode child would silently keep the operator's real
 * HOME instead of the run's isolated one — precisely the isolation ADR-022
 * § 8 exists to prevent.
 */
const OPENCODE_HOME_ENV = "HOME";

/**
 * Every variable the run's own environment may set on an opencode spawn AND
 * have actually reach the child (curateOpenCodeChildEnv applies exactly this
 * set): the isolation variables `nightgauge opencode config` prints as `env`
 * (ADR-022 § 8, OpenCodeIsolationEnv in the Go adapter), the per-run config,
 * the isolated HOME, `OPENCODE_DISABLE_PROJECT_CONFIG`, and the plugin/
 * handshake variables above. OPENCODE_CONFIG_DIR is set only when the
 * operator opted into their own OpenCode config (opencode.inherit_user_config).
 *
 * A run config may carry one more name, `NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK`
 * ({@link OPENCODE_RUN_ENV_WITHHELD_NAMES}): `checkRunConfig` accepts it —
 * `nightgauge opencode config --json` legitimately prints it — but it is not
 * in this set, so it is never applied to the child.
 */
const OPENCODE_RUN_ENV_NAMES: ReadonlySet<string> = new Set<string>([
  ...OPENCODE_ISOLATION_XDG,
  ...OPENCODE_DISABLE_FLAGS,
  OPENCODE_CONFIG_CONTENT_ENV,
  OPENCODE_DISABLE_PROJECT_CONFIG_ENV,
  OPENCODE_HOME_ENV,
  "OPENCODE_CONFIG_DIR",
  "GH_CONFIG_DIR",
  "NIGHTGAUGE_CONFIG_HOME",
  "NIGHTGAUGE_STATE_HOME",
  "GOCACHE",
  OPENCODE_PLUGIN_PATH_ENV,
  OPENCODE_PLUGIN_NONCE_ENV,
  OPENCODE_PLUGIN_SENTINEL_ENV,
]);

/**
 * `NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK` (`opencodeplugin.EnvOperatorInstallRisk`,
 * internal/execution/opencodeplugin/plugin.go), accepted by `checkRunConfig`
 * but never applied to a child's environment — the TS twin of the Go
 * adapter's own `BuildCommand`, which deletes this name from the child's env
 * right before returning it (`opencode.go`, `delete(env,
 * opencodeplugin.EnvOperatorInstallRisk)`, pinned by
 * `TestOpenCodeBuildCommandWithholdsOperatorInstallRiskFromTheChild`) with the
 * comment "this marker is manager-only ... must not reach the opencode child
 * process". #1802's child-env half of that leak is already closed on the Go
 * side by that delete; only the config verb's *printed* `env` still carries
 * the name, because the verb prints `RunRoot.Env` directly without going
 * through `BuildCommand`'s own withhold. So `checkRunConfig` must accept the
 * name — refusing it would fail closed on every machine where an operator's
 * `$HOME/.opencode` or an inherited `OPENCODE_CONFIG_DIR` happens to be
 * unsatisfied, which the operator neither set nor controls — while
 * `curateOpenCodeChildEnv` must still never apply it, mirroring
 * `BuildCommand`'s delete rather than reopening the leak on this spawn path.
 * A separate set (rather than adding the name to {@link OPENCODE_RUN_ENV_NAMES})
 * is required because that set has exactly one meaning used on both axes:
 * `checkRunConfig` accepts a name in it, and `curateOpenCodeChildEnv` forwards
 * one. This name needs "accepted" true and "forwarded" false, which only a
 * second set can express.
 */
const OPENCODE_RUN_ENV_WITHHELD_NAMES: ReadonlySet<string> = new Set<string>([
  "NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK",
]);

/** Whether the run's own environment may set `name` on an opencode spawn, applied to the child. */
export function isOpenCodeRunEnvName(name: string): boolean {
  return OPENCODE_RUN_ENV_NAMES.has(name);
}

/**
 * Whether `checkRunConfig` accepts `name` in a run config at all — every
 * forwarded run-env name, plus the names {@link OPENCODE_RUN_ENV_WITHHELD_NAMES}
 * accepts but `curateOpenCodeChildEnv` never applies.
 */
export function isOpenCodeRunEnvAccepted(name: string): boolean {
  return isOpenCodeRunEnvName(name) || OPENCODE_RUN_ENV_WITHHELD_NAMES.has(name);
}

/** The forge variables an opencode stage keeps: its bash tool runs `gh` and `git`. */
const OPENCODE_FORGE_ALLOW: ReadonlySet<string> = new Set(["GH_TOKEN", "GITHUB_TOKEN", "GH_HOST"]);

/**
 * Inherited tool settings the per-run root must not take from a stage's tools
 * (ADR-022 § 8): an inherited GIT_CONFIG_GLOBAL passes through, and so does
 * the operator's own GOCACHE, which the run's environment sets only when the
 * operator has not.
 */
const OPENCODE_TOOL_ALLOW: ReadonlySet<string> = new Set(["GIT_CONFIG_GLOBAL", "GOCACHE"]);

const OPENCODE_ISOLATION_XDG_SET: ReadonlySet<string> = new Set(OPENCODE_ISOLATION_XDG);

/**
 * Whether an inherited variable named `key` may reach an opencode child
 * dispatched to `model` (a `<provider>/<model>` value). Decided on the name
 * alone, so nothing it withholds can be logged. Exported for the drift guard.
 *
 * `NIGHTGAUGE_OPENCODE_` is denied alongside `OPENCODE_` explicitly, although
 * no such name is in {@link OPENCODE_NIGHTGAUGE_ALLOW} either: without it, a
 * nested SDK spawn (an opencode stage's own subprocess reaching for another
 * dispatch) would inherit the *parent* run's plugin path, handshake nonce and
 * sentinel path from `process.env` — letting a nested child write to the
 * parent run's own sentinel file — instead of minting its own through its own
 * `runConfigProvider` call, the only legitimate source of these names.
 */
export function isOpenCodeChildEnvAllowed(key: string, model: string, configContent = ""): boolean {
  if (
    key.startsWith("OPENCODE_") ||
    key.startsWith("NIGHTGAUGE_OPENCODE_") ||
    OPENCODE_ISOLATION_XDG_SET.has(key)
  ) {
    return false;
  }
  return (
    SYSTEM_ALLOW.has(key) ||
    OPENCODE_FORGE_ALLOW.has(key) ||
    OPENCODE_TOOL_ALLOW.has(key) ||
    (key.startsWith("NIGHTGAUGE_") && !openCodeWithholdsNightgaugeEnv(key, configContent)) ||
    openCodeProviderEnv(model).includes(key)
  );
}

/**
 * The environment of an opencode child dispatched to `model`: the inherited
 * variables {@link isOpenCodeChildEnvAllowed} keeps, then `runEnv`, the run's
 * own isolation variables and per-run config, which replace any inherited
 * value of the same name. A `runEnv` name outside the run's variables
 * ({@link isOpenCodeRunEnvName}) is not applied; the adapter refuses such a
 * run config before it gets here. `configContent` is the run's per-run config,
 * whose `{env:NAME}` references keep those NIGHTGAUGE_* variables; it defaults
 * to `runEnv`'s OPENCODE_CONFIG_CONTENT. Pure: mutates neither input.
 */
export function curateOpenCodeChildEnv(
  parentEnv: NodeJS.ProcessEnv,
  model: string,
  runEnv: Readonly<Record<string, string>>,
  configContent: string = runEnv[OPENCODE_CONFIG_CONTENT_ENV] ?? ""
): NodeJS.ProcessEnv {
  const curated: NodeJS.ProcessEnv = {};
  for (const [key, value] of Object.entries(parentEnv)) {
    if (value === undefined) continue;
    if (isOpenCodeChildEnvAllowed(key, model, configContent)) curated[key] = value;
  }
  for (const [key, value] of Object.entries(runEnv)) {
    if (isOpenCodeRunEnvName(key)) curated[key] = value;
  }
  return curated;
}
