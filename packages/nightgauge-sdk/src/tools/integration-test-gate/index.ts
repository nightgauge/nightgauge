/**
 * Integration Test Gate module — Issue #2909.
 *
 * Exported from the SDK so the feature-validate skill can invoke it from
 * bash via `node --input-type=module`.
 */

export {
  classifyIntegrationOutcome,
  detectIntegrationRequirement,
  evaluateGate,
} from "./IntegrationTestGate.js";
export type {
  ClassifiedIntegrationOutcome,
  IntegrationDetectionSignals,
  IntegrationGateDecision,
  IntegrationGateMode,
  IntegrationRequirement,
  IntegrationRunOutcome,
} from "./types.js";
