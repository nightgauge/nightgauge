/**
 * Action Center tree barrel — the DecisionRequest sidebar tree driven off the
 * local-first attention store via IPC (ADR 015 / Issue #325).
 */

export { AttentionTreeProvider, type AttentionIpcSource } from "./AttentionTreeProvider";
export {
  AttentionTreeItem,
  AttentionGroupTreeItem,
  AttentionRequestTreeItem,
  iconForRequest,
  formatContextLine,
  formatRelativeAge,
  formatDescription,
  describeAttentionOption,
  compareRequests,
} from "./attentionTreeItems";
