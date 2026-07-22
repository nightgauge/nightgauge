/**
 * AllowlistSuggestionService - Pure function service for generating allowlist suggestions
 *
 * Analyzes blocked/warned sanitization events and generates suggestions for
 * allowlist regex patterns or safe_directory entries. This is a deterministic
 * engine with no side effects or filesystem I/O.
 *
 * @see Issue #786 - Firewall Learning Mode
 */

import type { SanitizationEvent, AllowlistSuggestion } from "../views/dashboard/FirewallTypes";
import * as path from "path";

/** Maximum number of suggestions to return */
const MAX_SUGGESTIONS = 10;

/** Maximum number of recent events to analyze */
const MAX_EVENTS = 50;

/** Minimum events sharing a directory prefix to suggest safe_directory */
const SAFE_DIR_THRESHOLD = 2;

/**
 * Extract workspace-relative paths from event content.
 *
 * Looks for paths starting with './' or paths that are clearly relative
 * (no leading '/' and containing path separators). Filters out absolute
 * system paths to prevent suggesting bypasses for /usr, /etc, $HOME, etc.
 */
function extractPaths(content: string): string[] {
  const paths: string[] = [];

  // Match paths like ./build/foo, ./dist/bar.js, etc.
  const relativePathRegex = /\.\/[^\s'"`;|&<>]+/g;
  const matches = content.match(relativePathRegex);
  if (matches) {
    for (const match of matches) {
      // Normalize and clean trailing punctuation
      const cleaned = match.replace(/[,;:)}\]]+$/, "");
      if (cleaned.length > 2) {
        paths.push(cleaned);
      }
    }
  }

  return paths;
}

/**
 * Get the directory prefix from a relative path.
 * For './build/foo/bar.js' returns './build'.
 * For './file.js' returns '.'.
 */
function getDirectoryPrefix(relativePath: string): string {
  const parts = relativePath.split("/");
  if (parts.length <= 2) {
    // Path is like ./file.js — directory is '.'
    return ".";
  }
  // Return first two segments: ./build, ./dist, etc.
  return parts.slice(0, 2).join("/");
}

/**
 * Escape a string for use in a regex pattern
 */
