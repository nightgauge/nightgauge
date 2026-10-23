/**
 * stageBudget.processTree.test.ts (Issue #1668)
 *
 * A stage stopped at a stage budget must leave no member of its process tree
 * alive: a missed kill is an unbounded loop. Real processes, so the real
 * child_process and pgrep: a child that traps SIGTERM and has spawned a
 * grandchild ends with both pids gone.
 */
import { describe, it, expect } from "vitest";
import { spawn } from "child_process";
import { terminateStageProcessTree } from "../../src/utils/stageBudget";

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

describe.skipIf(process.platform === "win32")("terminateStageProcessTree (#1668)", () => {
  it("kills a TERM-trapping child and its grandchild, and verifies both are gone", async () => {
    // The child ignores SIGTERM, starts a grandchild, prints its pid, and
    // waits on it forever.
    const child = spawn(
      "/bin/sh",
      ["-c", "trap '' TERM; sleep 600 & echo $!; while :; do sleep 1; done"],
      { stdio: ["ignore", "pipe", "ignore"] }
    );
    const childPid = child.pid as number;
    const exited = new Promise<void>((resolve) => child.on("exit", () => resolve()));
    const grandchildPid = await new Promise<number>((resolve) => {
      child.stdout?.once("data", (d: Buffer) => resolve(Number(String(d).trim())));
    });
    try {
      expect(alive(childPid)).toBe(true);
      expect(alive(grandchildPid)).toBe(true);

      const result = await terminateStageProcessTree(childPid, {
        graceMs: 300,
        isRootGone: () => child.exitCode !== null || child.signalCode !== null,
        signalRoot: (sig) => child.kill(sig),
      });
      await exited;

      expect(result.pids).toEqual(expect.arrayContaining([childPid, grandchildPid]));
      expect(result.survivors).toEqual([]);
      expect(child.signalCode).toBe("SIGKILL");
      expect(() => process.kill(childPid, 0)).toThrow();
      expect(() => process.kill(grandchildPid, 0)).toThrow();
    } finally {
      for (const pid of [grandchildPid, childPid]) {
        try {
          process.kill(pid, "SIGKILL");
        } catch {
          /* gone */
        }
      }
    }
  }, 15_000);
});
