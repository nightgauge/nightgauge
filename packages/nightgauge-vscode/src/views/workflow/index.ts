/**
 * Workflow tree barrel — the live `run → phase → agent → judge` sidebar tree
 * driven off the SDK EventBus node stream (#3919).
 */

export { WorkflowTreeProvider, type WorkflowEventSource } from "./WorkflowTreeProvider";
export {
  WorkflowTreeModel,
  aggregateRun,
  latestJudge,
  type FoldedRun,
  type FoldedPhase,
  type FoldedAgent,
  type RunAggregate,
} from "./workflowTreeModel";
export {
  WorkflowTreeItem,
  WorkflowRunTreeItem,
  WorkflowPhaseTreeItem,
  WorkflowAgentTreeItem,
  WorkflowJudgeTreeItem,
} from "./workflowTreeItems";
