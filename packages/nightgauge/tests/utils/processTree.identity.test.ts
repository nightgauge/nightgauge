/**
 * processTree.identity.test.ts (Issue #2730)
 *
 * The reaper holds pids for as long as a stage runs, so it can hold one that has
 * since been reused. It must kill only a pid it can show is still the process
 * it tracked. Real processes, so a wrong kill is observed and not inferred.
 */
import { describe, it, expect } from "vitest";
import { spawn, type ChildProcess } from "child_process";
import { DescendantTracker, readProcessStartTime } from "../../src/utils/processTree";

const SKIP = process.platform === "win32";

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/** A parent shell with one `sleep` child; resolves once that child's pid is known. */
async function parentWithChild(): Promise<{ parent: ChildProcess; childPid: number }> {
  const parent = spawn("/bin/sh", ["-c", "sleep 600 & echo $!; wait"], {
    stdio: ["ignore", "pipe", "ignore"],
  });
  const childPid = await new Promise<number>((resolve) => {
    parent.stdout?.once("data", (d: Buffer) => resolve(Number(String(d).trim())));
  });
  return { parent, childPid };
}

async function tracked(
  tracker: DescendantTracker,
  rootPid: number,
  childPid: number
): Promise<void> {
  tracker.start(rootPid, 50);
  const view = tracker as unknown as { tracked: Map<number, string | null> };
  for (let i = 0; i < 100 && !view.tracked.has(childPid); i++) {
    await new Promise((r) => setTimeout(r, 20));
  }
  expect(view.tracked.has(childPid)).toBe(true);
}

async function waitGone(pid: number): Promise<void> {
  for (let i = 0; i < 100 && alive(pid); i++) {
    await new Promise((r) => setTimeout(r, 20));
  }
}

describe.skipIf(SKIP)("DescendantTracker identity (#2730)", () => {
  it("reads a live process's start time and null for a dead pid", async () => {
    expect(await readProcessStartTime(process.pid)).toMatch(/\d/);
    expect(await readProcessStartTime(99_999_999)).toBeNull();
  });

  it("kills a survivor whose identity still matches", async () => {
    const { parent, childPid } = await parentWithChild();
    const tracker = new DescendantTracker(async () => "same start time");
    try {
      await tracked(tracker, parent.pid as number, childPid);
      const killed = await tracker.killSurvivors();
      expect(killed).toEqual([childPid]);
      await waitGone(childPid);
      expect(alive(childPid)).toBe(false);
    } finally {
      parent.kill("SIGKILL");
    }
  }, 15_000);

  it("spares a reused pid: alive, but started at a different time", async () => {
    const { parent, childPid } = await parentWithChild();
    let reads = 0;
    const tracker = new DescendantTracker(async () => `boot ${++reads}`);
    const lines: string[] = [];
    try {
      await tracked(tracker, parent.pid as number, childPid);
      const killed = await tracker.killSurvivors((l) => lines.push(l));
      expect(killed).toEqual([]);
      expect(alive(childPid)).toBe(true);
      expect(lines).toEqual([]);
    } finally {
      process.kill(childPid, "SIGKILL");
      parent.kill("SIGKILL");
    }
  }, 15_000);

  it("spares a pid whose identity cannot be read, and logs it once", async () => {
    const { parent, childPid } = await parentWithChild();
    let current: string | null = "first";
    const tracker = new DescendantTracker(async () => current);
    const lines: string[] = [];
    try {
      await tracked(tracker, parent.pid as number, childPid);
      current = null;
      const killed = await tracker.killSurvivors((l) => lines.push(l));
      expect(killed).toEqual([]);
      expect(alive(childPid)).toBe(true);
      expect(lines.filter((l) => l.includes("identity unreadable"))).toHaveLength(1);
    } finally {
      process.kill(childPid, "SIGKILL");
      parent.kill("SIGKILL");
    }
  }, 15_000);
});
