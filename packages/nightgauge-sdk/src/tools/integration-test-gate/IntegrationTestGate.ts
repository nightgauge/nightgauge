/**
 * IntegrationTestGate — Issue #2909.
 *
 * Pure functions used by the feature-validate skill to keep integration-test
 * behavior honest. Split into three stages so each can be unit tested:
 *
 *   detectIntegrationRequirement → classifyIntegrationOutcome → evaluateGate
 *
 * Keeping these pure (no I/O, no process spawn) lets the skill invoke them
 * via `node --input-type=module` with signals pre-collected in bash, mirroring
 * the SelectiveTestRunner integration pattern.
 */

import type {
  ClassifiedIntegrationOutcome,
  IntegrationDetectionSignals,
  IntegrationGateDecision,
  IntegrationGateMode,
  IntegrationRequirement,
  IntegrationRunOutcome,
} from "./types.js";

/**
 * Signal substrings that indicate a test command failed for *environmental*
 * reasons (services unavailable, tools missing) rather than a genuine test
 * assertion. Matched case-insensitively against stdout + stderr combined.
 *
 * The list is intentionally conservative — false positives let broken tests
 * masquerade as environment problems, which is exactly the bug this module
 * exists to prevent. Prefer to extend this only when a real CI vs. local
 * divergence surfaces a new pattern.
 */
const ENVIRONMENTAL_FAILURE_SIGNALS: Array<{ pattern: RegExp; reason: string }> = [
  { pattern: /cannot connect to the docker daemon/i, reason: "docker-daemon-unavailable" },
  { pattern: /docker: command not found/i, reason: "docker-not-installed" },
  { pattern: /docker-compose: command not found/i, reason: "docker-compose-not-installed" },
  { pattern: /error response from daemon/i, reason: "docker-daemon-error" },
  {
    pattern: /econnrefused[^\n]*:(5432|6379|9000|3000|8080|4566)/i,
    reason: "service-port-unreachable",
  },
  { pattern: /econnrefused/i, reason: "connection-refused" },
  { pattern: /getaddrinfo (eai_again|enotfound)/i, reason: "dns-resolution-failure" },
  { pattern: /password authentication failed for user/i, reason: "database-auth-missing" },
  { pattern: /database "[^"]+" does not exist/i, reason: "database-not-provisioned" },
  { pattern: /could not connect to server: connection refused/i, reason: "postgres-unreachable" },
  { pattern: /redis connection refused/i, reason: "redis-unreachable" },
  { pattern: /connect etimedout/i, reason: "connect-timeout" },
  { pattern: /no such host/i, reason: "dns-resolution-failure" },
  { pattern: /pg_isready: command not found/i, reason: "postgres-client-missing" },
  { pattern: /environment variable [A-Z_]+ (is )?required/i, reason: "missing-env-var" },
];

/** npm script names that conventionally indicate integration tests. */
const INTEGRATION_SCRIPT_NAMES = [
  "test:integration",
  "test:integration:docker",
  "integration-test",
  "integration-tests",
  "test-integration",
];

/**
 * Decide whether the repo declares integration tests that must run in
 * feature-validate. Returns both the decision and the candidate commands the
 * skill should attempt.
 *
 * Detection is intentionally broad: if CI runs anything that looks like an
 * integration suite, local validation must exercise it too.
 */
export function detectIntegrationRequirement(
  signals: IntegrationDetectionSignals
): IntegrationRequirement {
  const commands: string[] = [];
  const reasons: string[] = [];

  if (signals.packageScripts) {
    for (const name of INTEGRATION_SCRIPT_NAMES) {
      if (signals.packageScripts[name]) {
        commands.push(`npm run ${name}`);
        reasons.push(`package.json script "${name}"`);
      }
    }
  }

  if (signals.workflowRunLines && signals.workflowRunLines.length > 0) {
    for (const line of signals.workflowRunLines) {
      if (!/integration/i.test(line)) continue;
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith("#")) continue;
      if (/^\s*(echo|printf|export|set\s)/i.test(trimmed)) continue;
      if (!commands.includes(trimmed)) commands.push(trimmed);
      reasons.push("ci workflow integration step");
    }
  }

  if (signals.hasIntegrationTestDir && commands.length === 0) {
    commands.push("npm test -- tests/integration");
    reasons.push("tests/integration/ directory");
  }

  const required = commands.length > 0;
  const detectedVia = required
    ? Array.from(new Set(reasons)).join("; ")
    : "no integration signals found";

  return { required, commands, detectedVia };
}

