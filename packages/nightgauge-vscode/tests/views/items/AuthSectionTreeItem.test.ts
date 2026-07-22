/**
 * AuthSectionTreeItem unit tests.
 *
 * Verifies auth section tree item updates for all auth states.
 *
 * @see Issue #1469 - Add auth status indicators to sidebar and status bar
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
  };
});

import { AuthSectionTreeItem } from "../../../src/views/items/AuthSectionTreeItem";
import type { SessionData } from "../../../src/platform/SessionManager";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeSessionData(overrides: Partial<SessionData> = {}): SessionData {
  return {
    accessToken: null,
    expiresAt: null,
    userEmail: null,
    userTier: null,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("AuthSectionTreeItem", () => {
  describe("initial state (unauthenticated)", () => {
    it('shows "Not Signed In" label', () => {
      const item = new AuthSectionTreeItem();
      expect(item.label).toBe("Not Signed In");
    });

    it("has auth-section contextValue", () => {
      const item = new AuthSectionTreeItem();
      expect(item.contextValue).toBe("auth-section");
    });

    it("returns a Sign In child", () => {
      const item = new AuthSectionTreeItem();
      const children = item.getChildren();
      expect(children).toHaveLength(1);
      expect(children[0].contextValue).toBe("auth-sign-in");
    });

    it("Sign In child has nightgauge.signIn command", () => {
      const item = new AuthSectionTreeItem();
      const children = item.getChildren();
      expect((children[0].command as { command: string }).command).toBe("nightgauge.signIn");
    });
  });

  describe("authenticated state", () => {
    it("shows user email as label when email is available", () => {
      const item = new AuthSectionTreeItem();
      item.update("authenticated", makeSessionData({ userEmail: "me@example.com" }));
      expect(item.label).toBe("me@example.com");
    });

    it('shows "Signed In" when no email', () => {
      const item = new AuthSectionTreeItem();
      item.update("authenticated", makeSessionData());
      expect(item.label).toBe("Signed In");
    });

    it("shows tier as description when tier is present", () => {
      const item = new AuthSectionTreeItem();
      item.update(
        "authenticated",
        makeSessionData({ userEmail: "me@example.com", userTier: "pro" })
      );
      expect(item.description).toBe("Pro");
    });

    it("shows no description when tier is null", () => {
      const item = new AuthSectionTreeItem();
      item.update("authenticated", makeSessionData({ userEmail: "me@example.com" }));
      expect(item.description).toBeUndefined();
    });

    it("returns tier badge and sign-out children", () => {
      const item = new AuthSectionTreeItem();
      item.update(
        "authenticated",
        makeSessionData({ userEmail: "me@example.com", userTier: "pro" })
      );
      const children = item.getChildren();
      expect(children).toHaveLength(2);
      expect(children[0].contextValue).toBe("auth-tier");
      expect(children[1].contextValue).toBe("auth-sign-out");
    });

    it("returns only sign-out child when no tier", () => {
      const item = new AuthSectionTreeItem();
      item.update("authenticated", makeSessionData({ userEmail: "me@example.com" }));
      const children = item.getChildren();
      expect(children).toHaveLength(1);
      expect(children[0].contextValue).toBe("auth-sign-out");
    });

    it("Sign Out child has nightgauge.signOut command", () => {
      const item = new AuthSectionTreeItem();
      item.update("authenticated", makeSessionData());
      const children = item.getChildren();
      const signOutItem = children.find((c) => c.contextValue === "auth-sign-out");
      expect((signOutItem?.command as { command: string } | undefined)?.command).toBe(
        "nightgauge.signOut"
      );
    });

    it('tier label "community" maps to "Community"', () => {
      const item = new AuthSectionTreeItem();
      item.update("authenticated", makeSessionData({ userTier: "community" }));
      expect(item.description).toBe("Community");
    });

    it('tier label "enterprise" maps to "Enterprise"', () => {
      const item = new AuthSectionTreeItem();
      item.update("authenticated", makeSessionData({ userTier: "enterprise" }));
      expect(item.description).toBe("Enterprise");
    });
  });

  describe("authenticating state", () => {
    it('shows "Signing in…" label', () => {
      const item = new AuthSectionTreeItem();
      item.update("authenticating", makeSessionData());
      expect(item.label).toBe("Signing in…");
    });

    it("returns a single authenticating child", () => {
      const item = new AuthSectionTreeItem();
      item.update("authenticating", makeSessionData());
      const children = item.getChildren();
      expect(children).toHaveLength(1);
      expect(children[0].contextValue).toBe("auth-authenticating");
    });
  });

  describe("error state", () => {
    it('shows "Auth Error" label', () => {
      const item = new AuthSectionTreeItem();
      item.update("error", makeSessionData());
      expect(item.label).toBe("Auth Error");
    });

    it("returns a Sign In child for retry", () => {
      const item = new AuthSectionTreeItem();
      item.update("error", makeSessionData());
      const children = item.getChildren();
      expect(children).toHaveLength(1);
      expect(children[0].contextValue).toBe("auth-sign-in");
    });
  });

  describe("state transitions", () => {
    it("transitions from unauthenticated to authenticated", () => {
      const item = new AuthSectionTreeItem();
      expect(item.label).toBe("Not Signed In");

      item.update("authenticated", makeSessionData({ userEmail: "user@test.com" }));
      expect(item.label).toBe("user@test.com");
    });

    it("transitions from authenticated to unauthenticated", () => {
      const item = new AuthSectionTreeItem();
      item.update("authenticated", makeSessionData({ userEmail: "user@test.com" }));
      item.update("unauthenticated", makeSessionData());
      expect(item.label).toBe("Not Signed In");
    });
  });
});
