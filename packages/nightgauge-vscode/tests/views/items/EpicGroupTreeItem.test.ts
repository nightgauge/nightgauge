import { describe, it, expect } from "vitest";
import * as vscode from "vscode";
import {
  EpicGroupTreeItem,
  groupIssuesByEpic,
  type EpicInfo,
  type GroupByEpicResult,
} from "../../../src/views/items/EpicGroupTreeItem";
import {
  createMockReadyIssue,
  createMockEpicIssue,
  createMockSubIssue,
} from "../../mocks/github-api";

describe("EpicGroupTreeItem", () => {
  describe("constructor", () => {
    it("should create tree item with epic label when epic info provided", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [createMockSubIssue(100)];

      const item = new EpicGroupTreeItem(epicInfo, issues);

      expect(item.label).toBe("Epic #100: User Authentication");
    });

    it('should create tree item with "No Epic" label when epic is null', () => {
      const issues = [createMockReadyIssue()];

      const item = new EpicGroupTreeItem(null, issues);

      expect(item.label).toBe("No Epic");
    });

    it("should be expanded by default", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [createMockSubIssue(100)];

      const item = new EpicGroupTreeItem(epicInfo, issues);

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);
    });

    it("should be collapsed when defaultCollapsed option is true", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [createMockSubIssue(100)];

      const item = new EpicGroupTreeItem(epicInfo, issues, {
        defaultCollapsed: true,
      });

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
    });

    it("should set context value to epicGroup for epics", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [createMockSubIssue(100)];

      const item = new EpicGroupTreeItem(epicInfo, issues);

      expect(item.contextValue).toBe("epicGroup");
    });

    it("should set context value to noEpicGroup for standalone issues", () => {
      const issues = [createMockReadyIssue()];

      const item = new EpicGroupTreeItem(null, issues);

      expect(item.contextValue).toBe("noEpicGroup");
    });

    it("should add child ReadyIssueTreeItems for each issue", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [
        createMockSubIssue(100, { number: 110, title: "Login form" }),
        createMockSubIssue(100, { number: 111, title: "Logout button" }),
        createMockSubIssue(100, { number: 112, title: "Session management" }),
      ];

      const item = new EpicGroupTreeItem(epicInfo, issues);
      const children = item.getChildren();

      expect(children).toHaveLength(3);
    });

    it("should show progress in description", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [
        createMockSubIssue(100),
        createMockSubIssue(100, { number: 111 }),
        createMockSubIssue(100, { number: 112 }),
      ];

      const item = new EpicGroupTreeItem(epicInfo, issues);

      expect(item.description).toBe("(0/3 complete)");
    });
  });

  describe("icon", () => {
    it("should use project icon for epics", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [createMockSubIssue(100)];

      const item = new EpicGroupTreeItem(epicInfo, issues);

      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("project");
    });

    it('should use folder icon for "No Epic" group', () => {
      const issues = [createMockReadyIssue()];

      const item = new EpicGroupTreeItem(null, issues);

      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("folder");
    });
  });

  describe("tooltip", () => {
    it("should include epic information in tooltip", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [createMockSubIssue(100)];

      const item = new EpicGroupTreeItem(epicInfo, issues);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("#100");
      expect(tooltip.value).toContain("User Authentication");
    });

    it('should show "Standalone Issues" for "No Epic" group', () => {
      const issues = [createMockReadyIssue()];

      const item = new EpicGroupTreeItem(null, issues);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("Standalone Issues");
    });

    it("should show progress in tooltip", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [createMockSubIssue(100), createMockSubIssue(100, { number: 111 })];

      const item = new EpicGroupTreeItem(epicInfo, issues);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("0/2 complete");
    });

    it("should list issues in tooltip", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [
        createMockSubIssue(100, { number: 110, title: "Login form" }),
        createMockSubIssue(100, { number: 111, title: "Logout button" }),
      ];

      const item = new EpicGroupTreeItem(epicInfo, issues);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("#110");
      expect(tooltip.value).toContain("#111");
    });
  });

  describe("command", () => {
    it("should set command to open epic URL", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [createMockSubIssue(100)];

      const item = new EpicGroupTreeItem(epicInfo, issues);

      expect(item.command).toBeDefined();
      expect(item.command?.command).toBe("vscode.open");
    });

    it('should not set command for "No Epic" group', () => {
      const issues = [createMockReadyIssue()];

      const item = new EpicGroupTreeItem(null, issues);

      expect(item.command).toBeUndefined();
    });
  });

  describe("getTotalCount", () => {
    it("should return the total number of issues", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [
        createMockSubIssue(100, { number: 110 }),
        createMockSubIssue(100, { number: 111 }),
        createMockSubIssue(100, { number: 112 }),
      ];

      const item = new EpicGroupTreeItem(epicInfo, issues);

      expect(item.getTotalCount()).toBe(3);
    });
  });

  describe("getChildIssueNumbers", () => {
    it("should return issue numbers from all child ReadyIssueTreeItems", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "User Authentication",
        url: "https://github.com/org/repo/issues/100",
      };
      const issues = [
        createMockSubIssue(100, { number: 110, title: "Login form" }),
        createMockSubIssue(100, { number: 111, title: "Logout button" }),
        createMockSubIssue(100, { number: 112, title: "Session management" }),
      ];

      const item = new EpicGroupTreeItem(epicInfo, issues);
      const issueNumbers = item.getChildIssueNumbers();

      expect(issueNumbers).toEqual([110, 111, 112]);
    });

    it("should return empty array when no child issues", () => {
      const epicInfo: EpicInfo = {
        number: 100,
        title: "Empty Epic",
        url: "https://github.com/org/repo/issues/100",
      };

      const item = new EpicGroupTreeItem(epicInfo, []);
      const issueNumbers = item.getChildIssueNumbers();

      expect(issueNumbers).toEqual([]);
    });

    it('should work with "No Epic" group', () => {
      const issues = [
        createMockReadyIssue({ number: 200, title: "Standalone issue 1" }),
        createMockReadyIssue({ number: 201, title: "Standalone issue 2" }),
      ];

      const item = new EpicGroupTreeItem(null, issues);
      const issueNumbers = item.getChildIssueNumbers();

      expect(issueNumbers).toEqual([200, 201]);
    });
  });
});