/**
 * Classify a command's exit status as either a genuine test result or an
 * environmental failure. A zero exit is always a pass. A non-zero exit with a
 * recognized environmental signal is flagged `ran=false` so the strict gate
 * can refuse to publish. Anything else is a real test failure.
 */
export function classifyIntegrationOutcome(
  outcome: IntegrationRunOutcome
): ClassifiedIntegrationOutcome {
  if (outcome.exitCode === 0) {
    return { ran: true, passed: true, environmentalFailure: false, reason: "tests-passed" };
  }

  const haystack = `${outcome.stdout}\n${outcome.stderr}`;
  for (const signal of ENVIRONMENTAL_FAILURE_SIGNALS) {
    if (signal.pattern.test(haystack)) {
      return {
        ran: false,
        passed: false,
        environmentalFailure: true,
        reason: `environmental: ${signal.reason}`,
      };
    }
  }

  return {
    ran: true,
    passed: false,
    environmentalFailure: false,
    reason: "assertion-failure",
  };
}

/**
 * Apply the configured gate mode to the detection + classification result.
 *
 * The invariant (#2909): in strict mode, a required integration suite that
 * did not run is a hard failure — not a pass by omission.
 */
export function evaluateGate(input: {
  requirement: IntegrationRequirement;
  outcome?: ClassifiedIntegrationOutcome;
  mode: IntegrationGateMode;
}): IntegrationGateDecision {
  const { requirement, outcome, mode } = input;

  if (mode === "off") {
    return {
      validationStatus: "passed",
      reason: "integration tests gate disabled (validation.integration_tests=off)",
      shouldEmitFeedback: false,
    };
  }

  if (!requirement.required) {
    return {
      validationStatus: "passed",
      reason: `no integration tests required (${requirement.detectedVia})`,
      shouldEmitFeedback: false,
    };
  }

  if (!outcome) {
    if (mode === "strict") {
      return {
        validationStatus: "failed",
        reason: `strict gate: integration tests required (${requirement.detectedVia}) but no attempt was made`,
        shouldEmitFeedback: true,
      };
    }
    return {
      validationStatus: "passed",
      reason: `best_effort: integration tests required (${requirement.detectedVia}) but skipped without execution`,
      shouldEmitFeedback: true,
    };
  }

  if (outcome.ran && outcome.passed) {
    return {
      validationStatus: "passed",
      reason: "integration tests ran and passed",
      shouldEmitFeedback: false,
    };
  }

  if (outcome.ran && !outcome.passed) {
    return {
      validationStatus: "failed",
      reason: `integration tests ran and failed (${outcome.reason})`,
      shouldEmitFeedback: false,
    };
  }

  // ran=false → environmental failure
  if (mode === "strict") {
    return {
      validationStatus: "failed",
      reason: `strict gate: integration tests could not run (${outcome.reason}). Stand up required services before retrying, or set validation.integration_tests=best_effort to record as a warning.`,
      shouldEmitFeedback: true,
    };
  }

  return {
    validationStatus: "passed",
    reason: `best_effort: integration tests skipped (${outcome.reason}) — warning only`,
    shouldEmitFeedback: true,
  };
}

/** Exported for tests only — keeps the signal list readable in assertions. */
export const __TESTING__ = { ENVIRONMENTAL_FAILURE_SIGNALS, INTEGRATION_SCRIPT_NAMES };
