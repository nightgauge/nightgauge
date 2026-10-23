/**
 * TeamSectionTreeItem unit tests.
 *
 * Verifies team section tree item renders correctly for all states:
 * unauthenticated, empty team, members present (including role icons,
 * owner/admin visual distinction, invited/pending indicators), and offline.
 *
 * @see Issue #1482 - Implement team member list view for Team+ tier
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
    Uri: {
      parse: (url: string) => ({ toString: () => url }),
    },
  };
});

import {
  TeamSectionTreeItem,
  type TeamDisplayData,
  formatTimeAgo,
} from "../../../src/views/items/TeamSectionTreeItem";
import { TierGate } from "../../../src/platform/TierGate";
import type { TeamMember } from "../../../src/platform/types";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeMember(overrides: Partial<TeamMember> = {}): TeamMember {
  return {
    memberId: "member-alice",
    accountId: "alice",
    role: "developer",
    joinedAt: "2025-01-01T00:00:00Z",
    name: "Alice Smith",
    email: "alice@example.com",
    status: "active",
    ...overrides,
  };
}

function makeData(overrides: Partial<TeamDisplayData> = {}): TeamDisplayData {
  return {
    members: [makeMember()],
    offline: false,
    lastUpdated: new Date(),
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("TeamSectionTreeItem", () => {
  describe("initial state (no data / unauthenticated)", () => {
    it('shows "Team" label', () => {
      const item = new TeamSectionTreeItem();
      expect(item.label).toBe("Team");
    });

    it("has team-section contextValue", () => {
      const item = new TeamSectionTreeItem();
      expect(item.contextValue).toBe("team-section");
    });

    it("is collapsible", () => {
      const item = new TeamSectionTreeItem();
      // TreeItemCollapsibleState.Collapsed = 1
      expect(item.collapsibleState).toBe(1);
    });

    it("uses organization icon with disabledForeground color", () => {
      const item = new TeamSectionTreeItem();
      expect((item.iconPath as { id: string }).id).toBe("organization");
      expect((item.iconPath as { color?: { id: string } }).color?.id).toBe("disabledForeground");
    });

    it('returns a single "Sign in to view team" child', () => {
      const item = new TeamSectionTreeItem();
      const children = item.getChildren();
      expect(children).toHaveLength(1);
      expect(children[0].contextValue).toBe("team-signed-out");
    });

    it('"Sign in to view team" child has no command', () => {
      const item = new TeamSectionTreeItem();
      const children = item.getChildren();
      expect((children[0] as { command?: unknown }).command).toBeUndefined();
    });
  });

  describe("update(null) — signed out state", () => {
    it("resets to unauthenticated state", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData());
      item.update(null);
      expect(item.description).toBeUndefined();
      expect(item.getChildren()[0].contextValue).toBe("team-signed-out");
    });
  });

  describe("empty team", () => {
    it('shows "0 members" description', () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [] }));
      expect(item.description).toBe("0 members");
    });

    it('returns "No team members found" child', () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [] }));
      const children = item.getChildren();
      expect(children).toHaveLength(1);
      expect(children[0].contextValue).toBe("team-empty");
    });
  });

  describe("members present", () => {
    it('shows "1 member" (singular) description for one member', () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember()] }));
      expect(item.description).toBe("1 member");
    });

    it('shows "N members" for multiple members', () => {
      const item = new TeamSectionTreeItem();
      item.update(
        makeData({
          members: [makeMember(), makeMember({ accountId: "bob" })],
        })
      );
      expect(item.description).toBe("2 members");
    });

    it("uses organization icon with testing.iconPassed color when members present", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember()] }));
      expect((item.iconPath as { id: string }).id).toBe("organization");
      expect((item.iconPath as { color?: { id: string } }).color?.id).toBe("testing.iconPassed");
    });

    it("returns one TeamMemberTreeItem per member", () => {
      const item = new TeamSectionTreeItem();
      item.update(
        makeData({
          members: [makeMember(), makeMember({ accountId: "bob" })],
        })
      );
      const children = item.getChildren();
      expect(children).toHaveLength(2);
    });

    it("member item label uses name when available", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember({ name: "Alice Smith" })] }));
      const children = item.getChildren();
      expect(children[0].label).toBe("Alice Smith");
    });

    it("member item label falls back to accountId when name absent", () => {
      const item = new TeamSectionTreeItem();
      item.update(
        makeData({
          members: [makeMember({ name: undefined, accountId: "user-123" })],
        })
      );
      const children = item.getChildren();
      expect(children[0].label).toBe("user-123");
    });

    it("member item description uses email when available", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember({ email: "alice@example.com" })] }));
      const children = item.getChildren();
      expect(children[0].description).toBe("alice@example.com");
    });

    it("member item description falls back to role when email absent", () => {
      const item = new TeamSectionTreeItem();
      item.update(
        makeData({
          members: [makeMember({ email: undefined, role: "viewer" })],
        })
      );
      const children = item.getChildren();
      expect(children[0].description).toBe("viewer");
    });

    it("member contextValue is team-member-readOnly when no currentUserRole", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember({ role: "admin" })] }));
      const children = item.getChildren();
      // No currentUserRole → defaults to readOnly (cannot manage)
      expect(children[0].contextValue).toBe("team-member-readOnly");
    });

    it("member contextValue is team-member-canManage when currentUserRole is admin", () => {
      const item = new TeamSectionTreeItem();
      item.update(
        makeData({
          members: [makeMember({ role: "developer" })],
          currentUserRole: "admin",
        })
      );
      const children = item.getChildren();
      expect(children[0].contextValue).toBe("team-member-canManage");
    });

    it("member contextValue is team-member-readOnly when currentUserRole is developer", () => {
      const item = new TeamSectionTreeItem();
      item.update(
        makeData({
          members: [makeMember({ role: "developer" })],
          currentUserRole: "developer",
        })
      );
      const children = item.getChildren();
      expect(children[0].contextValue).toBe("team-member-readOnly");
    });
  });

  // #4156 — TeamSectionTreeItem previously always constructed its own
  // TierGate instead of using the shared singleton wired by
  // PipelineTreeProvider.setTierGate().
  describe("setTierGate() — shared TierGate singleton (#4156)", () => {
    it("uses the injected TierGate's checkRole for member contextValue", () => {
      const injectedGate = new TierGate();
      const checkRoleSpy = vi.spyOn(injectedGate, "checkRole");

      const item = new TeamSectionTreeItem();
      item.setTierGate(injectedGate);
      item.update(
        makeData({
          members: [makeMember({ role: "developer" })],
          currentUserRole: "admin",
        })
      );
      const children = item.getChildren();

      expect(checkRoleSpy).toHaveBeenCalledWith("manage-team", "admin");
      expect(children[0].contextValue).toBe("team-member-canManage");
    });

    it("respects a stricter injected TierGate's checkRole result", () => {
      const injectedGate = new TierGate();
      vi.spyOn(injectedGate, "checkRole").mockReturnValue({
        allowed: false,
        requiredRole: "admin",
      });

      const item = new TeamSectionTreeItem();
      item.setTierGate(injectedGate);
      item.update(
        makeData({
          members: [makeMember({ role: "developer" })],
          currentUserRole: "admin", // would normally be allowed
        })
      );
      const children = item.getChildren();

      expect(children[0].contextValue).toBe("team-member-readOnly");
    });
  });

  describe("role icons", () => {
    it("owner uses crown icon", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember({ role: "owner" })] }));
      const children = item.getChildren();
      expect((children[0].iconPath as { id: string }).id).toBe("crown");
    });

    it("admin uses shield icon", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember({ role: "admin" })] }));
      const children = item.getChildren();
      expect((children[0].iconPath as { id: string }).id).toBe("shield");
    });

    it("developer uses code icon", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember({ role: "developer" })] }));
      const children = item.getChildren();
      expect((children[0].iconPath as { id: string }).id).toBe("code");
    });

    it("viewer uses eye icon", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember({ role: "viewer" })] }));
      const children = item.getChildren();
      expect((children[0].iconPath as { id: string }).id).toBe("eye");
    });
  });

  describe("owner/admin visual distinction", () => {
    it("owner has testing.iconPassed (green) color", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember({ role: "owner", status: "active" })] }));
      const children = item.getChildren();
      expect((children[0].iconPath as { color?: { id: string } }).color?.id).toBe(
        "testing.iconPassed"
      );
    });

    it("admin has terminal.ansiYellow (amber) color", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember({ role: "admin", status: "active" })] }));
      const children = item.getChildren();
      expect((children[0].iconPath as { color?: { id: string } }).color?.id).toBe(
        "terminal.ansiYellow"
      );
    });

    it("developer has no special color", () => {
      const item = new TeamSectionTreeItem();
      item.update(
        makeData({
          members: [makeMember({ role: "developer", status: "active" })],
        })
      );
      const children = item.getChildren();
      expect((children[0].iconPath as { color?: { id: string } }).color).toBeUndefined();
    });

    it("viewer has no special color", () => {
      const item = new TeamSectionTreeItem();
      item.update(
        makeData({
          members: [makeMember({ role: "viewer", status: "active" })],
        })
      );
      const children = item.getChildren();
      expect((children[0].iconPath as { color?: { id: string } }).color).toBeUndefined();
    });
  });

  describe("invited/pending indicator", () => {
    it("invited member uses disabledForeground color", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember({ status: "invited" })] }));
      const children = item.getChildren();
      expect((children[0].iconPath as { color?: { id: string } }).color?.id).toBe(
        "disabledForeground"
      );
    });

    it('invited member tooltip includes "(pending)"', () => {
      const item = new TeamSectionTreeItem();
      item.update(
        makeData({
          members: [makeMember({ role: "developer", status: "invited" })],
        })
      );
      const children = item.getChildren();
      expect(children[0].tooltip as string).toContain("(pending)");
    });

    it('active member tooltip does not include "(pending)"', () => {
      const item = new TeamSectionTreeItem();
      item.update(
        makeData({
          members: [makeMember({ role: "developer", status: "active" })],
        })
      );
      const children = item.getChildren();
      expect(children[0].tooltip as string).not.toContain("(pending)");
    });

    it("invited owner uses disabledForeground (not green) color", () => {
      const item = new TeamSectionTreeItem();
      item.update(
        makeData({
          members: [makeMember({ role: "owner", status: "invited" })],
        })
      );
      const children = item.getChildren();
      expect((children[0].iconPath as { color?: { id: string } }).color?.id).toBe(
        "disabledForeground"
      );
    });
  });

  describe("offline state", () => {
    it('appends "Offline — showing cached data" child', () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember()], offline: true }));
      const children = item.getChildren();
      const offlineItem = children.find((c) => c.contextValue === "team-offline");
      expect(offlineItem).toBeDefined();
    });

    it('offline child shows "just now" when recently updated', () => {
      const item = new TeamSectionTreeItem();
      item.update(
        makeData({
          members: [makeMember()],
          offline: true,
          lastUpdated: new Date(),
        })
      );
      const children = item.getChildren();
      const offlineItem = children.find((c) => c.contextValue === "team-offline");
      expect(offlineItem?.description as string).toContain("just now");
    });

    it("offline empty team also shows cached data child", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [], offline: true }));
      const children = item.getChildren();
      const offlineItem = children.find((c) => c.contextValue === "team-offline");
      expect(offlineItem).toBeDefined();
    });

    it("non-offline state does not show offline child", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember()], offline: false }));
      const children = item.getChildren();
      const offlineItem = children.find((c) => c.contextValue === "team-offline");
      expect(offlineItem).toBeUndefined();
    });
  });

  describe("getChildren()", () => {
    it("returns correct child instances for members", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember(), makeMember({ accountId: "m2" })] }));
      const children = item.getChildren();
      expect(children).toHaveLength(2);
      expect(children[0].label).toBeDefined();
    });

    it("returns offline child as last item when offline", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember()], offline: true }));
      const children = item.getChildren();
      expect(children[children.length - 1].contextValue).toBe("team-offline");
    });
  });

  describe("state transitions", () => {
    it("transitions from unauthenticated to members", () => {
      const item = new TeamSectionTreeItem();
      expect(item.getChildren()[0].contextValue).toBe("team-signed-out");

      item.update(makeData({ members: [makeMember()] }));
      expect(item.description).toBe("1 member");
    });

    it("transitions from members back to null (sign out)", () => {
      const item = new TeamSectionTreeItem();
      item.update(makeData({ members: [makeMember()] }));
      expect(item.description).toBe("1 member");

      item.update(null);
      expect(item.description).toBeUndefined();
      expect(item.getChildren()[0].contextValue).toBe("team-signed-out");
    });
  });
});

// ---------------------------------------------------------------------------
// formatTimeAgo helper tests
// ---------------------------------------------------------------------------

describe("formatTimeAgo", () => {
  it('returns "just now" for very recent date', () => {
    expect(formatTimeAgo(new Date())).toBe("just now");
  });

  it('returns "1 minute ago" for ~1 minute ago', () => {
    const oneMinuteAgo = new Date(Date.now() - 61 * 1000);
    expect(formatTimeAgo(oneMinuteAgo)).toBe("1 minute ago");
  });

  it('returns "N minutes ago" for multiple minutes', () => {
    const fiveMinutesAgo = new Date(Date.now() - 5 * 60 * 1000);
    expect(formatTimeAgo(fiveMinutesAgo)).toBe("5 minutes ago");
  });

  it('returns "1 hour ago" for ~1 hour', () => {
    const oneHourAgo = new Date(Date.now() - 61 * 60 * 1000);
    expect(formatTimeAgo(oneHourAgo)).toBe("1 hour ago");
  });

  it('returns "N hours ago" for multiple hours', () => {
    const threeHoursAgo = new Date(Date.now() - 3 * 60 * 60 * 1000);
    expect(formatTimeAgo(threeHoursAgo)).toBe("3 hours ago");
  });

  it('returns "N days ago" for multiple days', () => {
    const twoDaysAgo = new Date(Date.now() - 2 * 24 * 60 * 60 * 1000);
    expect(formatTimeAgo(twoDaysAgo)).toBe("2 days ago");
  });

  it('returns "1 day ago" for singular', () => {
    const oneDayAgo = new Date(Date.now() - 25 * 60 * 60 * 1000);
    expect(formatTimeAgo(oneDayAgo)).toBe("1 day ago");
  });
});
