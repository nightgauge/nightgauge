/**
 * AuditEventClient — client-side audit event collection and submission.
 *
 * Captures pipeline lifecycle events, validates them with Zod, batches them
 * in memory, and POSTs them to the platform audit API. Falls back to a local
 * JSON queue file when the platform is unreachable.
 *
 * Design decisions:
 * - Never throws: all errors are written to stderr to match the platform's
 *   AuditService pattern (silent-to-caller, logged internally).
 * - Uses Node.js built-in fetch (Node >=18 required, already enforced).
 * - Offline queue is a JSON file (no SQLite) to avoid native module issues
 *   in the VSCode extension host.
 * - Opt-in: callers instantiate this client and pass their EventBus reference.
 *   PipelineOrchestrator is NOT modified.
 *
 * @see Issue #1581 - Audit Event Client in SDK for Platform Audit Trail
 * @see acme-platform/src/services/audit-logs.ts (platform counterpart)
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import type { EventBus } from "../events/EventBus.js";
import {
  AuditEventSchema,
  type AuditConfig as AuditConfigBase,
  type AuditEvent,
} from "./schemas.js";

/** AuditConfig extended to accept a URL getter for per-request resolution. */
export type AuditConfig = Omit<AuditConfigBase, "platformUrl"> & {
  platformUrl?: string | (() => string);
};

/** An event persisted in the offline queue file (includes queued timestamp). */
interface QueuedAuditEvent extends AuditEvent {
  _queuedAt: string;
}

export class AuditEventClient {
  private readonly queue: AuditEvent[] = [];
  private flushTimer?: ReturnType<typeof setInterval>;

  constructor(
    private readonly config: AuditConfig,
    private readonly events?: EventBus
  ) {
    if (!config.enabled) return;

    if (typeof fetch === "undefined") {
      process.stderr.write(
        "[AuditEventClient] Node.js >=18 is required for built-in fetch support.\n"
      );
      return;
    }

    if (events) {
      // A stage finishing is a phase node reaching a terminal state; the whole
      // pipeline finishing is the root run node reaching a terminal state.
      events.on("phase", (node) => {
        if (node.status === "succeeded" || node.status === "failed") {
          void this.flush();
        }
      });
      events.on("run", (node) => {
        if (node.status !== "running") {
          void this.flushAll();
        }
      });
    }

    this.flushTimer = setInterval(() => {
      void this.flush();
    }, config.flushIntervalMs);
  }

  /**
   * Validate and enqueue an audit event.
   *
   * Invalid events are logged to stderr and discarded. Automatically
   * flushes in-memory queue when batchSize is reached.
   */
  enqueue(rawEvent: unknown): void {
    if (!this.config.enabled) return;

    const result = AuditEventSchema.safeParse(rawEvent);
    if (!result.success) {
      process.stderr.write(
        `[AuditEventClient] Invalid audit event discarded: ${result.error.message}\n`
      );
      return;
    }

    this.queue.push(result.data);

    if (this.queue.length >= this.config.batchSize) {
      void this.flush();
    }
  }

  /**
   * Flush the in-memory queue to the platform API.
   *
   * On 4xx: log and discard (client error — do not retry).
   * On 5xx or network error: write to offline queue for later retry.
   */
  async flush(): Promise<void> {
    if (!this.config.enabled || this.queue.length === 0) return;

    const batch = this.queue.splice(0, this.queue.length);
    await this.submitBatch(batch);
  }

  /**
   * Flush in-memory queue AND the offline queue file to the platform API.
   * Called on pipeline:complete to ensure nothing is left behind.
   */
  async flushAll(): Promise<void> {
    if (!this.config.enabled) return;
    await this.flush();
    await this.flushOfflineQueue();
  }

  /**
   * Clear the flush timer and perform a final flush.
   * Should be called when the client is no longer needed to prevent timer leaks.
   */
  async dispose(): Promise<void> {
    if (this.flushTimer !== undefined) {
      clearInterval(this.flushTimer);
      this.flushTimer = undefined;
    }
    await this.flushAll();
  }

  /**
   * Read the offline queue file and attempt to submit its contents.
   * Deletes the file on success. Leaves it intact on failure.
   */
  async flushOfflineQueue(): Promise<void> {
    if (!this.config.enabled) return;

    const queuePath = this.resolveQueuePath();
    let raw: string;
    try {
      raw = await fs.readFile(queuePath, "utf-8");
    } catch {
      // File doesn't exist — nothing to flush
      return;
    }

    let events: QueuedAuditEvent[];
    try {
      const parsed = JSON.parse(raw) as unknown;
      if (!Array.isArray(parsed)) {
        process.stderr.write(
          "[AuditEventClient] Offline queue file has unexpected format — treating as empty.\n"
        );
        await this.deleteQueueFile(queuePath);
        return;
      }
      events = parsed as QueuedAuditEvent[];
    } catch {
      process.stderr.write("[AuditEventClient] Offline queue file is corrupt JSON — recreating.\n");
      await this.deleteQueueFile(queuePath);
      return;
    }

    if (events.length === 0) {
      await this.deleteQueueFile(queuePath);
      return;
    }

    // Strip the internal _queuedAt field before submitting
    const auditEvents: AuditEvent[] = events.map(({ _queuedAt: _, ...e }) => e);
    const success = await this.submitBatch(auditEvents, /* skipOfflineOnFail */ true);
    if (success) {
      await this.deleteQueueFile(queuePath);
    }
  }

