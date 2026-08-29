/**
 * stageContextFiles — the ONE canonical stage → deliverable-file mapping.
 *
 * Pipeline deliverables are NOT named after the stage that wrote them. The
 * `feature-dev` stage writes `dev-{N}.json`; `feature-validate` writes
 * `validate-{N}.json`. Any consumer that interpolates the stage name directly
 * (`${stage}-${issueNumber}.json`) resolves a path that has never existed, and
 * because the miss surfaces as a plain ENOENT it fails silently forever.
 *
 * That is exactly how #1143 happened: `AutoRetroService.collectEvidence` built
 * its `pipeline_context` path from the stage name, so the one evidence source
 * carrying a stage-authored statement of the failure cause was never read, in
 * any run, since the code was written.
 *
 * The mapping therefore lives in a single dependency-free module rather than
 * being restated at each call site. Restating it is the `dual-path-drift`
 * defect class: HeadlessOrchestrator alone had three hand-written copies, each
 * free to disagree with `ContextManager.cleanup`'s canonical filename list.
 *
 * This module deliberately has NO runtime imports (both imports are type-only,
 * and erase at compile time) so any consumer — including ones under unit test
 * with a mocked filesystem — can depend on it without pulling in the
 * ContextAssembler dependency graph.
 *
 * @see Issue #1143 — auto-retro's pipeline_context source has never resolved
 * @see packages/nightgauge-sdk/src/context/ContextManager.ts `cleanup()` — the
 *      canonical deliverable filename list this mapping must agree with.
 */

import type { PipelineStage } from "@nightgauge/sdk";
import type { ContextFileType } from "../../services/RepositoryContextLoader";

/**
 * Maps skill stages to their expected output context file type.
 * Stages absent from this map produce no output file (bookends, pr-merge).
 *
 * @see Issue #637
 */
export const STAGE_OUTPUT_CONTEXT_TYPE: Partial<Record<PipelineStage, ContextFileType>> = {
  "issue-pickup": "issue",
  "feature-planning": "planning",
  "feature-dev": "dev",
  "feature-validate": "validate",
  "pr-create": "pr",
};

/**
 * Resolve the deliverable filename a stage writes for an issue.
 *
 * Accepts a plain `string` rather than `PipelineStage` because several callers
 * (AutoRetroService among them) receive the failed stage as untyped text off an
 * event payload. An unrecognized stage — including `pr-merge`, which legitimately
 * writes no deliverable — returns `null` so the caller can distinguish "this
 * stage has no deliverable" from "the deliverable is missing".
 *
 * @returns e.g. `"validate-1144.json"`, or `null` when the stage writes no file.
 */
export function stageContextFileName(stage: string, issueNumber: number): string | null {
  const type = STAGE_OUTPUT_CONTEXT_TYPE[stage as PipelineStage];
  return type ? `${type}-${issueNumber}.json` : null;
}
