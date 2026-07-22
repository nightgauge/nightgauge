/**
 * DashboardState.epicEstimates.test.ts
 *
 * Tests for the updated refreshEpicEstimates() behavior that handles
 * epics which fail estimation (e.g., no sub-issues) gracefully instead
 * of silently skipping them.
 *
 * @see Issue #987 - Epic Detection Fails Silently
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

// Mock vscode
vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn().mockReturnValue(undefined),
    })),
  },
  EventEmitter: class EventEmitter {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
}));

// Mock IpcClient
const mockIssueList = vi.fn();
vi.mock("../../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      issueList: mockIssueList,
    }),
  },
}));

// Mock getRepoIdentity
const mockGetRepoIdentity = vi.fn().mockResolvedValue({ owner: "test-owner", repo: "test-repo" });
vi.mock("../../../src/utils/configPathResolver", () => ({
  getRepoIdentity: (...args: unknown[]) => mockGetRepoIdentity(...args),
}));

// Mock EpicEstimator
const mockEstimateEpic = vi.fn();
vi.mock("@nightgauge/sdk", () => ({
  EpicEstimator: class MockEpicEstimator {
    estimateEpic = mockEstimateEpic;
  },
  EpicBatchAssessor: class MockEpicBatchAssessor {
    assess = vi.fn();
  },
}));

import { DashboardState, type EpicDisplayEntry } from "../../../src/views/dashboard/DashboardState";
import type { EpicEstimate } from "@nightgauge/sdk";

function makeEpicEstimate(
  epicNumber: number,
  title: string,
  remainingMinutes: number = 600
): EpicEstimate {
  return {
    epic_number: epicNumber,
    epic_title: title,
    sub_issues: [
      {
        number: epicNumber + 100,
        title: `Sub-issue of #${epicNumber}`,
        size: "M",
        estimated_minutes: remainingMinutes,
        status: "open",
      },
    ],
    total_estimated_minutes: remainingMinutes * 2,
    total_remaining_minutes: remainingMinutes,
    integration_buffer_minutes: Math.round(remainingMinutes * 0.15),
    confidence: "medium",
    confidence_detail: "Based on moderate historical data",
  };
}

describe("DashboardState - refreshEpicEstimates (Issue #987)", () => {
  let state: DashboardState;
  let workspaceState: vscode.Memento;

  beforeEach(() => {
    vi.clearAllMocks();
    mockGetRepoIdentity.mockResolvedValue({
      owner: "test-owner",
      repo: "test-repo",
    });
    workspaceState = createMockMemento();
    state = new DashboardState(workspaceState, "/test/workspace");
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("should return all entries with estimates when all epics estimate successfully", async () => {
    mockIssueList.mockResolvedValue([
      {
        number: 10,
        title: "Epic A",
        state: "open",
        labels: ["type:epic"],
        assignees: [],
        url: "",
        isEpic: true,
      },
      {
        number: 20,
        title: "Epic B",
        state: "open",
        labels: ["type:epic"],
        assignees: [],
        url: "",
        isEpic: true,
      },
    ]);
    mockEstimateEpic
      .mockResolvedValueOnce(makeEpicEstimate(10, "Epic A", 600))
      .mockResolvedValueOnce(makeEpicEstimate(20, "Epic B", 300));

    await state.refreshEpicEstimates();
    const aggregates = state.getAggregates();

    expect(aggregates.epicEstimates).toHaveLength(2);
    expect(aggregates.epicEstimates[0].estimate).not.toBeNull();
    expect(aggregates.epicEstimates[0].warning).toBeNull();
    expect(aggregates.epicEstimates[1].estimate).not.toBeNull();
    expect(aggregates.epicEstimates[1].warning).toBeNull();
  });

  it("should include failed epics with warning when some epics fail estimation", async () => {
    mockIssueList.mockResolvedValue([
      {
        number: 10,
        title: "Epic A",
        state: "open",
        labels: ["type:epic"],
        assignees: [],
        url: "",
        isEpic: true,
      },
      {
        number: 20,
        title: "Epic B (no sub-issues)",
        state: "open",
        labels: ["type:epic"],
        assignees: [],
        url: "",
        isEpic: true,
      },
    ]);
    mockEstimateEpic
      .mockResolvedValueOnce(makeEpicEstimate(10, "Epic A", 600))
      .mockRejectedValueOnce(new Error("Epic #20 has no sub-issue references."));

    await state.refreshEpicEstimates();
    const aggregates = state.getAggregates();

    expect(aggregates.epicEstimates).toHaveLength(2);

    // First entry: successfully estimated (sorted first)
    const estimated = aggregates.epicEstimates[0];
    expect(estimated.estimate).not.toBeNull();
    expect(estimated.epic_number).toBe(10);
    expect(estimated.warning).toBeNull();

    // Second entry: failed estimation
    const failed = aggregates.epicEstimates[1];
    expect(failed.estimate).toBeNull();
    expect(failed.epic_number).toBe(20);
    expect(failed.epic_title).toBe("Epic B (no sub-issues)");
    expect(failed.warning).toContain("no sub-issue references");
  });

  it("should return all entries with warnings when all epics fail estimation", async () => {
    mockIssueList.mockResolvedValue([
      {
        number: 10,
        title: "Epic A",
        state: "open",
        labels: ["type:epic"],
        assignees: [],
        url: "",
        isEpic: true,
      },
      {
        number: 20,
        title: "Epic B",
        state: "open",
        labels: ["type:epic"],
        assignees: [],
        url: "",
        isEpic: true,
      },
    ]);
    mockEstimateEpic
      .mockRejectedValueOnce(new Error("Epic #10 has no sub-issue references."))
      .mockRejectedValueOnce(new Error("Epic #20 has no sub-issue references."));

    await state.refreshEpicEstimates();
    const aggregates = state.getAggregates();

    expect(aggregates.epicEstimates).toHaveLength(2);
    expect(aggregates.epicEstimates.every((e) => e.estimate === null)).toBe(true);
    expect(aggregates.epicEstimates.every((e) => e.warning !== null)).toBe(true);
  });

  it("should return empty array when no epics from issue list", async () => {
    mockIssueList.mockResolvedValue([]);

    await state.refreshEpicEstimates();
    const aggregates = state.getAggregates();

    expect(aggregates.epicEstimates).toHaveLength(0);
    expect(mockEstimateEpic).not.toHaveBeenCalled();
  });

  it("should return empty array when issue list call fails", async () => {
    mockIssueList.mockRejectedValue(new Error("IPC connection failed"));

    await state.refreshEpicEstimates();
    const aggregates = state.getAggregates();

    expect(aggregates.epicEstimates).toHaveLength(0);
  });

  it("should sort estimated epics before failed epics", async () => {
    mockIssueList.mockResolvedValue([
      {
        number: 10,
        title: "Failed Epic",
        state: "open",
        labels: ["type:epic"],
        assignees: [],
        url: "",
        isEpic: true,
      },
      {
        number: 20,
        title: "Estimated Epic",
        state: "open",
        labels: ["type:epic"],
        assignees: [],
        url: "",
        isEpic: true,
      },
    ]);
    mockEstimateEpic
      .mockRejectedValueOnce(new Error("no sub-issues"))
      .mockResolvedValueOnce(makeEpicEstimate(20, "Estimated Epic", 300));

    await state.refreshEpicEstimates();
    const aggregates = state.getAggregates();

    // Estimated should come first even though Failed was listed first
    expect(aggregates.epicEstimates[0].epic_number).toBe(20);
    expect(aggregates.epicEstimates[0].estimate).not.toBeNull();
    expect(aggregates.epicEstimates[1].epic_number).toBe(10);
    expect(aggregates.epicEstimates[1].estimate).toBeNull();
  });

  it("should use epic title from gh query for failed epics", async () => {
    mockIssueList.mockResolvedValue([
      {
        number: 42,
        title: "My Epic Title From GH",
        state: "open",
        labels: ["type:epic"],
        assignees: [],
        url: "",
        isEpic: true,
      },
    ]);
    mockEstimateEpic.mockRejectedValueOnce(new Error("no sub-issues"));

    await state.refreshEpicEstimates();
    const aggregates = state.getAggregates();

    expect(aggregates.epicEstimates[0].epic_title).toBe("My Epic Title From GH");
  });

  it("should call issueList with type:epic label filter", async () => {
    mockIssueList.mockResolvedValue([]);

    await state.refreshEpicEstimates();

    expect(mockIssueList).toHaveBeenCalledWith("test-owner", "test-repo", {
      labels: ["type:epic"],
    });
  });
});
