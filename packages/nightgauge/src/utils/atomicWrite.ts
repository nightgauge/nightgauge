/**
 * atomicWrite - Replace a file's contents without ever exposing a partial one.
 *
 * `fs.writeFile` opens with `'w'`: it TRUNCATES the target, then writes. For
 * the duration of that write the file is zero bytes on disk, and any concurrent
 * reader sees an empty file rather than the old contents or the new ones.
 *
 * That window is not theoretical. `health-history.jsonl` is rewritten by a
 * retention prune after every append while the telemetry uploader reads it on
 * its own timer — a 10-second timer while any run is active. A read that landed
 * mid-prune saw zero lines, could not match its anchor record against an empty
 * file, and fell through to re-uploading the whole stream from line 0 (#1210).
 * The same shape loses the entire file if the process dies between the truncate
 * and the write.
 *
 * Write to a temp file in the SAME directory, then rename over the target.
 * `rename(2)` within a filesystem is atomic: a reader observes either the old
 * file or the new one, never an intermediate.
 *
 * @see Issue #1210 - a prune truncated the file readers were reading
 * @see Issue #777 - why the temp name must be unique per write
 */

import * as fs from "node:fs/promises";
import { randomBytes } from "node:crypto";

/**
 * Atomically replace `filePath` with `contents`.
 *
 * The temp file is created beside the target — NOT in the system temp
 * directory — because `rename` is only atomic within a single filesystem, and
 * `/tmp` is routinely a different mount.
 *
 * The temp name carries the pid and random bytes so two concurrent writers
 * cannot collide. A fixed name is not merely untidy: with two writers, the
 * first rename wins and the second fails `ENOENT` on a temp file the winner
 * already consumed. That exact failure rendered an empty dashboard with nothing
 * to say a write had raced (#777). Two concurrent writers are the normal case
 * here, not an exotic one.
 *
 * On failure the temp file is removed before rethrowing — per-write temp names
 * only stay leak-free if a failed write cleans up after itself, whereas the old
 * fixed name was at least overwritten by the next attempt.
 *
 * Callers are responsible for ensuring the parent directory exists; this
 * function deliberately does not create it, so a wrong path surfaces as an
 * error rather than a surprise directory tree.
 */
export async function writeFileAtomic(
  filePath: string,
  contents: string,
  encoding: BufferEncoding = "utf-8"
): Promise<void> {
  const tempPath = `${filePath}.${process.pid}.${randomBytes(6).toString("hex")}.tmp`;
  try {
    await fs.writeFile(tempPath, contents, encoding);
    await fs.rename(tempPath, filePath);
  } catch (err) {
    await fs.rm(tempPath, { force: true }).catch(() => {});
    throw err;
  }
}
