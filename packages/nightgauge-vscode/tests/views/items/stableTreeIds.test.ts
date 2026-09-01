/**
 * Stable tree-item identity (#1277).
 *
 * VS Code preserves a node's expansion state across `onDidChangeTreeData`
 * only when `TreeItem.id` is set and unchanged. None of the Repositories
 * tree's items set one, so identity fell back to `parentHandle/index:label`
 * — and the labels here carry live data ("Ready: 12 issues", "(blocked)",
 * "(empty)"). Every refresh that changed a count renamed the node, VS Code
 * treated it as new, and the constructor's hard-coded `Collapsed` state won.
 *
 * These tests pin the contract: every id is a function of WHAT the node is
 * (repo, status bucket, epic number, issue number) and never of what it
 * currently holds, and ids are parent-qualified so the same epic or issue
 * can appear under two buckets of one tree without colliding.
 */
import { describe, it, expect } from "vitest";
import { RepositoryTreeItem } from "../../../src/views/items/RepositoryTreeItem";
import { IssueSummaryTreeItem } from "../../../src/views/items/IssueSummaryTreeItem";
import { ReadyIssueTreeItem } from "../../../src/views/items/ReadyIssueTreeItem";
import { EpicGroupTreeItem } from "../../../src/views/items/EpicGroupTreeItem";
import type { EpicInfo } from "../../../src/views/items/EpicGroupTreeItem";
import type { ReadyIssue } from "../../../src/services/ProjectBoardService";
import type { Repository } from "../../../src/models/Repository";

const VOLATILE = /\d+ issues?|\(blocked\)|\(empty\)|awaiting decomposition/;

function issue(number: number, overrides: Partial<ReadyIssue> = {}): ReadyIssue {
  return {
    number,
    title: `Issue ${number}`,
    labels: [],
    url: `https://github.com/acme/alpha/issues/${number}`,
    ...overrides,
  } as ReadyIssue;
}

function repo(name: string, extra: Partial<Repository> = {}): Repository {
  return {
    name,
    path: `/ws/${name}`,
    role: "primary",
    isConfigLoaded: true,
    ...extra,
  } as Repository;
}

describe("stable tree-item ids (#1277)", () => {
  it("RepositoryTreeItem: id is the repo name, unchanged by branch, cap, active state or halt", () => {
    const a = new RepositoryTreeItem(repo("alpha"), false, true, false, undefined, "main");
    const b = new RepositoryTreeItem(repo("alpha"), true, false, true, 1, "feat/x", true, {
      repo: "acme/alpha",
      pausedAt: "2026-01-01T00:00:00Z",
    } as any);

    expect(a.id).toBe("repo:alpha");
    expect(b.id).toBe(a.id);
  });

  it("IssueSummaryTreeItem: the count is in the label and never in the id", () => {
    const three = new IssueSummaryTreeItem("ready", "alpha", 3);
    const twelve = new IssueSummaryTreeItem("ready", "alpha", 12);
    const backlog = new IssueSummaryTreeItem("backlog", "alpha", 12);

    expect(three.label).toContain("3 issues");
    expect(twelve.label).toContain("12 issues");
    expect(three.id).toBe("repo:alpha/status:ready");
    expect(twelve.id).toBe(three.id);
    expect(backlog.id).not.toBe(three.id);
    expect(three.id).not.toMatch(VOLATILE);
  });

  it("ReadyIssueTreeItem: flipping blockedBy changes the label, not the id", () => {
    const open = new ReadyIssueTreeItem(issue(7), { parentId: "repo:alpha/status:ready" });
    const blocked = new ReadyIssueTreeItem(
      issue(7, { blockedBy: [{ number: 6, title: "dep", state: "OPEN" } as any] }),
      { parentId: "repo:alpha/status:ready" }
    );

    expect(String(blocked.label)).toContain("(blocked)");
    expect(open.id).toBe("repo:alpha/status:ready/issue:7");
    expect(blocked.id).toBe(open.id);
    expect(blocked.id).not.toMatch(VOLATILE);
  });

  it("ReadyIssueTreeItem: the same issue number under two parents yields two ids", () => {
    const alpha = new ReadyIssueTreeItem(issue(7), { parentId: "repo:alpha/status:ready" });
    const beta = new ReadyIssueTreeItem(issue(7), { parentId: "repo:beta/status:ready" });

    expect(alpha.id).not.toBe(beta.id);
  });

  it("ReadyIssueTreeItem: no parent means no id, so a host tree without namespacing is unaffected", () => {
    expect(new ReadyIssueTreeItem(issue(7)).id).toBeUndefined();
  });

  it("EpicGroupTreeItem: id derives from the epic number and is inherited by its children", () => {
    const epic: EpicInfo = {
      number: 42,
      title: "Big epic",
      url: "https://example.com/42",
    } as EpicInfo;
    const group = new EpicGroupTreeItem(epic, [issue(1), issue(2)], {
      parentId: "repo:alpha/status:ready",
    });
    const blockedGroup = new EpicGroupTreeItem(
      { ...epic, title: "Renamed epic", blockedBy: [{ number: 9 } as any] } as EpicInfo,
      [issue(1)],
      { parentId: "repo:alpha/status:ready" }
    );

    expect(group.id).toBe("repo:alpha/status:ready/epic:42");
    expect(blockedGroup.id).toBe(group.id);
    expect(group.id).not.toMatch(VOLATILE);
    expect(group.getChildren().map((c) => c.id)).toEqual([
      "repo:alpha/status:ready/epic:42/issue:1",
      "repo:alpha/status:ready/epic:42/issue:2",
    ]);
  });

  it("EpicGroupTreeItem: the same epic under Ready and Backlog gets two ids", () => {
    const epic: EpicInfo = { number: 42, title: "Big epic", url: "" } as EpicInfo;
    const ready = new EpicGroupTreeItem(epic, [issue(1)], { parentId: "repo:alpha/status:ready" });
    const backlog = new EpicGroupTreeItem(epic, [issue(2)], {
      parentId: "repo:alpha/status:backlog",
    });

    expect(ready.id).not.toBe(backlog.id);
  });

  it("EpicGroupTreeItem: the no-epic bucket has a stable id too", () => {
    const none = new EpicGroupTreeItem(null, [issue(1)], { parentId: "repo:alpha/status:ready" });
    expect(none.id).toBe("repo:alpha/status:ready/epic:none");
  });
});
