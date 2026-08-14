/** Producer-owned identity for records synthesized after an orchestrator crash. */
export const ORCHESTRATOR_CRASH_TERMINAL_KIND = "orchestrator_crash" as const;

/**
 * Positively identify a synthesized orchestrator-crash record.
 *
 * Shape is deliberately irrelevant: real crash records overlap the phantom
 * backup-write population at one stage and zero cost. Consumers must exempt
 * only this producer-owned terminal marker (#447).
 */
export function isOrchestratorCrashRecord(
  value: unknown
): value is { terminal_failure_kind: typeof ORCHESTRATOR_CRASH_TERMINAL_KIND } {
  return (
    typeof value === "object" &&
    value !== null &&
    "terminal_failure_kind" in value &&
    value.terminal_failure_kind === ORCHESTRATOR_CRASH_TERMINAL_KIND
  );
}
