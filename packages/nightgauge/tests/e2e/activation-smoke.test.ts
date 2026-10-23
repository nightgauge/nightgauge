/**
 * E2E Smoke Tests — Extension Activation Flow
 *
 * Validates the full activation chain: config load → IPC board data fetch →
 * tree view population, including epic grouping and blocking display.
 *
 * These are "inside-out" integration tests that wire real service classes
 * together with only the outermost boundary (IPC / filesystem) mocked.
 *
 * @see Issue #1825 — Phase 5: End-to-end smoke tests for extension activation
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { BoardItem } from "../../src/services/IpcClient";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import type { ReadyIssue } from "../../src/services/ProjectBoardService";
import { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import { ReadyIssueTreeItem } from "../../src/views/items/ReadyIssueTreeItem";
import { EpicGroupTreeItem, groupIssuesByEpic } from "../../src/views/items/EpicGroupTreeItem";
import { isBlocked, getBlockerCount } from "../../src/utils/dependencyUtils";

// ---------------------------------------------------------------------------
// BoardItem factory — mimics the shape returned by IpcClient.boardList()
// ---------------------------------------------------------------------------

function createBoardItem(overrides: Partial<BoardItem> = {}): BoardItem {
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
    repo: overrides.repo ?? "nightgauge/nightgauge",
    url:
      overrides.url ?? `https://github.com/nightgauge/nightgauge/issues/${overrides.number ?? 1}`,
    isEpic: overrides.isEpic ?? false,
    blockedBy: overrides.blockedBy ?? [],
    blocking: overrides.blocking ?? [],
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Hoisted mocks — vi.mock factories are hoisted before variable declarations,
// so any mock function refs must be created via vi.hoisted().
// ---------------------------------------------------------------------------

const { mockBoardList, mockBoardCounts, mockReadFileSync } = vi.hoisted(() => ({
  mockBoardList: vi.fn(),
  mockBoardCounts: vi.fn(),
  mockReadFileSync: vi.fn(),
}));

const mockConfigGetProjectConfig = vi.fn().mockResolvedValue({
  owner: "nightgauge",
  projectNumber: 42,
  defaultRepo: "",
});

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      boardList: mockBoardList,
      boardCounts: mockBoardCounts,
      configGetProjectConfig: mockConfigGetProjectConfig,
      start: vi.fn(),
      stop: vi.fn(),
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(),
  logDeprecationWarning: vi.fn(),
}));

vi.mock("fs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("fs")>();
  return {
    ...actual,
    default: { ...actual, readFileSync: mockReadFileSync },
    readFileSync: mockReadFileSync,
  };
});

// ---------------------------------------------------------------------------
// Real-structure fixture: Epic #1819 with 3 sequential sub-issues
// Mirrors the actual nightgauge test quality audit epic
// ---------------------------------------------------------------------------

function createEpicFixture() {
  const epic = createBoardItem({
    number: 1819,
    title: "test: Test Quality Audit",
    status: "Ready",
    priority: "P1",
    size: "XL",
    labels: ["type:epic", "priority:high"],
    isEpic: true,
    subIssues: [
      { number: 1821, title: "Contract tests for Go↔TS", state: "CLOSED" },
      { number: 1823, title: "IPC protocol tests", state: "CLOSED" },
      { number: 1825, title: "E2E smoke tests", state: "OPEN" },
    ],
  });

  const sub1 = createBoardItem({
    number: 1821,
    title: "Contract tests for Go↔TypeScript type alignment",
    status: "Done",
    priority: "P2",
    size: "M",
    labels: ["type:chore", "priority:medium", "size:M"],
  });

  const sub2 = createBoardItem({
    number: 1823,
    title: "IPC protocol tests for JSON-over-stdio format",
    status: "Done",
    priority: "P2",
    size: "M",
    labels: ["type:chore", "priority:medium", "size:M"],
    blockedBy: [{ number: 1821, title: "Contract tests for Go↔TS", state: "CLOSED" }],
  });

  const sub3 = createBoardItem({
    number: 1825,
    title: "E2E smoke tests for extension activation",
    status: "Ready",
    priority: "P2",
    size: "L",
    labels: ["type:chore", "priority:medium", "size:L"],
    blockedBy: [{ number: 1823, title: "IPC protocol tests", state: "CLOSED" }],
  });

  // A standalone issue not linked to any epic
  const standalone = createBoardItem({
    number: 1830,
    title: "Fix typo in README",
    status: "Ready",
    priority: "P3",
    size: "XS",
    labels: ["type:docs", "priority:low", "size:XS"],
  });

  return { epic, sub1, sub2, sub3, standalone };
}

// Fixture with active blocking — sub3 blocked by an OPEN issue
function createBlockedFixture() {
  const epic = createBoardItem({
    number: 500,
    title: "Feature Epic",
    status: "Ready",
    priority: "P1",
    labels: ["type:epic", "priority:high"],
    isEpic: true,
    subIssues: [
      { number: 501, title: "Foundation work", state: "OPEN" },
      { number: 502, title: "Dependent feature", state: "OPEN" },
      { number: 503, title: "Final integration", state: "OPEN" },
    ],
  });

  const sub1 = createBoardItem({
    number: 501,
    title: "Foundation work",
    status: "Ready",
    priority: "P1",
    size: "M",
    labels: ["type:feature", "priority:high", "size:M"],
  });

  const sub2 = createBoardItem({
    number: 502,
    title: "Dependent feature",
    status: "Ready",
    priority: "P2",
    size: "L",
    labels: ["type:feature", "priority:medium", "size:L"],
    blockedBy: [{ number: 501, title: "Foundation work", state: "OPEN" }],
  });

  const sub3 = createBoardItem({
    number: 503,
    title: "Final integration",
    status: "Ready",
    priority: "P2",
    size: "L",
    labels: ["type:feature", "priority:medium", "size:L"],
    blockedBy: [
      { number: 501, title: "Foundation work", state: "OPEN" },
      { number: 502, title: "Dependent feature", state: "OPEN" },
    ],
  });

  return { epic, sub1, sub2, sub3 };
}

// ============================================================================
// Tests
// ============================================================================

describe("Extension Activation Smoke Tests", () => {
  let service: ProjectBoardService;

  beforeEach(() => {
    // Reset only test-specific mocks — vi.clearAllMocks() would also reset the
    // global ConfigBridge mock from setup.ts (onConfigChanged returning a disposable),
    // causing ProjectBoardTreeProvider constructor to push undefined into disposables.
    mockBoardList.mockReset();
    mockBoardCounts.mockReset();
    mockReadFileSync.mockReset();

    service = new ProjectBoardService("/test/workspace");
    // Bypass filesystem config loading by setting internals directly
    (service as any).projectNumber = 42;
    (service as any).owner = "nightgauge";
    (service as any).configLoaded = true;
    // Mock EventEmitter.event returns undefined by default — patch to return disposable
    // so ProjectBoardTreeProvider.constructor can push valid disposables
    (service as any).onItemsUpdated = vi.fn().mockReturnValue({ dispose: vi.fn() });
  });

  afterEach(() => {
    service.dispose();
  });

  // ==========================================================================
  // AC1: Config loads correctly
  // ==========================================================================

  describe("AC1: Config loads correctly", () => {
    it("should resolve owner and project number from config", () => {
      expect(service.getOwner()).toBe("nightgauge");
      expect(service.getProjectNumber()).toBe(42);
    });

    it("should load config via IPC and resolve owner/project", async () => {
      // Create a fresh service without pre-set config
      const freshService = new ProjectBoardService("/test/workspace");

      // Override IPC to return TestOrg config
      mockConfigGetProjectConfig.mockResolvedValueOnce({
        owner: "TestOrg",
        projectNumber: 99,
        defaultRepo: "",
      });

      await freshService.loadConfig();

      expect(freshService.getOwner()).toBe("TestOrg");
      expect(freshService.getProjectNumber()).toBe(99);
      expect(freshService.getProjects()).toEqual([{ name: "Default", number: 99, default: true }]);

      freshService.dispose();
    });

    it("should support project number from IPC config", async () => {
      const freshService = new ProjectBoardService("/test/workspace");

      // Override IPC to return FlatOrg config
      mockConfigGetProjectConfig.mockResolvedValueOnce({
        owner: "FlatOrg",
        projectNumber: 77,
        defaultRepo: "",
      });

      await freshService.loadConfig();

      expect(freshService.getOwner()).toBe("FlatOrg");
      expect(freshService.getProjectNumber()).toBe(77);
      expect(freshService.getSelectedProject()).toBe("Default");

      freshService.dispose();
    });

    it("should normalize status keys correctly", async () => {
      // Status normalization: kebab-case → title-case for API
      mockBoardList.mockResolvedValue([
        createBoardItem({ number: 1, status: "Ready" }),
        createBoardItem({ number: 2, status: "In progress" }),
        createBoardItem({ number: 3, status: "In review" }),
        createBoardItem({ number: 4, status: "Backlog" }),
      ]);

      // Calling getIssuesByStatus with kebab-case should normalize
      const readyIssues = await service.getIssuesByStatus("ready");
      expect(mockBoardList).toHaveBeenCalledWith("nightgauge", 42, "Ready", undefined, undefined);

      service.clearCache();
      await service.getIssuesByStatus("in-progress");
      expect(mockBoardList).toHaveBeenCalledWith(
        "nightgauge",
        42,
        "In progress",
        undefined,
        undefined
      );

      service.clearCache();
      await service.getIssuesByStatus("in-review");
      expect(mockBoardList).toHaveBeenCalledWith(
        "nightgauge",
        42,
        "In review",
        undefined,
        undefined
      );
    });

    it("should return empty array when config has no owner or project", async () => {
      const emptyService = new ProjectBoardService("/test/workspace");
      (emptyService as any).configLoaded = true;
      // owner and projectNumber are null by default

      const result = await emptyService.getIssuesByStatus("ready");
      expect(result).toEqual([]);
      expect(mockBoardList).not.toHaveBeenCalled();

      emptyService.dispose();
    });

    it("should aggregate status counts with normalized keys", async () => {
      mockBoardCounts.mockResolvedValue({
        ready: 2,
        inProgress: 1,
        inReview: 1,
        done: 0,
        backlog: 1,
      });

      const counts = await service.getAggregatedStatusCounts();
      expect(counts.ready).toBe(2);
      expect(counts.inProgress).toBe(1);
      expect(counts.inReview).toBe(1);
      expect(counts.backlog).toBe(1);
    });
  });

  // ==========================================================================
  // AC2: Board data flows through to tree view
  // ==========================================================================

  describe("AC2: Board data flows through to tree view", () => {
    it("should flow IPC data through service to tree provider", async () => {
      const boardItems = [
        createBoardItem({
          number: 10,
          title: "Implement login",
          status: "Ready",
          priority: "P1",
          size: "M",
          labels: ["type:feature", "priority:high", "size:M"],
        }),
        createBoardItem({
          number: 11,
          title: "Add tests",
          status: "Ready",
          priority: "P2",
          size: "S",
          labels: ["type:chore", "priority:medium", "size:S"],
        }),
      ];

      mockBoardList.mockResolvedValue(boardItems);

      // Create provider in flat mode (no epic grouping) for simpler verification
      const provider = new ProjectBoardTreeProvider(service, "ready");

      // Override groupByEpic to false for flat list
      (provider as any).groupByEpic = false;

      const children = await provider.getChildren();

      // Should return ReadyIssueTreeItem instances
      expect(children).toHaveLength(2);
      expect(children[0]).toBeInstanceOf(ReadyIssueTreeItem);
      expect(children[1]).toBeInstanceOf(ReadyIssueTreeItem);

      // Verify data fidelity
      const item0 = children[0] as ReadyIssueTreeItem;
      const item1 = children[1] as ReadyIssueTreeItem;
      expect(item0.issueNumber).toBe(10);
      expect(item0.label).toBe("#10 - Implement login");
      expect(item1.issueNumber).toBe(11);
      expect(item1.label).toBe("#11 - Add tests");

      expect(provider.getItemCount()).toBe(2);

      provider.dispose();
    });

    it("should populate tree view title with item count", async () => {
      mockBoardList.mockResolvedValue([
        createBoardItem({ number: 1, status: "Ready" }),
        createBoardItem({ number: 2, status: "Ready" }),
        createBoardItem({ number: 3, status: "Ready" }),
      ]);

      const provider = new ProjectBoardTreeProvider(service, "ready");
      (provider as any).groupByEpic = false;

      const mockTreeView = {
        title: "",
        dispose: vi.fn(),
        onDidChangeVisibility: vi.fn(),
        visible: true,
      };
      provider.setTreeView(mockTreeView as any);

      await provider.getChildren();

      expect(mockTreeView.title).toBe("Ready (3)");

      provider.dispose();
    });

    it("should show empty state when no issues match status", async () => {
      mockBoardList.mockResolvedValue([]);

      const provider = new ProjectBoardTreeProvider(service, "ready");
      const children = await provider.getChildren();

      expect(children).toHaveLength(1);
      expect(children[0].label).toContain("No ready issues found");

      provider.dispose();
    });

    it("should convert BoardItem fields to ReadyIssue correctly", async () => {
      mockBoardList.mockResolvedValue([
        createBoardItem({
          number: 42,
          title: "Test conversion",
          status: "Ready",
          priority: "critical",
          size: "xl",
          labels: ["type:bug", "priority:critical", "size:XL"],
        }),
      ]);

      const issues = await service.getIssuesByStatus("ready");

      expect(issues).toHaveLength(1);
      expect(issues[0].number).toBe(42);
      expect(issues[0].priority).toBe("P0"); // critical → P0
      expect(issues[0].size).toBe("XL"); // xl → XL (uppercase)
      expect(issues[0].labels).toContain("type:bug");
    });

    it("should filter issues by status tab", async () => {
      mockBoardList.mockResolvedValue([
        createBoardItem({ number: 1, status: "Ready" }),
        createBoardItem({ number: 2, status: "In progress" }),
      ]);

      // Ready tab should only get issues with status=Ready
      const readyIssues = await service.getIssuesByStatus("ready");
      // IPC boardList is called with the API-normalized status
      expect(mockBoardList).toHaveBeenCalledWith("nightgauge", 42, "Ready", undefined, undefined);
    });

    it("should cache board data and return from cache within TTL", async () => {
      mockBoardList.mockResolvedValue([createBoardItem({ number: 1, status: "Ready" })]);

      // First call hits IPC
      const first = await service.getIssuesByStatus("ready");
      expect(first).toHaveLength(1);
      expect(mockBoardList).toHaveBeenCalledTimes(1);

      // Second call should use cache
      const second = await service.getIssuesByStatus("ready");
      expect(second).toHaveLength(1);
      expect(mockBoardList).toHaveBeenCalledTimes(1); // Still 1 — cached
    });

    it("should clear cache on clearCache()", async () => {
      mockBoardList.mockResolvedValue([createBoardItem({ number: 1, status: "Ready" })]);

      await service.getIssuesByStatus("ready");
      expect(mockBoardList).toHaveBeenCalledTimes(1);

      service.clearCache();

      await service.getIssuesByStatus("ready");
      expect(mockBoardList).toHaveBeenCalledTimes(2); // Cache was cleared
    });
  });

  // ==========================================================================
  // AC3: Epic grouping and blocking verified with real-structure fixtures
  // ==========================================================================

  describe("AC3: Epic grouping and blocking with real-structure fixtures", () => {
    describe("epic grouping via groupIssuesByEpic()", () => {
      it("should group sub-issues under parent epic", () => {
        const { epic, sub1, sub2, sub3, standalone } = createEpicFixture();

        // Convert to ReadyIssue format (as the service would)
        const allItems: ReadyIssue[] = [
          {
            ...toReadyIssue(epic),
            isEpic: true,
            subIssueNumbers: [1821, 1823, 1825],
          },
          { ...toReadyIssue(sub1), epicRef: 1819 },
          { ...toReadyIssue(sub2), epicRef: 1819 },
          { ...toReadyIssue(sub3), epicRef: 1819 },
          toReadyIssue(standalone),
        ];

        // Ready tab contains sub3 (Ready) and standalone (Ready)
        const readyItems = allItems.filter(
          (i) => i.status === "Ready" && !i.labels.includes("type:epic")
        );

        // Build epic metadata map from all items (as ProjectBoardService would)
        const epicMetadata = new Map<number, { number: number; title: string; url: string }>();
        for (const item of allItems) {
          if (item.isEpic) {
            epicMetadata.set(item.number, {
              number: item.number,
              title: item.title,
              url: item.url,
            });
          }
        }
        const result = groupIssuesByEpic(readyItems, epicMetadata);

        // Should have 2 groups: Epic #1819 and "No Epic"
        expect(result.groups).toHaveLength(2);

        // First group: Epic #1819
        const epicGroup = result.groups[0];
        expect(epicGroup.epic).not.toBeNull();
        expect(epicGroup.epic!.number).toBe(1819);
        expect(epicGroup.epic!.title).toBe("test: Test Quality Audit");
        expect(epicGroup.issues).toHaveLength(1); // Only sub3 is Ready
        expect(epicGroup.issues[0].number).toBe(1825);

        // Second group: No Epic (standalone)
        const noEpicGroup = result.groups[1];
        expect(noEpicGroup.epic).toBeNull();
        expect(noEpicGroup.issues).toHaveLength(1);
        expect(noEpicGroup.issues[0].number).toBe(1830);
      });

      it("should skip type:epic issues in grouping (shown as headers only)", () => {
        const epicIssue: ReadyIssue = {
          number: 100,
          title: "Test Epic",
          labels: ["type:epic"],
          priority: "P1",
          size: "XL",
          url: "https://github.com/test/100",
          status: "Ready",
          isEpic: true,
          subIssueNumbers: [101, 102],
        };

        const sub1: ReadyIssue = {
          number: 101,
          title: "Sub 1",
          labels: ["type:feature"],
          priority: "P2",
          size: "M",
          url: "https://github.com/test/101",
          status: "Ready",
          epicRef: 100,
        };

        const epicMap = new Map([
          [
            100,
            {
              number: 100,
              title: "Test Epic",
              url: "https://github.com/test/100",
            },
          ],
        ]);
        const result = groupIssuesByEpic([epicIssue, sub1], epicMap);

        // Epic issue is skipped — only sub1 appears
        const allGroupedIssues = result.groups.flatMap((g) => g.issues);
        expect(allGroupedIssues).toHaveLength(1);
        expect(allGroupedIssues[0].number).toBe(101);
      });

      it("should render EpicGroupTreeItem with correct structure", () => {
        const epicInfo = {
          number: 1819,
          title: "Test Quality Audit",
          url: "https://github.com/nightgauge/nightgauge/issues/1819",
        };

        const issues: ReadyIssue[] = [
          {
            number: 1825,
            title: "E2E smoke tests",
            labels: ["type:chore"],
            priority: "P2",
            size: "L",
            url: "https://github.com/nightgauge/nightgauge/issues/1825",
            status: "Ready",
            epicRef: 1819,
          },
        ];

        const group = new EpicGroupTreeItem(epicInfo, issues);

        expect(group.label).toBe("Epic #1819: Test Quality Audit");
        expect(group.epic).toEqual(epicInfo);
        expect(group.getTotalCount()).toBe(1);

        // Children should be ReadyIssueTreeItems
        const children = group.getChildren();
        expect(children).toHaveLength(1);
        expect(children[0]).toBeInstanceOf(ReadyIssueTreeItem);
        expect((children[0] as ReadyIssueTreeItem).issueNumber).toBe(1825);
      });

      it('should render "No Epic" group for standalone issues', () => {
        const issues: ReadyIssue[] = [
          {
            number: 999,
            title: "Standalone issue",
            labels: [],
            priority: null,
            size: null,
            url: "https://github.com/test/999",
            status: "Ready",
          },
        ];

        const group = new EpicGroupTreeItem(null, issues);

        expect(group.label).toBe("No Epic");
        expect(group.epic).toBeNull();
      });
    });

    describe("blocking relationships and lock icons", () => {
      it("should detect blocked issues via isBlocked()", () => {
        const unblockedIssue: ReadyIssue = {
          number: 501,
          title: "Foundation work",
          labels: [],
          priority: "P1",
          size: "M",
          url: "https://github.com/test/501",
          blockedBy: [],
        };

        const blockedIssue: ReadyIssue = {
          number: 502,
          title: "Dependent feature",
          labels: [],
          priority: "P2",
          size: "L",
          url: "https://github.com/test/502",
          blockedBy: [{ number: 501, title: "Foundation work", url: "", state: "OPEN" }],
        };

        const resolvedIssue: ReadyIssue = {
          number: 503,
          title: "Resolved dependency",
          labels: [],
          priority: "P2",
          size: "M",
          url: "https://github.com/test/503",
          blockedBy: [{ number: 501, title: "Foundation work", url: "", state: "CLOSED" }],
        };

        expect(isBlocked(unblockedIssue)).toBe(false);
        expect(isBlocked(blockedIssue)).toBe(true);
        expect(isBlocked(resolvedIssue)).toBe(false); // CLOSED blockers don't count
      });

      it("should count open blockers correctly", () => {
        const issue: ReadyIssue = {
          number: 503,
          title: "Final integration",
          labels: [],
          priority: "P2",
          size: "L",
          url: "https://github.com/test/503",
          blockedBy: [
            { number: 501, title: "Foundation", url: "", state: "OPEN" },
            { number: 502, title: "Dependent", url: "", state: "OPEN" },
            { number: 500, title: "Old dep", url: "", state: "CLOSED" },
          ],
        };

        expect(getBlockerCount(issue)).toBe(2); // Only OPEN ones
      });

      it('should render blocked issue with lock icon and "(blocked)" suffix', () => {
        const blockedIssue: ReadyIssue = {
          number: 502,
          title: "Dependent feature",
          labels: ["type:feature", "priority:medium", "size:L"],
          priority: "P2",
          size: "L",
          url: "https://github.com/test/502",
          blockedBy: [{ number: 501, title: "Foundation work", url: "", state: "OPEN" }],
        };

        const item = new ReadyIssueTreeItem(blockedIssue);

        // Label should include "(blocked)"
        expect(item.label).toContain("(blocked)");
        expect(item.label).toBe("#502 - Dependent feature (blocked)");

        // Icon should be lock
        expect(item.iconPath).toBeDefined();
        expect((item.iconPath as any).id).toBe("lock");

        // Description should show blocker count
        expect(item.description).toContain("🔒1 blocker");
        expect(item.description).toContain("[L]");
      });

      it("should render unblocked issue with priority icon", () => {
        const unblockedIssue: ReadyIssue = {
          number: 501,
          title: "Foundation work",
          labels: ["type:feature", "priority:high", "size:M"],
          priority: "P1",
          size: "M",
          url: "https://github.com/test/501",
        };

        const item = new ReadyIssueTreeItem(unblockedIssue);

        // No "(blocked)" suffix
        expect(item.label).toBe("#501 - Foundation work");

        // Icon should be priority circle (P1 = warning color)
        expect((item.iconPath as any).id).toBe("circle-filled");

        // Description should just show size
        expect(item.description).toBe("[M]");
      });

      it("should add dependency children when blocked", () => {
        const blockedIssue: ReadyIssue = {
          number: 502,
          title: "Dependent feature",
          labels: [],
          priority: "P2",
          size: "L",
          url: "https://github.com/test/502",
          blockedBy: [{ number: 501, title: "Foundation work", url: "", state: "OPEN" }],
          blocks: [{ number: 503, title: "Final integration", url: "", state: "OPEN" }],
        };

        const item = new ReadyIssueTreeItem(blockedIssue, {
          showDependencies: true,
        });
        const children = item.getChildren();

        // Should have 2 dependency sections: blockedBy and blocks
        expect(children).toHaveLength(2);
      });
    });

    describe("full pipeline: IPC → service → provider → tree items", () => {
      it("should render epic-grouped tree view from IPC data", async () => {
        const fixture = createBlockedFixture();

        // All items returned by boardList (used for getAllItems)
        const allBoardItems = [fixture.epic, fixture.sub1, fixture.sub2, fixture.sub3];

        // boardList is called twice: once for getIssuesByStatus, once for getAllItems
        mockBoardList.mockResolvedValue(allBoardItems);

        const provider = new ProjectBoardTreeProvider(service, "ready");
        // epic grouping is enabled by default

        const children = await provider.getChildren();

        // Should have 1 epic group (all sub-issues belong to epic 500)
        expect(children).toHaveLength(1);
        expect(children[0]).toBeInstanceOf(EpicGroupTreeItem);

        const epicGroup = children[0] as EpicGroupTreeItem;
        expect(epicGroup.epic!.number).toBe(500);
        expect(epicGroup.epic!.title).toBe("Feature Epic");

        // Epic group should contain 3 sub-issues (epic issue is skipped)
        const subItems = epicGroup.getChildren();
        expect(subItems).toHaveLength(3);

        // Verify each sub-issue
        const sub1 = subItems[0] as ReadyIssueTreeItem;
        expect(sub1.issueNumber).toBe(501);
        expect(sub1.label).toBe("#501 - Foundation work"); // Unblocked

        const sub2 = subItems[1] as ReadyIssueTreeItem;
        expect(sub2.issueNumber).toBe(502);
        expect(sub2.label).toContain("(blocked)"); // Blocked by 501

        const sub3 = subItems[2] as ReadyIssueTreeItem;
        expect(sub3.issueNumber).toBe(503);
        expect(sub3.label).toContain("(blocked)"); // Blocked by 501 and 502

        provider.dispose();
      });

      it("should separate issues across status tabs without duplication", async () => {
        const { epic, sub1, sub2, sub3, standalone } = createEpicFixture();
        const allBoardItems = [epic, sub1, sub2, sub3, standalone];

        // IPC boardList filters server-side by status — mock this behavior
        mockBoardList.mockImplementation((_owner: string, _project: number, status?: string) => {
          if (!status) return Promise.resolve(allBoardItems);
          return Promise.resolve(allBoardItems.filter((i) => i.status === status));
        });

        // Ready tab
        const readyProvider = new ProjectBoardTreeProvider(service, "ready");
        const readyChildren = await readyProvider.getChildren();

        // Flatten all issue numbers from epic groups
        const readyIssueNumbers = flattenIssueNumbers(readyChildren);

        // sub3 (1825, Ready) and standalone (1830, Ready) should be in Ready tab
        // sub1 (Done) and sub2 (Done) should NOT be in Ready tab
        // epic (1819) should NOT appear as an issue (only as group header)
        expect(readyIssueNumbers).toContain(1825);
        expect(readyIssueNumbers).toContain(1830);
        expect(readyIssueNumbers).not.toContain(1819); // Epic is header, not issue
        expect(readyIssueNumbers).not.toContain(1821); // Done
        expect(readyIssueNumbers).not.toContain(1823); // Done

        readyProvider.dispose();
      });

      it("should topologically sort: unblocked first, blocked last", () => {
        const issues: ReadyIssue[] = [
          {
            number: 502,
            title: "Blocked",
            labels: [],
            priority: "P2",
            size: "M",
            url: "",
            blockedBy: [{ number: 501, title: "Blocker", url: "", state: "OPEN" }],
          },
          {
            number: 501,
            title: "Unblocked",
            labels: [],
            priority: "P1",
            size: "M",
            url: "",
          },
        ];

        const sorted = service.topologicalSort(issues);

        expect(sorted[0].number).toBe(501); // Unblocked first
        expect(sorted[1].number).toBe(502); // Blocked last
      });
    });
  });
});

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function toReadyIssue(item: BoardItem): ReadyIssue {
  const priorityMap: Record<string, "P0" | "P1" | "P2" | "P3"> = {
    P0: "P0",
    P1: "P1",
    P2: "P2",
    P3: "P3",
    critical: "P0",
    high: "P1",
    medium: "P2",
    low: "P3",
  };
  const sizeMap: Record<string, "XS" | "S" | "M" | "L" | "XL"> = {
    XS: "XS",
    S: "S",
    M: "M",
    L: "L",
    XL: "XL",
  };

  return {
    number: item.number,
    title: item.title,
    labels: item.labels,
    priority: priorityMap[item.priority] ?? null,
    size: sizeMap[item.size?.toUpperCase()] ?? null,
    url: item.url,
    status: item.status,
    blockedBy: item.blockedBy?.map((b) => ({
      number: b.number,
      title: b.title,
      url: "",
      state: b.state as "OPEN" | "CLOSED",
    })),
    blocks: item.blocking?.map((b) => ({
      number: b.number,
      title: b.title,
      url: "",
      state: b.state as "OPEN" | "CLOSED",
    })),
  };
}

function flattenIssueNumbers(treeItems: any[]): number[] {
  const numbers: number[] = [];
  for (const item of treeItems) {
    if (item instanceof ReadyIssueTreeItem) {
      numbers.push(item.issueNumber);
    } else if (item instanceof EpicGroupTreeItem) {
      const children = item.getChildren();
      for (const child of children) {
        if (child instanceof ReadyIssueTreeItem) {
          numbers.push(child.issueNumber);
        }
      }
    }
  }
  return numbers;
}
