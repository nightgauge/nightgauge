#!/usr/bin/env tsx
/**
 * Backfill: re-classify the last 30 days of retros under the #3275 logic
 *
 * Issue #3275 fixed several mis-attributions in `AutoRetroService.classifyFailure`:
 *
 *   - `stop-hook-error` was demoted + time-gated (only fires when the
 *     stop-hook notification PRECEDES the terminal `"type":"result"` event,
 *     or when no terminal result event exists at all).
 *   - A deterministic `cost-cap` extractor was added that fires when the
 *     `<stage>-cost-capped.log` diagnostic file is in `sources_analyzed`,
 *     even when the textual `[cost-cap-exceeded]` log line is absent.
 *   - A `skill-no-op` extractor was added for pr-merge runs whose
 *     post-merge verification reported "PR is not merged".
 *
 * This script walks `.nightgauge/retros/*_retro.json` from the last
 * 30 days, attempts to re-load each retro's recorded `sources_analyzed` from
 * disk (best-effort — sources may have rotated), and re-applies the post-#3275
 * structural signals to the original primary finding. It writes a
 * `<original>.v2.json` sidecar containing the new findings plus a diff
 * summary. ORIGINAL FILES ARE NEVER MUTATED.
 *
 * The `false-negative-shipped` override is NOT applied here — it requires a
 * live `gh pr view` IPC call against the platform, which is out of scope
 * for an offline backfill. Operators can re-run live retros for affected
 * pr-merge runs if needed.
 *
 * The classification logic is INTENTIONALLY DUPLICATED from
 * `AutoRetroService` rather than imported because that module transitively
 * imports the `vscode` API at module-load time, which is unavailable in a
 * standalone tsx script. The duplicated logic is small and is covered by
 * unit tests on the canonical implementation; this script is a one-off
 * audit tool, not a long-lived consumer.
 *
 * Usage:
 *   npx tsx scripts/retro/backfill-3275.ts                   # write sidecars
 *   npx tsx scripts/retro/backfill-3275.ts --days 60         # widen window
 *   npx tsx scripts/retro/backfill-3275.ts --force           # overwrite existing .v2.json
 *   npx tsx scripts/retro/backfill-3275.ts --dry-run         # summary only, no writes
 *
 * @see Issue #3275
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

interface CliOptions {
  days: number;
  force: boolean;
  dryRun: boolean;
  retrosDir: string;
  workspaceRoot: string;
}

interface OriginalFinding {
  category: string;
  severity?: string;
  summary?: string;
  evidence?: string[];
  recommendation?: string;
}

interface OriginalRetro {
  schema_version?: string;
  issue_number: number;
  failed_stage: string;
  created_at?: string;
  findings: OriginalFinding[];
  sources_analyzed: string[];
}

interface BackfillResult {
  file: string;
  issueNumber: number;
  failedStage: string;
  originalCategory: string;
  newCategory: string;
  changed: boolean;
  reason?: string;
  skipped?: string;
}

// =============================================================================
// CLI
// =============================================================================

function parseArgs(argv: string[]): CliOptions {
  const opts: CliOptions = {
    days: 30,
    force: false,
    dryRun: false,
    workspaceRoot: process.cwd(),
    retrosDir: path.join(process.cwd(), ".nightgauge", "retros"),
  };
  for (let i = 2; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--days") {
      opts.days = parseInt(argv[++i], 10) || 30;
    } else if (arg === "--force") {
      opts.force = true;
    } else if (arg === "--dry-run") {
      opts.dryRun = true;
    } else if (arg === "--workspace") {
      opts.workspaceRoot = argv[++i];
      opts.retrosDir = path.join(opts.workspaceRoot, ".nightgauge", "retros");
    }
  }
  return opts;
}

// =============================================================================
// Inlined #3275 classification helpers
// =============================================================================

/** Mirror of AutoRetroService.isPreResultStopHook (#3275). */
function isPreResultStopHook(text: string): boolean {
  const stopHookRe = /"key"\s*:\s*"stop-hook-error"|Stop hook error occurred/g;
  const resultRe = /"type"\s*:\s*"result"/g;

  let firstStopHookIdx = -1;
  const sm = stopHookRe.exec(text);
  if (sm) firstStopHookIdx = sm.index;
  if (firstStopHookIdx < 0) return false;

  let lastResultIdx = -1;
  let rm: RegExpExecArray | null;
  while ((rm = resultRe.exec(text)) !== null) {
    lastResultIdx = rm.index;
  }
  if (lastResultIdx < 0) return true;
  return firstStopHookIdx < lastResultIdx;
}

