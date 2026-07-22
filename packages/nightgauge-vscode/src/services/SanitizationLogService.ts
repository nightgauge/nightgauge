/**
 * SanitizationLogService - Service for reading and watching sanitization logs
 *
 * Provides functionality to:
 * - Parse NDJSON sanitization log files
 * - Watch for log file changes
 * - Filter and aggregate events
 * - Generate time series data for charts
 *
 * @see docs/SECURITY.md for sanitization framework documentation
 * @see Issue #387 - Prompt Injection Firewall Dashboard
 */

import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import type {
  SanitizationEvent,
  RawSanitizationLogEntry,
  SanitizationEventType,
  SanitizationCategory,
  FirewallFilterState,
  FirewallAggregates,
  FirewallTimeSeriesPoint,
  TimeSeriesGranularity,
} from "../views/dashboard/FirewallTypes";
import { EMPTY_FIREWALL_AGGREGATES } from "../views/dashboard/FirewallTypes";

/**
 * SanitizationLogService manages reading and watching the sanitization log file.
 *
 * @example
 * ```typescript
 * const service = new SanitizationLogService(workspaceRoot);
 * await service.initialize();
 *
 * // Get all events
 * const events = service.getEvents();
 *
 * // Get filtered events
 * const filtered = service.getFilteredEvents({
 *   eventTypes: ['blocked'],
 *   categories: ['destructive'],
 *   timeRange: '24h',
 *   searchText: '',
 * });
 *
 * // Subscribe to changes
 * service.onEventsChanged((events) => {
 *   console.log(`${events.length} events loaded`);
 * });
 *
 * // Cleanup
 * service.dispose();
 * ```
 */
export class SanitizationLogService implements vscode.Disposable {
  private events: SanitizationEvent[] = [];
  private watcher: vscode.FileSystemWatcher | null = null;
  private disposables: vscode.Disposable[] = [];
  private logFilePath: string;
  private isInitialized = false;

  private readonly _onEventsChanged = new vscode.EventEmitter<SanitizationEvent[]>();
  public readonly onEventsChanged = this._onEventsChanged.event;

  constructor(private readonly workspaceRoot: string) {
    this.logFilePath = path.join(workspaceRoot, ".nightgauge", "logs", "sanitization.log");
  }

  /**
   * Initialize the service by loading existing events and starting file watcher
   */
  async initialize(): Promise<void> {
    if (this.isInitialized) {
      return;
    }

    // Load existing events
    await this.loadEvents();

    // Start watching for changes
    this.startWatching();

    this.isInitialized = true;
  }

  /**
   * Load events from the log file
   */
  async loadEvents(): Promise<SanitizationEvent[]> {
    try {
      if (!fs.existsSync(this.logFilePath)) {
        this.events = [];
        return this.events;
      }

      const content = await fs.promises.readFile(this.logFilePath, "utf-8");
      this.events = this.parseNdjson(content);
      this._onEventsChanged.fire(this.events);
      return this.events;
    } catch (error) {
      console.error("SanitizationLogService: Failed to load events:", error);
      this.events = [];
      return this.events;
    }
  }

  /**
   * Parse NDJSON content into SanitizationEvent array
   *
   * Gracefully handles malformed lines by skipping them.
   */
  parseNdjson(content: string): SanitizationEvent[] {
    const lines = content.trim().split("\n");
    const events: SanitizationEvent[] = [];

    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) {
        continue;
      }

