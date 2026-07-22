/**
 * EpicDashboard.test.ts
 *
 * Comprehensive test suite for EpicDashboard cross-repository progress tracking.
 * Tests cover:
 * - Cross-repo epic detection
 * - Repository-grouped progress calculation
 * - Cache management and TTL
 * - Error handling for failed repo queries
 * - Mixed success/failure across repos
 *
 * @see Issue #330 - Epic Dashboard with Cross-Repo Progress
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  EpicDashboard,
  type CrossRepoEpicProgress,
  type RepositoryProgress,
} from "../../../src/views/dashboard/EpicDashboard";
import type { WorkspaceManager } from "../../../src/services/WorkspaceManager";
import type { Repository } from "../../../src/models/Repository";
import type { IssueDetail } from "../../../src/services/IpcClient";

// Mock IpcClient
const mockIssueView = vi.fn();
const mockIssueList = vi.fn();
// issueViewMany dispatches to issueView per number so existing test setups
// using mockIssueView.mockImplementation((owner, repo, number) => {...}) keep
// working — same data, batched response shape. Issues that throw are dropped
// from the batch result, matching the real Go implementation that silently
// omits null aliases.
const mockIssueViewMany = vi.fn(async (owner: string, repo: string, numbers: number[]) => {
  const results = [];
  for (const n of numbers) {
    try {
      results.push(await mockIssueView(owner, repo, n));
    } catch {
      // Drop missing issues — matches null-alias semantics.
    }
  }
  return results;
});
vi.mock("../../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      issueView: mockIssueView,
      issueList: mockIssueList,
      issueViewMany: mockIssueViewMany,
    }),
  },
}));

// Mock getRepoIdentity
const mockGetRepoIdentity = vi.fn().mockResolvedValue({ owner: "test-owner", repo: "test-repo" });
vi.mock("../../../src/utils/configPathResolver", () => ({
  getRepoIdentity: (...args: unknown[]) => mockGetRepoIdentity(...args),
}));

/**
 * Helper: create an IssueDetail response for an epic (with subIssues)
 */
function makeEpicIssue(number: number, title: string, subIssueNumbers: number[]): IssueDetail {
  return {
    number,
    title,
    body: "",
    state: "OPEN",
    labels: ["type:epic"],
    assignees: [],
    url: `https://github.com/test-owner/test-repo/issues/${number}`,
    isEpic: true,
    subIssues: subIssueNumbers.map((n) => ({
      number: n,
      title: `Sub-issue #${n}`,
      state: "OPEN",
    })),
  };
}

/**
 * Helper: create an IssueDetail response for a sub-issue
 */
function makeSubIssue(
  number: number,
  title: string,
  sizeLabel: string | null,
  state: "OPEN" | "CLOSED"
): IssueDetail {
  const labels: string[] = [];
  if (sizeLabel) {
    labels.push(`size:${sizeLabel}`);
  }
  return {
    number,
    title,
    body: "",
    state,
    labels,
    assignees: [],
    url: `https://github.com/test-owner/test-repo/issues/${number}`,
    isEpic: false,
  };
}

/**
 * Create a mock Repository
 */
function createMockRepository(
  name: string,
  path: string,
  role?: "primary" | "secondary" | "shared"
): Repository {
  return {
    name,
    path,
    role,
  } as Repository;
}

/**
 * Create a mock WorkspaceManager
 */
function createMockWorkspaceManager(
  repositories: Repository[],
  currentRepo?: Repository
): WorkspaceManager {
  return {
    getAllRepositories: vi.fn(() => repositories),
    getCurrentRepository: vi.fn(() => currentRepo ?? repositories[0] ?? null),
    isMultiWorkspace: vi.fn(() => repositories.length > 1),
    getRepository: vi.fn((name: string) => repositories.find((r) => r.name === name)),
  } as unknown as WorkspaceManager;
}

