/**
 * Phase Registry — Canonical phase names and ordering for each pipeline stage
 *
 * Defines the stable, deterministic phase definitions that skills emit as
 * structured HTML comment markers. The orchestrator and dashboard use this
 * registry to validate and display phase progress.
 *
 * Phase names are kebab-case identifiers that remain stable across versions.
 * Changing phase names or counts requires a skill version bump.
 *
 * @see Issue #1027 - Skills emit structured phase markers
 */

import type { PipelineStage } from "./EventBus.js";

/**
 * A single phase definition within a pipeline stage.
 */
export interface StagePhaseDefinition {
  /** Stable kebab-case phase identifier */
  name: string;
  /** 0-based position within the stage */
  index: number;
}

/**
 * Execution stages only — excludes bookend stages (pipeline-start, pipeline-finish)
 * that have no skill files and therefore no phases.
 */
export type ExecutionStage = Exclude<PipelineStage, "pipeline-start" | "pipeline-finish">;

/**
 * Canonical phase registry mapping each execution stage to its ordered phases.
 *
 * Phase counts per stage:
 * - issue-pickup: 14 (added blocked-dependency-gate phase, Issue #231)
 * - feature-planning: 14 (added recall-prior-decisions phase, Issue #3593)
 * - feature-dev: 18 (added recall-architectural-constraints phase, Issue #3594)
 * - feature-validate: 23 (added verify-ui-gate phase, Issue #4193)
 * - pr-create: 14 (added scope-drift-gate phase for type:docs/type:chore scope guard, Issue #3040)
 * - pr-merge: 14 (added retrospective-feedback phase, Issue #14)
 */
