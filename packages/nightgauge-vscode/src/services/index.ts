/**
 * Services barrel — public API surface for @nightgauge/nightgauge-vscode services.
 *
 * Re-exports stable contracts (interfaces, types, helpers) that downstream
 * consumers (views, SDK, tests) should depend on instead of concrete service
 * implementations.
 */

// Work-item contract — universal type and provider interface
export type { WorkItem, WorkItemSource, IWorkItemProvider } from "./types/WorkItemProvider";
export { normalizeToWorkItem, isBlocked, isEpicItem } from "./types/WorkItemProvider";

// Adapters — IWorkItemProvider implementations
export { GitHubIssuesAdapter } from "./adapters/GitHubIssuesAdapter";

// Telemetry upload service — ships local JSONL history to platform (#3315)
export { TelemetryUploaderService } from "./TelemetryUploaderService";
