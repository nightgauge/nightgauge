/**
 * FirewallTypes - Type definitions for the Prompt Injection Firewall Dashboard
 *
 * Defines interfaces for sanitization events, filters, and aggregates
 * used by the dashboard to display firewall activity.
 *
 * @see docs/SECURITY.md for sanitization framework documentation
 * @see Issue #387 - Prompt Injection Firewall Dashboard
 */

/**
 * Event types from the sanitization layer
 */
export type SanitizationEventType = "blocked" | "warned" | "bypassed";

/**
 * Categories of security events from sanitization-patterns.sh
 */
export type SanitizationCategory =
  | "destructive"
  | "exfiltration"
  | "privilege_escalation"
  | "prompt_injection"
  | "path_traversal"
  | "allowlist"
  | "unknown";

/**
 * A single sanitization event parsed from the NDJSON log
 *
 * Matches the schema output by sanitization-logger.sh
 */
export interface SanitizationEvent {
  /** ISO 8601 timestamp */
  timestamp: Date;
  /** Event type: blocked, warned, or bypassed */
  event: SanitizationEventType;
  /** Security category of the matched pattern */
  category: SanitizationCategory;
  /** Regex pattern that triggered the event */
  pattern: string;
  /** The command or content that was checked (truncated to 500 chars) */
  content: string;
  /** Tool name that triggered the event (e.g., "Bash") */
  tool: string;
  /** Git branch where the event occurred */
  branch: string;
  /** Optional context (e.g., "feature-dev stage") */
  context: string;
}

/**
 * Raw JSON structure from the NDJSON log file
 *
 * Intermediate type for parsing - converted to SanitizationEvent
 */
export interface RawSanitizationLogEntry {
  timestamp: string;
  event: string;
  category: string;
  pattern: string;
  content: string;
  tool: string;
  branch: string;
  context: string;
}

/**
 * Time range filter options for the dashboard
 */
export type TimeRangeFilter = "hour" | "24h" | "7d" | "all";

/**
 * Filter state for the firewall dashboard
 *
 * Persisted to workspace storage for session continuity
 */
export interface FirewallFilterState {
  /** Selected event types to display */
  eventTypes: SanitizationEventType[];
  /** Selected categories to display */
  categories: SanitizationCategory[];
  /** Time range filter */
  timeRange: TimeRangeFilter;
  /** Text search filter */
  searchText: string;
}

/**
 * Default filter state - show all events
 */
export const DEFAULT_FIREWALL_FILTERS: FirewallFilterState = {
  eventTypes: ["blocked", "warned", "bypassed"],
  categories: [
    "destructive",
    "exfiltration",
    "privilege_escalation",
    "prompt_injection",
    "path_traversal",
    "allowlist",
    "unknown",
  ],
  timeRange: "all",
  searchText: "",
};

/**
 * Aggregated metrics for the firewall dashboard summary cards
 */
export interface FirewallAggregates {
  /** Total blocked events in filtered range */
  totalBlocked: number;
  /** Total warned events in filtered range */
  totalWarned: number;
  /** Total bypassed events in filtered range */
  totalBypassed: number;
  /** Most common category in filtered range */
  mostCommonCategory: SanitizationCategory | null;
  /** Most recent event timestamp */
  mostRecentEvent: Date | null;
  /** Event counts by category */
  categoryBreakdown: Record<SanitizationCategory, number>;
  /** Event counts by tool */
  toolBreakdown: Record<string, number>;
}

/**
 * Default aggregates - all zeros
 */
export const EMPTY_FIREWALL_AGGREGATES: FirewallAggregates = {
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

/**
 * Time series data point for trend charts
 */
export interface FirewallTimeSeriesPoint {
  /** Bucket timestamp (start of hour/day) */
  timestamp: Date;
  /** Blocked count in this bucket */
  blocked: number;
  /** Warned count in this bucket */
  warned: number;
  /** Bypassed count in this bucket */
  bypassed: number;
}

/**
 * Granularity for time series aggregation
 */
export type TimeSeriesGranularity = "hour" | "day";

/**
 * All display labels for categories
 */
export const CATEGORY_LABELS: Record<SanitizationCategory, string> = {
  destructive: "Destructive",
  exfiltration: "Exfiltration",
  privilege_escalation: "Privilege Escalation",
  prompt_injection: "Prompt Injection",
  path_traversal: "Path Traversal",
  allowlist: "Allowlist",
  unknown: "Unknown",
};

/**
 * Colors for category display (matching dashboard theme)
 */
export const CATEGORY_COLORS: Record<SanitizationCategory, string> = {
  destructive: "rgba(255, 99, 132, 0.8)", // Red
  exfiltration: "rgba(255, 159, 64, 0.8)", // Orange
  privilege_escalation: "rgba(153, 102, 255, 0.8)", // Purple
  prompt_injection: "rgba(255, 206, 86, 0.8)", // Yellow
  path_traversal: "rgba(54, 162, 235, 0.8)", // Blue
  allowlist: "rgba(75, 192, 75, 0.8)", // Green
  unknown: "rgba(128, 128, 128, 0.8)", // Gray
};

/**
 * Colors for event types
 */
export const EVENT_TYPE_COLORS: Record<SanitizationEventType, string> = {
  blocked: "rgba(255, 99, 132, 0.8)", // Red
  warned: "rgba(255, 206, 86, 0.8)", // Yellow
  bypassed: "rgba(75, 192, 75, 0.8)", // Green
};

/**
 * A suggested allowlist or safe_directory entry generated from blocked/warned events
 *
 * @see Issue #786 - Firewall Learning Mode
 */
export interface AllowlistSuggestion {
  /** The suggested pattern (regex for allowlist, path for safe_directory) */
  pattern: string;
  /** Type of suggestion: 'allowlist' (command regex) or 'safe_directory' (path) */
  type: "allowlist" | "safe_directory";
  /** Number of blocked/warned events this would cover */
  frequency: number;
  /** Timestamp of most recent matching event */
  lastOccurrence: Date;
  /** Example content from the events that triggered this suggestion */
  exampleContent: string;
  /** Human-readable description of what this pattern permits */
  description: string;
}
