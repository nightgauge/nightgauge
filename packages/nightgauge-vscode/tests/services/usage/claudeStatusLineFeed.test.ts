/**
 * The statusline usage feed (Issue #730).
 *
 * Two concerns, both about the fact that `ClaudeRateLimitStore` now has a
 * second writer living in another process:
 *
 * 1. The store must see out-of-process writes, and must not clobber them.
 * 2. The settings module must wire that writer in without costing the operator
 *    the status line they already had.
 */

import { describe, expect, it } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { ClaudeRateLimitStore } from "../../../src/services/usage/ClaudeRateLimitStore";
import {
  buildStatusLineCommand,
  parseDelegate,
  quoteDelegate,
  readClaudeSettings,
  readStatusLineState,
  withStatusLineUnwired,
  withStatusLineWired,
  writeClaudeSettings,
} from "../../../src/services/usage/claudeStatusLineSetup";

async function tempRoot(): Promise<string> {
  return fs.mkdtemp(path.join(os.tmpdir(), "ng-usage-"));
}

const STORE_REL = ".nightgauge/usage/claude-rate-limits.json";

/** Write the store file the way the Go `claude-statusline` verb does. */
async function writeAsGoVerb(
  root: string,
  buckets: Record<string, { utilization: number; resetsAt: number; observedAt: Date }>
): Promise<void> {
  const filePath = path.join(root, STORE_REL);
  await fs.mkdir(path.dirname(filePath), { recursive: true });
  const doc = {
    version: 1,
    buckets: Object.fromEntries(
      Object.entries(buckets).map(([name, value]) => [
        name,
        {
          rateLimitType: name,
          utilization: value.utilization,
          resetsAt: value.resetsAt,
          status: "unknown",
          observedAt: value.observedAt.toISOString(),
        },
      ])
    ),
  };
  await fs.writeFile(filePath, `${JSON.stringify(doc, null, 2)}\n`, "utf8");
}

function unixSecondsFromNow(seconds: number): number {
  return Math.floor(Date.now() / 1000) + seconds;
}

describe("ClaudeRateLimitStore — the statusline writer is a second process", () => {
  it("picks up readings written after the first load", async () => {
    const root = await tempRoot();
    const store = new ClaudeRateLimitStore(root);

    // First load: nothing on disk. Before #730 this latched `loaded` and the
    // store would serve nothing for the rest of the window's life.
    await store.load();
    expect(store.readings()).toEqual([]);

    await writeAsGoVerb(root, {
      five_hour: { utilization: 44, resetsAt: unixSecondsFromNow(3600), observedAt: new Date() },
    });

    await store.load();
    const readings = store.readings();
    expect(readings).toHaveLength(1);
    expect(readings[0].rateLimitType).toBe("five_hour");
    expect(readings[0].utilization).toBe(44);
    // Anything read back off disk describes a moment that has passed, so it is
    // never live — which is what makes the provider report `estimated`.
    expect(readings[0].live).toBe(false);
  });

  it("re-reads when the file changes again", async () => {
    const root = await tempRoot();
    const store = new ClaudeRateLimitStore(root);
    const resetsAt = unixSecondsFromNow(3600);

    await writeAsGoVerb(root, {
      five_hour: { utilization: 10, resetsAt, observedAt: new Date(Date.now() - 60_000) },
    });
    await store.load();
    expect(store.readings()[0].utilization).toBe(10);

    await writeAsGoVerb(root, {
      five_hour: { utilization: 55, resetsAt, observedAt: new Date() },
    });
    await store.load();
    expect(store.readings()[0].utilization).toBe(55);
  });

  it("keeps a live same-run reading over an older one on disk", async () => {
    const root = await tempRoot();
    const store = new ClaudeRateLimitStore(root);
    const resetsAt = unixSecondsFromNow(3600);

    await writeAsGoVerb(root, {
      five_hour: { utilization: 10, resetsAt, observedAt: new Date(Date.now() - 600_000) },
    });
    await store.record(
      { rateLimitType: "five_hour", utilization: 72, resetsAt, status: "allowed" } as never,
      new Date()
    );

    await store.load();
    const reading = store.readings().find((r) => r.rateLimitType === "five_hour");
    expect(reading?.utilization).toBe(72);
    // The `live` flag is the difference between `measured` and `estimated`, and
    // a re-read triggered by the store's own persist must not cost it.
    expect(reading?.live).toBe(true);
  });

  it("yields to a newer out-of-process reading", async () => {
    const root = await tempRoot();
    const store = new ClaudeRateLimitStore(root);
    const resetsAt = unixSecondsFromNow(3600);

    await store.record(
      { rateLimitType: "five_hour", utilization: 30, resetsAt, status: "allowed" } as never,
      new Date(Date.now() - 600_000)
    );
    await writeAsGoVerb(root, {
      five_hour: { utilization: 88, resetsAt, observedAt: new Date() },
    });

    await store.load();
    expect(store.readings()[0].utilization).toBe(88);
  });

  it("does not delete buckets it has never seen when it writes", async () => {
    const root = await tempRoot();
    const store = new ClaudeRateLimitStore(root);
    const resetsAt = unixSecondsFromNow(3600);

    // The statusline verb records the weekly window; this process only ever
    // observes the five-hour one. A blind rewrite from memory would drop the
    // weekly bucket on the floor.
    await writeAsGoVerb(root, {
      seven_day: { utilization: 61, resetsAt: unixSecondsFromNow(86_400), observedAt: new Date() },
    });
    await store.record(
      { rateLimitType: "five_hour", utilization: 12, resetsAt, status: "allowed" } as never,
      new Date()
    );

    const onDisk = JSON.parse(await fs.readFile(path.join(root, STORE_REL), "utf8"));
    expect(Object.keys(onDisk.buckets).sort()).toEqual(["five_hour", "seven_day"]);
  });

  it("leaves no temp files behind", async () => {
    const root = await tempRoot();
    const store = new ClaudeRateLimitStore(root);

    await store.record(
      {
        rateLimitType: "five_hour",
        utilization: 12,
        resetsAt: unixSecondsFromNow(3600),
        status: "allowed",
      } as never,
      new Date()
    );

    const entries = await fs.readdir(path.join(root, ".nightgauge/usage"));
    expect(entries).toEqual(["claude-rate-limits.json"]);
  });

  it("resolves to the account root, not a workspace", () => {
    expect(ClaudeRateLimitStore.forAccount().filePath).toBe(path.join(os.homedir(), STORE_REL));
  });
});

