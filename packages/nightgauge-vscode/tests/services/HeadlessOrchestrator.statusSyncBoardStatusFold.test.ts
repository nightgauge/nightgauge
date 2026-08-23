/**
 * HeadlessOrchestrator.statusSyncBoardStatusFold.test.ts
 *
 * #623 — THE #699 CLOSED-ISSUE GUARD MUST FOLD ITS BOARD READ.
 *
 * `syncStageStatusTransition` carries a defence-in-depth guard (#699): before a
 * status-CHANGING transition (`issue-pickup` → "In progress", `pr-create` →
 * "In review") it reads the issue's CURRENT board Status and bails when that
 * column is already Done, so a `forceRerun` on a CLOSED issue cannot downgrade
 * it.
 *
 * The read is `getProjectItemStatus`, which returns
 * `ProjectV2ItemFieldSingleSelectValue.name` verbatim — the raw option label,
 * exactly the same provenance as `BoardItem.Status`. Comparing it with `===`
 * therefore asks "is this board spelled the way we happen to spell it", not
 * "is this the Done column". On a board whose Done column reads "done" or
 * "DONE" the guard fell through and the closed issue's status was overwritten
 * with "In progress" — silently, no error, no card. Same failure shape as the
 * stall watchdog this issue is named for.
 *
 * These cases were entirely uncovered before: neither `syncProjectStatus` nor
 * the "Skipping status sync" log line appeared anywhere under `tests/`.
 *
 * Proven red against the unfolded source: with `currentStatus === "Done"` in
 * place, every non-canonical capitalization below reached
 * `updateProjectItemStatus` with "In progress".
 *
 * @see packages/nightgauge-vscode/src/utils/projectFieldMapping.ts — boardStatusEquals
 */

import { describe, it, expect, vi, beforeEach, type Mock } from "vitest";

vi.mock("../../src/utils/projectFieldWriter", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../src/utils/projectFieldWriter")>();
  return {
    ...actual,
    getProjectItemStatus: vi.fn(),
    ensureIssueOnProject: vi.fn(),
    updateProjectItemStatus: vi.fn(),
  };
});

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import {
  getProjectItemStatus,
  ensureIssueOnProject,
  updateProjectItemStatus,
} from "../../src/utils/projectFieldWriter";
import { BOARD_STATUS } from "../../src/utils/projectFieldMapping";

const ISSUE = 623;

const readStatus = vi.mocked(getProjectItemStatus);
const ensureOnProject = vi.mocked(ensureIssueOnProject);
const writeStatus = vi.mocked(updateProjectItemStatus);

interface Harness {
  sync: (stage: string, issueNumber: number) => Promise<void>;
  logger: {
    info: Mock;
    warn: Mock;
    debug: Mock;
    error: Mock;
  };
}

/**
 * Build the smallest object `syncStageStatusTransition` needs.
 *
 * The method touches exactly four instance members — `contextLoader` (truthy
 * skips the "no workspace folder" bail that would otherwise make every case
 * pass vacuously), `logger`, `mainRepoRoot` (via `getPersistentRoot()`) and
 * `repoOverride` — so the real prototype is driven without standing up the
 * orchestrator's full dependency graph.
 */
function makeHarness(): Harness {
  const logger = {
    info: vi.fn(),
    warn: vi.fn(),
    debug: vi.fn(),
    error: vi.fn(),
  };
  const orchestrator = Object.create(HeadlessOrchestrator.prototype) as Record<string, unknown>;
  orchestrator.logger = logger;
  orchestrator.contextLoader = {};
  orchestrator.mainRepoRoot = "/tmp/nightgauge-623-status-sync";
  orchestrator.repoOverride = undefined;

  return {
    sync: (stage: string, issueNumber: number) =>
      (orchestrator.syncStageStatusTransition as (s: string, n: number) => Promise<void>).call(
        orchestrator,
        stage,
        issueNumber
      ),
    logger,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  ensureOnProject.mockResolvedValue(undefined as never);
  writeStatus.mockResolvedValue({ success: true } as never);
});

