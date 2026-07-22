/**
 * NotifierStatusTracker — lightweight in-memory singleton for recording per-notifier
 * send outcomes. Both DiscordService and MattermostService call recordSuccess/recordError
 * after each real send attempt so the NotifierSettingsPanel can display live status
 * without an IPC round-trip.
 *
 * @see Issue #3379 — Notifier Settings Panel
 */

export interface NotifierStatus {
  id: string;
  lastSuccessAt?: Date;
  lastErrorAt?: Date;
  lastError?: string;
}

export class NotifierStatusTracker {
  private static instance: NotifierStatusTracker | null = null;
  private readonly statuses = new Map<string, NotifierStatus>();

  private constructor() {}

  static initialize(): void {
    if (!NotifierStatusTracker.instance) {
      NotifierStatusTracker.instance = new NotifierStatusTracker();
    }
  }

  static getInstance(): NotifierStatusTracker | null {
    return NotifierStatusTracker.instance;
  }

  static resetInstance(): void {
    NotifierStatusTracker.instance = null;
  }

  recordSuccess(id: string): void {
    const existing = this.statuses.get(id) ?? { id };
    this.statuses.set(id, { ...existing, lastSuccessAt: new Date() });
  }

  recordError(id: string, redactedMessage: string): void {
    const existing = this.statuses.get(id) ?? { id };
    this.statuses.set(id, {
      ...existing,
      lastErrorAt: new Date(),
      lastError: redactedMessage,
    });
  }

  getStatus(id: string): NotifierStatus | undefined {
    return this.statuses.get(id);
  }

  getAllStatuses(): NotifierStatus[] {
    return Array.from(this.statuses.values());
  }

  clear(): void {
    this.statuses.clear();
  }
}
