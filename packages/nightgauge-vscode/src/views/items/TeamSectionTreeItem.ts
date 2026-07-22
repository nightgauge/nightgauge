/**
 * TeamSectionTreeItem — Sidebar team member list section.
 *
 * Displays team members (name, email, role, status) in the pipeline sidebar
 * for Team and Enterprise tier users. Gated via TierGate.check('team-dashboard').
 *
 * States:
 * - No data / signed out: shows "Sign in to view"
 * - Empty team: "No team members found"
 * - Members present: member list with role icons and pending indicators
 * - Offline: appends "Offline — showing cached data" child
 *
 * @see Issue #1482 - Implement team member list view for Team+ tier
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import type { TeamMember, TeamRole } from "../../platform/types";
import { TierGate } from "../../platform/TierGate";

/**
 * Fallback TierGate instance used only when the caller never wires the
 * shared singleton via TeamSectionTreeItem.setTierGate() (#4156). TierGate
 * is stateless, so this doesn't change behavior on its own — it exists so
 * `new TeamSectionTreeItem()` keeps working standalone (tests, docs) without
 * every caller having to thread a TierGate through — but PipelineTreeProvider
 * now injects the real shared instance from bootstrap/services.ts instead of
 * relying on this module-local one, per the architecture: TierGate is
 * instantiated ONCE in bootstrap/services.ts and injected everywhere else.
 */
const _tierGate = new TierGate();

/** Role codicon mapping for sidebar display. */
const ROLE_ICONS: Record<TeamRole, string> = {
  owner: "crown",
  admin: "shield",
  developer: "code",
  viewer: "eye",
};

/** Role icon colors for owner/admin visual distinction. */
const ROLE_ICON_COLORS: Partial<Record<TeamRole, string>> = {
  owner: "testing.iconPassed",
  admin: "terminal.ansiYellow",
};

/** Team data pushed to TeamSectionTreeItem.update(). */
export interface TeamDisplayData {
  members: TeamMember[];
  /** True when fetched data is from cache (offline). */
  offline: boolean;
  /** When the data was last fetched successfully. */
  lastUpdated: Date;
  /** Role of the currently authenticated user within the team. Null for non-team tiers or unauthenticated. */
  currentUserRole?: TeamRole | null;
}

/** Concrete minimal tree item for team child nodes. */
class TeamChildItem extends BaseTreeItem {
  constructor(
    label: string,
    options: {
      icon?: string;
      iconColor?: string;
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
    if (options.description) this.description = options.description;
    if (options.contextValue) this.contextValue = options.contextValue;
    if (options.tooltip) this.tooltip = options.tooltip;
  }
}

/**
 * Individual team member tree item.
 *
 * Label: member.name ?? member.accountId
 * Description: member.email ?? member.role
 * Icon: role codicon with owner/admin color distinction
 * Invited: disabledForeground color + "(pending)" tooltip suffix
 * contextValue: 'team-member-canManage' when viewer can manage, 'team-member-readOnly' otherwise
 */
class TeamMemberTreeItem extends BaseTreeItem {
  constructor(
    member: TeamMember,
    currentUserRole: TeamRole | null = null,
    tierGate: TierGate = _tierGate
  ) {
    const label = member.name ?? member.accountId;
    super(label, vscode.TreeItemCollapsibleState.None);

    this.description = member.email ?? member.role;

    // Set contextValue based on whether the current user can manage team members
    const canManage =
      currentUserRole !== null && tierGate.checkRole("manage-team", currentUserRole).allowed;
    this.contextValue = canManage ? "team-member-canManage" : "team-member-readOnly";

    const iconId = ROLE_ICONS[member.role] ?? "person";
    const isPending = member.status === "invited";

    if (isPending) {
      this.iconPath = new vscode.ThemeIcon(iconId, new vscode.ThemeColor("disabledForeground"));
    } else {
      const color = ROLE_ICON_COLORS[member.role];
      this.iconPath = color
        ? new vscode.ThemeIcon(iconId, new vscode.ThemeColor(color))
        : new vscode.ThemeIcon(iconId);
    }

    this.tooltip = `${member.role}${isPending ? " (pending)" : ""}`;
  }
}

/**
 * TeamSectionTreeItem — root team node shown in pipeline sidebar.
 *
 * When no data: shows "Sign in to view team" child.
 * When empty team: shows "No team members found" child.
 * When members present: lists members with role icons.
 * When offline: appends "Offline — showing cached data" child in any state.
 *
 * @example
 * ```typescript
 * const teamSection = new TeamSectionTreeItem();
 * teamSection.update({
 *   members: [{ memberId: '1', accountId: 'alice', role: 'owner', joinedAt: '...', name: 'Alice', status: 'active' }],
 *   offline: false,
 *   lastUpdated: new Date(),
 * });
 * ```
 */
export class TeamSectionTreeItem extends BaseTreeItem {
  private data: TeamDisplayData | null = null;
  /**
   * Defaults to the module-local fallback so `new TeamSectionTreeItem()`
   * keeps working standalone; PipelineTreeProvider.setTierGate() overrides
   * this with the shared singleton constructed once in bootstrap/services.ts
   * (#4156 — was previously always the module-local instance, never the
   * shared one).
   */
  private tierGate: TierGate = _tierGate;