  // ---------------------------------------------------------------------------
  // Private helpers
  // ---------------------------------------------------------------------------

  /**
   * Submit a batch of events to the platform API.
   *
   * @param events - Events to submit
   * @param skipOfflineOnFail - When true, do not write to offline queue on failure
   *   (used when flushing the offline queue itself to avoid double-queuing).
   * @returns true if submission succeeded, false otherwise
   */
  private async submitBatch(events: AuditEvent[], skipOfflineOnFail = false): Promise<boolean> {
    const rawPlatformUrl = this.config.platformUrl;
    const resolvedPlatformUrl =
      typeof rawPlatformUrl === "function" ? rawPlatformUrl() : rawPlatformUrl;
    if (!resolvedPlatformUrl || !this.config.apiKey) {
      process.stderr.write(
        "[AuditEventClient] platformUrl or apiKey not configured — writing to offline queue.\n"
      );
      if (!skipOfflineOnFail) {
        await this.writeToOfflineQueue(events);
      }
      return false;
    }

    const url = `${resolvedPlatformUrl}/api/v1/audit/events`;
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), this.config.timeoutMs);

    try {
      const response = await fetch(url, {
        method: "POST",
        headers: {
          Authorization: `Bearer ${this.config.apiKey}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ events }),
        signal: controller.signal,
      });

      clearTimeout(timeoutId);

      if (response.ok) {
        return true;
      }

      if (response.status >= 400 && response.status < 500) {
        // Client error — log and discard, do not retry
        process.stderr.write(
          `[AuditEventClient] Platform returned ${response.status} — discarding ${events.length} events.\n`
        );
        return false;
      }

      // Server error — write to offline queue for later retry
      process.stderr.write(
        `[AuditEventClient] Platform returned ${response.status} — writing ${events.length} events to offline queue.\n`
      );
      if (!skipOfflineOnFail) {
        await this.writeToOfflineQueue(events);
      }
      return false;
    } catch (err) {
      clearTimeout(timeoutId);
      const message = err instanceof Error ? err.message : String(err);
      process.stderr.write(
        `[AuditEventClient] Network error — writing ${events.length} events to offline queue: ${message}\n`
      );
      if (!skipOfflineOnFail) {
        await this.writeToOfflineQueue(events);
      }
      return false;
    }
  }

  /**
   * Append events to the offline queue file.
   * Caps the queue at offlineQueueMaxSize — oldest events are discarded when exceeded.
   */
  private async writeToOfflineQueue(events: AuditEvent[]): Promise<void> {
    const queuePath = this.resolveQueuePath();

    try {
      await fs.mkdir(path.dirname(queuePath), { recursive: true });
    } catch {
      // Directory may already exist
    }

    let existing: QueuedAuditEvent[] = [];
    try {
      const raw = await fs.readFile(queuePath, "utf-8");
      const parsed = JSON.parse(raw) as unknown;
      if (Array.isArray(parsed)) {
        existing = parsed as QueuedAuditEvent[];
      }
    } catch {
      // File doesn't exist or is invalid — start fresh
    }

    const now = new Date().toISOString();
    const incoming: QueuedAuditEvent[] = events.map((e) => ({
      ...e,
      _queuedAt: now,
    }));

    let combined = [...existing, ...incoming];

    // Cap at offlineQueueMaxSize — discard oldest
    if (combined.length > this.config.offlineQueueMaxSize) {
      const excess = combined.length - this.config.offlineQueueMaxSize;
      process.stderr.write(
        `[AuditEventClient] Offline queue cap reached — discarding ${excess} oldest events.\n`
      );
      combined = combined.slice(excess);
    }

    try {
      await fs.writeFile(queuePath, JSON.stringify(combined, null, 2), "utf-8");
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      process.stderr.write(`[AuditEventClient] Failed to write offline queue: ${message}\n`);
    }
  }

  private async deleteQueueFile(queuePath: string): Promise<void> {
    try {
      await fs.unlink(queuePath);
    } catch {
      // File may have already been deleted
    }
  }

  private resolveQueuePath(): string {
    const queuePath = this.config.offlineQueuePath;
    return path.isAbsolute(queuePath) ? queuePath : path.resolve(queuePath);
  }
}