function escapeRegex(str: string): string {
  return str.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Check if a pattern is already covered by an existing allowlist entry
 */
function isAlreadyAllowlisted(pattern: string, currentAllowlist: string[]): boolean {
  for (const existing of currentAllowlist) {
    try {
      const regex = new RegExp(existing);
      if (regex.test(pattern)) {
        return true;
      }
    } catch {
      // Invalid regex in allowlist — skip
      if (existing === pattern) {
        return true;
      }
    }
  }
  return false;
}

/**
 * Check if a directory is already in safe_directories
 */
function isAlreadySafeDirectory(dir: string, currentSafeDirectories: string[]): boolean {
  // Normalize for comparison
  const normalized = dir.replace(/\/$/, "");
  return currentSafeDirectories.some((sd) => {
    const normalizedSd = sd.replace(/\/$/, "");
    return normalizedSd === normalized || normalized.startsWith(normalizedSd + "/");
  });
}

/**
 * Check if a path is an absolute system path (not workspace-relative)
 */
function isSystemPath(pathStr: string): boolean {
  if (path.isAbsolute(pathStr)) {
    return true;
  }
  // Check for common system path indicators
  if (
    pathStr.startsWith("~") ||
    pathStr.startsWith("$HOME") ||
    pathStr.startsWith("/usr") ||
    pathStr.startsWith("/etc") ||
    pathStr.startsWith("/var") ||
    pathStr.startsWith("/tmp") ||
    pathStr.startsWith("/bin") ||
    pathStr.startsWith("/sbin")
  ) {
    return true;
  }
  return false;
}

export class AllowlistSuggestionService {
  /**
   * Generate allowlist/safe_directory suggestions from blocked/warned events.
   *
   * Algorithm (deterministic):
   * 1. Take the most recent MAX_EVENTS blocked/warned events
   * 2. Extract workspace-relative paths from each event's content
   * 3. Safety filter: discard absolute/system paths
   * 4. Group by directory prefix
   * 5. Rank groups by frequency (descending)
   * 6. For groups with 2+ events → suggest safe_directory
   * 7. For single events → suggest allowlist regex
   * 8. Exclude already-allowlisted and dismissed patterns
   * 9. Return sorted array capped at MAX_SUGGESTIONS
   */
  generateSuggestions(
    events: SanitizationEvent[],
    currentAllowlist: string[],
    currentSafeDirectories: string[],
    dismissedPatterns: string[]
  ): AllowlistSuggestion[] {
    // 1. Filter to blocked/warned events, take most recent
    const relevantEvents = events
      .filter((e) => e.event === "blocked" || e.event === "warned")
      .sort((a, b) => b.timestamp.getTime() - a.timestamp.getTime())
      .slice(0, MAX_EVENTS);

    if (relevantEvents.length === 0) {
      return [];
    }

    // 2-3. Extract paths and group by directory prefix
    const dirGroups = new Map<
      string,
      {
        events: SanitizationEvent[];
        paths: string[];
      }
    >();

    for (const event of relevantEvents) {
      const paths = extractPaths(event.content);

      for (const p of paths) {
        // Safety filter: discard system paths
        if (isSystemPath(p)) {
          continue;
        }

        const dir = getDirectoryPrefix(p);

        if (!dirGroups.has(dir)) {
          dirGroups.set(dir, { events: [], paths: [] });
        }
        const group = dirGroups.get(dir)!;
        group.events.push(event);
        group.paths.push(p);
      }

      // If no paths extracted, consider the full content for allowlist regex
      // but skip if the content references system paths (safety filter)
      if (paths.length === 0 && event.content.trim()) {
        const contentHasSystemPath =
          /(?:^|\s)\/(?:usr|etc|var|tmp|bin|sbin|opt|lib|home)\b/.test(event.content) ||
          /(?:^|\s)~\//.test(event.content) ||
          /\$HOME/.test(event.content);
        if (!contentHasSystemPath) {
          const key = `__content__${event.content.substring(0, 100)}`;
          if (!dirGroups.has(key)) {
            dirGroups.set(key, { events: [], paths: [] });
          }
          dirGroups.get(key)!.events.push(event);
        }
      }
    }

    // 4-5. Build suggestions ranked by frequency
    const suggestions: AllowlistSuggestion[] = [];

    for (const [dir, group] of dirGroups.entries()) {
      // Deduplicate events by timestamp
      const uniqueEvents = new Map<number, SanitizationEvent>();
      for (const e of group.events) {
        uniqueEvents.set(e.timestamp.getTime(), e);
      }
      const dedupedEvents = Array.from(uniqueEvents.values());
      const frequency = dedupedEvents.length;
      const mostRecent = dedupedEvents.sort(
        (a, b) => b.timestamp.getTime() - a.timestamp.getTime()
      )[0];

      if (dir.startsWith("__content__")) {
        // Content-based suggestion: generate allowlist regex
        const content = mostRecent.content;
        // Create a regex pattern that matches the command structure
        const pattern = escapeRegex(content.trim().substring(0, 200));

        if (
          isAlreadyAllowlisted(content, currentAllowlist) ||
          dismissedPatterns.includes(pattern)
        ) {
          continue;
        }

        suggestions.push({
          pattern,
          type: "allowlist",
          frequency,
          lastOccurrence: mostRecent.timestamp,
          exampleContent: content.substring(0, 200),
          description: `Allow command: ${content.substring(0, 80)}${content.length > 80 ? "..." : ""}`,
        });
      } else if (frequency >= SAFE_DIR_THRESHOLD && dir !== ".") {
        // Directory group with 2+ events: suggest safe_directory
        if (
          isAlreadySafeDirectory(dir, currentSafeDirectories) ||
          dismissedPatterns.includes(dir)
        ) {
          continue;
        }

        suggestions.push({
          pattern: dir,
          type: "safe_directory",
          frequency,
          lastOccurrence: mostRecent.timestamp,
          exampleContent: mostRecent.content.substring(0, 200),
          description: `Allow operations in ${dir}/ directory`,
        });
      } else {
        // Single event or root-level: suggest allowlist regex
        const examplePath = group.paths[0] || mostRecent.content;
        const pattern = escapeRegex(examplePath);

        if (
          isAlreadyAllowlisted(examplePath, currentAllowlist) ||
          dismissedPatterns.includes(pattern)
        ) {
          continue;
        }

        suggestions.push({
          pattern,
          type: "allowlist",
          frequency,
          lastOccurrence: mostRecent.timestamp,
          exampleContent: mostRecent.content.substring(0, 200),
          description: `Allow pattern: ${examplePath.substring(0, 80)}${examplePath.length > 80 ? "..." : ""}`,
        });
      }
    }

    // 9. Sort by frequency (descending) and cap at MAX_SUGGESTIONS
    suggestions.sort((a, b) => b.frequency - a.frequency);
    return suggestions.slice(0, MAX_SUGGESTIONS);
  }
}
