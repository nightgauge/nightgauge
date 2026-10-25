/**
 * #1501 — a board Status write that fails during a stage transition raises a
 * `board-status-write-failed` Action Center card, not only a WARN in the
 * session log. A successful write raises nothing.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

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

const ISSUE = 1501;
const writeStatus = vi.mocked(updateProjectItemStatus);

function makeHarness() {
  const raise = vi.fn().mockResolvedValue(undefined);
  const orchestrator = Object.create(HeadlessOrchestrator.prototype) as Record<string, unknown>;
  orchestrator.logger = { info: vi.fn(), warn: vi.fn(), debug: vi.fn(), error: vi.fn() };
  orchestrator.contextLoader = {};
  orchestrator.mainRepoRoot = "/tmp/nightgauge-1501-status-sync";
  orchestrator.repoOverride = undefined;
  orchestrator.raiseRunScopedAttention = raise;
  const sync = (stage: string) =>
    (orchestrator.syncStageStatusTransition as (s: string, n: number) => Promise<void>).call(
      orchestrator,
      stage,
      ISSUE
    );
  return { sync, raise };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(getProjectItemStatus).mockResolvedValue("Ready");
  vi.mocked(ensureIssueOnProject).mockResolvedValue(null);
});

describe("syncStageStatusTransition — failed board writes reach the Action Center", () => {
  it("raises board-status-write-failed when the write reports failure", async () => {
    writeStatus.mockResolvedValue({
      success: false,
      error: "Board fields unresolvable for acme/app: no project mapped",
    });
    const { sync, raise } = makeHarness();

    await sync("feature-dev");

    expect(raise).toHaveBeenCalledTimes(1);
    expect(raise).toHaveBeenCalledWith({
      producer: "board-status-write-failed",
      issueNumber: ISSUE,
      stage: "feature-dev",
    });
  });

  it("raises board-status-write-failed when the write throws", async () => {
    writeStatus.mockRejectedValue(new Error("boom"));
    const { sync, raise } = makeHarness();

    await sync("feature-dev");

    expect(raise).toHaveBeenCalledTimes(1);
  });

  it("raises nothing when the write succeeds", async () => {
    writeStatus.mockResolvedValue({ success: true });
    const { sync, raise } = makeHarness();

    await sync("feature-dev");

    expect(writeStatus).toHaveBeenCalled();
    expect(raise).not.toHaveBeenCalled();
  });
});
