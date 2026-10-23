/**
 * TierBadge - Source tier badge HTML generator for settings WebView
 *
 * Generates consistent visual badges showing which config tier a setting
 * value comes from. Used in the settings panel to show source visibility.
 *
 * @see Issue #440 - Multi-tier config GUI support
 * @see docs/CONFIGURATION.md - Configuration tier reference
 */

import type { ViewTier } from "./types";
import type { ConfigSource } from "../../config/schema";

/**
 * Badge color configuration per tier
 *
 * Uses CSS custom properties for VSCode theme compatibility.
 * Colors are designed to be distinguishable in both light and dark themes.
 */
export const TIER_BADGE_COLORS: Record<ViewTier | "cli", { bg: string; fg: string }> = {
  merged: {
    bg: "var(--vscode-badge-background)",
    fg: "var(--vscode-badge-foreground)",
  },
  default: {
    bg: "var(--vscode-descriptionForeground)",
    fg: "var(--vscode-editor-background)",
  },
  global: {
    bg: "var(--vscode-charts-blue)",
    fg: "var(--vscode-editor-background)",
  },
  project: {
    bg: "var(--vscode-charts-green)",
    fg: "var(--vscode-editor-background)",
  },
  local: {
    bg: "var(--vscode-charts-yellow)",
    fg: "var(--vscode-editor-background)",
  },
  env: {
    bg: "var(--vscode-charts-purple)",
    fg: "var(--vscode-editor-background)",
  },
  cli: {
    bg: "var(--vscode-charts-orange)",
    fg: "var(--vscode-editor-background)",
  },
};

/**
 * Short labels for tier badges (space-constrained)
 */
export const TIER_BADGE_LABELS: Record<ViewTier | "cli", string> = {
  merged: "Merged",
  default: "Default",
  global: "Global",
  project: "Project",
  local: "Local",
  env: "Env",
  cli: "CLI",
};

/**
 * Icons for tier badges (codicon names)
 */
export const TIER_BADGE_ICONS: Record<ViewTier | "cli", string> = {
  merged: "layers",
  default: "symbol-constant",
  global: "home",
  project: "folder",
  local: "person",
  env: "terminal",
  cli: "console",
};

/**
 * Tooltips for tier badges (full descriptions)
 */
export const TIER_BADGE_TOOLTIPS: Record<ViewTier | "cli", string> = {
  merged: "Effective value after merging all config tiers",
  default: "Built-in default value",
  global: "Set in ~/.nightgauge/config.yaml (user-wide)",
  project: "Set in .nightgauge/config.yaml (project)",
  local: "Set in .nightgauge/config.local.yaml (gitignored)",
  env: "Set via environment variable",
  cli: "Set via CLI flag",
};

/**
 * Convert ConfigSource to ViewTier
 *
 * ConfigSource and ViewTier are mostly aligned but this provides
 * explicit type conversion.
 */
export function sourceToViewTier(source: ConfigSource | "cli"): ViewTier | "cli" {
  return source as ViewTier | "cli";
}

/**
 * Generate HTML for a tier source badge
 *
 * @param source - The config source tier
 * @param options - Badge options
 * @returns HTML string for the badge
 *
 * @example
 * ```typescript
 * // Simple badge
 * getTierBadgeHtml('project');
 * // Returns: <span class="tier-badge tier-badge-project" title="...">Project</span>
 *
 * // Badge with env var name
 * getTierBadgeHtml('env', { envVarName: 'NIGHTGAUGE_PIPELINE_AUTO_FIX' });
 * // Returns badge with env var in tooltip
 * ```
 */
export function getTierBadgeHtml(
  source: ViewTier | ConfigSource | "cli",
  options: {
    /** Environment variable name (for env tier) */
    envVarName?: string;
    /** Show icon before label */
    showIcon?: boolean;
    /** Compact mode (icon only) */
    compact?: boolean;
  } = {}
): string {
  const tier = source as ViewTier | "cli";
  const colors = TIER_BADGE_COLORS[tier];
  const label = TIER_BADGE_LABELS[tier];
  const icon = TIER_BADGE_ICONS[tier];

  let tooltip = TIER_BADGE_TOOLTIPS[tier];
  if (tier === "env" && options.envVarName) {
    tooltip = `Set via $${options.envVarName}`;
  }

  const iconHtml =
    options.showIcon || options.compact ? `<span class="codicon codicon-${icon}"></span>` : "";
  const labelHtml = options.compact ? "" : label;
  const separator = options.showIcon && !options.compact ? " " : "";

  return `<span class="tier-badge tier-badge-${tier}"
    style="background: ${colors.bg}; color: ${colors.fg};"
    title="${escapeHtml(tooltip)}">${iconHtml}${separator}${labelHtml}</span>`;
}

/**
 * Generate CSS styles for tier badges
 *
 * Include this in the WebView HTML <style> block.
 */
export function getTierBadgeStyles(): string {
  return `
    .tier-badge {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      padding: 2px 6px;
      border-radius: 3px;
      font-size: 0.75em;
      font-weight: 500;
      text-transform: uppercase;
      letter-spacing: 0.02em;
      white-space: nowrap;
      cursor: help;
    }

    .tier-badge .codicon {
      font-size: 12px;
      line-height: 1;
    }

    /* Tier-specific colors (fallback if CSS vars not available) */
    .tier-badge-default {
      opacity: 0.7;
    }

    .tier-badge-env {
      font-family: var(--vscode-editor-font-family);
    }

    /* Modified indicator for values that differ from default */
    .setting-modified .tier-badge::before {
      content: '●';
      margin-right: 4px;
      color: var(--vscode-editorInfo-foreground);
    }
  `;
}

