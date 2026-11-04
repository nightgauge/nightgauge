/**
 * Launching-session residue (#2848).
 *
 * An editor started with `code .` from a Claude Code terminal inherits that
 * session's `CLAUDECODE` and `CLAUDE_*` variables (`CLAUDE_EFFORT`,
 * `CLAUDE_CODE_SESSION_ID`, ...). A stage spawned on top of `process.env`
 * would then behave differently depending on how the editor was started. Every
 * stage, resume, interactive and daemon spawn therefore starts from
 * `withoutLaunchingSessionEnv()` instead of `process.env`.
 *
 * The rule reads only names. Its Go twin is `launchingSessionEnv` in
 * `internal/execution/manager.go`; change both together.
 */

/**
 * `CLAUDE_*` names a stage needs on purpose, so they stay inherited: the
 * stage's auth, its plugin root, the documented thinking escape hatch and the
 * operator's account home. Go twin: `keptSessionEnv`.
 */
export const KEPT_SESSION_ENV: ReadonlySet<string> = new Set([
  "CLAUDE_CODE_OAUTH_TOKEN",
  "CLAUDE_PLUGIN_ROOT",
  "CLAUDE_CODE_DISABLE_THINKING",
  "CLAUDE_CONFIG_DIR",
  "CLAUDE_CODE_USE_BEDROCK",
  "CLAUDE_CODE_USE_VERTEX",
  "CLAUDE_CODE_SKIP_BEDROCK_AUTH",
  "CLAUDE_CODE_SKIP_VERTEX_AUTH",
]);

/** True when `name` is residue of the launching Claude Code session. */
export function isLaunchingSessionEnv(name: string): boolean {
  if (name === "CLAUDECODE") {
    return true;
  }
  return name.startsWith("CLAUDE_") && !KEPT_SESSION_ENV.has(name);
}

/**
 * A copy of `env` without the launching session's residue. The input is not
 * modified. Variables Nightgauge sets for a stage are spread after this base,
 * so they arrive whatever their name.
 */
export function withoutLaunchingSessionEnv(
  env: NodeJS.ProcessEnv = process.env
): NodeJS.ProcessEnv {
  const out: NodeJS.ProcessEnv = {};
  for (const [name, value] of Object.entries(env)) {
    if (!isLaunchingSessionEnv(name)) {
      out[name] = value;
    }
  }
  return out;
}
