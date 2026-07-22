/**
 * SuggestionEngine - Deterministic size suggestion based on complexity model
 *
 * Generates size suggestions by:
 * 1. Computing base score from issue type
 * 2. Applying priority adjustment
 * 3. Matching patterns and applying modifiers
 * 4. Mapping final score to size label
 * 5. Computing confidence based on observation counts
 *
 * This is a DETERMINISTIC operation - no AI interpretation needed.
 *
 * @see docs/ARCHITECTURE.md for Deterministic vs Probabilistic design
 */

import { ComplexityModelService } from "./ComplexityModelService.js";
import type {
  ComplexityModel,
  MatchedPattern,
  SizeSuggestion,
} from "../context/schemas/complexity-model.js";

/**
 * Issue type for suggestion input
 */
export type IssueType = "feature" | "bug" | "docs" | "refactor" | "chore" | "epic";

/**
 * Priority level for suggestion input
 */
export type Priority = "critical" | "high" | "medium" | "low";

/**
 * Size label output
 */
export type SizeLabel = "XS" | "S" | "M" | "L" | "XL";

/**
 * Optional scoring signals from issue metadata
 *
 * These additive modifiers are applied after pattern matching and before
 * scoreToSize() to improve size prediction accuracy for issues with
 * measurable scope indicators.
 *
 * @see Issue #1204
 */
export interface ScoringSignals {
  /** Number of acceptance criteria from issue context */
  acceptanceCriteriaCount?: number;
  /** Word count of the full issue body */
  bodyWordCount?: number;
  /** GitHub size label if present (e.g., 'L', 'XL') */
  sizeLabel?: SizeLabel;
  /** File paths referenced in technical notes (extracted by caller) */
  filesReferenced?: string[];
  /** Count of critical-registry files referenced in technical notes */
  criticalFilesReferenced?: number;
}

/**
 * SuggestionEngine generates size suggestions based on the complexity model
 *
 * @example
 * ```typescript
 * const modelService = new ComplexityModelService();
 * const engine = new SuggestionEngine(modelService);
 *
 * const suggestion = await engine.generateSuggestion(
 *   'Add OAuth2 login flow',
 *   'Implement OAuth2 authentication with Google and GitHub providers',
 *   'feature',
 *   'high'
 * );
 *
 * console.log(suggestion);
 * // { size: 'M', confidence: 0.78, rationale: '...', matched_patterns: ['OAuth', 'authentication'] }
 * ```
 */
export class SuggestionEngine {
  private modelService: ComplexityModelService;

  constructor(modelService: ComplexityModelService) {
    this.modelService = modelService;
  }

  /**
   * Generate a size suggestion for an issue
   *
   * @param title Issue title
   * @param description Issue description
   * @param type Issue type (feature, bug, etc.)
   * @param priority Issue priority (critical, high, medium, low)
   * @returns Size suggestion with confidence and rationale
   */
  async generateSuggestion(
    title: string,
    description: string,
    type: IssueType,
    priority: Priority,
    signals?: ScoringSignals
  ): Promise<SizeSuggestion> {
    const model = await this.modelService.load();

    // Combine title and description for pattern matching
    const combinedText = `${title} ${description}`;

    // Find matching patterns
    const matchedPatterns = this.modelService.findMatchingPatterns(combinedText, model);

    // Compute base score from type
    const baseScore = this.computeBaseScore(type, model);

    // Apply priority adjustment
    const priorityAdjustedScore = this.applyPriorityAdjustment(baseScore, priority, model);

    // Apply pattern modifiers
    let finalScore = this.applyPatternModifiers(priorityAdjustedScore, matchedPatterns);

    // Apply scoring signals (Issue #1204, #1309)
    finalScore = this.applySignals(finalScore, signals, model);

    // Map score to size
    const size = this.scoreToSize(finalScore, model);

    // Compute confidence
    const confidence = this.computeConfidence(matchedPatterns, type, priority, model);

    // Build rationale
    const rationale = this.buildRationale(type, priority, matchedPatterns, size, confidence, model);

    return {
      size,
      confidence,
      rationale,
      matched_patterns: matchedPatterns.map((m) => m.pattern.match),
    };
  }

  /**
   * Compute base score from issue type
   *
   * Score starts at 0 (baseline for M-sized issues)
   * Type adjustments move the score up or down
   */
  private computeBaseScore(type: IssueType, model: ComplexityModel): number {
    const adjustment = model.type_adjustments[type];
    if (adjustment) {
      return adjustment.modifier;
    }
    return 0; // Default to baseline
  }