/**
 * Generate HTML for tier override chain indicator
 *
 * Shows which tiers have values for a setting (e.g., "Default → Project → Local")
 *
 * @param tiers - Array of tiers that have values, in precedence order
 * @returns HTML string showing the override chain
 */
export function getTierChainHtml(tiers: Array<ViewTier | "cli">): string {
  if (tiers.length === 0) return "";
  if (tiers.length === 1) return getTierBadgeHtml(tiers[0], { compact: true });

  const badges = tiers.map((tier, index) => {
    const isLast = index === tiers.length - 1;
    const badge = getTierBadgeHtml(tier, { compact: true });
    const strikethrough = !isLast ? "text-decoration: line-through; opacity: 0.5;" : "";
    return `<span style="${strikethrough}">${badge}</span>`;
  });

  return `<span class="tier-chain">${badges.join(" → ")}</span>`;
}

/**
 * Get reset action options based on current tier
 *
 * Returns available reset targets for a setting based on where it's currently set.
 *
 * @param currentSource - Where the value is currently set
 * @param availableTiers - Which tiers have values for this setting
 * @returns Array of reset options
 */
export function getResetOptions(
  currentSource: ViewTier | "cli",
  availableTiers: Array<ViewTier | "cli">
): Array<{ tier: ViewTier; label: string; description: string }> {
  const options: Array<{ tier: ViewTier; label: string; description: string }> = [];

  // Always offer reset to default if not already default
  if (currentSource !== "default") {
    options.push({
      tier: "default" as ViewTier,
      label: "Reset to default",
      description: "Remove from all config files",
    });
  }

  // Offer reset to global if project/local override it
  if (
    (currentSource === "project" || currentSource === "local") &&
    availableTiers.includes("global")
  ) {
    options.push({
      tier: "global",
      label: "Reset to global",
      description: "Remove project/local override, use global value",
    });
  }

  // Offer reset to project if local overrides it
  if (currentSource === "local" && availableTiers.includes("project")) {
    options.push({
      tier: "project",
      label: "Reset to project",
      description: "Remove local override, use project value",
    });
  }

  return options;
}

// ============================================================================
// UX Tier Model (display-only layer for merged view — Issue #3339)
// ============================================================================

/**
 * 3-tier UX model displayed in merged view.
 *
 * Maps the 6-tier technical system to user-friendly labels:
 *   project  → "team"
 *   global   → "you"
 *   local / env / cli → "this-run"
 */
export type UxTier = "team" | "you" | "this-run";

export const UX_TIER_LABELS: Record<UxTier, string> = {
  team: "Team",
  you: "You",
  "this-run": "This run",
};

export const UX_TIER_COLORS: Record<UxTier, { bg: string; fg: string }> = {
  team: { bg: "var(--vscode-charts-green)", fg: "var(--vscode-editor-background)" },
  you: { bg: "var(--vscode-charts-blue)", fg: "var(--vscode-editor-background)" },
  "this-run": { bg: "var(--vscode-charts-purple)", fg: "var(--vscode-editor-background)" },
};

export const UX_TIER_TOOLTIPS: Record<UxTier, string> = {
  team: "Saved in `.nightgauge/config.yaml` (committed to git). Click 'Edit team config' to change.",
  you: "Saved in `~/.nightgauge/config.yaml` (your machine only).",
  "this-run": "Saved in VSCode local state. Per-workspace, never committed.",
};

/**
 * Map a technical tier to its UX alias for merged-view display.
 *
 * Returns null for tiers that have no UX alias (merged, default).
 */
export function techTierToUxTier(tier: ViewTier | "cli" | "default"): UxTier | null {
  if (tier === "project") return "team";
  if (tier === "global") return "you";
  if (tier === "local" || tier === "env" || tier === "cli") return "this-run";
  return null;
}

/**
 * Generate UX-tier badge HTML for merged view.
 *
 * Returns empty string for tiers with no UX alias (merged, default).
 */
export function getUxTierBadgeHtml(tier: ViewTier | "cli" | "default"): string {
  const uxTier = techTierToUxTier(tier);
  if (!uxTier) return "";
  const colors = UX_TIER_COLORS[uxTier];
  const label = UX_TIER_LABELS[uxTier];
  const tooltip = UX_TIER_TOOLTIPS[uxTier];
  return `<span class="tier-badge tier-badge-ux tier-badge-ux-${uxTier}"
    style="background: ${colors.bg}; color: ${colors.fg};"
    title="${escapeHtml(tooltip)}"
    aria-label="${escapeHtml(label)} tier: ${escapeHtml(tooltip)}">${escapeHtml(label)}</span>`;
}

// ============================================================================

/**
 * Escape HTML special characters
 */
function escapeHtml(text: string): string {
  const htmlEntities: Record<string, string> = {
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  };
  return text.replace(/[&<>"']/g, (char) => htmlEntities[char] || char);
}
