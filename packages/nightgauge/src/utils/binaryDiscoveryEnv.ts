/**
 * Environment that makes the Nightgauge binary reachable from a child process
 * or terminal (#2760).
 *
 * The extension bundles the binary at `<extensionPath>/dist/bin/…`, which is
 * NOT on the subprocess PATH. Skills historically located it by globbing the
 * `~/.vscode/extensions/nightgauge.<version>/dist/bin/` directory — a
 * VSCode-extension-specific path that breaks portability (and missed the
 * published, platform-suffixed binary). Instead the host now resolves the
 * binary authoritatively (BinaryResolver) and exports:
 *   - `NIGHTGAUGE_BIN` — absolute path, honored first by the skill cascade
 *     and `claude-plugins/.../guard.sh`.
 *   - `PATH` — with the binary's dir prepended so bare `nightgauge …` and
 *     `command -v nightgauge` resolve in every provider's shell.
 *
 * Returns `{}` when the binary is already on PATH (BinaryResolver's filesystem
 * tiers miss) — the skill's own `command -v` cascade covers that case — or when
 * resolution throws (defensive: never block a stage spawn over discovery).
 */

import * as fs from "fs";
import * as path from "path";
import { BinaryResolver } from "../services/BinaryResolver";

export function resolveBinaryDiscoveryEnv(): Record<string, string> {
  try {
    const binaryPath = BinaryResolver.fromVSCode().resolveSync();
    if (!binaryPath) return {};
    // Only export a path we can actually execute. If it exists but lacks the
    // execute bit, fall through to the skill's own cascade (which guards on
    // `[ -x ]`) rather than exporting a misleading NIGHTGAUGE_BIN.
    try {
      fs.accessSync(binaryPath, fs.constants.X_OK);
    } catch {
      return {};
    }
    const binDir = path.dirname(binaryPath);
    const sep = process.platform === "win32" ? ";" : ":";
    const currentPath = process.env.PATH ?? "";
    return {
      NIGHTGAUGE_BIN: binaryPath,
      PATH: currentPath ? `${binDir}${sep}${currentPath}` : binDir,
    };
  } catch {
    return {};
  }
}