export const PHASE_REGISTRY: Record<ExecutionStage, StagePhaseDefinition[]> = {
  "issue-pickup": [
    { name: "validate-environment", index: 0 },
    { name: "issue-selection", index: 1 },
    { name: "signal-stage-start", index: 2 },
    { name: "size-gate-preflight", index: 3 },
    { name: "baseline-ci-gate", index: 4 },
    { name: "blocked-dependency-gate", index: 5 },
    { name: "issue-analysis", index: 6 },
    { name: "read-git-workflow", index: 7 },
    { name: "branch-creation", index: 8 },
    { name: "environment-setup", index: 9 },
    { name: "output-summary", index: 10 },
    { name: "write-context", index: 11 },
    { name: "knowledge-scaffolding", index: 12 },
    { name: "self-assessment", index: 13 },
  ],
  "feature-planning": [
    { name: "feedback-context-check", index: 0 },
    { name: "load-context", index: 1 },
    { name: "batch-detection", index: 2 },
    { name: "ac-reconcile", index: 3 },
    { name: "assess-complexity", index: 4 },
    { name: "pattern-mining", index: 5 },
    { name: "documentation-analysis", index: 6 },
    { name: "knowledge-base-read", index: 7 },
    { name: "recall-prior-decisions", index: 8 },
    { name: "produce-plan", index: 9 },
    { name: "write-planning-context", index: 10 },
    { name: "knowledge-base-enrichment", index: 11 },
    { name: "complete-stage", index: 12 },
    { name: "self-assessment", index: 13 },
  ],
  // Issue #1608: removed 'commit' (index 11) and 'push-commits' (index 12).
  // Commit and push now happen in feature-validate after validation passes.
  "feature-dev": [
    { name: "validate-environment", index: 0 },
    { name: "read-planning-context", index: 1 },
    { name: "batch-plan-detection", index: 2 },
    { name: "feedback-context-check", index: 3 },
    { name: "plan-verification", index: 4 },
    { name: "knowledge-base-read", index: 5 },
    { name: "recall-architectural-constraints", index: 6 },
    { name: "standards-loading", index: 7 },
    { name: "implementation", index: 8 },
    { name: "testing", index: 9 },
    { name: "e2e-testing", index: 10 },
    { name: "quality-review", index: 11 },
    { name: "self-correction", index: 12 },
    { name: "feedback-signal-evaluation", index: 13 },
    { name: "write-dev-context", index: 14 },
    { name: "sync-project-status", index: 15 },
    { name: "output-summary", index: 16 },
    { name: "self-assessment", index: 17 },
  ],
  // Issue #1608: added 'commit-and-push' phase at index 12.
  // Issue #2609: added 'pre-push-merge-validation' phase at index 12.
  // Issue #3595: added 'knowledge-coverage-check' phase at index 12 (shifted pre-push+ by 1).
  // Issue #24: added 'mobile-mcp-tests' phase at index 11 (shifted ci-parity+ by 1).
  // Issue #4193: added 'verify-ui-gate' phase at index 12 (shifted ci-parity+ by 1).
  // Validated code is committed and pushed here before writing context.
  "feature-validate": [
    { name: "validate-environment", index: 0 },
    { name: "read-dev-context", index: 1 },
    { name: "batch-detection", index: 2 },
    { name: "ac-completion-check", index: 3 },
    { name: "detect-testing-environment", index: 4 },
    { name: "ptc-detection", index: 5 },
    { name: "freshness-check", index: 6 },
    { name: "build-verification", index: 7 },
    { name: "dead-code-detection", index: 8 },
    { name: "baseline-comparison", index: 9 },
    { name: "run-tests", index: 10 },
    { name: "mobile-mcp-tests", index: 11 },
    { name: "verify-ui-gate", index: 12 },
    { name: "ci-parity-check", index: 13 },
    { name: "knowledge-coverage-check", index: 14 },
    { name: "pre-push-merge-validation", index: 15 },
    { name: "generate-checklist", index: 16 },
    { name: "feedback-signal-evaluation", index: 17 },
    { name: "commit-and-push", index: 18 },
    { name: "write-validate-context", index: 19 },
    { name: "sync-project-status", index: 20 },
    { name: "output-summary", index: 21 },
    { name: "self-assessment", index: 22 },
  ],
  "pr-create": [
    { name: "auto-merge-guard", index: 0 },
    { name: "load-context", index: 1 },
    { name: "batch-detection", index: 2 },
    { name: "build-knowledge-section", index: 3 },
    { name: "build-what-to-test-section", index: 4 },
    { name: "preflight-checks", index: 5 },
    { name: "proactive-main-merge", index: 6 },
    { name: "security-rescan", index: 7 },
    { name: "scope-drift-gate", index: 8 },
    { name: "create-pr", index: 9 },
    { name: "verify-pr-created", index: 10 },
    { name: "monitor-ci-status", index: 11 },
    { name: "write-context", index: 12 },
    { name: "self-assessment", index: 13 },
  ],
  "pr-merge": [
    { name: "read-pr-context", index: 0 },
    { name: "batch-detection", index: 1 },
    { name: "validate-environment", index: 2 },
    { name: "ci-gate", index: 3 },
    { name: "auto-fix-retry", index: 4 },
    { name: "fetch-reviews", index: 5 },
    { name: "categorize-issues", index: 6 },
    { name: "address-feedback", index: 7 },
    { name: "freshness-check", index: 8 },
    { name: "merge", index: 9 },
    { name: "post-merge-cleanup", index: 10 },
    { name: "retrospective-feedback", index: 11 },
    { name: "output-summary", index: 12 },
    { name: "self-assessment", index: 13 },
  ],
};

/**
 * Get the total number of phases for a pipeline stage.
 *
 * @param stage - The execution stage
 * @returns The total phase count, or 0 if stage not found
 */
export function getPhaseTotal(stage: ExecutionStage): number {
  const phases = PHASE_REGISTRY[stage];
  return phases ? phases.length : 0;
}

/**
 * Get the 0-based index of a named phase within a stage.
 *
 * @param stage - The execution stage
 * @param phaseName - The kebab-case phase name
 * @returns The phase index, or -1 if not found
 */
export function getPhaseIndex(stage: ExecutionStage, phaseName: string): number {
  const phases = PHASE_REGISTRY[stage];
  if (!phases) return -1;
  const phase = phases.find((p) => p.name === phaseName);
  return phase ? phase.index : -1;
}

