/**
 * cliPipelineSlots.test.ts
 *
 * #586 — a `nightgauge run` started in a terminal was given a pipeline TREE
 * slot and nothing else. No Output-window slot was ever registered for it, so
 * the window went on showing the last extension-launched run's bytes with
 * nothing on screen saying whose they were.
 *
 * The callbacks that did that lived as an inline closure inside
 * `registerServices()`, where no test could execute them — which is why the
 * missing half stayed missing. This file imports the REAL
 * `createCliPipelineSlotCallbacks` and drives it with stub dependencies, so
 * the tree half and the Output-window half are both observed rather than
 * spelled: deleting either one turns this suite red.
 */

import { describe, it, expect, vi } from "vitest";
import { createCliPipelineSlotCallbacks } from "../../src/bootstrap/cliPipelineSlots";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { ReconciledCliRun } from "../../src/services/CliPipelineReconciliationService";

const RUN_ID = "01a0bea9-1669-745e-8be0-4dc1a7ccb83c";

function makeRun(overrides: Partial<ReconciledCliRun> = {}): ReconciledCliRun {
  return {
    key: `/repo::1644::${RUN_ID}`,
    root: "/repo",
    snapshot: {
      repo: "nightgauge/nightgauge",
      issueNumber: 1644,
      runId: RUN_ID,
      title: "OpenCode egress verification",
    },
    ...overrides,
  };
}

/** Stub deps plus the recorders the assertions read. */
function makeDeps(options: { treeSlotExists?: boolean } = {}) {
  const stateServices: Array<{
    root: string;
    issueNumber: number;
    disposed: boolean;
    applied: number;
  }> = [];

  const makeStateService = (root: string, issueNumber: number): PipelineStateService => {
    const record = { root, issueNumber, disposed: false, applied: 0 };
    stateServices.push(record);
    return {
      applyRuntimeSnapshot: vi.fn(() => {
        record.applied += 1;
      }),
      dispose: vi.fn(() => {
        record.disposed = true;
      }),
    } as unknown as PipelineStateService;
  };

  const tree = {
    getConcurrentSlot: vi.fn(() => (options.treeSlotExists ? {} : undefined)),
    addConcurrentSlot: vi.fn(),
    removeConcurrentSlotIfOwned: vi.fn(),
  };
  const output = {
    registerSlotInfo: vi.fn(),
    setSlotLogRoot: vi.fn(),
    removeSlotInfoIfOwned: vi.fn(),
  };

  let nextSlotIndex = 3;
  const deps = {
    tree: () => tree,
    output: () => output,
    createStateService: makeStateService,
    nextSlotIndex: () => nextSlotIndex++,
  };

  return { deps, tree, output, stateServices };
}

describe("CLI-discovered pipeline slots (#586)", () => {
  it("registers an Output-window slot, not only a tree slot, when a CLI run is discovered", () => {
    const { deps, tree, output } = makeDeps();
    const binding = createCliPipelineSlotCallbacks(deps);

    binding.callbacks.onDiscovered(makeRun());

    expect(tree.addConcurrentSlot).toHaveBeenCalledTimes(1);
    expect(output.registerSlotInfo).toHaveBeenCalledTimes(1);

    // Same slot index on both sides: the tab the user clicks and the tree row
    // they see must be the same run.
    const treeSlotIndex = tree.addConcurrentSlot.mock.calls[0][0];
    const outputSlotIndex = output.registerSlotInfo.mock.calls[0][0];
    expect(outputSlotIndex).toBe(treeSlotIndex);
  });

  it("carries the run's identity onto the Output-window slot", () => {
    const { deps, output } = makeDeps();
    const binding = createCliPipelineSlotCallbacks(deps);

    binding.callbacks.onDiscovered(makeRun());

    const [, issueNumber, title, repoSlug, slotOptions] = output.registerSlotInfo.mock.calls[0];
    expect(issueNumber).toBe(1644);
    expect(title).toBe("OpenCode egress verification");
    expect(repoSlug).toBe("nightgauge/nightgauge");
    expect(slotOptions).toEqual({ runId: RUN_ID, origin: "cli" });
  });

  it("falls back to an issue-numbered title when the snapshot carries none", () => {
    const { deps, output } = makeDeps();
    const binding = createCliPipelineSlotCallbacks(deps);

    const run = makeRun();
    delete run.snapshot.title;
    binding.callbacks.onDiscovered(run);

    expect(output.registerSlotInfo.mock.calls[0][2]).toBe("Issue #1644");
  });

  it("scopes the slot's disk log reads to the root the run executes in (#191)", () => {
    const { deps, output } = makeDeps();
    const binding = createCliPipelineSlotCallbacks(deps);

    binding.callbacks.onDiscovered(makeRun());

    expect(output.setSlotLogRoot).toHaveBeenCalledWith(
      output.registerSlotInfo.mock.calls[0][0],
      "/repo"
    );
  });

  it("yields to an IPC-managed slot for the same issue — that run is streamed", () => {
    const { deps, tree, output } = makeDeps({ treeSlotExists: true });
    const binding = createCliPipelineSlotCallbacks(deps);

    binding.callbacks.onDiscovered(makeRun());

    expect(tree.addConcurrentSlot).not.toHaveBeenCalled();
    expect(output.registerSlotInfo).not.toHaveBeenCalled();
  });

  it("removes BOTH slots, and disposes the state relay, when the run settles", () => {
    const { deps, tree, output, stateServices } = makeDeps();
    const binding = createCliPipelineSlotCallbacks(deps);
    const run = makeRun();

    binding.callbacks.onDiscovered(run);
    const slotIndex = output.registerSlotInfo.mock.calls[0][0];
    binding.callbacks.onSettled(run);

    expect(tree.removeConcurrentSlotIfOwned).toHaveBeenCalledTimes(1);
    expect(output.removeSlotInfoIfOwned).toHaveBeenCalledWith(slotIndex, RUN_ID);
    expect(output.setSlotLogRoot).toHaveBeenLastCalledWith(slotIndex, null);
    expect(stateServices[0].disposed).toBe(true);
  });

  it("ignores a settle for a run it never tracked", () => {
    const { deps, tree, output } = makeDeps();
    const binding = createCliPipelineSlotCallbacks(deps);

    binding.callbacks.onSettled(makeRun());

    expect(tree.removeConcurrentSlotIfOwned).not.toHaveBeenCalled();
    expect(output.removeSlotInfoIfOwned).not.toHaveBeenCalled();
  });

  it("feeds later snapshots to the tracked run's state relay only", () => {
    const { deps, stateServices } = makeDeps();
    const binding = createCliPipelineSlotCallbacks(deps);
    const run = makeRun();

    binding.callbacks.onDiscovered(run); // seeds the relay with the first snapshot
    binding.callbacks.onUpdated(run);
    binding.callbacks.onUpdated(makeRun({ key: "/other::99::x" })); // untracked — no throw

    expect(stateServices).toHaveLength(1);
    expect(stateServices[0].applied).toBe(2);
  });

  it("disposes every tracked relay on extension teardown", () => {
    const { deps, stateServices } = makeDeps();
    const binding = createCliPipelineSlotCallbacks(deps);

    binding.callbacks.onDiscovered(makeRun());
    binding.disposeAll();

    expect(stateServices[0].disposed).toBe(true);
  });
});
