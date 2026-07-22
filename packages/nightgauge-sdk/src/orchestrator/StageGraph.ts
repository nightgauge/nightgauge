/**
 * StageGraph — Pipeline producer/consumer graph derived from skill manifests.
 *
 * The Recovery Dialog (Issue #3239) needs to answer "which stage produces
 * this missing context file?" deterministically — without hardcoding the
 * pipeline shape. Skill SKILL.md frontmatters declare structured `inputs:`
 * and `outputs:` arrays; this module parses them at startup and exposes
 * `getProducingStage(missingFile)` keyed on file-path patterns.
 *
 * `{N}` in a path acts as the issue-number wildcard. Lookups substitute
 * any digit run in the input path with `{N}` before matching, so
 * `.nightgauge/pipeline/planning-42.json` matches the manifest
 * pattern `.nightgauge/pipeline/planning-{N}.json`.
 *
 * @see ADR-003 in .nightgauge/knowledge/features/3239-pipeline-error-ux-surface-recovery-actions-when-pi/decisions.md
 */

import type { PipelineStage } from "../events/EventBus.js";

/**
 * Parsed structured I/O from a single skill manifest's YAML frontmatter.
 */
export interface StageManifest {
  /** Stage identifier (e.g., "feature-planning"). */
  stage: PipelineStage;
  /** Input file patterns this stage reads. May contain `{N}` wildcards. */
  inputs: string[];
  /** Output file patterns this stage writes. May contain `{N}` wildcards. */
  outputs: string[];
}

/**
 * Result of `StageGraph.getProducingStage`. `name` is a human-readable
 * label suitable for dialog rendering (e.g., "Feature Planning").
 */
export interface StageProducer {
  stage: PipelineStage;
  name: string;
}

/**
 * Parsed manifest with a normalized pattern set. Internal — exported for
 * tests so they can assert manifest parsing without spinning up a full
 * StageGraph.
 */
export interface ParsedStageManifest extends StageManifest {
  /** Output patterns with file-system globs collapsed to `{N}` wildcards. */
  normalizedOutputs: string[];
}

const STAGE_DISPLAY_NAMES: Record<string, string> = {
  "issue-pickup": "Issue Pickup",
  "feature-planning": "Feature Planning",
  "feature-dev": "Feature Development",
  "feature-validate": "Feature Validation",
  "pr-create": "PR Create",
  "pr-merge": "PR Merge",
};

/**
 * Fallback producer table used when skill manifests are unreachable
 * (sandboxes, tests, partial installs). Mirrors the `STAGE_INPUT_PREREQUISITES`
 * literal in `ContextAssembler.ts`. CI guards against drift between this
 * fallback and the parsed manifests via `StageGraph.test.ts`.
 */
export const DEV_FALLBACK_PRODUCERS: ReadonlyArray<{
  pattern: string;
  stage: PipelineStage;
}> = Object.freeze([
  { pattern: ".nightgauge/pipeline/issue-{N}.json", stage: "issue-pickup" },
  { pattern: ".nightgauge/pipeline/planning-{N}.json", stage: "feature-planning" },
  { pattern: ".nightgauge/plans/{N}-*.md", stage: "feature-planning" },
  { pattern: ".nightgauge/pipeline/dev-{N}.json", stage: "feature-dev" },
  { pattern: ".nightgauge/pipeline/validate-{N}.json", stage: "feature-validate" },
  { pattern: ".nightgauge/pipeline/pr-{N}.json", stage: "pr-create" },
]);

/**
 * Replace any contiguous digit run in a path with `{N}` so callers can
 * compare a concrete missing-file path against an `{N}`-wildcarded pattern.
 *
 * Multi-digit issue numbers (e.g. 12345) are collapsed to a single `{N}`.
 * Globs (`*`) in the input are preserved.
 */
export function normalizePathToPattern(filePath: string): string {
  return filePath.replace(/\d+/g, "{N}");
}

/**
 * Parse a SKILL.md frontmatter block and return its structured I/O if
 * present. Returns null when the manifest doesn't declare `inputs:` /
 * `outputs:` arrays.
 *
 * The frontmatter parser is intentionally minimal — full YAML grammars are
 * out of scope; only the documented `- value` list-of-scalars form is
 * supported, matching the format used across all six pipeline skills.
 */
export function parseStageManifest(
  stage: PipelineStage,
  frontmatterContent: string
): ParsedStageManifest | null {
  const inputs = extractListField(frontmatterContent, "inputs");
  const outputs = extractListField(frontmatterContent, "outputs");

  if (inputs.length === 0 && outputs.length === 0) {
    return null;
  }

  return {
    stage,
    inputs,
    outputs,
    normalizedOutputs: outputs.map(normalizePathToPattern),
  };
}

/**
 * Extract a single `key: [- item]` list field from a YAML frontmatter
 * block. Returns an empty array when the key is absent or contains no
 * list entries.
 */