  /**
   * Apply priority adjustment to score
   */
  private applyPriorityAdjustment(
    score: number,
    priority: Priority,
    model: ComplexityModel
  ): number {
    const adjustment = model.priority_adjustments[priority];
    if (adjustment) {
      return score + adjustment.modifier;
    }
    return score;
  }

  /**
   * Apply pattern modifiers to score
   *
   * Pattern modifiers are weighted by their confidence scores
   */
  private applyPatternModifiers(score: number, patterns: MatchedPattern[]): number {
    let adjustedScore = score;

    for (const match of patterns) {
      // Weight the modifier by the pattern's confidence
      const weightedModifier = match.pattern.modifier * match.pattern.confidence;
      adjustedScore += weightedModifier;
    }

    return adjustedScore;
  }

  /**
   * Apply scoring signals to the score (Issue #1204, #1309)
   *
   * Signals are additive modifiers applied after pattern matching.
   * When signals are absent or undefined, +0 is applied (backward-compatible).
   */
  private applySignals(score: number, signals?: ScoringSignals, model?: ComplexityModel): number {
    if (!signals) return score;

    let adjusted = score;
    adjusted = this.applyAcceptanceCriteriaSignal(adjusted, signals.acceptanceCriteriaCount);
    adjusted = this.applyBodyWordCountSignal(adjusted, signals.bodyWordCount);
    adjusted = this.applySizeLabelSignal(adjusted, signals.sizeLabel);
    adjusted = this.applyFilesReferencedSignal(adjusted, signals.filesReferenced);
    adjusted = this.applyCriticalFilesSignal(adjusted, signals.criticalFilesReferenced, model);
    return adjusted;
  }

  /**
   * Acceptance criteria count signal
   *
   * More acceptance criteria implies larger scope.
   * Thresholds: 0-3 → +0, 4-7 → +0.3, 8-12 → +0.8, 13+ → +1.5
   */
  private applyAcceptanceCriteriaSignal(score: number, count?: number): number {
    if (count === undefined || count <= 3) return score;
    if (count <= 7) return score + 0.3;
    if (count <= 12) return score + 0.8;
    return score + 1.5;
  }

  /**
   * Issue body word count signal
   *
   * Longer issue bodies correlate with more detailed specs and larger scope.
   * Thresholds: <100 → +0, 100-300 → +0.2, 300-800 → +0.5, 800+ → +1.0
   */
  private applyBodyWordCountSignal(score: number, wordCount?: number): number {
    if (wordCount === undefined || wordCount < 100) return score;
    if (wordCount < 300) return score + 0.2;
    if (wordCount < 800) return score + 0.5;
    return score + 1.0;
  }

  /**
   * GitHub size label signal
   *
   * Human-labeled size serves as a calibration hint.
   * Only L and XL labels add a modifier (+0.5).
   */
  private applySizeLabelSignal(score: number, sizeLabel?: SizeLabel): number {
    if (sizeLabel === "L" || sizeLabel === "XL") return score + 0.5;
    return score;
  }

  /**
   * Files referenced signal (Issue #1309)
   *
   * Multi-service change detection based on file path diversity.
   * Thresholds:
   * - undefined or empty → +0
   * - 1-2 files → +0 (normal scope)
   * - 3-4 files across 3+ unique directories → +0.5
   * - 5+ files → +1.0
   */
  private applyFilesReferencedSignal(score: number, filesReferenced?: string[]): number {
    if (!filesReferenced || filesReferenced.length === 0) return score;

    if (filesReferenced.length >= 5) return score + 1.0;

    if (filesReferenced.length >= 3) {
      const uniqueDirs = new Set(
        filesReferenced.map((f) => {
          const parts = f.split("/");
          return parts.length > 1 ? parts[parts.length - 2] : parts[0];
        })
      );
      if (uniqueDirs.size >= 3) return score + 0.5;
    }

    return score;
  }

  /**
   * Critical files signal (Issue #1309)
   *
   * Boost score when critical infrastructure files are referenced.
   * Uses model.critical_files registry when available, falls back
   * to hardcoded defaults (+0.5 per file, max +1.5).
   */
  private applyCriticalFilesSignal(
    score: number,
    criticalFilesReferenced?: number,
    model?: ComplexityModel
  ): number {
    if (criticalFilesReferenced === undefined || criticalFilesReferenced <= 0) {
      return score;
    }

    const perFile = model?.critical_files?.per_file_modifier ?? 0.5;
    const maxMod = model?.critical_files?.max_modifier ?? 1.5;
    const modifier = Math.min(criticalFilesReferenced * perFile, maxMod);
    return score + modifier;
  }

