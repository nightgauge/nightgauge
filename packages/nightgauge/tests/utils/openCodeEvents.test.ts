/**
 * The compaction count of an editor-launched OpenCode stage (#1668), read
 * from the plugin's events file with the Go reader's bounds.
 */
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import {
  countOpenCodeCompactions,
  OPENCODE_EVENTS_READ_MAX_BYTES,
} from "../../src/utils/openCodeEvents";

const RUN = "01990000-0000-7000-8000-000000000001";
let dir: string;

beforeEach(() => {
  dir = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "oc-events-")));
});
afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

const line = (kind: string, extra: Record<string, unknown> = {}) =>
  JSON.stringify({ v: 1, ts: "2026-09-25T00:00:00Z", kind, session_id: "s", ...extra });

describe("countOpenCodeCompactions (#1668)", () => {
  it("counts compaction lines only", () => {
    fs.writeFileSync(
      path.join(dir, `opencode-events-${RUN}.jsonl`),
      [
        line("compaction"),
        line("stop_verify"),
        line("compaction"),
        "not json",
        line("compaction"),
      ].join("\n") + "\n"
    );
    expect(countOpenCodeCompactions(dir, RUN)).toBe(3);
  });

  it("counts 0 when the plugin wrote no file", () => {
    expect(countOpenCodeCompactions(dir, RUN)).toBe(0);
  });

  it("drops a line that carries text, as the Go reader does", () => {
    fs.writeFileSync(
      path.join(dir, `opencode-events-${RUN}.jsonl`),
      line("compaction", { text: "summary" }) + "\n" + line("compaction") + "\n"
    );
    expect(countOpenCodeCompactions(dir, RUN)).toBe(1);
  });

  it("refuses a symlink and a run id that is not a plain name", () => {
    const outside = path.join(dir, "..", `outside-${path.basename(dir)}.jsonl`);
    fs.writeFileSync(outside, line("compaction") + "\n");
    try {
      fs.symlinkSync(outside, path.join(dir, `opencode-events-${RUN}.jsonl`));
      expect(countOpenCodeCompactions(dir, RUN)).toBeUndefined();
    } finally {
      fs.rmSync(outside, { force: true });
    }
    expect(countOpenCodeCompactions(dir, "../x")).toBeUndefined();
  });

  it("reads no more than the file's cap", () => {
    const one = line("compaction") + "\n";
    const n = Math.ceil((OPENCODE_EVENTS_READ_MAX_BYTES * 2) / one.length);
    fs.writeFileSync(path.join(dir, `opencode-events-${RUN}.jsonl`), one.repeat(n));
    const counted = countOpenCodeCompactions(dir, RUN) ?? 0;
    expect(counted).toBeLessThanOrEqual(Math.floor(OPENCODE_EVENTS_READ_MAX_BYTES / one.length));
    expect(counted).toBeGreaterThan(0);
  });
});
