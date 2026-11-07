/**
 * #2706: the pr-merge stage sync writes Done only for a closed issue.
 *
 * The post-merge hook runs first. On a `Refs` merge it keeps the issue open and
 * writes Ready, so an unconditional Done left the issue open but Done, and the
 * scheduler never dispatched the remainder. The fake board below holds the
 * hook's write, so the assertion is on the status the board ends with.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../../src/utils/projectFieldWriter", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../src/utils/projectFieldWriter")>();
  return {
    ...actual,
    getIssueState: vi.fn(),
    getProjectItemStatus: vi.fn(),
    ensureIssueOnProject: vi.fn(),
    updateProjectItemStatus: vi.fn(),
  };
});

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import { getIssueState, updateProjectItemStatus } from "../../src/utils/projectFieldWriter";

const ISSUE = 2706;
const readState = vi.mocked(getIssueState);
const writeStatus = vi.mocked(updateProjectItemStatus);

function makeSync() {
  const orchestrator = Object.create(HeadlessOrchestrator.prototype) as Record<string, unknown>;
  orchestrator.logger = { info: vi.fn(), warn: vi.fn(), debug: vi.fn(), error: vi.fn() };
  orchestrator.contextLoader = {};
  orchestrator.mainRepoRoot = "/tmp/nightgauge-2706";
  orchestrator.repoOverride = undefined;
  orchestrator.raiseBoardStatusWriteFailed = vi.fn();
  return {
    orchestrator,
    sync: (stage: string) =>
      (
        orchestrator as unknown as {
          syncStageStatusTransition: (s: string, n: number) => Promise<void>;
        }
      ).syncStageStatusTransition(stage, ISSUE),
  };
}

describe("pr-merge stage sync", () => {
  let board: string;

  beforeEach(() => {
    vi.clearAllMocks();
    board = "Ready"; // the post-merge hook's write
    writeStatus.mockImplementation(async (_n, status) => {
      board = status;
      return { success: true };
    });
  });

  it("keeps the hook's Ready after a Refs merge leaves the issue open", async () => {
    readState.mockResolvedValue("OPEN");
    await makeSync().sync("pr-merge");
    expect(board).toBe("Ready");
  });

  it("writes Ready itself when the hook could not resolve a board", async () => {
    board = "In review";
    readState.mockResolvedValue("OPEN");
    await makeSync().sync("pr-merge");
    expect(board).toBe("Ready");
  });

  it("ends Done after a Closes merge closes the issue", async () => {
    readState.mockResolvedValue("CLOSED");
    await makeSync().sync("pr-merge");
    expect(board).toBe("Done");
  });

  it("writes nothing and raises the card when the state is unreadable", async () => {
    readState.mockResolvedValue(null);
    const { orchestrator, sync } = makeSync();
    await sync("pr-merge");
    expect(writeStatus).not.toHaveBeenCalled();
    expect(orchestrator.raiseBoardStatusWriteFailed).toHaveBeenCalledWith(ISSUE, "pr-merge");
  });
});