  /**
   * Map final score to size label
   *
   * Score thresholds:
   * - XS: score < -1.5
   * - S: -1.5 <= score < -0.5
   * - M: -0.5 <= score < 0.5
   * - L: 0.5 <= score < 1.5
   * - XL: score >= 1.5
   */
  private scoreToSize(score: number, _model: ComplexityModel): SizeLabel {
    if (score < -1.5) return "XS";
    if (score < -0.5) return "S";
    if (score < 0.5) return "M";
    if (score < 1.5) return "L";
    return "XL";
  }

  /**
   * Compute confidence score for the suggestion
   *
   * Confidence is based on:
   * 1. Number and quality of matched patterns
   * 2. Observation counts for type and priority
   * 3. Size calibration sample count
   */
  private computeConfidence(
    patterns: MatchedPattern[],
    type: IssueType,
    priority: Priority,
    model: ComplexityModel
  ): number {
    let confidence = 0.5; // Base confidence

    // Boost from matched patterns
    if (patterns.length > 0) {
      const avgPatternConfidence =
        patterns.reduce((sum, p) => sum + p.pattern.confidence, 0) / patterns.length;
      confidence += avgPatternConfidence * 0.2;
    }

    // Boost from type observations
    const typeAdjustment = model.type_adjustments[type];
    if (typeAdjustment && typeAdjustment.observations > 10) {
      confidence += 0.1;
    }

    // Boost from priority observations
    const priorityAdjustment = model.priority_adjustments[priority];
    if (priorityAdjustment && priorityAdjustment.observations > 10) {
      confidence += 0.05;
    }

    // Boost from total observations
    if (model.total_observations > 30) {
      confidence += 0.1;
    }

    // Cap confidence at 0.95 (never 100% certain)
    return Math.min(0.95, Math.max(0.3, confidence));
  }

  /**
   * Build human-readable rationale for the suggestion
   */
  private buildRationale(
    type: IssueType,
    priority: Priority,
    patterns: MatchedPattern[],
    size: SizeLabel,
    confidence: number,
    model: ComplexityModel
  ): string {
    const parts: string[] = [];

    // Base explanation
    parts.push(`Suggested ${size} based on ${type} issue with ${priority} priority.`);

    // Pattern matches
    if (patterns.length > 0) {
      const patternDescriptions = patterns
        .slice(0, 3) // Limit to top 3
        .map((p) => {
          const direction = p.pattern.modifier > 0 ? "↑" : "↓";
          return `"${p.pattern.match}" ${direction}`;
        });
      parts.push(`Matched patterns: ${patternDescriptions.join(", ")}.`);
    }

    // Calibration note
    const calibration = model.size_calibration[size];
    if (calibration.sample_count > 0) {
      parts.push(
        `Based on ${calibration.sample_count} previous ${size}-sized issues averaging ${calibration.actual_average_lines} lines.`
      );
    }

    // Confidence note
    if (confidence >= 0.8) {
      parts.push("High confidence due to multiple matching patterns and historical data.");
    } else if (confidence < 0.5) {
      parts.push("Lower confidence - consider reviewing the estimate manually.");
    }

    return parts.join(" ");
  }

  /**
   * Generate a suggestion without loading the model (for testing)
   */
  generateSuggestionFromModel(
    title: string,
    description: string,
    type: IssueType,
    priority: Priority,
    model: ComplexityModel,
    signals?: ScoringSignals
  ): SizeSuggestion {
    const combinedText = `${title} ${description}`;
    const matchedPatterns = this.modelService.findMatchingPatterns(combinedText, model);

    const baseScore = this.computeBaseScore(type, model);
    const priorityAdjustedScore = this.applyPriorityAdjustment(baseScore, priority, model);
    let finalScore = this.applyPatternModifiers(priorityAdjustedScore, matchedPatterns);
    finalScore = this.applySignals(finalScore, signals, model);
    const size = this.scoreToSize(finalScore, model);
    const confidence = this.computeConfidence(matchedPatterns, type, priority, model);
    const rationale = this.buildRationale(type, priority, matchedPatterns, size, confidence, model);

    return {
      size,
      confidence,
      rationale,
      matched_patterns: matchedPatterns.map((m) => m.pattern.match),
    };
  }
}
