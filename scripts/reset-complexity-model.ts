#!/usr/bin/env tsx
/**
 * Reset Complexity Model — Clean polluted calibration data and seed baselines
 *
 * Resets polluted data from complexity-model.yaml caused by #1042 bug
 * (predicted_size always defaulting to M). Preserves legitimate metadata
 * (total_observations, model_tracking, decay config) while resetting
 * calibration data and seeding initial patterns/adjustments.
 *
 * Usage:
 *   npx tsx scripts/reset-complexity-model.ts
 *   npx tsx scripts/reset-complexity-model.ts --dry-run
 *
 * @see Issue #1060 - Reset polluted complexity model calibration data
 * @see Issue #1042 - Root cause fix for predicted_size defaulting to M
 */

import { ComplexityModelService } from "../packages/nightgauge-sdk/src/services/ComplexityModelService.js";
import type { ComplexityModel } from "../packages/nightgauge-sdk/src/context/schemas/complexity-model.js";

const MODEL_PATH = ".nightgauge/complexity-model.yaml";

function parseArgs(): { dryRun: boolean } {
  return { dryRun: process.argv.includes("--dry-run") };
}

function resetModel(model: ComplexityModel): ComplexityModel {
  const reset: ComplexityModel = {
    ...model,

    // --- Reset size_calibration ---
    // Zero out sample counts and reset actual_average_lines to expected_lines
    size_calibration: {
      XS: {
        expected_lines: model.size_calibration.XS.expected_lines,
        actual_average_lines: model.size_calibration.XS.expected_lines,
        sample_count: 0,
        ...(model.size_calibration.XS.expected_minutes !== undefined && {
          expected_minutes: model.size_calibration.XS.expected_minutes,
        }),
      },
      S: {
        expected_lines: model.size_calibration.S.expected_lines,
        actual_average_lines: model.size_calibration.S.expected_lines,
        sample_count: 0,
        ...(model.size_calibration.S.expected_minutes !== undefined && {
          expected_minutes: model.size_calibration.S.expected_minutes,
        }),
      },
      M: {
        expected_lines: model.size_calibration.M.expected_lines,
        actual_average_lines: model.size_calibration.M.expected_lines,
        sample_count: 0,
        ...(model.size_calibration.M.expected_minutes !== undefined && {
          expected_minutes: model.size_calibration.M.expected_minutes,
        }),
      },
      L: {
        expected_lines: model.size_calibration.L.expected_lines,
        actual_average_lines: model.size_calibration.L.expected_lines,
        sample_count: 0,
        ...(model.size_calibration.L.expected_minutes !== undefined && {
          expected_minutes: model.size_calibration.L.expected_minutes,
        }),
      },
      XL: {
        expected_lines: model.size_calibration.XL.expected_lines,
        actual_average_lines: model.size_calibration.XL.expected_lines,
        sample_count: 0,
        ...(model.size_calibration.XL.expected_minutes !== undefined && {
          expected_minutes: model.size_calibration.XL.expected_minutes,
        }),
      },
    },

    // --- Reset prediction_accuracy ---
    prediction_accuracy: {
      total_predictions: 0,
      correct_predictions: 0,
      by_type: {},
      by_size: {},
      recent_outcomes: [],
    },

    // --- Seed patterns ---
    patterns: {
      high_complexity: [
        {
          match: "refactor|redesign|rewrite",
          modifier: 1.5,
          confidence: 0.5,
          rationale: "Refactoring/redesign typically requires touching many files",
          observations: 0,
        },
        {
          match: "pipeline|orchestrat",
          modifier: 1.3,
          confidence: 0.5,
          rationale: "Pipeline/orchestration changes span multiple layers",
          observations: 0,
        },
        {
          match: "multi.?repo|workspace|cross.?repo",
          modifier: 1.5,
          confidence: 0.5,
          rationale: "Multi-repo features require coordination across boundaries",
          observations: 0,
        },
        {
          match: "batch|parallel|concurrent",
          modifier: 1.3,
          confidence: 0.5,
          rationale: "Batch/parallel execution adds complexity for coordination",
          observations: 0,
        },
      ],
      medium_complexity: [
        {
          match: "config|setting|option",
          modifier: 0.0,
          confidence: 0.5,
          rationale: "Configuration changes are moderate scope",
          observations: 0,
        },
        {
          match: "dashboard|tree.?view|sidebar",
          modifier: 0.0,
          confidence: 0.5,
          rationale: "UI component changes are moderate scope",
          observations: 0,
        },
        {
          match: "validation|schema|zod",
          modifier: 0.0,
          confidence: 0.5,
          rationale: "Schema/validation changes are moderate scope",
          observations: 0,
        },
      ],
      low_complexity: [
        {
          match: "typo|spelling|wording",
          modifier: -1.0,
          confidence: 0.7,
          rationale: "Typo/spelling fixes are minimal scope",
          observations: 0,
        },
        {
          match: "readme|changelog|documentation",
          modifier: -0.8,
          confidence: 0.6,
          rationale: "Documentation-only changes are small scope",
          observations: 0,
        },
        {
          match: "bump|upgrade|version",
          modifier: -0.5,
          confidence: 0.5,
          rationale: "Version bumps are typically small",
          observations: 0,
        },
        {
          match: "comment|annotation|jsdoc",
          modifier: -0.8,
          confidence: 0.6,
          rationale: "Comment/annotation changes are minimal scope",
          observations: 0,
        },
      ],
    },

    // --- Seed type_adjustments ---
    type_adjustments: {
      feature: {
        modifier: 0.0,
        observations: 0,
        rationale: "Base type — no adjustment",
      },
      bug: {
        modifier: -0.2,
        observations: 0,
        rationale: "Bugs tend toward smaller scope",
      },
      docs: {
        modifier: -0.5,
        observations: 0,
        rationale: "Documentation changes are typically smaller",
      },
      refactor: {
        modifier: 0.3,
        observations: 0,
        rationale: "Refactors tend to touch more files",
      },
      chore: {
        modifier: -0.3,
        observations: 0,
        rationale: "Chores are typically small maintenance",
      },
    },

    // --- Seed priority_adjustments ---
    priority_adjustments: {
      critical: {
        modifier: 0.2,
        observations: 0,
        rationale: "Critical issues often have broader scope",
      },
      high: {
        modifier: 0.1,
        observations: 0,
        rationale: "High priority slightly correlates with complexity",
      },
      medium: {
        modifier: 0.0,
        observations: 0,
        rationale: "Baseline priority",
      },
      low: {
        modifier: -0.1,
        observations: 0,
        rationale: "Low priority often simpler scope",
      },
    },

    // --- Add learning entry ---
    learnings: [
      ...model.learnings,
      "2026-02-21: Reset calibration data after #1042 fix. Prior data polluted by predicted_size always defaulting to M. Seeded initial patterns and type adjustments.",
    ],
  };

  return reset;
}