function extractListField(frontmatter: string, key: string): string[] {
  const lines = frontmatter.split(/\r?\n/);
  const items: string[] = [];
  let inField = false;
  let baseIndent = -1;

  for (const line of lines) {
    if (!inField) {
      const match = line.match(new RegExp(`^(\\s*)${key}\\s*:\\s*$`));
      if (match) {
        inField = true;
        baseIndent = match[1].length;
      }
      continue;
    }

    // Stop when we hit a sibling key at the same or shallower indent.
    const trimmed = line.trim();
    if (trimmed.length === 0) continue;

    const leadingLength = line.length - line.trimStart().length;
    const itemText = line.trim().startsWith("-") ? line.trim().slice(1).trim() : "";
    if (itemText && leadingLength > baseIndent) {
      items.push(stripQuotes(itemText));
      continue;
    }

    const siblingKey = line.match(/^(\s*)([A-Za-z0-9_-]+)\s*:/);
    if (siblingKey && siblingKey[1].length <= baseIndent) {
      break;
    }
  }

  return items;
}

function stripQuotes(value: string): string {
  if (
    (value.startsWith('"') && value.endsWith('"')) ||
    (value.startsWith("'") && value.endsWith("'"))
  ) {
    return value.slice(1, -1);
  }
  return value;
}

/**
 * StageGraph — runtime lookup for "which stage produces this file?".
 *
 * Construct via `loadStageGraphFromManifests(skillsDir)` for the manifest-
 * backed graph, or via `StageGraph.fromFallback()` for the in-process
 * fallback used by tests and sandbox environments.
 */
export class StageGraph {
  private constructor(
    private readonly producers: ReadonlyArray<{ pattern: string; stage: PipelineStage }>,
    public readonly source: "manifests" | "fallback"
  ) {}

  /**
   * Return the producer table verbatim — used by tests asserting parity
   * with `DEV_FALLBACK_PRODUCERS`.
   */
  getProducers(): ReadonlyArray<{ pattern: string; stage: PipelineStage }> {
    return this.producers;
  }

  /**
   * Look up the producing stage for a missing file.
   *
   * Returns null when no manifest declares the path as an output. The
   * caller (Recovery Dialog) hides the "Run producing stage" action when
   * this returns null.
   */
  getProducingStage(missingFile: string): StageProducer | null {
    const normalized = normalizePathToPattern(missingFile);

    // Exact pattern match first.
    for (const producer of this.producers) {
      if (producer.pattern === normalized) {
        return {
          stage: producer.stage,
          name: STAGE_DISPLAY_NAMES[producer.stage] ?? producer.stage,
        };
      }
    }

    // Glob match for patterns containing `*` (e.g., plans/{N}-*.md).
    for (const producer of this.producers) {
      if (!producer.pattern.includes("*")) continue;
      const regex = globPatternToRegex(producer.pattern);
      if (regex.test(normalized)) {
        return {
          stage: producer.stage,
          name: STAGE_DISPLAY_NAMES[producer.stage] ?? producer.stage,
        };
      }
    }

    return null;
  }

  /**
   * Build a StageGraph from parsed manifests. Each manifest's outputs are
   * registered as a producer for that stage.
   */
  static fromManifests(manifests: ParsedStageManifest[]): StageGraph {
    const producers: Array<{ pattern: string; stage: PipelineStage }> = [];
    for (const m of manifests) {
      for (const out of m.normalizedOutputs) {
        producers.push({ pattern: out, stage: m.stage });
      }
    }
    return new StageGraph(producers, "manifests");
  }

  /**
   * Build a StageGraph from `DEV_FALLBACK_PRODUCERS`. Used when skill
   * manifests are unreachable (test sandbox, partial install).
   */
  static fromFallback(): StageGraph {
    return new StageGraph([...DEV_FALLBACK_PRODUCERS], "fallback");
  }
}

function globPatternToRegex(pattern: string): RegExp {
  // Escape regex metacharacters except `*`, then expand `*` to `[^/]*` and
  // `{N}` to a digit-friendly placeholder. We compare normalized strings
  // (digits already replaced with `{N}`), so `{N}` matches itself.
  const escaped = pattern.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]*");
  return new RegExp(`^${escaped}$`);
}

/**
 * Load and parse pipeline skill manifests from a skills directory, then
 * build a StageGraph. On any I/O error the function falls back to
 * `StageGraph.fromFallback()` so callers always get a usable graph.
 *
 * Implemented synchronously because it's called once at extension
 * activation; the file set is small (≤6 manifests).
 */
export function loadStageGraphFromManifests(
  skillsDir: string,
  fs: { readFileSync: (p: string) => string; existsSync: (p: string) => boolean },
  pipelineStages: ReadonlyArray<PipelineStage> = [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
  ]
): StageGraph {
  const manifests: ParsedStageManifest[] = [];

  for (const stage of pipelineStages) {
    const manifestPath = `${skillsDir}/nightgauge-${stage}/SKILL.md`;
    if (!fs.existsSync(manifestPath)) continue;

    let content: string;
    try {
      content = fs.readFileSync(manifestPath);
    } catch {
      continue;
    }

    const frontmatter = extractFrontmatter(content);
    if (!frontmatter) continue;

    const parsed = parseStageManifest(stage, frontmatter);
    if (parsed) manifests.push(parsed);
  }

  if (manifests.length === 0) {
    return StageGraph.fromFallback();
  }
  return StageGraph.fromManifests(manifests);
}

/** Extract the YAML frontmatter (text between leading `---` markers). */
function extractFrontmatter(source: string): string | null {
  const match = source.match(/^---\r?\n([\s\S]*?)\r?\n---/);
  return match ? match[1] : null;
}
