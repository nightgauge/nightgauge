/**
 * Zod schema for Health Score History records
 *
 * Defines the JSONL record format for persisted health score snapshots.
 * Written after each pipeline completion, read for 30-day trend visualization.
 *
 * Also defines the RecalibrationMarker type, written when the user triggers
 * a health score baseline recalibration (Issue #1262).
 *
 * @see Issue #789 - Persist Health Scores to Disk with 30-Day Trend
 * @see Issue #1262 - Add health score baseline recalibration after systemic fixes
 */

import { z } from "zod";

export const HealthScoreSnapshotSchema = z.object({
  schema_version: z.literal("1"),
  timestamp: z.string(), // ISO 8601
  score: z.number().min(0).max(100),
  status: z.enum(["excellent", "good", "fair", "poor", "critical"]),
  components: z.record(z.string(), z.number()),
  cacheHitRate: z.number(),
  costUsd: z.number(),
  issueNumber: z.number(),
});

export type HealthScoreSnapshot = z.infer<typeof HealthScoreSnapshotSchema>;

/**
 * Recalibration marker written to health-history.jsonl when the user resets
 * the health score trend baseline. Health trend components only consider
 * snapshots recorded after the most recent recalibration marker.
 *
 * @see Issue #1262 - Add health score baseline recalibration after systemic fixes
 */
export const RecalibrationMarkerSchema = z.object({
  schema_version: z.literal("1"),
  type: z.literal("recalibration"),
  timestamp: z.string(), // ISO 8601
  reason: z.string().optional(),
});

export type RecalibrationMarker = z.infer<typeof RecalibrationMarkerSchema>;