/**
 * Re-classify a single retro using only the post-#3275 structural signals.
 * Returns the new primary category and the reason it changed (or empty when
 * no change). Falls back to the original category when no new signal applies.
 */
function reclassify(
  text: string,
  sourcesAnalyzed: string[],
  failedStage: string,
  originalCategory: string
): { category: string; reason: string } {
  const diagFile = `${failedStage}-cost-capped.log`;
  if (sourcesAnalyzed.includes(diagFile)) {
    if (originalCategory !== "cost-cap") {
      return {
        category: "cost-cap",
        reason: `diagnostic file ${diagFile} present in sources_analyzed`,
      };
    }
    return { category: "cost-cap", reason: "" };
  }

  if (failedStage === "pr-merge") {
    if (
      /reported success but PR is not merged/i.test(text) ||
      /post[-_ ]merge verification failed/i.test(text) ||
      /"pr_merged"\s*:\s*false/.test(text)
    ) {
      if (originalCategory !== "skill-no-op") {
        return {
          category: "skill-no-op",
          reason: "pr-merge post-merge verification text present in evidence",
        };
      }
      return { category: "skill-no-op", reason: "" };
    }
  }

  if (originalCategory === "stop-hook-error") {
    const hasStopHook =
      /"key"\s*:\s*"stop-hook-error"/.test(text) || /Stop hook error occurred/.test(text);
    if (hasStopHook && !isPreResultStopHook(text)) {
      return {
        category: "unknown",
        reason: "stop-hook-error fires AFTER terminal result event (post-result teardown noise)",
      };
    }
  }

  return { category: originalCategory, reason: "" };
}

// =============================================================================
// Filesystem operations
// =============================================================================

async function listRetroFiles(dir: string, days: number): Promise<string[]> {
  let entries: string[];
  try {
    entries = await fs.readdir(dir);
  } catch {
    return [];
  }
  const cutoff = Date.now() - days * 24 * 60 * 60 * 1000;
  const out: string[] = [];
  for (const entry of entries) {
    if (!entry.endsWith("_retro.json")) continue;
    const full = path.join(dir, entry);
    try {
      const stat = await fs.stat(full);
      if (stat.mtimeMs >= cutoff) out.push(full);
    } catch {
      // skip unstattable
    }
  }
  return out.sort();
}

async function reconstructEvidenceText(
  workspaceRoot: string,
  original: OriginalRetro
): Promise<string> {
  const parts: string[] = [];
  const issueNumber = original.issue_number;
  const failedStage = original.failed_stage;

  for (const source of original.sources_analyzed) {
    let candidatePath: string | null = null;
    if (source === "session_log") {
      const dateMatch = original.created_at?.slice(0, 10);
      if (dateMatch) {
        candidatePath = path.join(
          workspaceRoot,
          ".nightgauge",
          "logs",
          `${dateMatch}_${issueNumber}_session.log`
        );
      }
    } else if (source === "pipeline_context") {
      candidatePath = path.join(
        workspaceRoot,
        ".nightgauge",
        "pipeline",
        `${failedStage}-${issueNumber}.json`
      );
    } else if (source === "execution_history") {
      continue;
    } else if (source.endsWith(".log")) {
      candidatePath = path.join(
        workspaceRoot,
        ".nightgauge",
        "pipeline",
        "history",
        String(issueNumber),
        source
      );
    }

    if (!candidatePath) continue;
    try {
      const content = await fs.readFile(candidatePath, "utf-8");
      parts.push(content);
    } catch {
      // Source rotated/missing — drop
    }
  }

  // Always include the original findings' evidence strings as a fallback so
  // structural extractors have SOMETHING to look at when sources rotated.
  for (const finding of original.findings) {
    if (finding.evidence) parts.push(finding.evidence.join("\n"));
  }

  return parts.join("\n");
}

