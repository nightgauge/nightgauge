/**
 * WorkspaceSyncSidebarItem unit tests.
 *
 * Verifies the sidebar tree item renders correctly for all four states:
 * hidden (default), synced, syncing, and failed.
 *
 * @see Issue #3669 — Workspace Sidebar Sync Indicator
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// VSCode mock
// ---------------------------------------------------------------------------

vi.mock("vscode", () => {
  return {
    TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
    ThemeColor: class {
      constructor(public readonly id: string) {}
    },
    ThemeIcon: class {
      constructor(
        public readonly id: string,
        public readonly color?: unknown
      ) {}
    },
    TreeItem: class {
      label: string | undefined;
      description: string | undefined;
      collapsibleState: number;
      iconPath: unknown;
      command: unknown;
      tooltip: unknown;
      contextValue: string | undefined;
      id: string | undefined;
      constructor(label: string, state: number) {
        this.label = label;
        this.collapsibleState = state;
      }
    },
    MarkdownString: class {
      constructor(public readonly value: string) {}
    },
    Uri: { file: (p: string) => ({ fsPath: p }) },
  };
});

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

import { WorkspaceSyncSidebarItem } from "../../../src/views/items/WorkspaceSyncSidebarItem";

describe("WorkspaceSyncSidebarItem", () => {
  let item: WorkspaceSyncSidebarItem;

  beforeEach(() => {
    item = new WorkspaceSyncSidebarItem();
  });

  it("defaults to hidden state", () => {
    expect(item.getState().status).toBe("hidden");
  });

  it("sets contextValue to workspace-sync-status", () => {
    expect(item.contextValue).toBe("workspace-sync-status");
  });

  describe("synced state", () => {
    beforeEach(() => {
      item.setState({
        status: "synced",
        repoCount: 2,
        workspaceName: "my-workspace",
        repos: ["nightgauge/nightgauge", "acme/platform"],
      });
    });

    it("label contains checkmark and repo count", () => {
      expect(item.label).toContain("✓");
      expect(item.label).toContain("2 repos");
    });

    it("uses check-all icon with passed color", () => {
      const icon = item.iconPath as { id: string; color: { id: string } };
      expect(icon.id).toBe("check-all");
      expect(icon.color.id).toBe("testing.iconPassed");
    });

    it("tooltip includes workspace name and repo list", () => {
      const tooltip = item.tooltip as string;
      expect(tooltip).toContain("my-workspace");
      expect(tooltip).toContain("nightgauge/nightgauge");
      expect(tooltip).toContain("acme/platform");
    });

    it("has retry command", () => {
      const cmd = item.command as { command: string };
      expect(cmd.command).toBe("nightgauge.retryWorkspaceSyncInternal");
    });

    it("getState returns the set state", () => {
      const state = item.getState();
      expect(state.status).toBe("synced");
      expect(state.repoCount).toBe(2);
      expect(state.workspaceName).toBe("my-workspace");
    });
  });

  describe("syncing state", () => {
    beforeEach(() => {
      item.setState({ status: "syncing", repoCount: 0 });
    });

    it("label contains Syncing", () => {
      expect(item.label).toContain("Syncing");
    });

    it("uses spin icon with yellow color", () => {
      const icon = item.iconPath as { id: string; color: { id: string } };
      expect(icon.id).toBe("sync~spin");
      expect(icon.color.id).toBe("charts.yellow");
    });

    it("has no command", () => {
      expect(item.command).toBeUndefined();
    });
  });

  describe("failed state", () => {
    beforeEach(() => {
      item.setState({
        status: "failed",
        repoCount: 0,
        errorMessage: "Connection refused",
      });
    });

    it("label contains failed", () => {
      expect(item.label).toContain("failed");
    });

    it("uses warning icon with failed color", () => {
      const icon = item.iconPath as { id: string; color: { id: string } };
      expect(icon.id).toBe("warning");
      expect(icon.color.id).toBe("testing.iconFailed");
    });

    it("tooltip includes error message and retry hint", () => {
      const tooltip = item.tooltip as string;
      expect(tooltip).toContain("Connection refused");
      expect(tooltip).toContain("retry");
    });

    it("has retry command", () => {
      const cmd = item.command as { command: string };
      expect(cmd.command).toBe("nightgauge.retryWorkspaceSyncInternal");
    });
  });

  describe("singular repo count", () => {
    it("uses 'repo' (not 'repos') when repoCount is 1", () => {
      item.setState({ status: "synced", repoCount: 1 });
      expect(item.label).toContain("1 repo");
      expect(item.label).not.toContain("1 repos");
    });
  });
});
