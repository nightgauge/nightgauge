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
 * The Status column labels the nightgauge provisioner writes.
 *
 * Mirrors the Go constants in `internal/state/board_state.go`
 * (`StatusBacklog` … `StatusDone`) so both layers name the same columns the
 * same way. Use these instead of retyping a literal — an in-line
 * `"In Progress"` is how #623 happened.
 */
export const BOARD_STATUS = {
  backlog: "Backlog",
  ready: "Ready",
  inProgress: "In progress",
  inReview: "In review",
  done: "Done",
} as const satisfies Record<string, StatusValue>;

/**
 * Compare two board Status labels for column identity, ignoring
 * capitalization.
 *
 * **Never compare a status READ off a board with `===`** (#623). The
 * `BOARD_STATUS` labels above are what the nightgauge provisioner writes, but
 * a board's actual option labels are whatever its creator typed — a hand-made
 * board commonly spells the same column "In Progress". Reads return that raw
 * label verbatim (Go's `gh.BoardService.ListItems` copies the single-select
 * option name straight into `BoardItem.Status`, and it reaches TypeScript
 * unchanged over IPC), so an exact comparison silently answers "different
 * column" for a board that merely capitalizes differently, and the caller
 * takes the wrong branch — with no error, no log, and no card.
 *
 * This is the TypeScript counterpart of Go's `state.BoardStatus.EqualFold`,
 * and exists for the same reason: one notion of column identity on both
 * sides of the IPC boundary.
 *
 * Nullish operands normalize to `""`, so a missing status matches only the
 * empty label.
 *
 * @param a - A board Status label (raw board read, or a `BOARD_STATUS` value)
 * @param b - The label to compare against
 * @returns true when both name the same column
 */
export function boardStatusEquals(
  a: string | null | undefined,
  b: string | null | undefined
): boolean {
  return (a ?? "").toLowerCase() === (b ?? "").toLowerCase();
}

/**
 * A real Status column — `StatusValue` minus the `""` absent sentinel.
 *
 * Derived from `BOARD_STATUS` so the vocabulary is declared exactly once.
 */
export type BoardStatusValue = (typeof BOARD_STATUS)[keyof typeof BOARD_STATUS];

/**
 * Folded label → canonical label. Built from `BOARD_STATUS` itself, so a column
 * added there is canonicalizable immediately and the two cannot drift apart.
 */
const CANONICAL_BY_FOLDED_LABEL = new Map<string, BoardStatusValue>(
  Object.values(BOARD_STATUS).map((label) => [label.toLowerCase(), label] as const)
);

/**
 * Resolve a raw board Status label to its one canonical spelling (#623).
 *
 * Use this — not `boardStatusEquals` — wherever a board read is about to be
 * **kept** rather than merely compared. Folding at a comparison answers "is
 * this the same column?" but still leaves the caller holding the raw,
 * provenance-dependent label; canonicalizing at the boundary means everything
 * downstream holds the one spelling and may compare it exactly forever after.
 * That is the stronger contract, and it is the only one that is safe when the
 * value is stored, returned, or widened to `ProjectBoardStatus`.
 *
 * Matching ignores capitalization and surrounding whitespace, because both are
 * board-provenance artifacts: nightgauge's provisioner writes "In progress"
 * (`DefaultFieldSchema` in `internal/github/project.go`) while a hand-made
 * board commonly spells the same column "In Progress", and Go's
 * `gh.BoardService.ListItems` copies whichever one it finds into
 * `BoardItem.Status` verbatim.
 *
 * **An unrecognized label returns `null` — it is never coerced.** A board may
 * carry columns nightgauge knows nothing about ("Blocked", "Won't do"), and
 * quietly rounding one of those to the nearest known column would assert a
 * status the board never held. `null` means "not a column I can name", and the
 * caller decides what to do with that; it is deliberately indistinguishable
 * from an absent status, since neither yields a usable column.
 *
 * @param raw - A Status label as read off a board, in any capitalization
 * @returns The canonical label, or `null` if it names no known column
 */
export function canonicalizeBoardStatus(raw: string | null | undefined): BoardStatusValue | null {
  return CANONICAL_BY_FOLDED_LABEL.get((raw ?? "").trim().toLowerCase()) ?? null;
}

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
