/**
 * Types for skill self-amendment detection.
 *
 * When pipeline agents write context files with incorrect field values,
 * the Zod validation errors are captured here and surfaced in the
 * post-pipeline self-check output so the responsible SKILL.md can be
 * amended.
 */

export interface ValidationErrorRecord {
  /** Joined field path, e.g. "requirements.technical_notes" */
  path: string;
  /** Zod error code, e.g. "invalid_enum_value" | "invalid_type" */
  code: string;
  /** Human-readable message from Zod */
  message: string;
  /** The value the agent actually wrote (coerced to string) */
  received?: string;
  /** The valid values the schema expected */
  expected?: string[];
}

export interface SkillAmendmentProposal {
  /** Pipeline stage that produced the bad context, e.g. "issue-pickup" */
  stage: string;
  /** Relative path to the responsible SKILL.md */
  skillFile: string;
  /** Context file field that failed validation, e.g. "type" */
  field: string;
  /** Zod error code driving this proposal */
  errorCode: string;
  /** All distinct bad values seen across runs */
  receivedValues: string[];
  /** The valid values from the schema */
  expectedValues: string[];
  /** Number of distinct pipeline runs that hit this error */
  occurrenceCount: number;
  /** Issue numbers from affected runs */
  affectedRuns: number[];
  /** Human-readable proposed fix for the SKILL.md output contract */
  proposedConstraint: string;
}

export interface SkillAmendmentAnalysisResult {
  analyzedAt: string;
  recordsAnalyzed: number;
  proposals: SkillAmendmentProposal[];
}
