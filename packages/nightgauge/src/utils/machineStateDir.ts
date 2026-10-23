/**
 * Machine-state directory — the extension's resolver for the `STATE` root
 * (ADR-024 § 1, § 8).
 *
 * The Go binary resolves the same root in `internal/layout.StateHomePathFrom`,
 * and the two must agree byte for byte: the `nightgauge hook
 * claude-statusline` verb writes `usage/claude-rate-limits.json` under it and
 * the extension reads that file (ADR-024 § 2, #2032). Resolution, highest
 * first:
 *
 *  1. `NIGHTGAUGE_STATE_HOME`, which must be absolute;
 *  2. `$XDG_STATE_HOME/nightgauge` on every OS (a relative value is ignored,
 *     as the XDG specification requires);
 *  3. the platform default: `~/.local/state/nightgauge` on Linux,
 *     `%LOCALAPPDATA%\nightgauge\state` on Windows, and
 *     `~/.nightgauge/state` everywhere else (macOS).
 *
 * It never falls back into a workspace or the legacy `~/.nightgauge`: an
 * unresolvable root is `undefined`, and a caller for which the file is only a
 * hint (the rate-limit readings) starts cold.
 *
 * @see Issue #2032
 */

import * as os from "os";
import * as path from "path";

/** The override the Go resolver honours first (`layout.EnvStateHome`). */
export const STATE_HOME_ENV = "NIGHTGAUGE_STATE_HOME";

/**
 * Resolve the machine-state root without touching the filesystem.
 *
 * @param env Environment to read (default: `process.env`).
 * @param platform Platform whose default applies (default: the host's).
 * @param home Home directory (default: `os.homedir()`).
 * @returns The absolute root, or `undefined` when none can be resolved (an
 *   override that is not absolute, or no absolute home directory).
 */
export function resolveStateHome(
  env: NodeJS.ProcessEnv = process.env,
  platform: NodeJS.Platform = process.platform,
  home: string = os.homedir()
): string | undefined {
  const p = platform === "win32" ? path.win32 : path.posix;
  const override = env[STATE_HOME_ENV];
  if (override) {
    return p.isAbsolute(override) ? clean(p, override) : undefined;
  }
  const xdg = env.XDG_STATE_HOME;
  if (xdg && p.isAbsolute(xdg)) {
    return p.join(xdg, "nightgauge");
  }
  if (platform === "win32") {
    const local = env.LOCALAPPDATA;
    if (local && p.isAbsolute(local)) {
      return p.join(local, "nightgauge", "state");
    }
  }
  if (!home || !p.isAbsolute(home)) {
    return undefined;
  }
  switch (platform) {
    case "linux":
      return p.join(home, ".local", "state", "nightgauge");
    case "win32":
      return p.join(home, "AppData", "Local", "nightgauge", "state");
    default:
      return p.join(home, ".nightgauge", "state");
  }
}

/** `filepath.Clean` for an absolute path: normalized, with no trailing separator. */
function clean(p: typeof path.posix, dir: string): string {
  const n = p.normalize(dir);
  const root = p.parse(n).root;
  return n.length > root.length && n.endsWith(p.sep) ? n.slice(0, -1) : n;
}
