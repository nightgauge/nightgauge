/**
 * Integration tests for Project Board Mapping in Service Context
 *
 * Tests the integration of mapping functions with ProjectBoardService,
 * verifying that labels are correctly extracted and mapped during
 * issue processing.
 *
 * Phase 5: Updated to mock IpcClient.boardList() instead of execAsync/GraphQL.
 *
 * These tests focus on:
 * - Priority/size parsing from BoardItem fields
 * - Priority mapping during issue retrieval
 * - Integration with mock field mappings
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import type { ReadyIssue } from "../../src/services/ProjectBoardService";
import type { BoardItem } from "../../src/services/IpcClient";
import { createMockReadyIssue } from "../mocks/github-api";
import { mapPriorityLabel, extractPriorityLabel } from "../../src/utils/projectFieldMapping";
import {
  MOCK_FIELD_MAPPINGS,
  getMockStatusOptionId,
  getMockPriorityOptionId,
  getMockSizeOptionId,
} from "../mocks/field-mappings";

// ---------------------------------------------------------------------------
// Helper to create a mock BoardItem (the shape returned by IpcClient.boardList)
// ---------------------------------------------------------------------------

function createMockBoardItem(overrides: Partial<BoardItem> = {}): BoardItem {
  return {
    id: `item-${overrides.number ?? 1}`,
    number: overrides.number ?? 1,
    title: overrides.title ?? `Issue #${overrides.number ?? 1}`,
    state: overrides.state ?? "OPEN",
    status: overrides.status ?? "Ready",
    priority: overrides.priority ?? "",
    size: overrides.size ?? "",
    labels: overrides.labels ?? [],
    assignees: overrides.assignees ?? [],
    repo: overrides.repo ?? "test-org/test-repo",
    url: overrides.url ?? `https://github.com/test-org/test-repo/issues/${overrides.number ?? 1}`,
    isEpic: overrides.isEpic ?? false,
    blockedBy: overrides.blockedBy ?? [],
    blocking: overrides.blocking ?? [],
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Mock IpcClient singleton so boardList() returns controlled data
// ---------------------------------------------------------------------------

const mockBoardList = vi.fn();

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      boardList: mockBoardList,
    }),
  },
}));

describe("ProjectBoardService - Label Mapping Integration", () => {
  let service: ProjectBoardService;
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    service = new ProjectBoardService(workspaceRoot);
    // Set config to avoid loadConfig file-system read
    (service as any).projectNumber = 1;
    (service as any).owner = "test-org";
    (service as any).configLoaded = true;
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("priority extraction from IPC board items", () => {
    it("should parse P0 priority from critical", async () => {
      mockBoardList.mockResolvedValue([
        createMockBoardItem({
          number: 1,
          labels: ["type:feature", "priority:critical", "size:M"],
          priority: "critical",
        }),
      ]);

      const issues = await service.getIssuesByStatus("ready");

      expect(issues).toHaveLength(1);
      expect(issues[0].priority).toBe("P0");
    });

    it("should parse P1 priority from high", async () => {
      mockBoardList.mockResolvedValue([
        createMockBoardItem({
          number: 2,
          labels: ["type:bug", "priority:high"],
          priority: "high",
        }),
      ]);

      const issues = await service.getIssuesByStatus("ready");

      expect(issues).toHaveLength(1);
      expect(issues[0].priority).toBe("P1");
    });

    it("should parse P2 priority from medium", async () => {
      mockBoardList.mockResolvedValue([
        createMockBoardItem({
          number: 3,
          labels: ["priority:medium"],
          priority: "medium",
        }),
      ]);

      const issues = await service.getIssuesByStatus("ready");

      expect(issues).toHaveLength(1);
      expect(issues[0].priority).toBe("P2");
    });

    it("should parse P3 priority from low", async () => {
      mockBoardList.mockResolvedValue([
        createMockBoardItem({
          number: 4,
          labels: ["priority:low"],
          priority: "low",
        }),
      ]);

      const issues = await service.getIssuesByStatus("ready");

      expect(issues).toHaveLength(1);
      expect(issues[0].priority).toBe("P3");
    });

    it("should return null priority when no priority field set", async () => {
      mockBoardList.mockResolvedValue([
        createMockBoardItem({
          number: 5,
          labels: ["type:feature", "size:M"],
          priority: "",
        }),
      ]);

      const issues = await service.getIssuesByStatus("ready");

      expect(issues).toHaveLength(1);
      expect(issues[0].priority).toBeNull();
    });

    it("should parse P0-P3 priority values directly", async () => {
      mockBoardList.mockResolvedValue([
        createMockBoardItem({ number: 10, priority: "P0" }),
        createMockBoardItem({ number: 11, priority: "P1" }),
        createMockBoardItem({ number: 12, priority: "P2" }),
        createMockBoardItem({ number: 13, priority: "P3" }),
      ]);

      const issues = await service.getIssuesByStatus("ready");

      expect(issues).toHaveLength(4);
      expect(issues[0].priority).toBe("P0");
      expect(issues[1].priority).toBe("P1");
      expect(issues[2].priority).toBe("P2");
      expect(issues[3].priority).toBe("P3");
    });
  });

  describe("size extraction from IPC board items", () => {
    const sizeCases: Array<[string, string]> = [
      ["XS", "XS"],
      ["S", "S"],
      ["M", "M"],
      ["L", "L"],
      ["XL", "XL"],
    ];

    sizeCases.forEach(([input, expected]) => {
      it(`should parse ${expected} size from '${input}'`, async () => {
        mockBoardList.mockResolvedValue([
          createMockBoardItem({
            number: 10,
            labels: ["type:feature"],
            size: input,
          }),
        ]);

        const issues = await service.getIssuesByStatus("ready");

        expect(issues).toHaveLength(1);
        expect(issues[0].size).toBe(expected);
      });
    });

    it("should handle case-insensitive size values", async () => {
      mockBoardList.mockResolvedValue([
        createMockBoardItem({ number: 20, size: "xs" }),
        createMockBoardItem({ number: 21, size: "m" }),
        createMockBoardItem({ number: 22, size: "xl" }),
      ]);

      const issues = await service.getIssuesByStatus("ready");

      expect(issues).toHaveLength(3);
      expect(issues[0].size).toBe("XS");
      expect(issues[1].size).toBe("M");
      expect(issues[2].size).toBe("XL");
    });
  });

  describe("mapping function integration", () => {
    it("should produce consistent priority mappings with utility functions", () => {
      const labels = ["type:feature", "priority:high", "size:M"];
      const priorityLabel = extractPriorityLabel(labels);
      const priorityValue = mapPriorityLabel(priorityLabel);

      expect(priorityValue).toBe("P1");
    });

    it("should handle issues with all field labels via IPC", async () => {
      mockBoardList.mockResolvedValue([
        createMockBoardItem({
          number: 20,
          labels: ["type:feature", "priority:critical", "size:XL"],
          priority: "P0",
          size: "XL",
        }),
      ]);

      const issues = await service.getIssuesByStatus("ready");

      expect(issues).toHaveLength(1);
      expect(issues[0].priority).toBe("P0");
      expect(issues[0].size).toBe("XL");
      expect(issues[0].labels).toContain("type:feature");
      expect(issues[0].labels).toContain("priority:critical");
      expect(issues[0].labels).toContain("size:XL");
    });

    it("should correctly extract priority from issue labels", () => {
      const issue = createMockReadyIssue({
        labels: ["type:feature", "priority:high", "size:M"],
      });

      const priorityLabel = extractPriorityLabel(issue.labels);
      expect(priorityLabel).toBe("priority:high");

      const priorityValue = mapPriorityLabel(priorityLabel);
      expect(priorityValue).toBe("P1");
    });
  });

  describe("mock field mappings integration", () => {
    it("should provide valid option IDs for status values", () => {
      expect(getMockStatusOptionId("Ready")).toBe("opt_ready_id");
      expect(getMockStatusOptionId("In progress")).toBe("opt_in_progress_id");
      expect(getMockStatusOptionId("In review")).toBe("opt_in_review_id");
      expect(getMockStatusOptionId("Done")).toBe("opt_done_id");
      expect(getMockStatusOptionId("Backlog")).toBe("opt_backlog_id");
    });

    it("should provide valid option IDs for priority values", () => {
      expect(getMockPriorityOptionId("P0")).toBe("opt_p0_id");
      expect(getMockPriorityOptionId("P1")).toBe("opt_p1_id");
      expect(getMockPriorityOptionId("P2")).toBe("opt_p2_id");
    });

    it("should provide valid option IDs for size values", () => {
      expect(getMockSizeOptionId("XS")).toBe("opt_xs_id");
      expect(getMockSizeOptionId("S")).toBe("opt_s_id");
      expect(getMockSizeOptionId("M")).toBe("opt_m_id");
      expect(getMockSizeOptionId("L")).toBe("opt_l_id");
      expect(getMockSizeOptionId("XL")).toBe("opt_xl_id");
    });

    it("should return undefined for unknown option values", () => {
      expect(getMockStatusOptionId("Unknown")).toBeUndefined();
      expect(getMockPriorityOptionId("P4")).toBeUndefined();
      expect(getMockSizeOptionId("XXL")).toBeUndefined();
    });

    it("should have all expected fields in mock mappings", () => {
      expect(MOCK_FIELD_MAPPINGS.project.id).toBe("PVT_test_project_id");
      expect(MOCK_FIELD_MAPPINGS.fields.status.id).toBe("PVTSSF_test_status");
      expect(MOCK_FIELD_MAPPINGS.fields.priority.id).toBe("PVTSSF_test_priority");
      expect(MOCK_FIELD_MAPPINGS.fields.size.id).toBe("PVTSSF_test_size");
    });
  });

  describe("sorting with mapped priorities", () => {
    it("should sort issues by extracted priority correctly", async () => {
      mockBoardList.mockResolvedValue([
        createMockBoardItem({
          number: 100,
          labels: ["priority:medium"],
          priority: "P2",
        }),
        createMockBoardItem({
          number: 101,
          labels: ["priority:critical"],
          priority: "P0",
        }),
        createMockBoardItem({
          number: 102,
          labels: ["priority:high"],
          priority: "P1",
        }),
      ]);

      const issues = await service.getIssuesByStatus("ready", "priority", "asc");

      expect(issues).toHaveLength(3);
      expect(issues[0].priority).toBe("P0");
      expect(issues[0].number).toBe(101);
      expect(issues[1].priority).toBe("P1");
      expect(issues[1].number).toBe(102);
      expect(issues[2].priority).toBe("P2");
      expect(issues[2].number).toBe(100);
    });
  });

  describe("label preservation", () => {
    it("should preserve all original labels in issue object", async () => {
      const originalLabels = [
        "type:feature",
        "priority:high",
        "size:M",
        "area:frontend",
        "needs-review",
      ];

      mockBoardList.mockResolvedValue([
        createMockBoardItem({
          number: 200,
          labels: originalLabels,
          priority: "high",
          size: "M",
        }),
      ]);

      const issues = await service.getIssuesByStatus("ready");

      expect(issues).toHaveLength(1);
      expect(issues[0].labels).toEqual(originalLabels);
    });
  });
});

describe("Mapping Function Edge Cases", () => {
  describe("multiple labels of same type", () => {
    it("should use first priority label when multiple exist", () => {
      const labels = ["priority:high", "priority:low", "type:feature"];
      const priorityLabel = extractPriorityLabel(labels);

      expect(priorityLabel).toBe("priority:high");
      expect(mapPriorityLabel(priorityLabel)).toBe("P1");
    });
  });
});

// ---------------------------------------------------------------------------
// Fallback priority/size inference (GitHubIssuesAdapter — Issue #2568)
// ---------------------------------------------------------------------------

import {
  inferPriorityFromLabels,
  inferSizeFromLabels,
} from "../../src/services/adapters/GitHubIssuesAdapter";

describe("inferPriorityFromLabels (fallback for repo-only issues)", () => {
  it("maps priority:critical → P0", () => {
    expect(inferPriorityFromLabels(["priority:critical"])).toBe("P0");
  });

  it("maps priority:high → P1", () => {
    expect(inferPriorityFromLabels(["priority:high"])).toBe("P1");
  });

  it("maps priority:medium → P2", () => {
    expect(inferPriorityFromLabels(["priority:medium"])).toBe("P2");
  });

  it("maps priority:low → P3", () => {
    expect(inferPriorityFromLabels(["priority:low"])).toBe("P3");
  });

  it("defaults to P2 when no priority label is present", () => {
    expect(inferPriorityFromLabels(["type:feature", "size:m"])).toBe("P2");
  });

  it("defaults to P2 for empty label array", () => {
    expect(inferPriorityFromLabels([])).toBe("P2");
  });

  it("is case-insensitive", () => {
    expect(inferPriorityFromLabels(["Priority:High"])).toBe("P1");
    expect(inferPriorityFromLabels(["PRIORITY:CRITICAL"])).toBe("P0");
  });

  it("uses the first matching priority label when multiple are present", () => {
    expect(inferPriorityFromLabels(["priority:critical", "priority:low"])).toBe("P0");
  });
});

describe("inferSizeFromLabels (fallback for repo-only issues)", () => {
  it("maps size:xs → XS", () => {
    expect(inferSizeFromLabels(["size:xs"])).toBe("XS");
  });

  it("maps size:s → S", () => {
    expect(inferSizeFromLabels(["size:s"])).toBe("S");
  });

  it("maps size:m → M", () => {
    expect(inferSizeFromLabels(["size:m"])).toBe("M");
  });

  it("maps size:l → L", () => {
    expect(inferSizeFromLabels(["size:l"])).toBe("L");
  });

  it("maps size:xl → XL", () => {
    expect(inferSizeFromLabels(["size:xl"])).toBe("XL");
  });

  it("defaults to M when no size label is present", () => {
    expect(inferSizeFromLabels(["type:feature", "priority:high"])).toBe("M");
  });

  it("defaults to M for empty label array", () => {
    expect(inferSizeFromLabels([])).toBe("M");
  });

  it("is case-insensitive", () => {
    expect(inferSizeFromLabels(["Size:XL"])).toBe("XL");
    expect(inferSizeFromLabels(["SIZE:S"])).toBe("S");
  });

  it("uses the first matching size label when multiple are present", () => {
    expect(inferSizeFromLabels(["size:xl", "size:xs"])).toBe("XL");
  });
});
