#!/usr/bin/env tsx
/**
 * Backfill complexity-model.yaml with historical PR data
 *
 * Queries closed PRs from the last 6 months, extracts size labels,
 * and estimates work time based on lines changed to bootstrap the model.
 *
 * Usage:
 *   npm run backfill-model
 *   npm run backfill-model -- --months 12
 *   npm run backfill-model -- --dry-run
 *
 * @see Issue #310 - Add actual work time feedback loop
 */

import { execSync } from "child_process";
import * as fs from "fs/promises";
import * as yaml from "js-yaml";

interface HistoricalPR {
  number: number;
  issueNumber: number;
  title: string;
  sizeLabel: "XS" | "S" | "M" | "L" | "XL";
  linesChanged: number;
  estimatedMinutes: number;
  mergedAt: string;
}

interface WorkTimeObservation {
  issue_number: number;
  size: string;
  priority: string | null;
  task_type: string | null;
  actual_work_minutes: number;
  estimated_minutes: number;
  routing: string;
  stages_completed: string[];
  timestamp: string;
}

/**
 * Estimate work time from lines changed using empirical heuristics
 *
 * Based on industry averages:
 * - Junior dev: ~10 lines/hour (includes thinking, testing, debugging)
 * - Mid-level: ~20-30 lines/hour
 * - Senior: ~30-50 lines/hour (but more complex code)
 *
 * We use 25 lines/hour as a reasonable middle ground.
 */
function estimateMinutesFromLines(linesChanged: number, size: string): number {
  // Base estimate: 25 lines per hour = 2.4 minutes per line
  const baseMinutes = linesChanged * 2.4;

  // Size-based confidence adjustment
  // Smaller changes are often quicker per line (focused edits)
  // Larger changes have more overhead (architecture, testing, integration)
  const sizeMultiplier: Record<string, number> = {
    XS: 0.7, // Small edits are efficient
    S: 0.85,
    M: 1.0, // Baseline
    L: 1.15, // More overhead
    XL: 1.3, // Significant overhead
  };

  const multiplier = sizeMultiplier[size] || 1.0;
  return Math.round(baseMinutes * multiplier);
}

/**
 * Query GitHub for closed PRs in the specified time range
 */