      try {
        const event = this.parseNdjsonLine(trimmed);
        if (event) {
          events.push(event);
        }
      } catch (error) {
        // Skip malformed lines
        console.warn("SanitizationLogService: Skipping malformed line:", trimmed.substring(0, 100));
      }
    }

    return events;
  }

  /**
   * Parse a single NDJSON line into a SanitizationEvent
   *
   * Returns null if the line cannot be parsed or is missing required fields.
   */
  parseNdjsonLine(line: string): SanitizationEvent | null {
    try {
      const raw = JSON.parse(line) as RawSanitizationLogEntry;

      // Validate required fields
      if (!raw.timestamp || !raw.event) {
        return null;
      }

      // Parse timestamp
      const timestamp = new Date(raw.timestamp);
      if (isNaN(timestamp.getTime())) {
        return null;
      }

      // Validate and normalize event type
      const eventType = this.normalizeEventType(raw.event);
      if (!eventType) {
        return null;
      }

      // Normalize category
      const category = this.normalizeCategory(raw.category);

      return {
        timestamp,
        event: eventType,
        category,
        pattern: raw.pattern || "",
        content: raw.content || "",
        tool: raw.tool || "unknown",
        branch: raw.branch || "unknown",
        context: raw.context || "",
      };
    } catch (error) {
      return null;
    }
  }

  /**
   * Normalize event type string to SanitizationEventType
   */
  private normalizeEventType(event: string): SanitizationEventType | null {
    const normalized = event.toLowerCase();
    if (normalized === "blocked" || normalized === "warned" || normalized === "bypassed") {
      return normalized;
    }
    return null;
  }

  /**
   * Normalize category string to SanitizationCategory
   */
  private normalizeCategory(category: string): SanitizationCategory {
    const normalized = category?.toLowerCase() || "unknown";
    const validCategories: SanitizationCategory[] = [
      "destructive",
      "exfiltration",
      "privilege_escalation",
      "prompt_injection",
      "path_traversal",
      "allowlist",
    ];

    if (validCategories.includes(normalized as SanitizationCategory)) {
      return normalized as SanitizationCategory;
    }
    return "unknown";
  }

  /**
   * Start watching the log file for changes
   */
  private startWatching(): void {
    const pattern = new vscode.RelativePattern(
      this.workspaceRoot,
      ".nightgauge/logs/sanitization.log"
    );

    this.watcher = vscode.workspace.createFileSystemWatcher(pattern);

    this.watcher.onDidChange(async () => {
      await this.loadEvents();
    });

    this.watcher.onDidCreate(async () => {
      await this.loadEvents();
    });

    this.watcher.onDidDelete(() => {
      this.events = [];
      this._onEventsChanged.fire(this.events);
    });

    this.disposables.push(this.watcher);
  }

  /**
   * Get all events
   */
  getEvents(): SanitizationEvent[] {
    return this.events;
  }

  /**
   * Get filtered events based on filter state
   */
  getFilteredEvents(filters: FirewallFilterState): SanitizationEvent[] {
    let filtered = [...this.events];

    // Filter by event type
    if (filters.eventTypes.length > 0) {
      filtered = filtered.filter((e) => filters.eventTypes.includes(e.event));
    }

    // Filter by category
    if (filters.categories.length > 0) {
      filtered = filtered.filter((e) => filters.categories.includes(e.category));
    }

    // Filter by time range
    const now = new Date();
    switch (filters.timeRange) {
      case "hour":
        filtered = filtered.filter((e) => now.getTime() - e.timestamp.getTime() <= 60 * 60 * 1000);
        break;
      case "24h":
        filtered = filtered.filter(
          (e) => now.getTime() - e.timestamp.getTime() <= 24 * 60 * 60 * 1000
        );
        break;
      case "7d":
        filtered = filtered.filter(
          (e) => now.getTime() - e.timestamp.getTime() <= 7 * 24 * 60 * 60 * 1000
        );
        break;
      case "all":
        // No time filtering
        break;
    }

    // Filter by search text
    if (filters.searchText.trim()) {
      const search = filters.searchText.toLowerCase();
      filtered = filtered.filter(
        (e) =>
          e.content.toLowerCase().includes(search) ||
          e.pattern.toLowerCase().includes(search) ||
          e.tool.toLowerCase().includes(search) ||
          e.branch.toLowerCase().includes(search) ||
          e.context.toLowerCase().includes(search)
      );
    }

    return filtered;
  }

  /**
   * Get aggregates for filtered events
   */
  getAggregates(filters: FirewallFilterState): FirewallAggregates {
    const events = this.getFilteredEvents(filters);

    if (events.length === 0) {
      return { ...EMPTY_FIREWALL_AGGREGATES };
    }

    const aggregates: FirewallAggregates = {
      totalBlocked: 0,
      totalWarned: 0,
      totalBypassed: 0,
      mostCommonCategory: null,
      mostRecentEvent: null,
      categoryBreakdown: {
        destructive: 0,
        exfiltration: 0,
        privilege_escalation: 0,
        prompt_injection: 0,
        path_traversal: 0,
        allowlist: 0,
        unknown: 0,
      },
      toolBreakdown: {},
    };

    let mostRecent: Date | null = null;

    for (const event of events) {
      // Count by event type
      switch (event.event) {
        case "blocked":
          aggregates.totalBlocked++;
          break;
        case "warned":
          aggregates.totalWarned++;
          break;
        case "bypassed":
          aggregates.totalBypassed++;
          break;
      }

      // Count by category
      aggregates.categoryBreakdown[event.category]++;

      // Count by tool
      if (!aggregates.toolBreakdown[event.tool]) {
        aggregates.toolBreakdown[event.tool] = 0;
      }
      aggregates.toolBreakdown[event.tool]++;

      // Track most recent
      if (!mostRecent || event.timestamp > mostRecent) {
        mostRecent = event.timestamp;
      }
    }

    aggregates.mostRecentEvent = mostRecent;

    // Find most common category
    let maxCount = 0;
    for (const [category, count] of Object.entries(aggregates.categoryBreakdown)) {
      if (count > maxCount) {
        maxCount = count;
        aggregates.mostCommonCategory = category as SanitizationCategory;
      }
    }

    return aggregates;
  }

  /**
   * Get time series data for charts
   */
  getTimeSeriesData(
    filters: FirewallFilterState,
    granularity: TimeSeriesGranularity
  ): FirewallTimeSeriesPoint[] {
    const events = this.getFilteredEvents(filters);

    if (events.length === 0) {
      return [];
    }

    // Group events by time bucket
    const buckets = new Map<string, FirewallTimeSeriesPoint>();

    for (const event of events) {
      const bucketKey = this.getBucketKey(event.timestamp, granularity);

      if (!buckets.has(bucketKey)) {
        buckets.set(bucketKey, {
          timestamp: this.getBucketTimestamp(event.timestamp, granularity),
          blocked: 0,
          warned: 0,
          bypassed: 0,
        });
      }

      const bucket = buckets.get(bucketKey)!;
      switch (event.event) {
        case "blocked":
          bucket.blocked++;
          break;
        case "warned":
          bucket.warned++;
          break;
        case "bypassed":
          bucket.bypassed++;
          break;
      }
    }

    // Sort by timestamp and return
    return Array.from(buckets.values()).sort(
      (a, b) => a.timestamp.getTime() - b.timestamp.getTime()
    );
  }

  /**
   * Get bucket key for grouping events
   */
  private getBucketKey(date: Date, granularity: TimeSeriesGranularity): string {
    if (granularity === "hour") {
      return `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}-${date.getHours()}`;
    } else {
      return `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`;
    }
  }

  /**
   * Get bucket start timestamp
   */
  private getBucketTimestamp(date: Date, granularity: TimeSeriesGranularity): Date {
    if (granularity === "hour") {
      return new Date(
        date.getFullYear(),
        date.getMonth(),
        date.getDate(),
        date.getHours(),
        0,
        0,
        0
      );
    } else {
      return new Date(date.getFullYear(), date.getMonth(), date.getDate(), 0, 0, 0, 0);
    }
  }

  /**
   * Check if the log file exists
   */
  logFileExists(): boolean {
    return fs.existsSync(this.logFilePath);
  }

  /**
   * Get the log file path
   */
  getLogFilePath(): string {
    return this.logFilePath;
  }

  /**
   * Dispose of resources
   */
  dispose(): void {
    this._onEventsChanged.dispose();

    for (const disposable of this.disposables) {
      disposable.dispose();
    }
    this.disposables = [];

    if (this.watcher) {
      this.watcher.dispose();
      this.watcher = null;
    }
  }
}
