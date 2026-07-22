/**
 * Project Board Field Mapping Functions
 *
 * Maps labels to project field values for Priority and Size fields.
 * Status is managed directly via project board fields (not labels).
 *
 * @module utils/projectFieldMapping
 */

/**
 * Priority field values on the GitHub Project board
 */
export type PriorityValue = "P0" | "P1" | "P2" | "P3" | "";

/**
 * Status field values on the GitHub Project board
 */
export type StatusValue = "Backlog" | "Ready" | "In progress" | "In review" | "Done" | "";

/**
 * Size field values on the GitHub Project board
 */
export type SizeValue = "XS" | "S" | "M" | "L" | "XL" | "";

/**
 * Priority labels that can be applied to issues
 */
export type PriorityLabel =
  "priority:critical" | "priority:high" | "priority:medium" | "priority:low";

/**
 * Size labels that can be applied to issues
 */
export type SizeLabel = "size:XS" | "size:S" | "size:M" | "size:L" | "size:XL";

/**
 * Status labels that can be applied to issues for legacy/label-fallback support
 */
export type StatusLabel =
  "status:ready" | "status:in-progress" | "status:in-review" | "status:done" | "status:backlog";

// ============================================================================
// Forward Mappings: Label → Field Value
// ============================================================================

/**
 * Map priority label to project Priority field value
 *
 * @param label - The priority label (e.g., 'priority:high')
 * @returns The Priority field value (e.g., 'P1') or empty string if no mapping
 */
export function mapPriorityLabel(label: string | null | undefined): PriorityValue {
  if (!label) {
    return "";
  }

  switch (label) {
    case "priority:critical":
      return "P0";
    case "priority:high":
      return "P1";
    case "priority:medium":
      return "P2";
    case "priority:low":
      return "P3";
    default:
      return "";
  }
}

/**
 * Map status label to project Status field value
 *
 * @param label - The status label (e.g., 'status:ready')
 * @returns The Status field value (e.g., 'Ready') or empty string if no mapping
 */
export function mapStatusLabel(label: string | null | undefined): StatusValue {
  if (!label) {
    return "";
  }

  switch (label) {
    case "status:ready":
      return "Ready";
    case "status:in-progress":
      return "In progress";
    case "status:in-review":
      return "In review";
    case "status:done":
      return "Done";
    case "status:backlog":
      return "Backlog";
    default:
      return "";
  }
}

/**
 * Map size label to project Size field value
 *
 * @param label - The size label (e.g., 'size:M')
 * @returns The Size field value (e.g., 'M') or empty string if no mapping
 */
export function mapSizeLabel(label: string | null | undefined): SizeValue {
  if (!label) {
    return "";
  }

  switch (label) {
    case "size:XS":
      return "XS";
    case "size:S":
      return "S";
    case "size:M":
      return "M";
    case "size:L":
      return "L";
    case "size:XL":
      return "XL";
    default:
      return "";
  }
}

// ============================================================================
// Label Extraction Helpers
// ============================================================================

/**
 * Extract priority label from an array of labels
 *
 * @param labels - Array of label strings
 * @returns The first priority label found, or undefined
 */
export function extractPriorityLabel(labels: string[]): PriorityLabel | undefined {
  const priorityLabels: PriorityLabel[] = [
    "priority:critical",
    "priority:high",
    "priority:medium",
    "priority:low",
  ];

  return labels.find((label) => priorityLabels.includes(label as PriorityLabel)) as
    PriorityLabel | undefined;
}

/**
 * Extract size label from an array of labels
 *
 * @param labels - Array of label strings
 * @returns The first size label found, or undefined
 */
export function extractSizeLabel(labels: string[]): SizeLabel | undefined {
  const sizeLabels: SizeLabel[] = ["size:XS", "size:S", "size:M", "size:L", "size:XL"];

  return labels.find((label) => sizeLabels.includes(label as SizeLabel)) as SizeLabel | undefined;
}

/**
 * Extract status label from an array of labels
 *
 * @param labels - Array of label strings
 * @returns The first status label found, or undefined
 */
export function extractStatusLabel(labels: string[]): StatusLabel | undefined {
  const statusLabels: StatusLabel[] = [
    "status:ready",
    "status:in-progress",
    "status:in-review",
    "status:done",
    "status:backlog",
  ];

  return labels.find((label) => statusLabels.includes(label as StatusLabel)) as
    StatusLabel | undefined;
}

// ============================================================================
// Validation Helpers
// ============================================================================

/**
 * Check if a string is a valid priority label
 */
export function isPriorityLabel(label: string): label is PriorityLabel {
  return ["priority:critical", "priority:high", "priority:medium", "priority:low"].includes(label);
}

/**
 * Check if a string is a valid size label
 */
export function isSizeLabel(label: string): label is SizeLabel {
  return ["size:XS", "size:S", "size:M", "size:L", "size:XL"].includes(label);
}

/**
 * Check if a string is a valid status label
 */
export function isStatusLabel(label: string): label is StatusLabel {
  return [
    "status:ready",
    "status:in-progress",
    "status:in-review",
    "status:done",
    "status:backlog",
  ].includes(label);
}

/**
 * Check if a string is a valid priority value
 */
export function isPriorityValue(value: string): value is PriorityValue {
  return ["P0", "P1", "P2", "P3", ""].includes(value);
}

/**
 * Check if a string is a valid status value
 */
export function isStatusValue(value: string): value is StatusValue {
  return ["Backlog", "Ready", "In progress", "In review", "Done", ""].includes(value);
}

/**
 * Check if a string is a valid size value
 */
export function isSizeValue(value: string): value is SizeValue {
  return ["XS", "S", "M", "L", "XL", ""].includes(value);
}
