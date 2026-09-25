/**
 * The compaction count of an editor-launched OpenCode stage (#1668), read
 * from the events file the Nightgauge OpenCode plugin writes (#1641).
 *
 * The plugin (internal/execution/opencodeplugin/plugin/nightgauge/session.js,
 * `eventsPath`) writes `opencode-events-<NIGHTGAUGE_RUN_ID>.jsonl` beside
 * `NIGHTGAUGE_OUTPUT_FILE`. Both variables reach the opencode process through
 * the SDK stage CLI (OPENCODE_NIGHTGAUGE_ALLOW), so skillRunner gives each
 * OpenCode dispatch a fresh directory for that file and counts it here once
 * the stage has ended.
 *
 * The file is transcript-adjacent, so it is read with the Go reader's bounds
 * (opencodeplugin.ReadRunEvents, #1653): a fixed name, a regular file
 * directly inside the dispatch's own directory (a symlink is refused), at
 * most 1 MiB plus the writer's overshoot, and only each line's `kind` is
 * looked at. Only the count leaves this module.
 */
import * as fs from "fs";
import * as path from "path";

/** The events file's own cap plus the overshoot its writer may leave (events.go). */
export const OPENCODE_EVENTS_READ_MAX_BYTES = 1024 * 1024 + 4 * 1024;

const RUN_ID_SHAPE = /^[A-Za-z0-9._-]+$/;

/** The events file the plugin writes for runId beside an output file in dir. */
export function openCodeEventsFile(dir: string, runId: string): string | undefined {
  if (!RUN_ID_SHAPE.test(runId) || !path.isAbsolute(dir)) return undefined;
  return path.join(dir, `opencode-events-${runId}.jsonl`);
}

/**
 * How many "compaction" events the dispatch's events file holds: 0 when the
 * plugin wrote none (no file), undefined when the file cannot be read within
 * its bounds, which the caller records as "not counted".
 */
export function countOpenCodeCompactions(dir: string, runId: string): number | undefined {
  const file = openCodeEventsFile(dir, runId);
  if (!file) return undefined;
  try {
    let st: fs.Stats;
    try {
      st = fs.lstatSync(file);
    } catch (err) {
      return (err as NodeJS.ErrnoException)?.code === "ENOENT" ? 0 : undefined;
    }
    if (!st.isFile()) return undefined;
    if (fs.realpathSync(path.dirname(file)) !== fs.realpathSync(dir)) return undefined;
    const fd = fs.openSync(file, "r");
    let text: string;
    try {
      const buf = Buffer.alloc(Math.min(st.size, OPENCODE_EVENTS_READ_MAX_BYTES));
      const n = fs.readSync(fd, buf, 0, buf.length, 0);
      text = buf.subarray(0, n).toString("utf-8");
    } finally {
      fs.closeSync(fd);
    }
    let count = 0;
    for (const line of text.split("\n")) {
      if (!line.includes('"compaction"')) continue;
      try {
        const event = JSON.parse(line) as { kind?: unknown; text?: unknown };
        if (event.kind === "compaction" && !("text" in event)) count++;
      } catch {
        // A partial or malformed line is dropped, as the Go reader drops it.
      }
    }
    return count;
  } catch {
    return undefined;
  }
}
