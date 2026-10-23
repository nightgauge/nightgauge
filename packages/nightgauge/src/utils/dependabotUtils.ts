/**
 * Utilities for detecting and parsing Dependabot dependency update issues.
 *
 * Dependabot issues are identified by their labels (dependencies, security,
 * or language labels like go, javascript, python, rust). Detection is
 * label-based since the author field is not available in the ReadyIssue type.
 */

/** Labels that indicate a Dependabot-generated issue */
const DEPENDABOT_LABELS = new Set([
  "dependencies",
  "security",
  "go",
  "javascript",
  "python",
  "rust",
  "java",
  "ruby",
  "php",
  "dotnet",
  "docker",
  "github-actions",
  "npm",
]);

/**
 * Check if an issue is a Dependabot dependency update.
 * Detects by presence of known Dependabot labels.
 */
export function isDependabotIssue(labels: string[]): boolean {
  return labels.some((l) => DEPENDABOT_LABELS.has(l));
}

/**
 * Get the Dependabot update type based on labels.
 * Returns "security" for security advisories, "dependency" for routine bumps.
 */
export function getDependabotType(labels: string[]): "security" | "dependency" | null {
  if (!isDependabotIssue(labels)) {
    return null;
  }
  if (labels.includes("security")) {
    return "security";
  }
  return "dependency";
}

/**
 * Extract package name and version delta from a Dependabot issue title/body.
 *
 * Supports common Dependabot title formats:
 *   "Bump lodash from 4.17.20 to 4.17.21"
 *   "build(deps): bump lodash from 4.17.20 to 4.17.21"
 *   "chore(deps): update package from 1.0.0 to 1.1.0"
 *
 * Returns null when the title does not match known patterns.
 */
export function getDependencyPackageInfo(
  title: string,
  body?: string
): { name: string; from: string; to: string } | null {
  // Pattern 1: "Bump {name} from {from} to {to}" (Dependabot default)
  const bumpMatch = title.match(/[Bb]ump\s+(\S+)\s+from\s+([\d.]+[\w.-]*)\s+to\s+([\d.]+[\w.-]*)/i);
  if (bumpMatch) {
    return { name: bumpMatch[1], from: bumpMatch[2], to: bumpMatch[3] };
  }

  // Pattern 2: "update {name} from {from} to {to}"
  const updateMatch = title.match(
    /[Uu]pdate\s+(\S+)\s+from\s+([\d.]+[\w.-]*)\s+to\s+([\d.]+[\w.-]*)/i
  );
  if (updateMatch) {
    return { name: updateMatch[1], from: updateMatch[2], to: updateMatch[3] };
  }

  // Pattern 3: conventional commit prefix + bump/update in body
  if (body && (title.toLowerCase().includes("bump") || title.toLowerCase().includes("update"))) {
    const versionMatch = body.match(/([\d.]+[\w.-]*)\s*(?:→|->|to)\s*([\d.]+[\w.-]*)/);
    if (versionMatch) {
      const nameMatch = title.match(/(?:bump|update)\s+(\S+)/i);
      const name = nameMatch ? nameMatch[1] : "dependency";
      return { name, from: versionMatch[1], to: versionMatch[2] };
    }
  }

  return null;
}