describe("claudeStatusLineSetup — wiring the feed into Claude Code", () => {
  const BIN = "/opt/nightgauge/bin/nightgauge";

  it("wires a bare command when no status line exists", () => {
    expect(buildStatusLineCommand(BIN, null)).toBe(`'${BIN}' hook claude-statusline`);
  });

  it("preserves an existing status line as the delegate", () => {
    const existing = "~/.claude/statusline.sh | tr a-z A-Z";
    expect(buildStatusLineCommand(BIN, existing)).toBe(
      `'${BIN}' hook claude-statusline --delegate '${existing}'`
    );
  });

  it("does not nest when applied twice", () => {
    const existing = "~/.claude/statusline.sh";
    const once = buildStatusLineCommand(BIN, existing);
    const twice = buildStatusLineCommand(BIN, once);
    expect(twice).toBe(once);
    // A nested wrapper would fork a second nightgauge per render and record the
    // same reading twice.
    expect(twice.match(/hook claude-statusline/g)).toHaveLength(1);
  });

  it("re-wires to a new binary path without losing the delegate", () => {
    const existing = buildStatusLineCommand("/old/nightgauge", "~/sl.sh");
    expect(buildStatusLineCommand(BIN, existing)).toBe(
      `'${BIN}' hook claude-statusline --delegate '~/sl.sh'`
    );
  });

  it("round-trips a command containing single quotes", () => {
    const nasty = `echo 'it'\\''s mine' | sed "s/x/y/"`;
    expect(parseDelegate(`x hook claude-statusline --delegate ${quoteDelegate(nasty)}`)).toBe(
      nasty
    );
  });

  it("reads an unwired state from a foreign status line", () => {
    const state = readStatusLineState({ statusLine: { type: "command", command: "~/sl.sh" } });
    expect(state).toEqual({ wired: false, command: "~/sl.sh", delegate: "~/sl.sh" });
  });

  it("reads a wired state and its delegate", () => {
    const state = readStatusLineState({
      statusLine: { type: "command", command: buildStatusLineCommand(BIN, "~/sl.sh") },
    });
    expect(state.wired).toBe(true);
    expect(state.delegate).toBe("~/sl.sh");
  });

  it("treats an absent or malformed statusLine as unwired", () => {
    expect(readStatusLineState({}).wired).toBe(false);
    expect(readStatusLineState({ statusLine: "nonsense" }).wired).toBe(false);
    expect(readStatusLineState({ statusLine: { type: "command" } }).command).toBeNull();
  });

  it("keeps every other setting, and every other statusLine key", () => {
    const before = {
      model: "opus",
      permissions: { allow: ["Bash"] },
      statusLine: { type: "command", command: "~/sl.sh", padding: 0 },
    };
    const after = withStatusLineWired(before, BIN);
    expect(after.model).toBe("opus");
    expect(after.permissions).toEqual({ allow: ["Bash"] });
    expect((after.statusLine as { padding: number }).padding).toBe(0);
  });

  it("unwires back to exactly what was there", () => {
    const original = {
      model: "opus",
      statusLine: { type: "command", command: "~/sl.sh", padding: 0 },
    };
    expect(withStatusLineUnwired(withStatusLineWired(original, BIN))).toEqual(original);
  });

  it("removes statusLine entirely when there was nothing to restore", () => {
    const wired = withStatusLineWired({ model: "opus" }, BIN);
    expect(withStatusLineUnwired(wired)).toEqual({ model: "opus" });
  });

  it("leaves an unwired document alone", () => {
    const settings = { statusLine: { type: "command", command: "~/sl.sh" } };
    expect(withStatusLineUnwired(settings)).toBe(settings);
  });

  it("treats a missing settings file as an empty document", async () => {
    const root = await tempRoot();
    expect(await readClaudeSettings(path.join(root, "settings.json"))).toEqual({});
  });

  it("refuses to interpret a non-object settings file", async () => {
    const root = await tempRoot();
    const file = path.join(root, "settings.json");
    await fs.writeFile(file, "[1,2,3]", "utf8");
    expect(await readClaudeSettings(file)).toBeNull();
    await fs.writeFile(file, "{ not json", "utf8");
    expect(await readClaudeSettings(file)).toBeNull();
  });

  it("writes atomically and leaves no temp file", async () => {
    const root = await tempRoot();
    const file = path.join(root, "nested", "settings.json");
    await writeClaudeSettings(file, { model: "opus" });
    expect(await readClaudeSettings(file)).toEqual({ model: "opus" });
    expect(await fs.readdir(path.dirname(file))).toEqual(["settings.json"]);
  });
});
