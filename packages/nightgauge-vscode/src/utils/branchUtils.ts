/**
 * Branch utilities for target branch selection
 *
 * Provides functions for listing, filtering, and sorting git branches
 * to support the target branch selection UI.
 *
 * All git operations go through the Go binary IPC server.
 *
 * @see Issue #101 - Add target branch selection UI
 */

import { IpcClient } from "../services/IpcClient";

/**
 * Branch information
 */
export interface BranchInfo {
  name: string;
  isRemote: boolean;
  isDefault: boolean;
  category: BranchCategory;
}

/**
 * Branch categories for sorting and filtering
 */
export type BranchCategory = "default" | "develop" | "release" | "epic" | "feature" | "other";

/**
 * Default branch patterns for filtering
 * Order matters - first match wins for categorization
 */
const BRANCH_PATTERNS: Array<{
  pattern: RegExp;
  category: BranchCategory;
}> = [
  { pattern: /^(main|master)$/, category: "default" },
  { pattern: /^develop(ment)?$/, category: "develop" },
  { pattern: /^release\//, category: "release" },
  { pattern: /^epic\//, category: "epic" },
  { pattern: /^(feat|feature)\//, category: "feature" },
];

/**
 * Category sort order - lower number = higher priority
 */
const CATEGORY_ORDER: Record<BranchCategory, number> = {
  default: 0,
  develop: 1,
  release: 2,
  epic: 3,
  feature: 4,
  other: 5,
};

/**
 * Categorize a branch name
 */
export function categorizeBranch(branchName: string): BranchCategory {
  for (const { pattern, category } of BRANCH_PATTERNS) {
    if (pattern.test(branchName)) {
      return category;
    }
  }
  return "other";
}

/**
 * Parse branch name from remote ref
 * @example "origin/main" -> "main"
 * @example "origin/release/v1.0" -> "release/v1.0"
 */
export function parseRemoteBranch(ref: string): string {
  // Remove origin/ prefix
  return ref.replace(/^origin\//, "");
}

/**
 * Validate a branch name for use in git commands
 * Prevents command injection and ensures valid git ref
 */
export function isValidBranchName(name: string): boolean {
  if (!name || typeof name !== "string") {
    return false;
  }

  // Git ref restrictions
  // See: https://git-scm.com/docs/git-check-ref-format
  const invalidPatterns = [
    /\.\./, // No consecutive dots
    /^[./]/, // Can't start with dot or slash
    /[/.]$/, // Can't end with slash or dot
    /@\{/, // No @{
    /\\/, // No backslash
    // eslint-disable-next-line no-control-regex
    /[\x00-\x1f]/, // No control characters
    /[\x7f]/, // No DEL
    /[ ~^:?*[]/, // No space, tilde, caret, colon, question, asterisk, bracket
    /\.lock$/, // Can't end with .lock
  ];

  for (const pattern of invalidPatterns) {
    if (pattern.test(name)) {
      return false;
    }
  }

  return true;
}

/**
 * List remote branches from the repository via Go binary IPC
 *
 * @param cwd - Working directory (repository root)
 * @returns Array of branch names (without origin/ prefix)
 */
export async function listRemoteBranches(cwd: string): Promise<string[]> {
  try {
    const ipc = IpcClient.getInstance();

    // Fetch latest refs first (optional, may fail without network)
    try {
      await ipc.gitFetch(true, cwd);
    } catch {
      // Ignore fetch errors - use cached refs
    }

    const branches = await ipc.gitListRemoteBranches(cwd);
    return branches
      .filter((name) => !name.includes("HEAD"))
      .filter((name) => isValidBranchName(name));
  } catch {
    return [];
  }
}

/**
 * Get branch suggestions filtered and sorted for display
 *
 * @param branches - Raw branch names
 * @param configSuggestions - Additional suggestions from .nightgauge/config.yaml
 * @returns Sorted and categorized branches
 */
export function getSortedBranches(
  branches: string[],
  configSuggestions: string[] = []
): BranchInfo[] {
  // Create unique set of branches
  const uniqueBranches = [...new Set([...configSuggestions, ...branches])];

  // Map to BranchInfo with categorization
  const branchInfos: BranchInfo[] = uniqueBranches.map((name) => ({
    name,
    isRemote: branches.includes(name),
    isDefault: name === "main" || name === "master",
    category: categorizeBranch(name),
  }));

  // Sort by category order, then alphabetically within category
  return branchInfos.sort((a, b) => {
    const categoryDiff = CATEGORY_ORDER[a.category] - CATEGORY_ORDER[b.category];
    if (categoryDiff !== 0) {
      return categoryDiff;
    }
    return a.name.localeCompare(b.name);
  });
}

/**
 * Filter branches to show only relevant target branches
 *
 * Filters out:
 * - Feature branches (feat/*, fix/*, etc.)
 * - Personal branches
 * - Very old release branches (configurable)
 *
 * @param branches - Branch infos to filter
 * @param options - Filter options
 * @returns Filtered branch list
 */
export function filterTargetBranches(
  branches: BranchInfo[],
  options: {
    includeFeatureBranches?: boolean;
    maxReleaseBranches?: number;
  } = {}
): BranchInfo[] {
  const { includeFeatureBranches = false, maxReleaseBranches = 5 } = options;

  let filtered = branches;

  // Filter out feature branches unless explicitly included
  if (!includeFeatureBranches) {
    filtered = filtered.filter((b) => b.category !== "feature" && b.category !== "other");
  }

  // Limit release branches (keep most recent by name)
  const releaseBranches = filtered
    .filter((b) => b.category === "release")
    .sort((a, b) => b.name.localeCompare(a.name)) // Reverse sort - newest first
    .slice(0, maxReleaseBranches);

  const nonReleaseBranches = filtered.filter((b) => b.category !== "release");

  return [...nonReleaseBranches, ...releaseBranches].sort((a, b) => {
    const categoryDiff = CATEGORY_ORDER[a.category] - CATEGORY_ORDER[b.category];
    if (categoryDiff !== 0) {
      return categoryDiff;
    }
    return a.name.localeCompare(b.name);
  });
}

/**
 * Get display label for a branch
 *
 * @param branch - Branch info
 * @param currentBaseBranch - Currently selected base branch (for marking)
 * @param protectedBranches - List of protected branch names from config
 * @returns Display label with icons
 */
export function getBranchLabel(
  branch: BranchInfo,
  currentBaseBranch?: string,
  protectedBranches: string[] = []
): string {
  const isCurrent = branch.name === currentBaseBranch;
  const isProtected = protectedBranches.includes(branch.name);

  // Build prefix: check mark for current, lock for protected
  let prefix = "";
  if (isCurrent) {
    prefix = "$(check) ";
  }
  if (isProtected) {
    prefix += "$(lock) ";
  }

  switch (branch.category) {
    case "default":
      return `${prefix}${branch.name} (default)`;
    case "develop":
      return `${prefix}${branch.name} (development)`;
    case "release":
      return `${prefix}${branch.name}`;
    case "epic":
      return `${prefix}${branch.name} (epic)`;
    default:
      return `${prefix}${branch.name}`;
  }
}

/**
 * Get description for a branch
 *
 * @param branch - Branch info
 * @param protectedBranches - List of protected branch names from config
 * @returns Description text for the branch
 */
export function getBranchDescription(branch: BranchInfo, protectedBranches: string[] = []): string {
  const isProtected = protectedBranches.includes(branch.name);
  const protectedSuffix = isProtected ? " (protected)" : "";

  switch (branch.category) {
    case "default":
      return `Production branch - standard target for features${protectedSuffix}`;
    case "develop":
      return `Development branch - integration before production${protectedSuffix}`;
    case "release":
      return `Release branch - for release preparation${protectedSuffix}`;
    case "epic":
      return `Epic branch - for large feature work${protectedSuffix}`;
    default:
      return `Remote branch${protectedSuffix}`;
  }
}
