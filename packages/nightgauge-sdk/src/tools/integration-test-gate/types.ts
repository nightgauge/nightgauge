/**
 * Types for the Integration Test Gate — Issue #2909.
 *
 * Gives feature-validate a deterministic way to:
 *   1. decide whether the repo under test has CI-declared integration tests
 *   2. classify whether a non-zero integration test command was a real test
 *      failure or an environmental failure (missing docker, postgres, etc.)
 *   3. translate the classification into a pass/fail validation status based on
 *      the configured mode
 *
 * A "strict" gate means: if CI runs integration tests and we can't run them
 * locally for any reason, the feature-validate stage fails — we never publish
 * a PR that CI will immediately reject for reasons we could have seen locally.
 */

/**
 * Configured behavior when integration tests are required by CI but cannot be
 * executed locally (missing services, docker unavailable, etc.).
 */
export type IntegrationGateMode =
  /** Fail feature-validate if required integration tests did not run. */
  | "strict"
  /** Record a warning but allow PR creation. Legacy behavior pre-#2909. */
  | "best_effort"
  /** Do not evaluate integration tests at all. */
  | "off";

/** Signals collected from the repo to decide whether integration tests are required. */
export interface IntegrationDetectionSignals {
  /** npm scripts keyed by script name, if a package.json exists. */
  packageScripts?: Record<string, string>;
  /** Raw `run:` lines extracted from .github/workflows/*.yml steps. */
  workflowRunLines?: string[];
  /** True when a `tests/integration/` or `integration-tests/` directory exists. */
  hasIntegrationTestDir?: boolean;
  /** True when a docker-compose.yml (any dialect) is present at the repo root. */
  hasDockerCompose?: boolean;
}

/** Detection result — whether integration tests must run in feature-validate. */
export interface IntegrationRequirement {
  required: boolean;
  /** Candidate commands the skill should attempt to run, in priority order. */
  commands: string[];
  /** Short human-readable reason the gate triggered (or why it didn't). */
  detectedVia: string;
}

/** Raw result of actually running an integration test command. */
export interface IntegrationRunOutcome {
  exitCode: number;
  stdout: string;
  stderr: string;
}

/**
 * Classified outcome. `ran=true` means the test framework actually executed
 * tests and produced a real pass/fail signal. `ran=false` with
 * `environmentalFailure=true` means the command never got far enough to run
 * tests (service unreachable, docker missing, etc.).
 */
export interface ClassifiedIntegrationOutcome {
  ran: boolean;
  passed: boolean;
  environmentalFailure: boolean;
  /** Short reason (e.g. "docker-unavailable", "postgres-unreachable", "assertion-failure"). */
  reason: string;
}

/** Final gate decision the skill applies to VALIDATION_STATUS. */
export interface IntegrationGateDecision {
  validationStatus: "passed" | "failed";
  /** Human-readable explanation suitable for logging and the feedback signal. */
  reason: string;
  /** True if the stage should emit an upstream feedback signal. */
  shouldEmitFeedback: boolean;
}
