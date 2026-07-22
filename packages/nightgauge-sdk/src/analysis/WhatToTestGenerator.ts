/**
 * "What to Test" PR section generator.
 *
 * Formats an `ImpactAnalysisResult` (from the Change Impact Analyzer) into a
 * human-readable `## What to Test` Markdown section suitable for injection
 * into a pull request body.
 *
 * Single named export: `generateWhatToTestSection`
 *
 * @see Issue #1972 - "What to Test" PR Section Generator
 * @see Issue #1971 - Change Impact Analyzer (produces ImpactAnalysisResult)
 */

import type { ImpactAnalysisResult } from "./change-impact-types.js";
import type { WhatToTestOptions, WhatToTestSection } from "./what-to-test-types.js";

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Generate a `## What to Test` Markdown section from an impact analysis result.
 *
 * The section is structured by confidence tier (high → medium → low) and
 * truncated per the `maxHighConfidence` / `maxMediumConfidence` limits.
 *
 * When `result.affectedTests` is empty and the impact level is not
 * `'infrastructure'`, `generated` is `false` and `markdown` is an empty
 * string — the caller should omit the section from the PR body.
 *
 * When the impact level is `'infrastructure'`, an explicit warning is included
 * even if no individual test files were identified.
 */
export function generateWhatToTestSection(
  result: ImpactAnalysisResult,
  options?: WhatToTestOptions
): WhatToTestSection {
  const projectRoot = options?.projectRoot ?? process.cwd();
  const maxHigh = options?.maxHighConfidence ?? 10;
  const maxMedium = options?.maxMediumConfidence ?? 5;
  const includeLow = options?.includeLowConfidence ?? false;

  const high = result.affectedTests.filter((t) => t.confidence === "high");
  const medium = result.affectedTests.filter((t) => t.confidence === "medium");
  const low = result.affectedTests.filter((t) => t.confidence === "low");
  const total = result.summary.totalAffectedTests;

  // When no tests were found and this is not an infrastructure change,
  // return an empty section so the caller can omit it from the PR body.
  if (total === 0 && result.impactLevel !== "infrastructure") {
    return {
      markdown: "",
      generated: false,
      stats: {
        impactLevel: result.impactLevel,
        testsListed: 0,
        testsOmitted: 0,
      },
    };
  }

  // Build count summary line
  const countParts: string[] = [];
  if (high.length > 0) countParts.push(`${high.length} high`);
  if (medium.length > 0) countParts.push(`${medium.length} medium`);
  if (includeLow && low.length > 0) countParts.push(`${low.length} low`);

  const countStr =
    countParts.length > 0 ? `${total} total — ${countParts.join(", ")}` : `${total} total`;

  let md = "## What to Test\n\n";
  md += `**Impact level**: ${result.impactLevel}\n\n`;
  md += `**Affected test files** (${countStr}):\n\n`;

  // Infrastructure warning — use specific trigger rule when available
  if (result.impactLevel === "infrastructure") {
    if (result.summary.regressionTrigger) {
      const { type, reason } = result.summary.regressionTrigger;
      md += `> **Full regression required** — ${reason}\n\n`;
      md += `> _Trigger rule_: \`${type}\`\n\n`;
    } else {
      md += "> **Infrastructure change detected** — full regression recommended.\n\n";
    }
  }

  if (total === 0) {
    md += "> No affected test files detected.\n";
  } else {
    // High confidence
    if (high.length > 0) {
      md += "### High confidence (direct imports)\n\n";
      const shown = high.slice(0, maxHigh);
      for (const t of shown) {
        md += `- \`${relativizePath(t.testFile, projectRoot)}\` — ${t.reason}\n`;
      }
      if (high.length > maxHigh) {
        md += `- ... ${high.length - maxHigh} more\n`;
      }
      md += "\n";
    }

    // Medium confidence
    if (medium.length > 0) {
      md += "### Medium confidence (transitive)\n\n";
      const shown = medium.slice(0, maxMedium);
      for (const t of shown) {
        md += `- \`${relativizePath(t.testFile, projectRoot)}\` — ${t.reason}\n`;
      }
      if (medium.length > maxMedium) {
        md += `- ... ${medium.length - maxMedium} more\n`;
      }
      md += "\n";
    }

    // Low confidence (only when opt-in)
    if (includeLow && low.length > 0) {
      md += "### Low confidence (heuristic)\n\n";
      for (const t of low) {
        md += `- \`${relativizePath(t.testFile, projectRoot)}\` — ${t.reason}\n`;
      }
      md += "\n";
    }
  }

  // Stats
  const listedHigh = Math.min(high.length, maxHigh);
  const listedMedium = Math.min(medium.length, maxMedium);
  const listedLow = includeLow ? low.length : 0;
  const testsListed = listedHigh + listedMedium + listedLow;
  const testsOmitted =
    high.length - listedHigh + (medium.length - listedMedium) + (includeLow ? 0 : low.length);

  return {
    markdown: md,
    generated: true,
    stats: {
      impactLevel: result.impactLevel,
      testsListed,
      testsOmitted: Math.max(0, testsOmitted),
    },
  };
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

/**
 * Strip the project root prefix from a path to produce a relative display path.
 * If the path does not start with the project root, it is returned as-is.
 */
function relativizePath(testFile: string, projectRoot: string): string {
  const root = projectRoot.endsWith("/") ? projectRoot : `${projectRoot}/`;
  if (testFile.startsWith(root)) {
    return testFile.slice(root.length);
  }
  return testFile;
}
