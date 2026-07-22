/**
 * cli/workflow — the canonical provider-neutral workflow contract (epic #3899).
 *
 * Re-exports the `schemaVersion-4` event tree, the `WorkflowSpec` plan, and the
 * `WorkflowEventSink` write boundary. SDK consumers import from here; the SDK
 * barrel (`src/index.ts`) re-exports this module.
 *
 * @see docs/WORKFLOW_ORCHESTRATION.md
 */

export {
  WORKFLOW_SCHEMA_VERSION,
  isWorkflowRun,
  isSubAgentNode,
  isJudgeVerdict,
  zeroUsage,
  type WorkflowSchemaVersion,
  type OrchestrationCapability,
  type WorkflowNodeKind,
  type WorkflowNodeStatus,
  type WorkflowTerminalKind,
  type WorkflowJudgeVerdict,
  type WorkflowAgentUsage,
  type WorkflowRun,
  type WorkflowPhase,
  type SubAgentNode,
  type JudgeVerdict,
  type WorkflowNode,
  type WorkflowEvent,
} from "./WorkflowEvent.js";

export {
  CLAUDE_CEILING,
  FANOUT_CEILING,
  ABSOLUTE_CEILING,
  plannedAgentCount,
  validateWorkflowSpec,
  type WorkflowConcurrencyCeiling,
  type WorkflowAgentSpec,
  type WorkflowJudgeSpec,
  type WorkflowPhaseSpec,
  type WorkflowSpec,
} from "./WorkflowSpec.js";

export {
  ArrayWorkflowEventSink,
  createSeqCounter,
  type WorkflowEventSink,
} from "./WorkflowEventSink.js";

export {
  DEFAULT_ORCHESTRATION_CONFIG,
  DISABLE_WORKFLOWS_ENV,
  resolveOrchestrationConfig,
  prefersNativeOffload,
  type OrchestrationConfig,
  type ResolvedOrchestrationConfig,
  type OrchestrationStage,
  type PreferNativeOffloadMap,
} from "./OrchestrationConfig.js";

export {
  runSdkFanout,
  AgentExecutionError,
  type AgentExecutionResult,
  type JudgeExecutionResult,
  type WorkflowExecutorBindings,
  type WorkflowPhaseSummary,
  type WorkflowRunSummary,
  type RunSdkFanoutOptions,
} from "./SdkFanoutRunner.js";

export {
  makeSdkFanoutBindings,
  adapterEphemeralExec,
  parseJudgeOutcome,
  EphemeralTimeoutError,
  type EphemeralExec,
  type EphemeralExecResult,
  type SdkFanoutBindingsOptions,
} from "./SdkFanoutExecutors.js";

export {
  evaluateQuotaGate,
  gateWorkflowFanout,
  DEFAULT_LARGE_FANOUT_THRESHOLD,
  type WorkflowQuotaState,
  type QuotaStateProvider,
  type QuotaGateAction,
  type QuotaGateDecision,
} from "./WorkflowQuotaGate.js";

export {
  parseOrchestrationFrontmatter,
  type OrchestrationFrontmatterContext,
} from "./parseOrchestrationFrontmatter.js";