  constructor() {
    super("Team", vscode.TreeItemCollapsibleState.Collapsed);
    this.contextValue = "team-section";
    this.render();
  }

  /**
   * Inject the shared TierGate singleton (#4156). Call once during wiring
   * (mirrors PipelineTreeProvider.setTierGate) so role checks for team
   * members use the same instance as every other tier-gated call site.
   */
  setTierGate(tierGate: TierGate): void {
    this.tierGate = tierGate;
  }

  /**
   * Update the team section with the latest member data.
   * Call this whenever team data is refreshed or session state changes.
   * Pass null to reset to the "no data / signed out" state.
   */
  update(data: TeamDisplayData | null): void {
    this.data = data;
    this.render();
  }

  override getChildren(): BaseTreeItem[] {
    return this.buildChildren();
  }

  // ---------------------------------------------------------------------------
  // Private
  // ---------------------------------------------------------------------------

  private render(): void {
    if (!this.data) {
      this.label = "Team";
      this.description = undefined;
      this.iconPath = new vscode.ThemeIcon(
        "organization",
        new vscode.ThemeColor("disabledForeground")
      );
      this.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
      return;
    }

    const count = this.data.members.length;
    this.label = "Team";
    this.description = count === 1 ? "1 member" : `${count} members`;
    this.iconPath = new vscode.ThemeIcon(
      "organization",
      new vscode.ThemeColor("testing.iconPassed")
    );
    this.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
  }

  private buildChildren(): BaseTreeItem[] {
    if (!this.data) {
      return [
        new TeamChildItem("Sign in to view team", {
          icon: "person",
          iconColor: "disabledForeground",
          contextValue: "team-signed-out",
        }),
      ];
    }

    const { members, offline, lastUpdated, currentUserRole = null } = this.data;
    const children: BaseTreeItem[] = [];

    if (members.length === 0) {
      children.push(
        new TeamChildItem("No team members found", {
          icon: "organization",
          iconColor: "disabledForeground",
          contextValue: "team-empty",
        })
      );
    } else {
      for (const member of members) {
        children.push(new TeamMemberTreeItem(member, currentUserRole, this.tierGate));
      }
    }

    if (offline) {
      children.push(
        new TeamChildItem("Offline — showing cached data", {
          icon: "plug",
          iconColor: "terminal.ansiYellow",
          description: `Last updated: ${formatTimeAgo(lastUpdated)}`,
          contextValue: "team-offline",
        })
      );
    }

    return children;
  }
}

// ---------------------------------------------------------------------------
// Helpers (exported for testing)
// ---------------------------------------------------------------------------

/**
 * Format a date as a relative "time ago" string for the offline indicator.
 */
export function formatTimeAgo(date: Date): string {
  const diffMs = Date.now() - date.getTime();
  const diffMinutes = Math.floor(diffMs / (1000 * 60));

  if (diffMinutes < 1) return "just now";
  if (diffMinutes === 1) return "1 minute ago";
  if (diffMinutes < 60) return `${diffMinutes} minutes ago`;

  const diffHours = Math.floor(diffMinutes / 60);
  if (diffHours === 1) return "1 hour ago";
  if (diffHours < 24) return `${diffHours} hours ago`;

  const diffDays = Math.floor(diffHours / 24);
  return `${diffDays} ${diffDays === 1 ? "day" : "days"} ago`;
}
