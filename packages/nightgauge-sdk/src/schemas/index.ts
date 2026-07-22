/**
 * Pipeline stage output schemas for structured context handoff.
 *
 * Used with `claude -p --json-schema <schema> --output-format json`
 * to get typed, validated output from each pipeline stage.
 *
 * The response JSON has the structured output in the `structured_output` field.
 */

import issuePickupSchema from "./issue-pickup-output.json";
import featurePlanningSchema from "./feature-planning-output.json";
import featureDevSchema from "./feature-dev-output.json";
import featureValidateSchema from "./feature-validate-output.json";
import prCreateSchema from "./pr-create-output.json";
import prMergeSchema from "./pr-merge-output.json";

export const PIPELINE_SCHEMAS = {
  "issue-pickup": issuePickupSchema,
  "feature-planning": featurePlanningSchema,
  "feature-dev": featureDevSchema,
  "feature-validate": featureValidateSchema,
  "pr-create": prCreateSchema,
  "pr-merge": prMergeSchema,
} as const;

export type PipelineStage = keyof typeof PIPELINE_SCHEMAS;

/** Get the JSON schema string for a pipeline stage (for --json-schema flag) */
export function getStageSchema(stage: PipelineStage): string {
  return JSON.stringify(PIPELINE_SCHEMAS[stage]);
}
