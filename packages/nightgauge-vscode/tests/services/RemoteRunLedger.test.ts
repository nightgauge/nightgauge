/**
 * RemoteRunLedger.test.ts — the machine's windows agree on who answers a
 * platform run verb (#2357), through files in a shared directory.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { CLOSED_LISTING_GRACE_MS, RemoteRunLedger } from "../../src/services/RemoteRunLedger";

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

  it("follows the latest set, and an empty set holds nothing", async () => {
    let now = Date.now();
    const a = new RemoteRunLedger(dir, { windowId: 101, isAlive: () => true, now: () => now });
    const b = new RemoteRunLedger(dir, { windowId: 102, isAlive: () => true, now: () => now });

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
    // A closed window publishes nothing more.
    await a.publish(["run-5"]);
    expect(await b.heldElsewhere("run-5")).toBe(false);
    // Its last listing counts for the grace a reload needs, and no longer.
    expect(await b.heldElsewhere("run-4")).toBe(true);
    now += CLOSED_LISTING_GRACE_MS;
    expect(await b.heldElsewhere("run-4")).toBe(false);
    expect(fs.existsSync(path.join(dir, "holders", "101.json"))).toBe(false);
  });

  // #2357: a window reload ends the window's process; until the window is
  // back and lists its queued runs again, the others must not refuse them.
  it("keeps a reloading window's runs held for the grace after its process is gone", async () => {
    let now = Date.now();
    let reloading = true;
    const a = new RemoteRunLedger(dir, { windowId: 101, now: () => now });
    const b = new RemoteRunLedger(dir, {
      windowId: 102,
      isAlive: (pid) => pid !== 101 || !reloading,
      now: () => now,
    });
    await a.publish(["run-1"]);
    a.dispose();
    expect(await b.heldElsewhere("run-1")).toBe(true);
    now += CLOSED_LISTING_GRACE_MS - 1;
    expect(await b.heldElsewhere("run-1")).toBe(true);
    now += 1;
    expect(await b.heldElsewhere("run-1")).toBe(false);
    reloading = false;

    // A window that held nothing leaves nothing behind.
    const c = new RemoteRunLedger(dir, { windowId: 103 });
    await c.publish([]);
    c.dispose();
    expect(fs.existsSync(path.join(dir, "holders", "103.json"))).toBe(false);
  });

  // #2339: every window of a clone finds the same paused snapshot; one holds it.
  it("gives one live window the claim on a run, and lets a gone window's claim be taken over", async () => {
    let aliveA = true;
    const a = windowOf(101);
    const b = windowOf(102, (pid) => pid !== 101 || aliveA);

    const claims = await Promise.all([a.claimRun("run-1"), b.claimRun("run-1")]);
    expect(claims.filter(Boolean)).toHaveLength(1);
    const [holder, other] = claims[0] ? [a, b] : [b, a];
    expect(await holder.claimRun("run-1")).toBe(true);
    expect(await other.claimRun("run-1")).toBe(false);

    // Released by its holder, the claim is free; another window's is kept.
    await other.releaseRun("run-1");
    expect(await other.claimRun("run-1")).toBe(false);
    await holder.releaseRun("run-1");
    expect(await other.claimRun("run-1")).toBe(true);

    // A claim whose window is gone is taken over.
    expect(await a.claimRun("run-2")).toBe(true);
    expect(await b.claimRun("run-2")).toBe(false);
    aliveA = false;
    expect(await b.claimRun("run-2")).toBe(true);
    const claim = JSON.parse(fs.readFileSync(path.join(dir, "claims", "run-2.json"), "utf8"));
    expect(claim).toEqual({ pid: 102 });
  });

  it("claims nothing when the claim cannot be recorded", async () => {
    fs.writeFileSync(path.join(dir, "claims"), "");
    expect(await windowOf(101).claimRun("run-1")).toBe(false);
  });

  it("takes over a claim lock a window died holding", async () => {
    let now = Date.now();
    const a = new RemoteRunLedger(dir, { windowId: 101, now: () => now });
    fs.mkdirSync(path.join(dir, "claims", "run-1.json.lock"), { recursive: true });
    now += 60_000;
    expect(await a.claimRun("run-1")).toBe(true);
  });

  it("ignores and removes the listing of a window whose process is gone", async () => {
    await windowOf(101).publish(["run-1"]);
    const b = windowOf(102, (pid) => pid !== 101);

    expect(await b.heldElsewhere("run-1")).toBe(false);
    expect(fs.existsSync(path.join(dir, "holders", "101.json"))).toBe(false);
  });

  it("counts the run as held when a listing cannot be read", async () => {
    fs.mkdirSync(path.join(dir, "holders"), { recursive: true });
    for (const content of ["{not json", "null", "7"]) {
      fs.writeFileSync(path.join(dir, "holders", "101.json"), content);
      expect(await windowOf(102).heldElsewhere("run-1")).toBe(true);
    }
    // Unless its window is gone: then it is removed.
    expect(await windowOf(102, (pid) => pid !== 101).heldElsewhere("run-1")).toBe(false);
    expect(fs.existsSync(path.join(dir, "holders", "101.json"))).toBe(false);
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

  // #2357: a window that reloaded lists its runs as closed for a while; a
  // run it no longer holds is then held by nobody once that listing lapses.
  it("says how long only a closed window's listing still holds a run", async () => {
    let now = Date.now();
    const at = (pid: number) =>
      new RemoteRunLedger(dir, { windowId: pid, isAlive: () => true, now: () => now });
    const a = at(101);
    const b = at(102);
    await a.publish(["run-1", "run-2"]);
    expect(await b.closedHoldLeftMs("run-1")).toBeNull();

    a.dispose();
    now += 1000;
    expect(await b.heldElsewhere("run-1")).toBe(true);
    expect(await b.closedHoldLeftMs("run-1")).toBe(CLOSED_LISTING_GRACE_MS - 1000);
    // A live window lists run-2 too, and nobody lists run-3.
    await at(103).publish(["run-2"]);
    expect(await b.closedHoldLeftMs("run-2")).toBeNull();
    expect(await b.closedHoldLeftMs("run-3")).toBeNull();

    now += CLOSED_LISTING_GRACE_MS;
    expect(await b.closedHoldLeftMs("run-1")).toBeNull();
    expect(await b.heldElsewhere("run-1")).toBe(false);
  });

  // #2357: a window that claimed an answer and closed or reloaded before it
  // answered leaves a claim nobody would answer; one window takes it over.
  it("lets one window take over the answer a gone window claimed", async () => {
    let aliveA = true;
    const a = windowOf(101);
    const b = windowOf(102, (pid) => pid !== 101 || aliveA);
    const c = windowOf(103, (pid) => pid !== 101 || aliveA);
    const answer = path.join(dir, "answers", "cmd-1");

    expect(await a.claimAnswer("cmd-1")).toBe(true);
    expect(JSON.parse(fs.readFileSync(answer, "utf8"))).toEqual({ pid: 101 });
    expect(await b.claimAnswer("cmd-1")).toBe(false);

    aliveA = false;
    const taken = await Promise.all([b.claimAnswer("cmd-1"), c.claimAnswer("cmd-1")]);
    expect(taken.filter(Boolean)).toHaveLength(1);
    const taker = JSON.parse(fs.readFileSync(answer, "utf8")).pid;
    expect(taker).toBe(taken[0] ? 102 : 103);
    // The taker lives: nobody takes the answer from it.
    expect(await b.claimAnswer("cmd-1")).toBe(false);
    expect(await c.claimAnswer("cmd-1")).toBe(false);

    // A claim that names no window, such as one still being written, is kept.
    fs.writeFileSync(path.join(dir, "answers", "cmd-2"), "");
    expect(await b.claimAnswer("cmd-2")).toBe(false);
  });

  it("claims nothing when the answer cannot be recorded", async () => {
    // The answers directory is a file: nothing can be created in it.
    fs.writeFileSync(path.join(dir, "answers"), "");
    expect(await windowOf(101).claimAnswer("cmd-1")).toBe(false);
  });
});
