#!/usr/bin/env tsx
/**
 * Estimate total work time for an epic
 *
 * Usage:
 *   npm run estimate-epic 295
 *   tsx scripts/estimate-epic.ts 295
 */

import { EpicEstimator } from "../packages/nightgauge-sdk/src/services/EpicEstimator";

async function main() {
  const args = process.argv.slice(2);

  // Check for --json flag
  const jsonOutput = args.includes("--json");
  const filteredArgs = args.filter((arg) => arg !== "--json");

  if (filteredArgs.length === 0) {
    console.error("Usage: npm run estimate-epic <epic-number> [--json]");
    console.error("Example: npm run estimate-epic 295");
    console.error("         npm run estimate-epic 295 --json");
    process.exit(1);
  }

  const epicNumber = parseInt(filteredArgs[0], 10);

  if (isNaN(epicNumber) || epicNumber <= 0) {
    console.error(`Error: Invalid epic number "${filteredArgs[0]}"`);
    process.exit(1);
  }

  try {
    const estimator = new EpicEstimator();
    const estimate = await estimator.estimateEpic(epicNumber);

    if (jsonOutput) {
      // Output JSON for programmatic use
      console.log(JSON.stringify(estimate, null, 2));
    } else {
      // Output formatted text for humans
      const formatted = EpicEstimator.format(estimate);
      console.log(formatted);
    }
  } catch (error) {
    console.error("Error:", error instanceof Error ? error.message : String(error));
    process.exit(1);
  }
}

main();
