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
 */

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
