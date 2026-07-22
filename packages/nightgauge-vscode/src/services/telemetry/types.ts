/**
 * Telemetry stream taxonomy used by both the consent service and the uploader.
 *
 * @see Issue #3327
 */

export type TelemetryStream = "pipeline-run" | "health" | "recommendation" | "trace";

export const ALL_STREAMS: readonly TelemetryStream[] = [
  "pipeline-run",
  "health",
  "recommendation",
  "trace",
] as const;

export function isTelemetryStream(value: unknown): value is TelemetryStream {
  return typeof value === "string" && (ALL_STREAMS as readonly string[]).includes(value);
}
