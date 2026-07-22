/**
 * Audit event schemas — mirrors the platform's audit_logs table structure.
 *
 * IMPORTANT: The AUDIT_ACTIONS constant must stay in sync with the platform's
 * AuditAction enum defined in acme-platform/src/services/audit-logs.ts.
 * When the platform adds new action types, add them here as well.
 *
 * @see acme-platform/src/services/audit-logs.ts (source of truth)
 */

import { z } from "zod";

/**
 * All recognized audit action values, mirroring the platform's AuditAction enum.
 * Source of truth: acme-platform/src/services/audit-logs.ts
 */
export const AUDIT_ACTIONS = [
  "auth.login",
  "auth.logout",
  "auth.failed",
  "license.create",
  "license.revoke",
  "license.rotate",
  "billing.subscribe",
  "billing.cancel",
  "billing.upgrade",
  "team.invite",
  "team.remove",
  "team.role_change",
  "api_key.create",
  "api_key.revoke",
  "webhook.create",
  "webhook.delete",
  "stage.started",
  "stage.completed",
  "stage.failed",
  "pipeline.started",
  "pipeline.completed",
  "pipeline.failed",
  "commit.created",
  "pr.created",
  "pr.merged",
  "cost.recorded",
  "skill.invoked",
  "audit.purged",
  "compliance.report.generated",
  "compliance.report.scheduled",
] as const;

export const AuditActionSchema = z.enum(AUDIT_ACTIONS);
export type AuditAction = z.infer<typeof AuditActionSchema>;

export const AuditEventSchema = z.object({
  action: AuditActionSchema,
  resourceType: z.string().max(100).optional(),
  resourceId: z.string().max(255).optional(),
  metadata: z.record(z.string(), z.unknown()).optional(),
});
export type AuditEvent = z.infer<typeof AuditEventSchema>;

export const AuditConfigSchema = z.object({
  enabled: z.boolean().default(false),
  platformUrl: z.string().url().optional(),
  apiKey: z.string().optional(),
  batchSize: z.number().int().min(1).max(1000).default(100),
  flushIntervalMs: z.number().int().default(30_000),
  offlineQueuePath: z.string().default(".nightgauge/audit-queue.json"),
  offlineQueueMaxSize: z.number().int().default(10_000),
  retryMaxAttempts: z.number().int().default(3),
  retryBackoffMs: z.number().int().default(1_000),
  timeoutMs: z.number().int().default(5_000),
});
export type AuditConfig = z.infer<typeof AuditConfigSchema>;