async function main(): Promise<void> {
  const { dryRun } = parseArgs();
  const service = new ComplexityModelService(MODEL_PATH);

  console.log(`Loading complexity model from ${MODEL_PATH}...`);
  const model = await service.load();
  console.log(`Model loaded: ${model.total_observations} total observations`);

  // Log pre-reset state
  console.log("\n=== Pre-Reset State ===");
  console.log(
    `Size calibration M: ${model.size_calibration.M.sample_count} samples, avg ${model.size_calibration.M.actual_average_lines} lines`
  );
  console.log(
    `Patterns: high=${model.patterns.high_complexity.length}, medium=${model.patterns.medium_complexity.length}, low=${model.patterns.low_complexity.length}`
  );
  console.log(`Type adjustments: ${Object.keys(model.type_adjustments).length} entries`);
  console.log(`Priority adjustments: ${Object.keys(model.priority_adjustments).length} entries`);
  console.log(
    `Prediction accuracy: ${model.prediction_accuracy?.correct_predictions ?? 0}/${model.prediction_accuracy?.total_predictions ?? 0}`
  );

  // Apply reset
  console.log("\n=== Applying Reset ===");
  const resetted = resetModel(model);

  // Recalibrate thresholds from clean expected values
  const recalibrated = service.recalibrateThresholds(resetted);

  // Log post-reset state
  console.log("\n=== Post-Reset State ===");
  console.log(
    `Size calibration M: ${recalibrated.size_calibration.M.sample_count} samples, avg ${recalibrated.size_calibration.M.actual_average_lines} lines`
  );
  console.log(
    `Patterns: high=${recalibrated.patterns.high_complexity.length}, medium=${recalibrated.patterns.medium_complexity.length}, low=${recalibrated.patterns.low_complexity.length}`
  );
  console.log(`Type adjustments: ${Object.keys(recalibrated.type_adjustments).length} entries`);
  console.log(
    `Priority adjustments: ${Object.keys(recalibrated.priority_adjustments).length} entries`
  );
  console.log(
    `Prediction accuracy: ${recalibrated.prediction_accuracy?.correct_predictions ?? 0}/${recalibrated.prediction_accuracy?.total_predictions ?? 0}`
  );
  console.log(
    `Lines changed thresholds: XS=${recalibrated.lines_changed_thresholds.XS}, S=${recalibrated.lines_changed_thresholds.S}, M=${recalibrated.lines_changed_thresholds.M}, L=${recalibrated.lines_changed_thresholds.L}, XL=${recalibrated.lines_changed_thresholds.XL}`
  );
  console.log(`Learnings: ${recalibrated.learnings.length} entries`);

  // Preserved fields
  console.log("\n=== Preserved Fields ===");
  console.log(`total_observations: ${recalibrated.total_observations} (unchanged)`);
  console.log(
    `model_tracking: ${JSON.stringify(recalibrated.model_tracking.observations_by_model)}`
  );
  console.log(
    `decay: enabled=${recalibrated.decay.enabled}, half_life=${recalibrated.decay.half_life_days}d`
  );
  console.log(`bootstrap_date: ${recalibrated.bootstrap_date}`);

  if (dryRun) {
    console.log("\n[DRY RUN] No changes written to disk.");
    return;
  }

  // Save with atomic write and Zod validation
  await service.save(recalibrated);
  console.log(`\nModel saved to ${MODEL_PATH}`);
  console.log("Reset complete. Run audit script to verify:");
  console.log(
    "  npx tsx scripts/audit-complexity-model.ts --output reports/complexity-model-audit-post-reset.md"
  );
}

main().catch((err) => {
  console.error("Fatal error:", err);
  process.exit(1);
});
