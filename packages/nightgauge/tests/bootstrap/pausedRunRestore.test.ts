/**
 * #2339: the paused runs a window finds when it activates.
 *
 * Every run a window reload ended is held for the platform's verbs before
 * the first prompt waits for the operator, and a Resume consumes the
 * snapshot before it starts the new run, so it starts none when the platform
 * cancelled the run while the prompt was on screen, or another window
 * resumed it first.
 *
 * The holds and the machine's ledger are real; the pipeline manager they
 * hand the runs to, and the operator, are fixtures.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({}));

import {
  consumePausedSnapshot,
  restorePausedRuns,
  scanPausedSnapshots,
  type PausedSnapshot,
} from "../../src/bootstrap/pausedRunRestore";
import { RemoteRunLedger } from "../../src/services/RemoteRunLedger";
import { ReloadInterruptedRunHolds } from "../../src/utils/reloadInterruptedRun";

let dir: string;

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), "paused-run-restore-"));
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

/** A paused snapshot on disk; `remoteRunId` makes it a platform run a reload ended. */
function snapshot(issueNumber: number, remoteRunId?: string): PausedSnapshot {
  const filePath = path.join(dir, `runtime-${issueNumber}.json`);
  fs.writeFileSync(filePath, JSON.stringify({ paused: true, issueNumber }));
  return {
    filePath,
    issueNumber,
    interrupted: remoteRunId ? { remoteRunId, issueNumber } : null,
  };
}

/** One window: its holds over the machine's ledger, handed to its pipeline manager. */
function makeWindow(pid: number) {
  const held = new Map<string, () => Promise<void>>();
  const holds = new ReloadInterruptedRunHolds(
    new RemoteRunLedger(path.join(dir, "ledger"), { windowId: pid, isAlive: () => true })
  );
  holds.attach({
    holdReloadInterruptedRun: (remoteRunId, _issueNumber, end) => {
      held.set(remoteRunId, end ?? (async () => {}));
    },
    dropReloadInterruptedRun: (remoteRunId) => {
      held.delete(remoteRunId);
    },
  });
  /** The platform cancels the run: the manager ends it, as cancelByRemoteRunId does. */
  const cancel = async (remoteRunId: string) => {
    const end = held.get(remoteRunId);
    held.delete(remoteRunId);
    await end?.();
  };
  return { holds, held, cancel };
}

function makeDeps(holds: ReloadInterruptedRunHolds, answers: Array<Promise<string | undefined>>) {
  const asked: number[] = [];
  return {
    asked,
    deps: {
      holds,
      ask: vi.fn((issueNumber: number) => {
        asked.push(issueNumber);
        return answers.shift() ?? Promise.resolve(undefined);
      }),
      resume: vi.fn(async () => {}),
      gone: vi.fn(),
      logger: { info: vi.fn(), warn: vi.fn() },
    },
  };
}

describe("restorePausedRuns (#2339)", () => {
  it("holds every run a reload ended before the first prompt waits for the operator", async () => {
    const window = makeWindow(101);
    const first = deferred<string | undefined>();
    const { deps, asked } = makeDeps(window.holds, [first.promise]);

    const done = restorePausedRuns([snapshot(1, "run-1"), snapshot(2, "run-2")], deps);
    await vi.waitFor(() => expect(asked).toEqual([1]));
    // The first prompt is on screen: both runs are held, so the platform's
    // verbs for the second one are answered meanwhile.
    expect([...window.held.keys()].sort()).toEqual(["run-1", "run-2"]);

    first.resolve("Cancel");
    await done;
    expect(asked).toEqual([1, 2]);
    expect(deps.resume).not.toHaveBeenCalled();
  });

  it("starts nothing when the platform cancelled the run while its prompt was on screen", async () => {
    const window = makeWindow(101);
    const answer = deferred<string | undefined>();
    const { deps, asked } = makeDeps(window.holds, [answer.promise]);
    const paused = snapshot(3, "run-3");

    const done = restorePausedRuns([paused], deps);
    await vi.waitFor(() => expect(asked).toEqual([3]));
    await window.cancel("run-3");
    expect(fs.existsSync(paused.filePath)).toBe(false);

    answer.resolve("Resume");
    await done;
    expect(deps.resume).not.toHaveBeenCalled();
    expect(deps.gone).toHaveBeenCalledWith(3);
  });

  it("consumes the snapshot, gives the hold up, and starts the new run on Resume", async () => {
    const window = makeWindow(101);
    const { deps } = makeDeps(window.holds, [Promise.resolve("Resume")]);
    const paused = snapshot(4, "run-4");

    await restorePausedRuns([paused], deps);
    expect(fs.existsSync(paused.filePath)).toBe(false);
    expect(window.held.has("run-4")).toBe(false);
    expect(deps.resume).toHaveBeenCalledWith(4, undefined, undefined);
    expect(deps.gone).not.toHaveBeenCalled();
  });

  // Every window on a worktree of one clone finds the same snapshot and
  // prompts for it; at most one of them starts a run from it.
  it("starts one run from a snapshot two windows both resume", async () => {
    const a = makeWindow(101);
    const b = makeWindow(102);
    const paused = snapshot(5);
    const one = makeDeps(a.holds, [Promise.resolve("Resume")]);
    const two = makeDeps(b.holds, [Promise.resolve("Resume")]);

    await Promise.all([
      restorePausedRuns([paused], one.deps),
      restorePausedRuns([{ ...paused }], two.deps),
    ]);
    expect(one.deps.resume.mock.calls.length + two.deps.resume.mock.calls.length).toBe(1);
    expect(one.deps.gone.mock.calls.length + two.deps.gone.mock.calls.length).toBe(1);
  });

  it("still resumes when the snapshot cannot be removed for another reason", async () => {
    const window = makeWindow(101);
    const { deps } = makeDeps(window.holds, [Promise.resolve("Resume")]);
    const denied = Object.assign(new Error("permission denied"), { code: "EACCES" });

    await restorePausedRuns([snapshot(6)], { ...deps, consume: vi.fn().mockRejectedValue(denied) });
    expect(deps.resume).toHaveBeenCalledWith(6, undefined, undefined);
    expect(deps.logger.warn).toHaveBeenCalledWith(
      "Could not remove the paused snapshot after Resume",
      expect.objectContaining({ issueNumber: 6 })
    );
  });

  // An unlink is not exclusive: two concurrent unlinks of one file can both
  // succeed (APFS). A rename is, so exactly one consumer takes a snapshot.
  it("lets exactly one of several concurrent consumers take a snapshot", async () => {
    for (let round = 0; round < 50; round++) {
      const { filePath } = snapshot(9);
      const taken = await Promise.all([1, 2, 3].map(() => consumePausedSnapshot(filePath)));
      expect(taken.filter(Boolean)).toHaveLength(1);
      expect(fs.existsSync(filePath)).toBe(false);
    }
    expect(fs.readdirSync(dir).filter((f) => f.startsWith("runtime-9"))).toEqual([]);
    expect(await consumePausedSnapshot(path.join(dir, "missing.json"))).toBe(false);
  });

  it("goes on to the next prompt when a resume fails", async () => {
    const window = makeWindow(101);
    const { deps, asked } = makeDeps(window.holds, [
      Promise.resolve("Resume"),
      Promise.resolve("Resume"),
    ]);
    deps.resume.mockRejectedValueOnce(new Error("no daemon"));

    await restorePausedRuns([snapshot(7), snapshot(8)], deps);
    expect(asked).toEqual([7, 8]);
    expect(deps.resume).toHaveBeenCalledTimes(2);
  });
});