describe("EpicDashboard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    mockGetRepoIdentity.mockResolvedValue({
      owner: "test-owner",
      repo: "test-repo",
    });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  describe("constructor", () => {
    it("should create instance with default cache TTL", () => {
      const manager = createMockWorkspaceManager([]);
      const dashboard = new EpicDashboard(manager);

      expect(dashboard).toBeInstanceOf(EpicDashboard);
    });

    it("should create instance with custom cache TTL", () => {
      const manager = createMockWorkspaceManager([]);
      const customTtl = 60000; // 1 minute
      const dashboard = new EpicDashboard(manager, customTtl);

      expect(dashboard).toBeInstanceOf(EpicDashboard);
    });
  });

  describe("getCrossRepoProgress()", () => {
    it("should fetch epic metadata and calculate progress", async () => {
      const repos = [
        createMockRepository("frontend", "/test/frontend", "primary"),
        createMockRepository("backend", "/test/backend", "secondary"),
      ];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      // Epic #100 has sub-issues 101, 102, 103
      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 100) {
          return Promise.resolve(makeEpicIssue(100, "Epic: User Authentication", [101, 102, 103]));
        }
        if (number === 101) {
          return Promise.resolve(makeSubIssue(101, "Login UI", "M", "CLOSED"));
        }
        if (number === 102) {
          return Promise.resolve(makeSubIssue(102, "Auth API", "L", "OPEN"));
        }
        if (number === 103) {
          return Promise.reject(new Error("Issue not found"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(100);

      expect(progress.epicNumber).toBe(100);
      expect(progress.epicTitle).toBe("Epic: User Authentication");
      expect(progress.fetchedAt).toBeInstanceOf(Date);
    });

    it("should calculate correct completion percentage", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 200) {
          return Promise.resolve(makeEpicIssue(200, "Test Epic", [201, 202]));
        }
        if (number === 201) {
          return Promise.resolve(makeSubIssue(201, "Closed Issue", "M", "CLOSED"));
        }
        if (number === 202) {
          return Promise.resolve(makeSubIssue(202, "Open Issue", "M", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(200);

      // 1 of 2 M-sized issues closed = 50%
      expect(progress.overallCompletionPercent).toBe(50);
    });

    it("should identify cross-repo epics correctly", async () => {
      const repos = [
        createMockRepository("frontend", "/test/frontend"),
        createMockRepository("backend", "/test/backend"),
      ];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 300) {
          return Promise.resolve(makeEpicIssue(300, "Cross-Repo Epic", [301, 302]));
        }
        if (number === 301) {
          return Promise.resolve(makeSubIssue(301, "Frontend Task", "S", "OPEN"));
        }
        if (number === 302) {
          return Promise.resolve(makeSubIssue(302, "Backend Task", "M", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(300);

      expect(progress.isCrossRepo).toBe(true);
      expect(progress.repositories.length).toBeGreaterThanOrEqual(1);
    });

    it("should handle single-repo epic", async () => {
      const repos = [createMockRepository("mono", "/test/mono")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 400) {
          return Promise.resolve(makeEpicIssue(400, "Single Repo Epic", [401, 402]));
        }
        if (number === 401) {
          return Promise.resolve(makeSubIssue(401, "Task 1", "S", "CLOSED"));
        }
        if (number === 402) {
          return Promise.resolve(makeSubIssue(402, "Task 2", "S", "CLOSED"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(400);

      expect(progress.isCrossRepo).toBe(false);
    });

    it("should calculate integration buffer (15%)", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 500) {
          return Promise.resolve(makeEpicIssue(500, "Buffer Test Epic", [501]));
        }
        if (number === 501) {
          return Promise.resolve(makeSubIssue(501, "Task", "L", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(500);

      // L = 1920 minutes, buffer = 15% = 288 minutes
      expect(progress.integrationBufferMinutes).toBe(Math.round(1920 * 0.15));
    });
  });

  describe("Cache Management", () => {
    it("should return cached result within TTL", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager, 60000); // 1 minute TTL

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 600) {
          return Promise.resolve(makeEpicIssue(600, "Cached Epic", [601]));
        }
        if (number === 601) {
          return Promise.resolve(makeSubIssue(601, "Task", "S", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const result1 = await dashboard.getCrossRepoProgress(600);
      vi.clearAllMocks();

      // Advance time by 30 seconds (within TTL)
      vi.advanceTimersByTime(30000);

      const result2 = await dashboard.getCrossRepoProgress(600);

      // Should return cached result, no new API calls
      expect(mockIssueView).not.toHaveBeenCalled();
      expect(result1.fetchedAt).toEqual(result2.fetchedAt);
    });

    it("should refetch after TTL expires", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager, 60000); // 1 minute TTL

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 700) {
          return Promise.resolve(makeEpicIssue(700, "TTL Test Epic", [701]));
        }
        if (number === 701) {
          return Promise.resolve(makeSubIssue(701, "Task", "S", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      await dashboard.getCrossRepoProgress(700);

      // Advance time past TTL
      vi.advanceTimersByTime(70000);

      await dashboard.getCrossRepoProgress(700);

      // Should have made new API calls after TTL
      expect(mockIssueView).toHaveBeenCalled();
    });

    it("should bypass cache with forceRefresh", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager, 300000); // 5 minute TTL

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 800) {
          return Promise.resolve(makeEpicIssue(800, "Force Refresh Epic", [801]));
        }
        if (number === 801) {
          return Promise.resolve(makeSubIssue(801, "Task", "S", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      await dashboard.getCrossRepoProgress(800);
      vi.clearAllMocks();

      // Re-set the mock after clearAllMocks
      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 800) {
          return Promise.resolve(makeEpicIssue(800, "Force Refresh Epic", [801]));
        }
        if (number === 801) {
          return Promise.resolve(makeSubIssue(801, "Task", "S", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });
      mockGetRepoIdentity.mockResolvedValue({
        owner: "test-owner",
        repo: "test-repo",
      });

      // Force refresh should bypass cache
      await dashboard.getCrossRepoProgress(800, true);

      expect(mockIssueView).toHaveBeenCalled();
    });

    it("should invalidate specific epic cache", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager, 300000);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 900) {
          return Promise.resolve(makeEpicIssue(900, "Invalidate Test", [901]));
        }
        if (number === 901) {
          return Promise.resolve(makeSubIssue(901, "Task", "S", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      await dashboard.getCrossRepoProgress(900);
      vi.clearAllMocks();

      // Re-set mocks after clearAllMocks
      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 900) {
          return Promise.resolve(makeEpicIssue(900, "Invalidate Test", [901]));
        }
        if (number === 901) {
          return Promise.resolve(makeSubIssue(901, "Task", "S", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });
      mockGetRepoIdentity.mockResolvedValue({
        owner: "test-owner",
        repo: "test-repo",
      });

      dashboard.invalidateCache(900);
      await dashboard.getCrossRepoProgress(900);

      expect(mockIssueView).toHaveBeenCalled();
    });

    it("should invalidate all cache", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager, 300000);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 1000) {
          return Promise.resolve(makeEpicIssue(1000, "Epic 1000", []));
        }
        if (number === 1001) {
          return Promise.resolve(makeEpicIssue(1001, "Epic 1001", []));
        }
        return Promise.reject(new Error("Not found"));
      });

      await dashboard.getCrossRepoProgress(1000);
      await dashboard.getCrossRepoProgress(1001);
      vi.clearAllMocks();

      // Re-set mocks after clearAllMocks
      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 1000) {
          return Promise.resolve(makeEpicIssue(1000, "Epic 1000", []));
        }
        if (number === 1001) {
          return Promise.resolve(makeEpicIssue(1001, "Epic 1001", []));
        }
        return Promise.reject(new Error("Not found"));
      });
      mockGetRepoIdentity.mockResolvedValue({
        owner: "test-owner",
        repo: "test-repo",
      });

      dashboard.invalidateAllCache();

      await dashboard.getCrossRepoProgress(1000);
      await dashboard.getCrossRepoProgress(1001);

      // Should have refetched both
      expect(mockIssueView).toHaveBeenCalled();
    });
  });

  describe("Error Handling", () => {
    it("should throw error when epic fetch fails", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockRejectedValue(new Error("Epic not found"));

      await expect(dashboard.getCrossRepoProgress(9999)).rejects.toThrow(
        "Failed to fetch epic #9999"
      );
    });

    it("should handle repository query errors gracefully", async () => {
      const repos = [
        createMockRepository("good", "/test/good"),
        createMockRepository("bad", "/test/bad"),
      ];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      // getRepoIdentity returns different identities per path
      mockGetRepoIdentity.mockImplementation((path: string) => {
        if (path === "/test/good") {
          return Promise.resolve({ owner: "test-owner", repo: "good-repo" });
        }
        if (path === "/test/bad") {
          return Promise.resolve({ owner: "test-owner", repo: "bad-repo" });
        }
        // Current repo for fetchEpicMetadata
        return Promise.resolve({ owner: "test-owner", repo: "test-repo" });
      });

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 1100) {
          return Promise.resolve(makeEpicIssue(1100, "Mixed Results Epic", [1101]));
        }
        if (number === 1101) {
          if (repo === "good-repo") {
            return Promise.resolve(makeSubIssue(1101, "Good Task", "M", "OPEN"));
          }
          return Promise.reject(new Error("Repository unavailable"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(1100);

      // Should still succeed with partial data
      const goodRepo = progress.repositories.find((r) => r.name === "good");

      expect(goodRepo?.status).toBe("success");
      expect(goodRepo?.subIssues.length).toBe(1);

      // Bad repo is filtered out (no-data status repos are removed from results)
      const badRepo = progress.repositories.find((r) => r.name === "bad");
      expect(badRepo).toBeUndefined();
    });

    it("should return no-data status for repos without sub-issues", async () => {
      const repos = [
        createMockRepository("with-issues", "/test/with"),
        createMockRepository("empty", "/test/empty"),
      ];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockGetRepoIdentity.mockImplementation((path: string) => {
        if (path === "/test/with") {
          return Promise.resolve({ owner: "test-owner", repo: "with-repo" });
        }
        if (path === "/test/empty") {
          return Promise.resolve({ owner: "test-owner", repo: "empty-repo" });
        }
        return Promise.resolve({ owner: "test-owner", repo: "test-repo" });
      });

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 1200) {
          return Promise.resolve(makeEpicIssue(1200, "Partial Epic", [1201]));
        }
        if (number === 1201) {
          if (repo === "with-repo") {
            return Promise.resolve(makeSubIssue(1201, "Task", "S", "OPEN"));
          }
          return Promise.reject(new Error("Not found"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(1200);

      const emptyRepo = progress.repositories.find((r) => r.name === "empty");
      expect(emptyRepo === undefined || emptyRepo.status === "no-data").toBe(true);
    });
  });

  describe("Confidence Calculation", () => {
    it("should return high confidence when 90%+ issues have size labels", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 1300) {
          return Promise.resolve(
            makeEpicIssue(1300, "High Confidence Epic", [1301, 1302, 1303, 1304, 1305])
          );
        }
        // All 5 issues have size labels
        if (number >= 1301 && number <= 1305) {
          return Promise.resolve(makeSubIssue(number, `Task ${number}`, "S", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(1300);

      expect(progress.confidence).toBe("high");
      expect(progress.confidenceDetail).toContain("5/5");
    });

    it("should return medium confidence when 60-89% issues have size labels", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 1400) {
          return Promise.resolve(
            makeEpicIssue(1400, "Medium Confidence Epic", [1401, 1402, 1403, 1404, 1405])
          );
        }
        if (number >= 1401 && number <= 1405) {
          // 4 of 5 have size labels (80%)
          const size = number <= 1404 ? "S" : null;
          return Promise.resolve(makeSubIssue(number, `Task ${number}`, size, "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(1400);

      expect(progress.confidence).toBe("medium");
    });

    it("should return low confidence when <60% issues have size labels", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 1500) {
          return Promise.resolve(
            makeEpicIssue(1500, "Low Confidence Epic", [1501, 1502, 1503, 1504, 1505])
          );
        }
        if (number >= 1501 && number <= 1505) {
          // Only 2 of 5 have size labels (40%)
          const size = number <= 1502 ? "S" : null;
          return Promise.resolve(makeSubIssue(number, `Task ${number}`, size, "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(1500);

      expect(progress.confidence).toBe("low");
    });

    it("should return low confidence with no sub-issues", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 1600) {
          // Epic with no native sub-issues
          return Promise.resolve(makeEpicIssue(1600, "Empty Epic", []));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(1600);

      expect(progress.confidence).toBe("low");
      expect(progress.confidenceDetail).toContain("No sub-issues");
    });
  });

  describe("getAllCrossRepoProgress()", () => {
    it("should fetch progress for all open epics", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueList.mockResolvedValue([
        {
          number: 1701,
          title: "Epic 1",
          body: "",
          state: "OPEN",
          labels: ["type:epic"],
          assignees: [],
          url: "",
          isEpic: true,
        },
        {
          number: 1702,
          title: "Epic 2",
          body: "",
          state: "OPEN",
          labels: ["type:epic"],
          assignees: [],
          url: "",
          isEpic: true,
        },
      ]);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 1701) {
          return Promise.resolve(makeEpicIssue(1701, "Epic 1", [1711]));
        }
        if (number === 1702) {
          return Promise.resolve(makeEpicIssue(1702, "Epic 2", [1721]));
        }
        if (number === 1711) {
          return Promise.resolve(makeSubIssue(1711, "Task 1", "M", "OPEN"));
        }
        if (number === 1721) {
          return Promise.resolve(makeSubIssue(1721, "Task 2", "L", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const allProgress = await dashboard.getAllCrossRepoProgress();

      expect(allProgress.length).toBe(2);
      expect(allProgress[0].epicNumber).toBeDefined();
      expect(allProgress[1].epicNumber).toBeDefined();
    });

    it("should return empty array when no current repository", async () => {
      const manager = createMockWorkspaceManager([]);
      (manager.getCurrentRepository as ReturnType<typeof vi.fn>).mockReturnValue(null);
      const dashboard = new EpicDashboard(manager);

      const result = await dashboard.getAllCrossRepoProgress();

      expect(result).toEqual([]);
    });

    it("should return empty array when no open epics", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueList.mockResolvedValue([]);

      const result = await dashboard.getAllCrossRepoProgress();

      expect(result).toEqual([]);
    });

    it("should handle individual epic fetch failures gracefully", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueList.mockResolvedValue([
        {
          number: 1801,
          title: "Good Epic",
          body: "",
          state: "OPEN",
          labels: ["type:epic"],
          assignees: [],
          url: "",
          isEpic: true,
        },
        {
          number: 1802,
          title: "Bad Epic",
          body: "",
          state: "OPEN",
          labels: ["type:epic"],
          assignees: [],
          url: "",
          isEpic: true,
        },
      ]);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 1801) {
          return Promise.resolve(makeEpicIssue(1801, "Good Epic", []));
        }
        if (number === 1802) {
          return Promise.reject(new Error("Epic fetch failed"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      const result = await dashboard.getAllCrossRepoProgress();

      // Should return the one that succeeded
      expect(result.length).toBe(1);
      expect(result[0].epicNumber).toBe(1801);

      warnSpy.mockRestore();
    });

    describe("Error Differentiation (Issue #639)", () => {
      it("should return empty array and log warning when IPC throws an error", async () => {
        const repos = [createMockRepository("main", "/test/main")];
        const manager = createMockWorkspaceManager(repos);
        const dashboard = new EpicDashboard(manager);

        // issueList throws (e.g., Go binary not available, IPC error)
        mockIssueList.mockRejectedValue(new Error("gh: command not found"));

        const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

        const result = await dashboard.getAllCrossRepoProgress();

        expect(result).toEqual([]);
        // Should log the error via console.warn
        expect(warnSpy).toHaveBeenCalledWith(
          "Failed to fetch open epic numbers:",
          expect.stringContaining("gh: command not found")
        );

        warnSpy.mockRestore();
      });

      it("should return empty array with no warning when no epics exist", async () => {
        const repos = [createMockRepository("main", "/test/main")];
        const manager = createMockWorkspaceManager(repos);
        const dashboard = new EpicDashboard(manager);

        // issueList returns a valid but empty result — no epics found
        mockIssueList.mockResolvedValue([]);

        const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

        const result = await dashboard.getAllCrossRepoProgress();

        expect(result).toEqual([]);
        // Should NOT log any warning — empty epics is a normal condition
        expect(warnSpy).not.toHaveBeenCalled();

        warnSpy.mockRestore();
      });

      it("should distinguish IPC error from genuinely empty epic list", async () => {
        const repos = [createMockRepository("main", "/test/main")];
        const manager = createMockWorkspaceManager(repos);

        // Test 1: IPC error path — still returns empty array but logs warning
        const dashboardWithError = new EpicDashboard(manager);

        mockIssueList.mockRejectedValue(new Error("HTTP 502: Bad Gateway"));

        const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

        const errorResult = await dashboardWithError.getAllCrossRepoProgress();

        expect(errorResult).toEqual([]);
        expect(warnSpy).toHaveBeenCalledTimes(1);
        expect(warnSpy).toHaveBeenCalledWith(
          "Failed to fetch open epic numbers:",
          expect.stringContaining("HTTP 502")
        );

        warnSpy.mockRestore();
        vi.clearAllMocks();

        // Re-set mocks after clearAllMocks
        mockGetRepoIdentity.mockResolvedValue({
          owner: "test-owner",
          repo: "test-repo",
        });

        // Test 2: Genuine empty result — no warning logged
        const dashboardEmpty = new EpicDashboard(manager);

        mockIssueList.mockResolvedValue([]);

        const warnSpy2 = vi.spyOn(console, "warn").mockImplementation(() => {});

        const emptyResult = await dashboardEmpty.getAllCrossRepoProgress();

        expect(emptyResult).toEqual([]);
        expect(warnSpy2).not.toHaveBeenCalled();

        warnSpy2.mockRestore();
      });

      it("should not attempt to fetch individual epic progress when IPC errors", async () => {
        const repos = [createMockRepository("main", "/test/main")];
        const manager = createMockWorkspaceManager(repos);
        const dashboard = new EpicDashboard(manager);

        mockIssueList.mockRejectedValue(new Error("authentication required"));

        const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

        const result = await dashboard.getAllCrossRepoProgress();

        expect(result).toEqual([]);
        // issueView should never have been called since issueList failed
        expect(mockIssueView).not.toHaveBeenCalled();
        // Only the epic list warning should have been logged
        expect(warnSpy).toHaveBeenCalledTimes(1);

        warnSpy.mockRestore();
      });
    });

    it("should sort results by remaining time (descending)", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueList.mockResolvedValue([
        {
          number: 1901,
          title: "Small Epic",
          body: "",
          state: "OPEN",
          labels: ["type:epic"],
          assignees: [],
          url: "",
          isEpic: true,
        },
        {
          number: 1902,
          title: "Large Epic",
          body: "",
          state: "OPEN",
          labels: ["type:epic"],
          assignees: [],
          url: "",
          isEpic: true,
        },
      ]);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 1901) {
          return Promise.resolve(makeEpicIssue(1901, "Small Epic", [1911]));
        }
        if (number === 1902) {
          return Promise.resolve(makeEpicIssue(1902, "Large Epic", [1921]));
        }
        if (number === 1911) {
          return Promise.resolve(makeSubIssue(1911, "Small Task", "S", "OPEN")); // 120 min
        }
        if (number === 1921) {
          return Promise.resolve(makeSubIssue(1921, "Large Task", "XL", "OPEN")); // 4800 min
        }
        return Promise.reject(new Error("Not found"));
      });

      const result = await dashboard.getAllCrossRepoProgress();

      // Large epic should be first (more remaining time)
      expect(result[0].epicNumber).toBe(1902);
      expect(result[1].epicNumber).toBe(1901);
    });
  });

  describe("isCrossRepoEpic()", () => {
    it("should return true for cross-repo epic", async () => {
      const repos = [
        createMockRepository("frontend", "/test/frontend"),
        createMockRepository("backend", "/test/backend"),
      ];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 2000) {
          return Promise.resolve(makeEpicIssue(2000, "Cross-Repo Epic", [2001, 2002]));
        }
        if (number === 2001) {
          return Promise.resolve(makeSubIssue(2001, "Frontend Task", "S", "OPEN"));
        }
        if (number === 2002) {
          return Promise.resolve(makeSubIssue(2002, "Backend Task", "S", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const isCrossRepo = await dashboard.isCrossRepoEpic(2000);

      expect(isCrossRepo).toBe(true);
    });

    it("should return false for single-repo epic", async () => {
      const repos = [createMockRepository("mono", "/test/mono")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 2100) {
          return Promise.resolve(makeEpicIssue(2100, "Single Repo Epic", [2101]));
        }
        if (number === 2101) {
          return Promise.resolve(makeSubIssue(2101, "Task", "S", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const isCrossRepo = await dashboard.isCrossRepoEpic(2100);

      expect(isCrossRepo).toBe(false);
    });
  });

  describe("Batched Sub-Issue Fetch (#3290)", () => {
    it("should call issueViewMany once per repo with all sub-issue numbers", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueViewMany.mockClear();
      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 7700) {
          return Promise.resolve(
            makeEpicIssue(7700, "Batched Epic", [7701, 7702, 7703, 7704, 7705])
          );
        }
        if (number >= 7701 && number <= 7705) {
          return Promise.resolve(makeSubIssue(number, `Sub ${number}`, "S", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      await dashboard.getCrossRepoProgress(7700);

      // Exactly one batched call for the 5 sub-issues — not 5 separate
      // issueView round-trips.
      expect(mockIssueViewMany).toHaveBeenCalledTimes(1);
      expect(mockIssueViewMany).toHaveBeenCalledWith(
        "test-owner",
        "test-repo",
        [7701, 7702, 7703, 7704, 7705]
      );
    });

    it("should degrade gracefully when issueViewMany rejects", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 7800) {
          return Promise.resolve(makeEpicIssue(7800, "Failing Epic", [7801, 7802]));
        }
        return Promise.reject(new Error("Not found"));
      });
      mockIssueViewMany.mockRejectedValueOnce(new Error("network down"));

      // Should not throw; the failing repo is filtered out (status="no-data"),
      // matching the prior per-issue graceful-skip behavior.
      const progress = await dashboard.getCrossRepoProgress(7800);
      expect(progress.repositories).toEqual([]);
      expect(progress.epicNumber).toBe(7800);
      expect(progress.totalMinutes).toBe(0);
    });
  });

  describe("Native Sub-Issues (GraphQL)", () => {
    it("should fetch sub-issues via native IPC subIssues field", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 2200) {
          return Promise.resolve(makeEpicIssue(2200, "Native Sub-Issues Epic", [2201, 2202, 2203]));
        }
        if (number >= 2201 && number <= 2203) {
          return Promise.resolve(makeSubIssue(number, `Task ${number}`, "XS", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(2200);

      const mainRepo = progress.repositories.find((r) => r.name === "main");
      expect(mainRepo?.subIssues.length).toBe(3);
    });

    it("should handle epic with no native sub-issues", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 2300) {
          // Epic with zero native sub-issues
          return Promise.resolve(makeEpicIssue(2300, "Empty Epic", []));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(2300);

      expect(progress.repositories.length).toBe(0);
      expect(progress.overallCompletionPercent).toBe(0);
    });

    it("should throw when epic not found in IPC response", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockRejectedValue(new Error("Issue not found"));

      await expect(dashboard.getCrossRepoProgress(2400)).rejects.toThrow(
        "Failed to fetch epic #2400"
      );
    });

    it("should sort sub-issue numbers for consistent ordering", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 2500) {
          // Sub-issues returned in non-sorted order
          return Promise.resolve(makeEpicIssue(2500, "Sorting Test", [2503, 2501, 2502]));
        }
        if (number === 2501) {
          return Promise.resolve(makeSubIssue(2501, "First Task", "M", "OPEN"));
        }
        if (number === 2502) {
          return Promise.resolve(makeSubIssue(2502, "Second Task", "M", "OPEN"));
        }
        if (number === 2503) {
          return Promise.resolve(makeSubIssue(2503, "Third Task", "M", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(2500);

      const mainRepo = progress.repositories.find((r) => r.name === "main");
      expect(mainRepo?.subIssues.length).toBe(3);
      // Sub-issues should be queried in sorted order
      expect(mainRepo?.subIssues[0].number).toBe(2501);
      expect(mainRepo?.subIssues[1].number).toBe(2502);
      expect(mainRepo?.subIssues[2].number).toBe(2503);
    });
  });

  describe("Size Label to Minutes Mapping", () => {
    it("should map XS to 30 minutes", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 2600) {
          return Promise.resolve(makeEpicIssue(2600, "XS Epic", [2601]));
        }
        if (number === 2601) {
          return Promise.resolve(makeSubIssue(2601, "XS Task", "XS", "OPEN"));
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(2600);
      const repo = progress.repositories.find((r) => r.name === "main");

      expect(repo?.subIssues[0].estimated_minutes).toBe(30);
    });

    it("should map all size labels correctly", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      const sizeMapping = {
        XS: 30,
        S: 120,
        M: 600,
        L: 1920,
        XL: 4800,
      };

      const sizes = ["XS", "S", "M", "L", "XL"];

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 2700) {
          return Promise.resolve(
            makeEpicIssue(2700, "All Sizes Epic", [2701, 2702, 2703, 2704, 2705])
          );
        }
        if (number >= 2701 && number <= 2705) {
          const sizeIndex = number - 2701;
          return Promise.resolve(
            makeSubIssue(number, `${sizes[sizeIndex]} Task`, sizes[sizeIndex], "OPEN")
          );
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(2700);
      const repo = progress.repositories.find((r) => r.name === "main");

      expect(repo?.subIssues.length).toBe(5);

      const expectedMinutes = [30, 120, 600, 1920, 4800];
      repo?.subIssues.forEach((issue, i) => {
        expect(issue.estimated_minutes).toBe(expectedMinutes[i]);
      });
    });

    it("should return 0 for issues without size label", async () => {
      const repos = [createMockRepository("main", "/test/main")];
      const manager = createMockWorkspaceManager(repos);
      const dashboard = new EpicDashboard(manager);

      mockIssueView.mockImplementation((owner: string, repo: string, number: number) => {
        if (number === 2800) {
          return Promise.resolve(makeEpicIssue(2800, "No Size Epic", [2801]));
        }
        if (number === 2801) {
          // No size label, just type:bug
          return Promise.resolve({
            number: 2801,
            title: "Unsized Task",
            body: "",
            state: "OPEN",
            labels: ["type:bug"],
            assignees: [],
            url: "https://github.com/test-owner/test-repo/issues/2801",
            isEpic: false,
          });
        }
        return Promise.reject(new Error("Not found"));
      });

      const progress = await dashboard.getCrossRepoProgress(2800);
      const repo = progress.repositories.find((r) => r.name === "main");

      expect(repo?.subIssues[0].estimated_minutes).toBe(0);
    });
  });
});
