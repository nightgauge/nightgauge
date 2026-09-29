/**
 * Re-date a copy of the demo workspace to the moment a session starts (#2285).
 *
 * `demo/workspace/` is committed with fixed dates, written as of the reference
 * scenario's `state.now` ({@link DEMO_WORKSPACE_NOW}). A copy used as it is
 * shows a history months old, so the Audit Trail, which opens on the last seven
 * days, finds nothing. {@link redateWorkspace} moves every date in the copy's
 * JSON and JSONL files by one offset with the helper the demo daemon rebases
 * scenario state with (`rebaseValue` in `daemon/scenario.cjs`): timestamps by
 * the exact offset, calendar dates by whole days. `DEMO_WORKSPACE_NOW` lands on
 * the session's start, and every event keeps its order and spacing.
 *
 * Run history files are named by the UTC day they were written
 * (`pipeline/history/YYYY-MM-DD.jsonl`), and retention and the history index's
 * staleness check read that name, so the re-dated records are filed under the
 * day of their re-dated `recorded_at`, as the writer would have filed them.
 */

import * as fs from "node:fs";
import * as path from "node:path";
import { rebaseValue } from "./daemon/scenario.cjs";

/** The instant the demo workspace's dates were written against. */
export const DEMO_WORKSPACE_NOW = "2026-01-15T09:30:00.000Z";

/** Where run history lives under a workspace root. */
export const HISTORY_DIR = path.join(".nightgauge", "pipeline", "history");

function listFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) return listFiles(full);
    return entry.isFile() ? [full] : [];
  });
}

function jsonLines(file: string): unknown[] {
  return fs
    .readFileSync(file, "utf8")
    .split("\n")
    .filter((line) => line.trim())
    .map((line) => JSON.parse(line) as unknown);
}

/** The UTC day a history record was written on. */
function recordDay(record: unknown, fallback: string): string {
  const fields = (record ?? {}) as Record<string, unknown>;
  const stamp = [fields.recorded_at, fields.completed_at, fields.started_at].find(
    (value): value is string => typeof value === "string" && value.length >= 10
  );
  return (stamp ?? fallback).slice(0, 10);
}

/**
 * Shift every date in `root`'s JSON and JSONL files so that
 * {@link DEMO_WORKSPACE_NOW} becomes `epochMs`. Other files are left as they
 * are. `root` must be a copy: the files are rewritten in place.
 */
export function redateWorkspace(root: string, epochMs: number): void {
  const deltaMs = epochMs - Date.parse(DEMO_WORKSPACE_NOW);
  const nowIso = new Date(epochMs).toISOString();
  const historyDir = path.join(root, HISTORY_DIR);
  const history: unknown[] = [];

  // Sorted, so date-named history files are read oldest first.
  for (const file of listFiles(root).sort()) {
    if (file.endsWith(".jsonl")) {
      const records = jsonLines(file).map((record) => rebaseValue(record, deltaMs, nowIso));
      if (path.dirname(file) === historyDir) {
        history.push(...records);
        fs.rmSync(file);
      } else {
        fs.writeFileSync(file, records.map((record) => `${JSON.stringify(record)}\n`).join(""));
      }
    } else if (file.endsWith(".json")) {
      const value = JSON.parse(fs.readFileSync(file, "utf8")) as unknown;
      fs.writeFileSync(file, `${JSON.stringify(rebaseValue(value, deltaMs, nowIso), null, 2)}\n`);
    }
  }

  const byDay = new Map<string, string[]>();
  for (const record of history) {
    const day = recordDay(record, nowIso);
    byDay.set(day, [...(byDay.get(day) ?? []), JSON.stringify(record)]);
  }
  for (const [day, lines] of byDay) {
    fs.writeFileSync(path.join(historyDir, `${day}.jsonl`), `${lines.join("\n")}\n`);
  }
}