describe("syncStageStatusTransition — #699 closed-issue guard folds the board read", () => {
  // The provisioner writes "Done"; a hand-made board's creator types whatever
  // they like. Every one of these names the SAME column.
  const doneSpellings = ["Done", "done", "DONE", "dOnE"];

  for (const spelling of doneSpellings) {
    it(`skips the issue-pickup downgrade when the board's Done column reads "${spelling}"`, async () => {
      readStatus.mockResolvedValue(spelling);
      const { sync, logger } = makeHarness();

      await sync("issue-pickup", ISSUE);

      expect(readStatus).toHaveBeenCalledTimes(1);
      expect(writeStatus).not.toHaveBeenCalled();
      expect(ensureOnProject).not.toHaveBeenCalled();
      expect(logger.info).toHaveBeenCalledWith(
        "Skipping status sync — issue is CLOSED with Done status",
        expect.objectContaining({ stage: "issue-pickup", issueNumber: ISSUE })
      );
    });

    it(`skips the pr-create downgrade when the board's Done column reads "${spelling}"`, async () => {
      readStatus.mockResolvedValue(spelling);
      const { sync } = makeHarness();

      await sync("pr-create", ISSUE);

      expect(writeStatus).not.toHaveBeenCalled();
    });
  }

  it("still writes when the issue is genuinely not Done", async () => {
    for (const current of [BOARD_STATUS.ready, BOARD_STATUS.inProgress, BOARD_STATUS.backlog]) {
      vi.clearAllMocks();
      writeStatus.mockResolvedValue({ success: true } as never);
      readStatus.mockResolvedValue(current);
      const { sync } = makeHarness();

      await sync("issue-pickup", ISSUE);

      expect(writeStatus).toHaveBeenCalledWith(
        ISSUE,
        BOARD_STATUS.inProgress,
        expect.any(String),
        expect.anything(),
        undefined
      );
    }
  });

  it("does not confuse a similarly-spelled non-Done column with Done", async () => {
    // The fold is case-insensitive, NOT fuzzy: "Not done" is a different column.
    readStatus.mockResolvedValue("Not done");
    const { sync } = makeHarness();

    await sync("issue-pickup", ISSUE);

    expect(writeStatus).toHaveBeenCalledTimes(1);
  });

  it("writes when the board has no Status value at all", async () => {
    readStatus.mockResolvedValue(null);
    const { sync } = makeHarness();

    await sync("issue-pickup", ISSUE);

    expect(writeStatus).toHaveBeenCalledTimes(1);
  });

  it("proceeds with the write when the board read throws (fail-open, unchanged)", async () => {
    readStatus.mockRejectedValue(new Error("graphql 502"));
    const { sync, logger } = makeHarness();

    await sync("issue-pickup", ISSUE);

    expect(logger.debug).toHaveBeenCalledWith(
      "Could not pre-check issue state for status guard",
      expect.objectContaining({ issueNumber: ISSUE })
    );
    expect(writeStatus).toHaveBeenCalledTimes(1);
  });

  it("does not spend the read on mid-pipeline reinforcement stages", async () => {
    // feature-planning/dev/validate all repeat "In progress"; the issue cannot
    // have been closed mid-pipeline, so the guard's API call is skipped.
    for (const stage of ["feature-planning", "feature-dev", "feature-validate"]) {
      vi.clearAllMocks();
      writeStatus.mockResolvedValue({ success: true } as never);
      const { sync } = makeHarness();

      await sync(stage, ISSUE);

      expect(readStatus).not.toHaveBeenCalled();
      expect(writeStatus).toHaveBeenCalledTimes(1);
    }
  });

  it("does not run the guard against itself on pr-merge (the stage that sets Done)", async () => {
    const { sync } = makeHarness();

    await sync("pr-merge", ISSUE);

    expect(readStatus).not.toHaveBeenCalled();
    expect(writeStatus).toHaveBeenCalledWith(
      ISSUE,
      BOARD_STATUS.done,
      expect.any(String),
      expect.anything(),
      undefined
    );
  });
});