// Review finding A4: a platform run of a linked repository pauses in that
// repository's clone, so a window must hold every repository's paused runs,
// not only the primary one's, and route a Resume to the run's repository.
describe("paused runs of every repository of the window (#2339)", () => {
  function writeRuntime(pipelineDir: string, issueNumber: number, extra: object): string {
    fs.mkdirSync(pipelineDir, { recursive: true });
    const file = path.join(pipelineDir, `runtime-${issueNumber}.json`);
    fs.writeFileSync(
      file,
      JSON.stringify({
        paused: true,
        issueNumber,
        repo: "acme/api",
        stage: "feature-dev",
        ...extra,
      })
    );
    return file;
  }
  const logger = { info: vi.fn(), warn: vi.fn() };

  it("finds a linked repository's paused platform run and marks its repository", async () => {
    const pipelineDir = path.join(dir, "api-clone", "pipeline");
    writeRuntime(pipelineDir, 7, { remoteRunId: "run-7", ownerPid: 999999 });

    const found = await scanPausedSnapshots(
      { pipelineDir, containingRepoSlug: "acme/api", repo: { owner: "acme", repo: "api" } },
      logger,
      () => false
    );

    expect(found).toEqual([
      {
        filePath: path.join(pipelineDir, "runtime-7.json"),
        issueNumber: 7,
        interrupted: { remoteRunId: "run-7", issueNumber: 7 },
        repo: { owner: "acme", repo: "api" },
      },
    ]);
  });

  it("leaves the primary repository's snapshots unmarked, and a live owner's run unheld", async () => {
    const pipelineDir = path.join(dir, "primary", "pipeline");
    writeRuntime(pipelineDir, 8, { remoteRunId: "run-8", ownerPid: 4242 });

    const found = await scanPausedSnapshots(
      { pipelineDir, containingRepoSlug: "acme/api" },
      logger,
      (pid) => pid === 4242
    );

    expect(found).toHaveLength(1);
    expect(found[0].interrupted).toBeNull();
    expect("repo" in found[0]).toBe(false);
  });

  it("holds a linked repository's run and resumes it in that repository", async () => {
    const window = makeWindow(101);
    const { deps } = makeDeps(window.holds, [Promise.resolve("Resume")]);
    const pipelineDir = path.join(dir, "api-clone", "pipeline");
    writeRuntime(pipelineDir, 9, { remoteRunId: "run-9" });
    const found = await scanPausedSnapshots(
      { pipelineDir, containingRepoSlug: "acme/api", repo: { owner: "acme", repo: "api" } },
      logger,
      () => false
    );

    const done = restorePausedRuns(found, deps);
    await done;

    expect(deps.resume).toHaveBeenCalledWith(9, { owner: "acme", repo: "api" }, undefined);
    expect(fs.existsSync(path.join(pipelineDir, "runtime-9.json"))).toBe(false);
  });

  it("holds an unidentified repository's run for the platform but offers no Resume", async () => {
    const window = makeWindow(101);
    const { deps, asked } = makeDeps(window.holds, []);
    const filePath = writeRuntime(path.join(dir, "unknown", "pipeline"), 10, {
      remoteRunId: "run-10",
    });

    await restorePausedRuns(
      [
        {
          filePath,
          issueNumber: 10,
          interrupted: { remoteRunId: "run-10", issueNumber: 10 },
          repo: null,
        },
      ],
      deps
    );

    expect(window.held.has("run-10")).toBe(true);
    expect(asked).toEqual([]);
    expect(deps.resume).not.toHaveBeenCalled();
    // A platform cancel still ends it.
    await window.cancel("run-10");
    expect(fs.existsSync(filePath)).toBe(false);
  });
});
