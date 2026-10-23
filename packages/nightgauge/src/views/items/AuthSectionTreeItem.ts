/**
 * AuthSectionTreeItem — Sidebar auth status section.
 *
 * Displays the user's authentication state in the pipeline sidebar:
 * - Signed out: shows "Sign In" action item
 * - Signed in: shows user email (or "Signed In") with tier badge as children
 *
 * Updates in real-time via SessionManager.onSessionChanged events.
 *
 * @see Issue #1469 - Add auth status indicators to sidebar and status bar
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import type { SessionState, SessionData } from "../../platform/SessionManager";

/** Tier display labels mapping internal values to user-facing strings. */
const TIER_LABELS: Record<string, string> = {
  community: "Community",
  pro: "Pro",
  team: "Team",
  enterprise: "Enterprise",
};

/** Concrete minimal tree item for auth child nodes. */
class AuthChildItem extends BaseTreeItem {
  constructor(
    label: string,
    options: {
      icon?: string;
      iconColor?: string;
      command?: vscode.Command;
      description?: string;
      contextValue?: string;
      tooltip?: string;
    } = {}
  ) {
    super(label, vscode.TreeItemCollapsibleState.None);
    if (options.icon) {
      if (options.iconColor) {
        this.iconPath = new vscode.ThemeIcon(
          options.icon,
          new vscode.ThemeColor(options.iconColor)
        );
      } else {
        this.iconPath = new vscode.ThemeIcon(options.icon);
      }
    }
    if (options.command) this.command = options.command;
    if (options.description) this.description = options.description;
    if (options.contextValue) this.contextValue = options.contextValue;
    if (options.tooltip) this.tooltip = options.tooltip;
  }
}

/**
 * AuthSectionTreeItem — root auth node shown in the pipeline sidebar.
 *
 * When signed out: collapsible with a single "Sign In" child action.
 * When signed in: collapsible with user info and tier badge children.
 * When authenticating: shows a spinner indicator.
 *
 * @example
 * \`\`\`typescript
 * const authSection = new AuthSectionTreeItem();
 * authSection.update('authenticated', { userEmail: 'me@example.com', userTier: 'pro', ... });
 * \`\`\`
 */
export class AuthSectionTreeItem extends BaseTreeItem {
  private sessionState: SessionState = "unauthenticated";
  private sessionData: SessionData | null = null;

  constructor() {
    super("Platform Account", vscode.TreeItemCollapsibleState.Collapsed);
    this.contextValue = "auth-section";
    this.render();
  }

  /**
   * Update the auth section with the latest session state and data.
   * Call this whenever SessionManager emits onSessionChanged.
   */
  update(state: SessionState, data: SessionData | null): void {
    this.sessionState = state;
    this.sessionData = data;
    this.render();
  }

  override getChildren(): BaseTreeItem[] {
    if (this.sessionState === "authenticated") {
      return this.buildSignedInChildren();
    }
    if (this.sessionState === "authenticating") {
      return [
        new AuthChildItem("Signing in\u2026", {
          icon: "sync~spin",
          contextValue: "auth-authenticating",
        }),
      ];
    }
    // unauthenticated or error
    return [
      new AuthChildItem("Sign In", {
        icon: "sign-in",
        iconColor: "terminal.ansiCyan",
        command: {
          command: "nightgauge.signIn",
          title: "Sign In to Nightgauge",
        },
        tooltip: "Sign in to access platform features",
        contextValue: "auth-sign-in",
      }),
    ];
  }

  // ---------------------------------------------------------------------------
  // Private
  // ---------------------------------------------------------------------------

  private render(): void {
    switch (this.sessionState) {
      case "authenticated": {
        const email = this.sessionData?.userEmail;
        const tier = this.sessionData?.userTier;
        this.label = email ?? "Signed In";
        this.description = tier ? (TIER_LABELS[tier] ?? tier) : undefined;
        this.iconPath = new vscode.ThemeIcon(
          "account",
          new vscode.ThemeColor("testing.iconPassed")
        );
        this.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
        break;
      }
      case "authenticating":
        this.label = "Signing in\u2026";
        this.description = undefined;
        this.iconPath = new vscode.ThemeIcon("sync~spin");
        this.collapsibleState = vscode.TreeItemCollapsibleState.None;
        break;
      case "error":
        this.label = "Auth Error";
        this.description = "Sign in again";
        this.iconPath = new vscode.ThemeIcon("error", new vscode.ThemeColor("errorForeground"));
        this.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
        break;
      default:
        // unauthenticated
        this.label = "Not Signed In";
        this.description = undefined;
        this.iconPath = new vscode.ThemeIcon(
          "account",
          new vscode.ThemeColor("disabledForeground")
        );
        this.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
        break;
    }
  }

  private buildSignedInChildren(): BaseTreeItem[] {
    const children: BaseTreeItem[] = [];

    // Tier badge item
    if (this.sessionData?.userTier) {
      children.push(
        new AuthChildItem(TIER_LABELS[this.sessionData.userTier] ?? this.sessionData.userTier, {
          icon: "star",
          iconColor: "terminal.ansiYellow",
          description: "Subscription tier",
          contextValue: "auth-tier",
        })
      );
    }

    // Sign Out action
    children.push(
      new AuthChildItem("Sign Out", {
        icon: "sign-out",
        command: {
          command: "nightgauge.signOut",
          title: "Sign Out",
        },
        tooltip: "Sign out of Nightgauge",
        contextValue: "auth-sign-out",
      })
    );

    return children;
  }
}