async function processOne(
  filePath: string,
  workspaceRoot: string,
  opts: CliOptions
): Promise<BackfillResult> {
  const sidecar = filePath.replace(/\.json$/, ".v2.json");

  if (!opts.force && !opts.dryRun) {
    try {
      await fs.stat(sidecar);
      return {
        file: filePath,
        issueNumber: -1,
        failedStage: "",
        originalCategory: "",
        newCategory: "",
        changed: false,
        skipped: "sidecar exists (use --force to overwrite)",
      };
    } catch {
      // No sidecar — proceed
    }
  }

  let original: OriginalRetro;
  try {
    const raw = await fs.readFile(filePath, "utf-8");
    original = JSON.parse(raw) as OriginalRetro;
  } catch (err) {
    return {
      file: filePath,
      issueNumber: -1,
      failedStage: "",
      originalCategory: "",
      newCategory: "",
      changed: false,
      skipped: `read/parse failed: ${err instanceof Error ? err.message : String(err)}`,
    };
  }

  const text = await reconstructEvidenceText(workspaceRoot, original);
  const originalCategory = original.findings[0]?.category ?? "unknown";
  const { category: newCategory, reason } = reclassify(
    text,
    original.sources_analyzed,
    original.failed_stage,
    originalCategory
  );
  const changed = originalCategory !== newCategory;

  if (!opts.dryRun) {
    const sidecarPayload = {
      schema_version: "1.0-v2",
      backfill_issue: 3275,
      backfill_at: new Date().toISOString(),
      original_file: path.basename(filePath),
      issue_number: original.issue_number,
      failed_stage: original.failed_stage,
      diff: {
        changed,
        original_category: originalCategory,
        new_category: newCategory,
        reason: reason || null,
      },
      // The sidecar mirrors the original finding shape, with category swapped.
      // Severities/summaries/recommendations for new categories are NOT
      // re-derived here — the canonical AutoRetroService does that at live
      // classification time. Operators consulting the sidecar should treat
      // `diff.new_category` as the source of truth.
      original_findings: original.findings,
      sources_analyzed: original.sources_analyzed,
    };
    await fs.writeFile(sidecar, JSON.stringify(sidecarPayload, null, 2), "utf-8");
  }

  return {
    file: filePath,
    issueNumber: original.issue_number,
    failedStage: original.failed_stage,
    originalCategory,
    newCategory,
    changed,
    reason,
  };
}

// =============================================================================
// Main
// =============================================================================

async function main(): Promise<void> {
  const opts = parseArgs(process.argv);

  console.log(`Backfill #3275 — last ${opts.days} days of retros`);
  console.log(`  retros dir: ${opts.retrosDir}`);
  console.log(`  workspace:  ${opts.workspaceRoot}`);
  console.log(`  dry-run:    ${opts.dryRun}`);
  console.log(`  force:      ${opts.force}`);
  console.log("");

  const files = await listRetroFiles(opts.retrosDir, opts.days);
  if (files.length === 0) {
    console.log(`No retro files found in ${opts.retrosDir} within ${opts.days} days.`);
    return;
  }

  const results: BackfillResult[] = [];
  for (const f of files) {
    const r = await processOne(f, opts.workspaceRoot, opts);
    results.push(r);
  }

  const skipped = results.filter((r) => r.skipped);
  const processed = results.filter((r) => !r.skipped);
  const changed = processed.filter((r) => r.changed);

  console.log(`Walked: ${results.length} retro file(s)`);
  console.log(`Processed: ${processed.length}`);
  console.log(`Skipped: ${skipped.length}`);
  console.log(`Changed classification: ${changed.length}`);
  console.log("");

  if (changed.length > 0) {
    console.log("Reclassifications:");
    for (const r of changed) {
      console.log(
        `  #${r.issueNumber} (${r.failedStage}): ${r.originalCategory} → ${r.newCategory}` +
          (r.reason ? `  [${r.reason}]` : "")
      );
    }
  }

  if (skipped.length > 0) {
    console.log("");
    console.log("Skipped:");
    for (const r of skipped) {
      console.log(`  ${path.basename(r.file)}: ${r.skipped}`);
    }
  }
}

main().catch((err) => {
  console.error("Backfill failed:", err instanceof Error ? err.stack : String(err));
  process.exit(1);
});
