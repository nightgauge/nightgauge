/**
 * RepositoryTreeItem.halt.test.ts
 *
 * Repo-scoped autonomous halt badge (#1148 visibility).
 *
 * A terminal stage failure halts ONE repository and deliberately leaves the
 * FLEET status reading "running". That is better behaviour and worse
 * discoverability: the only symptom on the Repositories view was an absence —
 * a Ready issue nobody picks up — which is indistinguishable from the repo
 * being unchecked, having nothing on the board, or the scheduler being busy
 * elsewhere. These tests pin the warning badge that tells the two apart, and
 * the `-halted` contextValue the inline Resume action is gated on.
 */

import { describe, it, expect, vi } from "vitest";
import { RepositoryTreeItem } from "../../../src/views/items/RepositoryTreeItem";
import type { Repository } from "../../../src/models/Repository";
import type { AutonomousRepoPause } from "../../../src/services/IpcClientBase";

vi.mock("vscode", () => ({
  TreeItemCheckboxState: { Checked: 1, Unchecked: 0 },
  TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
  TreeItem: class {
    constructor(
      public label: string,
      public collapsibleState = 0
    ) {}
  },
  ThemeIcon: class {
    constructor(
      public id: string,
      public color?: { id: string }
    ) {}
  },
  ThemeColor: class {
    constructor(public id: string) {}
  },
  MarkdownString: class {
    constructor(public value: string = "") {}
  },
}));

function makeRepo(name: string): Repository {
  return {
    name,
    path: `/path/to/${name}`,
    role: "primary",
    isConfigLoaded: true,
    github: { owner: "acme", repo: name },
  } as unknown as Repository;
}

const halt: AutonomousRepoPause = {
  repo: "acme/dashboard",
  reason: "haltQueueOnSlotFailure: issue #42 failed at feature-validate",
  triggeredBy: "haltQueueOnSlotFailure",
  pausedAt: "2026-08-31T12:00:00Z",
  issue: 42,
  stage: "feature-validate",
};

function iconOf(item: RepositoryTreeItem): { id: string; color?: { id: string } } {
  return item.iconPath as unknown as { id: string; color?: { id: string } };
}

function tooltipOf(item: RepositoryTreeItem): string {
  return (item.tooltip as unknown as { value: string }).value;
}

describe("RepositoryTreeItem — repo halt badge (#1148)", () => {
  it("renders no halt affordance when the repo is not halted", () => {
    const item = new RepositoryTreeItem(makeRepo("dashboard"), false, true);

    expect(item.isHalted).toBe(false);
    expect(item.haltedRepoKey).toBeUndefined();
    expect(item.contextValue).toBe("repository");
    expect(iconOf(item).id).toBe("repo");
    expect(String(item.description)).not.toContain("halted");
  });

  it("badges a halted repo with a warning icon, description and -halted contextValue", () => {
    const item = new RepositoryTreeItem(
      makeRepo("dashboard"),
      false,
      true,
      false,
      undefined,
      "main",
      false,
      halt
    );

    expect(item.isHalted).toBe(true);
    expect(item.haltedRepoKey).toBe("acme/dashboard");
    expect(item.contextValue).toBe("repository-halted");
    expect(iconOf(item).id).toBe("warning");
    expect(iconOf(item).color?.id).toBe("list.warningForeground");
    // The glyph and the word both — icon colour alone is invisible in a
    // high-contrast theme or a screenshot.
    expect(String(item.description)).toContain("⚠ Autonomous halted");
    // The rest of the description survives.
    expect(String(item.description)).toContain("main");
  });

  it("keeps the sequential/active contextValue variants when halted", () => {
    const active = new RepositoryTreeItem(
      makeRepo("dashboard"),
      true,
      true,
      true,
      1,
      "main",
      false,
      halt
    );
    // Every menu contribution matches on the `repository` prefix, so the
    // suffix must be appended, not substituted.
    expect(active.contextValue).toBe("repository-active-sequential-halted");
    // Halt outranks "active" for the icon slot.
    expect(iconOf(active).id).toBe("warning");
  });

  it("names the issue and stage that stopped the repo in the tooltip", () => {
    const item = new RepositoryTreeItem(
      makeRepo("dashboard"),
      false,
      true,
      false,
      undefined,
      "main",
      false,
      halt
    );

    expect(tooltipOf(item)).toContain("Autonomous dispatch is halted for this repository");
    expect(tooltipOf(item)).toContain("Issue #42 failed at feature-validate");
    expect(tooltipOf(item)).toContain("haltQueueOnSlotFailure");
    expect(tooltipOf(item)).toContain("Other repositories keep dispatching");
  });

  it("falls back to the raw reason when the halt carries no issue number", () => {
    const item = new RepositoryTreeItem(
      makeRepo("dashboard"),
      false,
      true,
      false,
      undefined,
      undefined,
      false,
      { repo: "acme/dashboard", reason: "operator halt" }
    );

    expect(tooltipOf(item)).toContain("Cause: operator halt");
  });

  it("repaints every derived visual when the halt is applied and released", () => {
    // The provider caches rows and refreshes them by firing the change event
    // with the SAME object, so a halt that only moved provider-side state
    // would repaint nothing.
    const item = new RepositoryTreeItem(
      makeRepo("dashboard"),
      false,
      true,
      false,
      undefined,
      "main"
    );

    item.applyHaltState(halt);
    expect(item.contextValue).toBe("repository-halted");
    expect(iconOf(item).id).toBe("warning");
    expect(String(item.description)).toContain("Autonomous halted");
    expect(tooltipOf(item)).toContain("#42");

    item.applyHaltState(undefined);
    expect(item.contextValue).toBe("repository");
    expect(iconOf(item).id).toBe("repo");
    expect(String(item.description)).toBe("main");
    expect(tooltipOf(item)).not.toContain("halted");
  });
});
