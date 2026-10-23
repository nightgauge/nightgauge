/**
 * SkillLoader — Encapsulate skill file resolution and loading logic
 *
 * Provides a typed interface for locating and reading SKILL.md files
 * for pipeline stages. Delegates path resolution to the existing
 * `findSkillFile()` utility so the resolution algorithm stays in one place.
 *
 * @see Issue #2770 — HeadlessOrchestrator decomposition (Part 3)
 */

import * as fs from "fs";
import type { PipelineStage } from "@nightgauge/sdk";
import type { Logger } from "../../utils/logger";
import { findSkillFile } from "../../utils/skillRunner";

/**
 * Result returned by SkillLoader.loadSkillContent().
 */
export interface SkillLoadResult {
  /** Raw SKILL.md content */
  content: string;
  /** Absolute path to the skill file on disk */
  path: string;
  /** Source of the skill (currently always 'local'; 'platform' reserved for future paid tiers) */
  source: "local";
}

/**
 * SkillLoader — resolves and reads SKILL.md files for pipeline stages.
 *
 * Responsibilities:
 * - Locate SKILL.md on the local filesystem via `findSkillFile()`
 * - Read the file content and return it for use in skill invocation
 * - Log warnings when files cannot be found or read
 *
 * Future: when platform skill serving is supported, this class will add
 * a `source: 'platform'` code path and handle the API call transparently.
 */
export class SkillLoader {
  constructor(private logger: Logger) {}

  /**
   * Resolve the local filesystem path to the SKILL.md file for a given stage.
   *
   * Returns null if no skill file can be located, which triggers graceful
   * fallback in the caller (e.g., schema repair skips if skill not found).
   *
   * @param stage - Pipeline stage identifier (e.g., "feature-dev")
   * @returns Absolute path to the skill file, or null if not found
   */
  resolveLocalSkillPath(stage: PipelineStage): string | null {
    return findSkillFile(stage);
  }

  /**
   * Resolve and read the skill content for a given stage.
   *
   * Returns null on any failure (file not found, read error) so callers
   * can handle missing skills without throwing.
   *
   * @param stage - Pipeline stage identifier
   * @returns Skill content and path, or null if unavailable
   */
  loadSkillContent(stage: PipelineStage): SkillLoadResult | null {
    const skillPath = this.resolveLocalSkillPath(stage);
    if (!skillPath) {
      this.logger.warn("SkillLoader: skill file not found", { stage });
      return null;
    }

    try {
      const content = fs.readFileSync(skillPath, "utf-8");
      return { content, path: skillPath, source: "local" };
    } catch (err) {
      this.logger.warn("SkillLoader: failed to read skill file", {
        stage,
        skillPath,
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }
}
