/**
 * Zod schema and types for stall detection event records.
 *
 * Stall events are emitted by skillRunner during pipeline stage execution
 * when the stall detection ticker fires or a user responds to a stall prompt.
 * They are persisted in JSONL execution history records for threshold calibration
 * and autonomous-mode diagnostics.
 *
 * @see Issue #2652 — Record stall events and user responses in run history
 * @see docs/HEALTH_MONITORING.md — Context for future threshold calibration
 */

import { z } from "zod";

/**
 * A single stall detection event record.
 *
 * Emitted at three points during skill execution:
 * - When the stall warning threshold is reached (action: "warn")
 * - When the user responds to the stall prompt (action: "keep_waiting" | "stop_stage")
 * - When the stall kill threshold triggers forcible termination (action: "kill")
 */
export const StallEventSchema = z.object({
  /** ISO 8601 timestamp when this event occurred */
  timestamp: z.string(),
  /** Milliseconds elapsed since skill execution start when this event occurred */
  elapsed_ms: z.number().int().min(0),
  /** Stall threshold (in ms) that triggered this event (from config) */
  threshold_ms: z.number().int().min(0),
  /**
   * What action was taken:
   * - "warn": stall warning shown (warn threshold reached)
   * - "keep_waiting": user clicked "Keep Waiting" on the stall prompt
   * - "stop_stage": user clicked "Stop Stage" on the stall prompt
   * - "kill": process was forcibly terminated by stall auto-kill
   * - "escalation_pause": autonomous mode pause triggered at extreme threshold (Issue #2656)
   * - "auto_abort": autonomous pause timed out without user response (Issue #2656)
   * - "resume": user chose Resume during autonomous pause (Issue #2656)
   * - "abort": user chose Abort during autonomous pause (Issue #2656)
   * - "connectivity_paused": stall-kill suspended because the ConnectivityStateBus
   *   reports the network is offline (Issue #3203)
   * - "connectivity_resumed": connectivity returned; idle counter reset and
   *   accumulated offline duration recorded (Issue #3203)
   */
  action: z.enum([
    "warn",
    "keep_waiting",
    "stop_stage",
    "kill",
    "escalation_pause",
    "auto_abort",
    "resume",
    "abort",
    "connectivity_paused",
    "connectivity_resumed",
  ]),
});

export type StallEvent = z.infer<typeof StallEventSchema>;