/**
 * Generate the HTML comment phase marker string for a given stage and phase.
 *
 * Adapter-neutral and no-op-safe (#4029): the marker is an HTML comment, so it
 * is never interpreted as code under any adapter. Under Codex it passes through
 * the `--json` summarizer as plain text and is parsed identically to Claude
 * output by the extension's streamOutputHandler. Skills emit markers
 * unconditionally — no per-adapter guard is needed. See docs/SKILL_PORTABILITY.md §3.
 *
 * @param stage - The execution stage
 * @param phaseName - The kebab-case phase name
 * @returns The formatted HTML comment marker, or empty string if phase not found
 *
 * @example
 * ```typescript
 * formatPhaseMarker('feature-dev', 'implementation')
 * // '<!-- phase:start name="implementation" index=4 total=13 stage="feature-dev" -->'
 * ```
 */
export function formatPhaseMarker(stage: ExecutionStage, phaseName: string): string {
  const index = getPhaseIndex(stage, phaseName);
  const total = getPhaseTotal(stage);
  if (index === -1) return "";
  return `<!-- phase:start name="${phaseName}" index=${index} total=${total} stage="${stage}" -->`;
}

/**
 * Parsed result from a phase marker HTML comment.
 */
export interface ParsedPhaseMarker {
  /** Kebab-case phase name */
  name: string;
  /** 0-based index within the stage */
  index: number;
  /** Total phases in the stage */
  total: number;
  /** Pipeline stage this phase belongs to */
  stage: string;
}

/**
 * Global-flag regex for scanning all phase markers in a text block.
 * Required for String.prototype.matchAll — using /g flag is mandatory.
 *
 * Captures: name, index, total, stage
 */
const PHASE_MARKER_RE_GLOBAL =
  /<!-- phase:start name="([a-z][a-z0-9-]*)" index=(\d+) total=(\d+) stage="([a-z][a-z0-9-]*)" -->/g;

/**
 * Parse all phase markers from a text string, in document order.
 *
 * When Claude emits multiple phase markers in a single content block
 * (bundled stream flush), this function returns all of them rather than
 * silently dropping markers 2-N as the singular variant did.
 *
 * @param text - The text to scan (may contain surrounding content)
 * @returns Array of parsed phase markers in document order (empty if none found)
 *
 * @example
 * ```typescript
 * parsePhaseMarkers('<!-- phase:start name="implementation" index=7 total=17 stage="feature-dev" -->\n<!-- phase:start name="testing" index=8 total=17 stage="feature-dev" -->')
 * // [{ name: 'implementation', index: 7, total: 17, stage: 'feature-dev' }, { name: 'testing', index: 8, total: 17, stage: 'feature-dev' }]
 * ```
 */
export function parsePhaseMarkers(text: string): ParsedPhaseMarker[] {
  const results: ParsedPhaseMarker[] = [];
  for (const match of text.matchAll(PHASE_MARKER_RE_GLOBAL)) {
    results.push({
      name: match[1],
      index: parseInt(match[2], 10),
      total: parseInt(match[3], 10),
      stage: match[4],
    });
  }
  return results;
}

/**
 * Parse a phase marker from a text string.
 *
 * Scans the input for the `<!-- phase:start ... -->` HTML comment pattern
 * that skills emit at the beginning of each phase. Returns the parsed
 * fields if found, or `null` if no marker is present.
 *
 * For text blocks that may contain multiple bundled markers, use
 * `parsePhaseMarkers` (plural) instead to capture all of them.
 *
 * @param text - The text to scan (may contain surrounding content)
 * @returns Parsed phase marker or null
 *
 * @example
 * ```typescript
 * parsePhaseMarker('<!-- phase:start name="implementation" index=4 total=13 stage="feature-dev" -->')
 * // { name: 'implementation', index: 4, total: 13, stage: 'feature-dev' }
 * ```
 */
export function parsePhaseMarker(text: string): ParsedPhaseMarker | null {
  return parsePhaseMarkers(text)[0] ?? null;
}
