/**
 * ComplexityModelService - YAML file persistence with Zod validation
 *
 * Handles reading and writing the complexity model file.
 *
 * @see docs/ARCHITECTURE.md for design rationale
 * @see .nightgauge/complexity-model.yaml for file format
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as crypto from "node:crypto";
import * as yaml from "js-yaml";
import {
  ComplexityModelSchema,
  type ComplexityModel,
  type ComplexityPattern,
  type MatchedPattern,
  type PipelineOutcome,
} from "../context/schemas/complexity-model.js";

/**
 * Error thrown when model file validation fails
 */
export class ModelValidationError extends Error {
  constructor(
    public readonly filename: string,
    public readonly details: string
  ) {
    super(`Invalid complexity model: ${filename}\n${details}`);
    this.name = "ModelValidationError";
  }
}

/**
 * ComplexityModelService handles persistence and operations on the complexity model
 *
 * @example
 * ```typescript
 * const service = new ComplexityModelService('.nightgauge/complexity-model.yaml');
 *
 * // Load model
 * const model = await service.load();
 *
 * // Find matching patterns
 * const patterns = service.findMatchingPatterns('Add OAuth login', model);
 *
 * // Record outcome after merge
 * const updated = service.recordOutcome(model, outcome);
 * await service.save(updated);
 * ```
 */
export class ComplexityModelService {
  private modelPath: string;

  constructor(modelPath: string = ".nightgauge/complexity-model.yaml") {
    this.modelPath = modelPath;
  }

  /**
   * Load and validate the complexity model from YAML.
   *
   * If the model file does not exist, creates a bootstrap model with
   * universal baseline calibration data and saves it. This enables the
   * pipeline to run on new repos without manual model setup.
   *
   * @returns The validated complexity model
   * @throws {ModelValidationError} If validation fails
   * @see Issue #1316 - Bootstrap model for new repos
   */
  async load(): Promise<ComplexityModel> {
    // Auto-bootstrap if file doesn't exist (#1316)
    if (!(await this.exists())) {
      const bootstrap = ComplexityModelService.createBootstrapModel();
      await this.save(bootstrap);
      return bootstrap;
    }

    const content = await fs.readFile(this.modelPath, "utf-8");

    let data: unknown;
    try {
      data = yaml.load(content);
    } catch (error) {
      throw new ModelValidationError(
        this.modelPath,
        `Failed to parse YAML: ${error instanceof Error ? error.message : String(error)}`
      );
    }

    const result = ComplexityModelSchema.safeParse(data);
    if (!result.success) {
      throw new ModelValidationError(
        this.modelPath,
        result.error.issues.map((e) => `${e.path.join(".")}: ${e.message}`).join("\n")
      );
    }

    return result.data;
  }

