/**
 * RemoteRunLedger.test.ts — the machine's windows agree on who answers a
 * platform run verb (#2357), through files in a shared directory.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { RemoteRunLedger } from "../../src/services/RemoteRunLedger";

let dir: string;

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), "remote-run-ledger-"));
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

/** A window of the machine: its own process id, every other one alive. */
function windowOf(pid: number, alive: (pid: number) => boolean = () => true): RemoteRunLedger {
  return new RemoteRunLedger(dir, { windowId: pid, isAlive: alive });
}

describe("RemoteRunLedger (#2357)", () => {
  it("tells a window which runs another live window holds, never its own", async () => {
    const a = windowOf(101);
    const b = windowOf(102);

    await a.publish(["run-1", "run-2"]);

    expect(await b.heldElsewhere("run-1")).toBe(true);
    expect(await b.heldElsewhere("run-3")).toBe(false);
    // A window's own listing is not "elsewhere".
    expect(await a.heldElsewhere("run-1")).toBe(false);
  });

  it("follows the latest set, and an empty set or a closed window holds nothing", async () => {
    const a = windowOf(101);
    const b = windowOf(102);

    // Writes queued back to back: only the newest set ends up on disk.
    void a.publish(["run-1"]);
    void a.publish(["run-2"]);
    await a.publish(["run-3"]);
    expect(await b.heldElsewhere("run-1")).toBe(false);
    expect(await b.heldElsewhere("run-3")).toBe(true);

    await a.publish([]);
    expect(await b.heldElsewhere("run-3")).toBe(false);

    await a.publish(["run-4"]);
    a.dispose();
    expect(await b.heldElsewhere("run-4")).toBe(false);
    // A closed window publishes nothing more.
    await a.publish(["run-5"]);
    expect(await b.heldElsewhere("run-5")).toBe(false);
  });

  it("ignores and removes the listing of a window whose process is gone", async () => {
    await windowOf(101).publish(["run-1"]);
    const b = windowOf(102, (pid) => pid !== 101);

    expect(await b.heldElsewhere("run-1")).toBe(false);
    expect(fs.existsSync(path.join(dir, "holders", "101.json"))).toBe(false);
  });

  it("counts the run as held when a listing cannot be read", async () => {
    fs.mkdirSync(path.join(dir, "holders"), { recursive: true });
    fs.writeFileSync(path.join(dir, "holders", "101.json"), "{not json");
    expect(await windowOf(102).heldElsewhere("run-1")).toBe(true);
  });

  it("holds nothing anywhere before any window published", async () => {
    expect(await windowOf(102).heldElsewhere("run-1")).toBe(false);
  });

  it("gives one window the answer to a command", async () => {
    const a = windowOf(101);
    const b = windowOf(102);

    const claims = await Promise.all([a.claimAnswer("cmd-1"), b.claimAnswer("cmd-1")]);
    expect(claims.filter(Boolean)).toHaveLength(1);
    expect(await a.claimAnswer("cmd-1")).toBe(false);
    expect(await b.claimAnswer("cmd-2")).toBe(true);
  });

  it("keeps an odd command id inside its directory", async () => {
    const a = windowOf(101);
    expect(await a.claimAnswer("../../escape")).toBe(true);
    expect(await a.claimAnswer("../../escape")).toBe(false);
    expect(fs.readdirSync(dir).sort()).toEqual(["answers"]);
  });

  it("sweeps answers older than a day", async () => {
    let now = Date.now();
    const a = new RemoteRunLedger(dir, { windowId: 101, now: () => now });
    expect(await a.claimAnswer("cmd-old")).toBe(true);
    const old = path.join(dir, "answers", "cmd-old");
    const twoDaysAgo = new Date(now - 2 * 24 * 60 * 60 * 1000);
    fs.utimesSync(old, twoDaysAgo, twoDaysAgo);

    now += 1000;
    expect(await a.claimAnswer("cmd-new")).toBe(true);
    await expect.poll(() => fs.existsSync(old)).toBe(false);
    expect(fs.existsSync(path.join(dir, "answers", "cmd-new"))).toBe(true);
  });

  it("claims nothing when the answer cannot be recorded", async () => {
    // The answers directory is a file: nothing can be created in it.
    fs.writeFileSync(path.join(dir, "answers"), "");
    expect(await windowOf(101).claimAnswer("cmd-1")).toBe(false);
  });
});
