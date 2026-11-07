/**
 * Per-user binary shim (#2765).
 *
 * The Claude plugin's hooks and `nightgauge doctor` need to find the
 * nightgauge binary from any editor and any terminal. Globbing editor
 * extension directories only ever covered `~/.vscode/extensions`, so Cursor,
 * Windsurf, VSCodium, Insiders and plain terminals had no binary. Instead the
 * running extension links one stable location, `~/.nightgauge/bin/nightgauge`,
 * to its own bundled binary on every activation. guard.sh (step 3a) and
 * `internal/doctor/binary_resolve.go` (`StepUserShim`) resolve it.
 */
import { constants as fsConstants, promises as fsp } from "node:fs";
import * as path from "node:path";

/** Outcome of one {@link installBinaryShim} call. */
export type BinaryShimResult = "created" | "unchanged" | "skipped";

export interface BinaryShimOptions {
  /** Absolute path of the extension's bundled binary (`<bundle>/dist/bin/nightgauge`). */
  bundledBinary: string;
  /** The user's home directory. */
  homeDir: string;
  /** `process.platform`; the shim is a symlink, so win32 is skipped. */
  platform: NodeJS.Platform;
  /** Suffix that keeps two windows' temporary links apart. Defaults to the pid. */
  tempSuffix?: string;
}

/** `~/.nightgauge/bin/nightgauge` for the given home directory. */
export function userShimPath(homeDir: string): string {
  return path.join(homeDir, ".nightgauge", "bin", "nightgauge");
}

/**
 * Point `~/.nightgauge/bin/nightgauge` at `bundledBinary`.
 *
 * Returns `"unchanged"` when the link already points there, `"skipped"` on
 * win32 or when the bundled binary is missing or not executable (a link to
 * nothing would only shadow a working fallback), and `"created"` otherwise.
 * The link is written under a temporary name and renamed over the target, so
 * a concurrent reader (or a second window) never sees a missing or partial
 * shim. Filesystem errors propagate; callers log them.
 */
export async function installBinaryShim(opts: BinaryShimOptions): Promise<BinaryShimResult> {
  if (opts.platform === "win32") return "skipped";
  try {
    const stat = await fsp.stat(opts.bundledBinary);
    if (!stat.isFile()) return "skipped";
    await fsp.access(opts.bundledBinary, fsConstants.X_OK);
  } catch {
    return "skipped";
  }

  const shim = userShimPath(opts.homeDir);
  try {
    if ((await fsp.readlink(shim)) === opts.bundledBinary) return "unchanged";
  } catch {
    // Absent, or not a symlink: replace it below.
  }

  await fsp.mkdir(path.dirname(shim), { recursive: true });
  const temp = `${shim}.${opts.tempSuffix ?? String(process.pid)}.tmp`;
  await fsp.rm(temp, { force: true });
  await fsp.symlink(opts.bundledBinary, temp);
  try {
    await fsp.rename(temp, shim);
  } catch (error) {
    await fsp.rm(temp, { force: true });
    throw error;
  }
  return "created";
}