  /**
   * Create a bootstrap complexity model with universal baseline data.
   *
   * Baseline calibration is derived from 347 observations across the
   * nightgauge repo. Type adjustments, size calibration, and
   * thresholds represent cross-project averages. Repo-specific patterns
   * are intentionally omitted — they will be learned organically.
   *
   * @returns A valid ComplexityModel ready for use on a new repo
   * @see Issue #1316 - Bootstrap model for new repos
   */
  static createBootstrapModel(): ComplexityModel {
    const today = new Date().toISOString().split("T")[0];
    return {
      schema_version: "1.0",
      last_updated: today,
      bootstrap_date: today,
      total_observations: 0,
      decay: {
        enabled: false,
        half_life_days: 30,
      },
      model_tracking: {
        current_default: "claude-sonnet-4-6",
        observations_by_model: {},
      },
      patterns: {
        high_complexity: [
          {
            match: "refactor|redesign|rewrite",
            modifier: 1.5,
            confidence: 0.45,
            rationale: "Refactoring/redesign typically requires touching many files",
            observations: 0,
          },
          {
            match: "migrate|migration",
            modifier: 1.3,
            confidence: 0.45,
            rationale: "Migration work spans analysis, planning, and execution layers",
            observations: 0,
          },
          {
            match: "multi.?repo|workspace|cross.?repo",
            modifier: 1.5,
            confidence: 0.5,
            rationale: "Multi-repo features require coordination across boundaries",
            observations: 0,
          },
        ],
        medium_complexity: [
          {
            match: "config|setting|option",
            modifier: 0,
            confidence: 0.5,
            rationale: "Configuration changes are moderate scope",
            observations: 0,
          },
          {
            match: "validation|schema|zod",
            modifier: 0,
            confidence: 0.57,
            rationale: "Schema/validation changes are moderate scope",
            observations: 0,
          },
        ],
        low_complexity: [
          {
            match: "typo|spelling|wording",
            modifier: -1,
            confidence: 0.7,
            rationale: "Typo/spelling fixes are minimal scope",
            observations: 0,
          },
          {
            match: "readme|changelog|documentation",
            modifier: -0.8,
            confidence: 0.65,
            rationale: "Documentation-only changes are small scope",
            observations: 0,
          },
          {
            match: "bump|upgrade|version",
            modifier: -0.5,
            confidence: 0.56,
            rationale: "Version bumps are typically small",
            observations: 0,
          },
        ],
      },
      size_calibration: {
        XS: {
          expected_lines: 50,
          actual_average_lines: 59,
          sample_count: 0,
        },
        S: {
          expected_lines: 150,
          actual_average_lines: 213,
          sample_count: 0,
        },
        M: {
          expected_lines: 500,
          actual_average_lines: 574,
          sample_count: 0,
        },
        L: {
          expected_lines: 1200,
          actual_average_lines: 1476,
          sample_count: 0,
        },
        XL: {
          expected_lines: 2500,
          actual_average_lines: 2352,
          sample_count: 0,
        },
      },
      type_adjustments: {
        feature: {
          modifier: -1.45,
          observations: 0,
          rationale:
            "Seeded from cross-repo baseline (45 observations show features over-predicted)",
        },
        bug: {
          modifier: -0.6,
          observations: 0,
          rationale: "Bugs tend toward smaller scope",
        },
        docs: {
          modifier: -0.7,
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
      priority_adjustments: {
        critical: {
          modifier: 0.2,
          rationale: "Critical issues often have broader scope",
          observations: 0,
        },
        high: {
          modifier: 0.1,
          rationale: "High priority slightly correlates with complexity",
          observations: 0,
        },
        medium: {
          modifier: 0,
          rationale: "Baseline priority",
          observations: 0,
        },
        low: {
          modifier: -0.1,
          rationale: "Low priority often simpler scope",
          observations: 0,
        },
      },
      lines_changed_thresholds: {
        XS: 100,
        S: 325,
        M: 850,
        L: 1850,
        XL: 2500,
      },
      learnings: [
        `${today}: Bootstrap model created with universal baseline calibration from cross-repo data.`,
      ],
      prediction_accuracy: {
        total_predictions: 0,
        correct_predictions: 0,
        by_type: {},
        by_size: {},
        recent_outcomes: [],
      },
      critical_files: {
        description:
          "Files whose modification significantly increases issue complexity. When referenced in technical notes, each critical file bumps the complexity score.",
        registry: [],
        per_file_modifier: 0.5,
        max_modifier: 1.5,
      },
    };
  }

  /**
   * Create a seeded complexity model from an existing repo's model.
   *
   * Copies universal calibration data (type_adjustments, size_calibration
   * averages, cross-project patterns) while zeroing out repo-specific data
   * (recent_outcomes, per-model counts, prediction accuracy).
   *
   * Repo-specific patterns (source: 'repo-specific') are filtered out.
   *
   * @param sourceModel The model from the source repo to seed from
   * @param sourcePath Optional path/identifier of the source (for provenance)
   * @returns A new ComplexityModel ready for use in a new repo
   * @see Issue #1323 - Cross-repo complexity model seeding
   */
  static seedFromModel(sourceModel: ComplexityModel, sourcePath?: string): ComplexityModel {
    const today = new Date().toISOString().split("T")[0];

    // Filter out repo-specific patterns; keep cross-project and bootstrap patterns
    const filterPatterns = (patterns: ComplexityPattern[]): ComplexityPattern[] =>
      patterns.filter((p) => p.source !== "repo-specific");

    // Reset sample_counts in size_calibration (keep the learned averages)
    const resetSizeCalibration = Object.fromEntries(
      Object.entries(sourceModel.size_calibration).map(([k, v]) => [k, { ...v, sample_count: 0 }])
    ) as ComplexityModel["size_calibration"];

    return {
      ...sourceModel,
      last_updated: today,
      bootstrap_date: today,
      seeded_from: sourcePath,
      total_observations: 0,
      model_tracking: {
        ...sourceModel.model_tracking,
        observations_by_model: {},
      },
      patterns: {
        high_complexity: filterPatterns(sourceModel.patterns.high_complexity),
        medium_complexity: filterPatterns(sourceModel.patterns.medium_complexity),
        low_complexity: filterPatterns(sourceModel.patterns.low_complexity),
      },
      size_calibration: resetSizeCalibration,
      learnings: [
        `${today}: Model seeded from cross-repo baseline${sourcePath ? `: ${sourcePath}` : ""}.`,
      ],
      prediction_accuracy: {
        total_predictions: 0,
        correct_predictions: 0,
        by_type: {},
        by_size: {},
        recent_outcomes: [],
      },
    };
  }

  /**
   * Check if the model file exists
   */
  async exists(): Promise<boolean> {
    try {
      await fs.access(this.modelPath);
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Save the complexity model to YAML with atomic write
   *
   * @param model The model to save
   */
  async save(model: ComplexityModel): Promise<void> {
    // Validate before saving
    const result = ComplexityModelSchema.safeParse(model);
    if (!result.success) {
      throw new ModelValidationError(
        this.modelPath,
        result.error.issues.map((e) => `${e.path.join(".")}: ${e.message}`).join("\n")
      );
    }

    // Update last_updated timestamp
    const modelToSave = {
      ...result.data,
      last_updated: new Date().toISOString().split("T")[0],
    };

    const content = yaml.dump(modelToSave, {
      lineWidth: 100,
      noRefs: true,
      quoteStyle: "double",
    });

    // Atomic write: write to temp file, copy to target, verify, then clean up
    const dir = path.dirname(this.modelPath);
    await fs.mkdir(dir, { recursive: true });

    const tempPath = path.join(dir, `.complexity-model-${crypto.randomUUID()}.yaml.tmp`);
    try {
      await fs.writeFile(tempPath, content, "utf-8");
      await fs.copyFile(tempPath, this.modelPath);
    } catch (error) {
      await fs.unlink(tempPath).catch(() => {});
      throw error;
    }

    // Post-write verification: read back and validate
    const verifyError = await this.verifyWrittenFile();
    if (verifyError) {
      // Attempt restore from temp file (known-good copy)
      try {
        await fs.rename(tempPath, this.modelPath);
      } catch (restoreError) {
        throw new ModelValidationError(
          this.modelPath,
          `Post-write verification failed: ${verifyError}. ` +
            `Restore from temp also failed: ${restoreError instanceof Error ? restoreError.message : String(restoreError)}`
        );
      }
      throw new ModelValidationError(
        this.modelPath,
        `Post-write verification failed (restored from temp): ${verifyError}`
      );
    }

    // Verification passed — clean up temp file
    await fs.unlink(tempPath).catch(() => {});
  }

  /**
   * Find all patterns that match the given text
   *
   * @param text The text to search (typically issue title + description)
   * @param model The complexity model to search
   * @returns Array of matched patterns with their categories
   */
  findMatchingPatterns(text: string, model: ComplexityModel): MatchedPattern[] {
    const matches: MatchedPattern[] = [];
    const normalizedText = text.toLowerCase();

    const searchCategory = (
      patterns: ComplexityPattern[],
      category: "high_complexity" | "medium_complexity" | "low_complexity"
    ) => {
      for (const pattern of patterns) {
        try {
          const regex = new RegExp(pattern.match, "i");
          const match = normalizedText.match(regex);
          if (match) {
            matches.push({
              pattern,
              category,
              matched_text: match[0],
            });
          }
        } catch {
          // Invalid regex - try simple string match
          if (normalizedText.includes(pattern.match.toLowerCase())) {
            matches.push({
              pattern,
              category,
              matched_text: pattern.match,
            });
          }
        }
      }
    };

    searchCategory(model.patterns.high_complexity, "high_complexity");
    searchCategory(model.patterns.medium_complexity, "medium_complexity");
    searchCategory(model.patterns.low_complexity, "low_complexity");

    return matches;
  }

  /**
   * Record a pipeline outcome to update the model
   *
   * Updates:
   * - total_observations count
   * - size_calibration sample counts and averages
   * - model_tracking observations by model
   *
   * @param model The current complexity model
   * @param outcome The pipeline outcome to record
   * @returns Updated complexity model
   */
  recordOutcome(model: ComplexityModel, outcome: PipelineOutcome): ComplexityModel {
    const sizeKey = outcome.size_label;

    // Use batch-attributed lines_changed when available (Issue #805)
    const linesChanged = outcome.batch_context?.attributed_lines_changed ?? outcome.lines_changed;

    // Update size calibration
    const currentCalibration = model.size_calibration[sizeKey];
    const newSampleCount = currentCalibration.sample_count + 1;

    // Calculate new running average
    const newActualAverage =
      (currentCalibration.actual_average_lines * currentCalibration.sample_count + linesChanged) /
      newSampleCount;

    const updatedSizeCalibration = {
      ...model.size_calibration,
      [sizeKey]: {
        ...currentCalibration,
        actual_average_lines: Math.round(newActualAverage),
        sample_count: newSampleCount,
      },
    };

    // Update model tracking
    const currentModelCount = model.model_tracking.observations_by_model[outcome.model_id] || 0;
    const updatedModelTracking = {
      ...model.model_tracking,
      observations_by_model: {
        ...model.model_tracking.observations_by_model,
        [outcome.model_id]: currentModelCount + 1,
      },
    };

    return {
      ...model,
      total_observations: model.total_observations + 1,
      size_calibration: updatedSizeCalibration,
      model_tracking: updatedModelTracking,
    };
  }

  /**
   * Check if an outcome for this issue has already been recorded.
   *
   * Uses issue_number as dedup key in prediction_accuracy.recent_outcomes.
   *
   * @see Issue #650 - Feedback Loop: Record Execution Outcomes
   */
  isOutcomeRecorded(model: ComplexityModel, issueNumber: number): boolean {
    return (model.prediction_accuracy?.recent_outcomes ?? []).some(
      (o) => o.issue_number === issueNumber
    );
  }

  /**
   * Verify the written file by reading it back and validating against the schema.
   *
   * @returns Error description string if verification failed, or null if valid
   */
  protected async verifyWrittenFile(): Promise<string | null> {
    try {
      const content = await fs.readFile(this.modelPath, "utf-8");
      const data = yaml.load(content);
      const result = ComplexityModelSchema.safeParse(data);
      if (!result.success) {
        return result.error.issues.map((e) => `${e.path.join(".")}: ${e.message}`).join("; ");
      }
      return null;
    } catch (error) {
      return error instanceof Error ? error.message : String(error);
    }
  }

  /**
   * Get the model file path
   */
  getModelPath(): string {
    return this.modelPath;
  }
}