describe("groupIssuesByEpic", () => {
  /** Helper to build epicMetadata map from mock epic issues. */
  function buildEpicMap(...epics: ReturnType<typeof createMockEpicIssue>[]): Map<number, EpicInfo> {
    const map = new Map<number, EpicInfo>();
    for (const e of epics) {
      map.set(e.number, { number: e.number, title: e.title, url: e.url });
    }
    return map;
  }

  it("should group issues by their epic reference", () => {
    const epic1 = createMockEpicIssue({ number: 100, title: "Auth Epic" });
    const epic2 = createMockEpicIssue({ number: 200, title: "Dashboard Epic" });
    const subIssue1 = createMockSubIssue(100, { number: 110 });
    const subIssue2 = createMockSubIssue(100, { number: 111 });
    const subIssue3 = createMockSubIssue(200, { number: 210 });

    const issues = [subIssue1, subIssue2, subIssue3];
    const epicMap = buildEpicMap(epic1, epic2);

    const { groups } = groupIssuesByEpic(issues, epicMap);

    expect(groups).toHaveLength(2);
    expect(groups[0].epic?.number).toBe(100);
    expect(groups[0].issues).toHaveLength(2);
    expect(groups[1].epic?.number).toBe(200);
    expect(groups[1].issues).toHaveLength(1);
  });

  it('should put issues without epic reference in "No Epic" group', () => {
    const standaloneIssue = createMockReadyIssue({
      number: 300,
      epicRef: undefined,
    });

    const issues = [standaloneIssue];

    const { groups } = groupIssuesByEpic(issues, new Map());

    expect(groups).toHaveLength(1);
    expect(groups[0].epic).toBeNull();
    expect(groups[0].issues).toHaveLength(1);
  });

  it('should put "No Epic" group last', () => {
    const epic = createMockEpicIssue({ number: 100 });
    const subIssue = createMockSubIssue(100, { number: 110 });
    const standaloneIssue = createMockReadyIssue({
      number: 300,
      epicRef: undefined,
    });

    const issues = [standaloneIssue, subIssue];
    const epicMap = buildEpicMap(epic);

    const { groups } = groupIssuesByEpic(issues, epicMap);

    expect(groups).toHaveLength(2);
    expect(groups[0].epic?.number).toBe(100);
    expect(groups[1].epic).toBeNull();
  });

  it("should sort epic groups by epic number", () => {
    const epic100 = createMockEpicIssue({ number: 100 });
    const epic50 = createMockEpicIssue({ number: 50 });
    const epic200 = createMockEpicIssue({ number: 200 });
    const subIssue200 = createMockSubIssue(200, { number: 210 });
    const subIssue100 = createMockSubIssue(100, { number: 110 });
    const subIssue50 = createMockSubIssue(50, { number: 60 });

    const issues = [subIssue200, subIssue100, subIssue50];
    const epicMap = buildEpicMap(epic100, epic50, epic200);

    const { groups } = groupIssuesByEpic(issues, epicMap);

    expect(groups).toHaveLength(3);
    expect(groups[0].epic?.number).toBe(50);
    expect(groups[1].epic?.number).toBe(100);
    expect(groups[2].epic?.number).toBe(200);
  });

  it("should handle unknown epic references gracefully", () => {
    const subIssue = createMockSubIssue(999, { number: 110 });

    const issues = [subIssue];

    const { groups } = groupIssuesByEpic(issues, new Map());

    expect(groups).toHaveLength(1);
    expect(groups[0].epic?.number).toBe(999);
    expect(groups[0].epic?.title).toBe("(loading...)");
  });

  it("should not duplicate the epic itself inside its own group's issue list", () => {
    // The epic creates a group entry (so its header renders even with no
    // sub-issues — Issue #3329) but is NOT pushed into the group's issue
    // list, since the group header IS the epic.
    const epic = createMockEpicIssue({ number: 100 });
    const subIssue = createMockSubIssue(100, { number: 110 });

    const issues = [epic, subIssue];
    const epicMap = buildEpicMap(epic);

    const { groups } = groupIssuesByEpic(issues, epicMap);

    // Sub-issue should be in groups
    expect(groups).toHaveLength(1);
    expect(groups[0].issues).toHaveLength(1);
    expect(groups[0].issues[0].number).toBe(110);
  });

  it("should return empty arrays for empty input", () => {
    const { groups } = groupIssuesByEpic([], new Map());

    expect(groups).toHaveLength(0);
  });

  it("should populate epic info from epicMetadata", () => {
    const epic = createMockEpicIssue({
      number: 100,
      title: "My Epic Title",
      url: "https://github.com/org/repo/issues/100",
    });
    const subIssue = createMockSubIssue(100, { number: 110 });

    const issues = [subIssue];
    const epicMap = buildEpicMap(epic);

    const { groups } = groupIssuesByEpic(issues, epicMap);

    expect(groups[0].epic?.number).toBe(100);
    expect(groups[0].epic?.title).toBe("My Epic Title");
    expect(groups[0].epic?.url).toBe("https://github.com/org/repo/issues/100");
  });

  it("should render an empty group header for each epic with no sub-issues (#3329)", () => {
    // Freshly-created epics have type:epic label but no children yet.
    // They must still render as group headers so the user can see them in
    // the tree — otherwise they're invisible until decomposed.
    const epic1 = createMockEpicIssue({ number: 100, title: "Epic 1" });
    const epic2 = createMockEpicIssue({ number: 200, title: "Epic 2" });
    const epic3 = createMockEpicIssue({ number: 300, title: "Epic 3" });

    const issues = [epic1, epic2, epic3];
    const epicMap = buildEpicMap(epic1, epic2, epic3);

    const { groups } = groupIssuesByEpic(issues, epicMap);

    expect(groups).toHaveLength(3);
    expect(groups.map((g) => g.epic?.number)).toEqual([100, 200, 300]);
    for (const group of groups) {
      expect(group.issues).toHaveLength(0);
    }
  });

  it("should render empty epic alongside non-empty epic in same status (#3329)", () => {
    const emptyEpic = createMockEpicIssue({ number: 100, title: "Empty" });
    const populatedEpic = createMockEpicIssue({ number: 200, title: "Populated" });
    const subIssue = createMockSubIssue(200, { number: 210 });

    const issues = [emptyEpic, populatedEpic, subIssue];
    const epicMap = buildEpicMap(emptyEpic, populatedEpic);

    const { groups } = groupIssuesByEpic(issues, epicMap);

    expect(groups).toHaveLength(2);
    const empty = groups.find((g) => g.epic?.number === 100);
    const populated = groups.find((g) => g.epic?.number === 200);
    expect(empty?.issues).toHaveLength(0);
    expect(populated?.issues).toHaveLength(1);
    expect(populated?.issues[0].number).toBe(210);
  });

  it("should backfill epicMetadata from the epic's own row when missing (#3329)", () => {
    // If the epic isn't yet in the metadata map (e.g. cache miss across
    // status tabs) but appears in the current `issues` set, the function
    // should populate metadata from the epic itself so the header renders
    // with a real title rather than "(loading…)".
    const epic = createMockEpicIssue({
      number: 100,
      title: "Recovered Title",
      url: "https://example.test/100",
    });

    const { groups } = groupIssuesByEpic([epic], new Map());

    expect(groups).toHaveLength(1);
    expect(groups[0].epic?.title).toBe("Recovered Title");
    expect(groups[0].epic?.url).toBe("https://example.test/100");
  });

  it("should return GroupByEpicResult with correct structure", () => {
    const epic = createMockEpicIssue({ number: 100 });
    const subIssue = createMockSubIssue(100, { number: 110 });
    const standaloneIssue = createMockReadyIssue({ number: 200 });

    const issues = [epic, subIssue, standaloneIssue];
    const epicMap = buildEpicMap(epic);

    const result: GroupByEpicResult = groupIssuesByEpic(issues, epicMap);

    // Should have groups property
    expect(result).toHaveProperty("groups");

    // Verify structure
    expect(Array.isArray(result.groups)).toBe(true);
  });
});