async function fetchClosedPRs(monthsBack: number): Promise<HistoricalPR[]> {
  const sinceDate = new Date();
  sinceDate.setMonth(sinceDate.getMonth() - monthsBack);
  const sinceISO = sinceDate.toISOString().split("T")[0];

  console.log(`Fetching closed PRs since ${sinceISO}...`);

  // Resolve owner/repo dynamically
  const repoFull = execSync("gh repo view --json nameWithOwner -q .nameWithOwner", {
    encoding: "utf-8",
  }).trim();
  const [repoOwner, repoName] = repoFull.split("/");

  // Query closed PRs with issue references
  const query = `is:pr is:merged merged:>=${sinceISO} sort:updated-desc`;
  const result = execSync(
    `gh pr list --search "${query}" --limit 200 --json number,title,mergedAt,additions,deletions,closedAt`,
    { encoding: "utf-8" }
  );

  const prs = JSON.parse(result);
  const historicalPRs: HistoricalPR[] = [];

  for (const pr of prs) {
    // Extract issue number from title (e.g., "[FEAT][#123]" or "#123")
    const issueMatch = pr.title.match(/#(\d+)/);
    if (!issueMatch) {
      continue; // Skip PRs without issue references
    }

    const issueNumber = parseInt(issueMatch[1], 10);

    // Fetch size: try project board field first (primary), fall back to label
    try {
      let sizeLabel: "XS" | "S" | "M" | "L" | "XL" | undefined;

      try {
        const graphqlResult = execSync(
          `gh api graphql -f query='query { repository(owner: "${repoOwner}", name: "${repoName}") { issue(number: ${issueNumber}) { projectItems(first: 5) { nodes { fieldValues(first: 15) { nodes { ... on ProjectV2ItemFieldSingleSelectValue { name field { ... on ProjectV2SingleSelectField { name } } } } } } } } }'`,
          { encoding: "utf-8" }
        );
        const gql = JSON.parse(graphqlResult);
        const fieldValues =
          gql.data?.repository?.issue?.projectItems?.nodes?.[0]?.fieldValues?.nodes ?? [];
        const sizeNode = fieldValues.find((n: any) => n?.field?.name === "Size");
        if (sizeNode?.name) {
          sizeLabel = sizeNode.name as "XS" | "S" | "M" | "L" | "XL";
        }
      } catch {
        // GraphQL failed — fall through to label fallback
      }

      if (!sizeLabel) {
        // Label fallback for historical issues that predate board field migration
        const issueData = execSync(`gh issue view ${issueNumber} --json labels`, {
          encoding: "utf-8",
        });
        const issue = JSON.parse(issueData);
        sizeLabel = issue.labels
          ?.find((l: any) => l.name.startsWith("size:"))
          ?.name.replace("size:", "") as "XS" | "S" | "M" | "L" | "XL" | undefined;
      }

      if (!sizeLabel) {
        continue; // Skip issues without size (neither board nor label)
      }

      const linesChanged = pr.additions + pr.deletions;
      const estimatedMinutes = estimateMinutesFromLines(linesChanged, sizeLabel);

      historicalPRs.push({
        number: pr.number,
        issueNumber,
        title: pr.title,
        sizeLabel,
        linesChanged,
        estimatedMinutes,
        mergedAt: pr.mergedAt,
      });
    } catch (error) {
      console.warn(
        `Skipping issue #${issueNumber}: ${error instanceof Error ? error.message : "Unknown error"}`
      );
      continue;
    }
  }

  return historicalPRs;
}

/**
 * Convert historical PRs to observations format
 */
function convertToObservations(prs: HistoricalPR[]): WorkTimeObservation[] {
  return prs.map((pr) => ({
    issue_number: pr.issueNumber,
    size: pr.sizeLabel,
    priority: null, // Historical data doesn't have priority
    task_type: null, // Historical data doesn't have task type
    actual_work_minutes: pr.estimatedMinutes, // Using lines-based estimate as "actual"
    estimated_minutes: pr.estimatedMinutes, // Same for now (no initial estimate in history)
    routing: "unknown",
    stages_completed: ["backfilled"], // Mark as backfilled
    timestamp: pr.mergedAt,
  }));
}

/**
 * Append observations to complexity-model.yaml
 */
async function appendToModel(
  observations: WorkTimeObservation[],
  modelPath: string
): Promise<void> {
  // Read existing model
  let model: any;
  try {
    const content = await fs.readFile(modelPath, "utf-8");
    model = yaml.load(content);
  } catch (error: any) {
    if (error.code === "ENOENT") {
      console.error("Error: .nightgauge/complexity-model.yaml not found");
      console.error("Run the pipeline at least once to create the model file.");
      process.exit(1);
    }
    throw error;
  }

  // Get or initialize work_time_feedback section
  if (!model.work_time_feedback) {
    model.work_time_feedback = {
      enabled: true,
      observations: [],
      size_averages: {},
    };
  }

  // Append new observations (keeping last 50 total)
  const existingObs = model.work_time_feedback.observations || [];
  const allObs = [...existingObs, ...observations];

  // Keep last 50
  model.work_time_feedback.observations = allObs.slice(-50);

  // Recalculate size averages
  model.work_time_feedback.size_averages = calculateSizeAverages(
    model.work_time_feedback.observations
  );

  // Update total observations count
  model.total_observations = (model.total_observations || 0) + observations.length;

  // Write back to file
  const yamlContent = yaml.dump(model, {
    lineWidth: 100,
    noRefs: true,
  });

  await fs.writeFile(modelPath, yamlContent, "utf-8");
}

/**
 * Calculate size averages from observations
 */
function calculateSizeAverages(observations: WorkTimeObservation[]): any {
  const sizeGroups: Record<string, number[]> = {};

  for (const obs of observations) {
    if (!obs.size || obs.actual_work_minutes <= 0) {
      continue;
    }

    if (!sizeGroups[obs.size]) {
      sizeGroups[obs.size] = [];
    }
    sizeGroups[obs.size].push(obs.actual_work_minutes);
  }

  const averages: any = {};
  for (const [size, times] of Object.entries(sizeGroups)) {
    if (times.length === 0) {
      continue;
    }

    const sum = times.reduce((a, b) => a + b, 0);
    const avg = sum / times.length;

    averages[size] = {
      estimated: Math.round(avg), // Use average as estimate
      actual_average: Math.round(avg),
      observation_count: times.length,
    };
  }

  return averages;
}

/**
 * Main execution
 */
async function main() {
  const args = process.argv.slice(2);
  const dryRun = args.includes("--dry-run");
  const monthsArg = args.find((arg) => arg.startsWith("--months="));
  const months = monthsArg ? parseInt(monthsArg.split("=")[1], 10) : 6;

  console.log("🔄 Backfilling Complexity Model");
  console.log(`   Months back: ${months}`);
  console.log(`   Dry run: ${dryRun ? "YES" : "NO"}`);
  console.log("");

  // Fetch historical PRs
  const prs = await fetchClosedPRs(months);
  console.log(`✓ Found ${prs.length} PRs with size labels\n`);

  if (prs.length === 0) {
    console.log("No historical data to backfill.");
    return;
  }

  // Show sample
  console.log("Sample observations:");
  prs.slice(0, 5).forEach((pr) => {
    console.log(
      `  #${pr.issueNumber} (${pr.sizeLabel}): ${pr.linesChanged} lines → ~${pr.estimatedMinutes} min`
    );
  });
  console.log("");

  // Convert to observations
  const observations = convertToObservations(prs);

  // Show size breakdown
  const sizeCounts: Record<string, number> = {};
  for (const obs of observations) {
    sizeCounts[obs.size] = (sizeCounts[obs.size] || 0) + 1;
  }
  console.log("Size distribution:");
  for (const [size, count] of Object.entries(sizeCounts).sort()) {
    console.log(`  ${size}: ${count} observations`);
  }
  console.log("");

  if (dryRun) {
    console.log("Dry run - no changes made.");
    return;
  }

  // Append to model
  const modelPath = ".nightgauge/complexity-model.yaml";
  await appendToModel(observations, modelPath);

  console.log(`✓ Appended ${observations.length} observations to ${modelPath}`);
  console.log("✓ Recalculated size averages");
  console.log("");
  console.log("Run a pipeline to see calibrated estimates in action!");
}

main().catch((error) => {
  console.error("Error:", error.message);
  process.exit(1);
});
