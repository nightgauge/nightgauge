/**
 * Unit tests for Project Field Mapping Functions
 *
 * Tests the TypeScript implementation of label-to-field mappings for
 * Priority and Size fields. Status is managed directly via project
 * board fields (not labels).
 *
 * Test coverage includes:
 * - Forward mappings: label → field value (priority, size)
 * - Label extraction from arrays
 * - Type validation functions
 * - Edge cases (null, undefined, empty, unknown values)
 */

import { describe, it, expect } from "vitest";
import {
  // Forward mappings
  mapPriorityLabel,
  mapSizeLabel,
  // Label extraction
  extractPriorityLabel,
  extractSizeLabel,
  // Type validation
  isPriorityLabel,
  isSizeLabel,
  isPriorityValue,
  isStatusValue,
  isSizeValue,
  // Board status identity (#623)
  BOARD_STATUS,
  boardStatusEquals,
  canonicalizeBoardStatus,
  // Types
  type PriorityLabel,
  type SizeLabel,
} from "../../src/utils/projectFieldMapping";

// ============================================================================
// Forward Mappings: Label → Field Value
// ============================================================================

describe("projectFieldMapping", () => {
  describe("mapPriorityLabel", () => {
    it("should map priority:critical to P0", () => {
      expect(mapPriorityLabel("priority:critical")).toBe("P0");
    });

    it("should map priority:high to P1", () => {
      expect(mapPriorityLabel("priority:high")).toBe("P1");
    });

    it("should map priority:medium to P2", () => {
      expect(mapPriorityLabel("priority:medium")).toBe("P2");
    });

    it("should map priority:low to P3", () => {
      expect(mapPriorityLabel("priority:low")).toBe("P3");
    });

    it("should return empty string for unknown labels", () => {
      expect(mapPriorityLabel("priority:urgent")).toBe("");
      expect(mapPriorityLabel("unknown")).toBe("");
      expect(mapPriorityLabel("type:feature")).toBe("");
    });

    it("should handle null input gracefully", () => {
      expect(mapPriorityLabel(null)).toBe("");
    });

    it("should handle undefined input gracefully", () => {
      expect(mapPriorityLabel(undefined)).toBe("");
    });

    it("should handle empty string input gracefully", () => {
      expect(mapPriorityLabel("")).toBe("");
    });

    it("should be case-sensitive", () => {
      expect(mapPriorityLabel("PRIORITY:HIGH")).toBe("");
      expect(mapPriorityLabel("Priority:High")).toBe("");
    });
  });

  describe("mapSizeLabel", () => {
    it("should map size:XS to XS", () => {
      expect(mapSizeLabel("size:XS")).toBe("XS");
    });

    it("should map size:S to S", () => {
      expect(mapSizeLabel("size:S")).toBe("S");
    });

    it("should map size:M to M", () => {
      expect(mapSizeLabel("size:M")).toBe("M");
    });

    it("should map size:L to L", () => {
      expect(mapSizeLabel("size:L")).toBe("L");
    });

    it("should map size:XL to XL", () => {
      expect(mapSizeLabel("size:XL")).toBe("XL");
    });

    it("should return empty string for unknown labels", () => {
      expect(mapSizeLabel("size:XXL")).toBe("");
      expect(mapSizeLabel("size:small")).toBe("");
      expect(mapSizeLabel("unknown")).toBe("");
    });

    it("should handle null input gracefully", () => {
      expect(mapSizeLabel(null)).toBe("");
    });

    it("should handle undefined input gracefully", () => {
      expect(mapSizeLabel(undefined)).toBe("");
    });

    it("should handle empty string input gracefully", () => {
      expect(mapSizeLabel("")).toBe("");
    });

    it("should be case-sensitive for size values", () => {
      expect(mapSizeLabel("size:xs")).toBe("");
      expect(mapSizeLabel("size:m")).toBe("");
      expect(mapSizeLabel("SIZE:M")).toBe("");
    });
  });

  // ============================================================================
  // Label Extraction from Arrays
  // ============================================================================

  describe("extractPriorityLabel", () => {
    it("should extract priority label from mixed labels", () => {
      expect(extractPriorityLabel(["type:feature", "priority:high", "size:M"])).toBe(
        "priority:high"
      );
    });

    it("should return first priority label when multiple exist", () => {
      expect(extractPriorityLabel(["priority:high", "priority:low"])).toBe("priority:high");
    });

    it("should return undefined when no priority label exists", () => {
      expect(extractPriorityLabel(["type:bug", "size:S"])).toBeUndefined();
    });

    it("should handle empty array", () => {
      expect(extractPriorityLabel([])).toBeUndefined();
    });

    it("should extract all priority label types", () => {
      expect(extractPriorityLabel(["priority:critical"])).toBe("priority:critical");
      expect(extractPriorityLabel(["priority:high"])).toBe("priority:high");
      expect(extractPriorityLabel(["priority:medium"])).toBe("priority:medium");
      expect(extractPriorityLabel(["priority:low"])).toBe("priority:low");
    });
  });

  describe("extractSizeLabel", () => {
    it("should extract size label from mixed labels", () => {
      expect(extractSizeLabel(["type:feature", "priority:high", "size:M"])).toBe("size:M");
    });

    it("should return first size label when multiple exist", () => {
      expect(extractSizeLabel(["size:M", "size:L"])).toBe("size:M");
    });

    it("should return undefined when no size label exists", () => {
      expect(extractSizeLabel(["type:bug", "priority:low"])).toBeUndefined();
    });

    it("should handle empty array", () => {
      expect(extractSizeLabel([])).toBeUndefined();
    });

    it("should extract all size label types", () => {
      expect(extractSizeLabel(["size:XS"])).toBe("size:XS");
      expect(extractSizeLabel(["size:S"])).toBe("size:S");
      expect(extractSizeLabel(["size:M"])).toBe("size:M");
      expect(extractSizeLabel(["size:L"])).toBe("size:L");
      expect(extractSizeLabel(["size:XL"])).toBe("size:XL");
    });
  });

  // ============================================================================
  // Type Validation Functions
  // ============================================================================

  describe("isPriorityLabel", () => {
    it("should return true for valid priority labels", () => {
      expect(isPriorityLabel("priority:critical")).toBe(true);
      expect(isPriorityLabel("priority:high")).toBe(true);
      expect(isPriorityLabel("priority:medium")).toBe(true);
      expect(isPriorityLabel("priority:low")).toBe(true);
    });

    it("should return false for invalid priority labels", () => {
      expect(isPriorityLabel("priority:urgent")).toBe(false);
      expect(isPriorityLabel("type:feature")).toBe(false);
      expect(isPriorityLabel("status:ready")).toBe(false);
      expect(isPriorityLabel("")).toBe(false);
    });
  });

  describe("isSizeLabel", () => {
    it("should return true for valid size labels", () => {
      expect(isSizeLabel("size:XS")).toBe(true);
      expect(isSizeLabel("size:S")).toBe(true);
      expect(isSizeLabel("size:M")).toBe(true);
      expect(isSizeLabel("size:L")).toBe(true);
      expect(isSizeLabel("size:XL")).toBe(true);
    });

    it("should return false for invalid size labels", () => {
      expect(isSizeLabel("size:XXL")).toBe(false);
      expect(isSizeLabel("size:small")).toBe(false);
      expect(isSizeLabel("type:feature")).toBe(false);
      expect(isSizeLabel("")).toBe(false);
    });
  });

  describe("isPriorityValue", () => {
    it("should return true for valid priority values", () => {
      expect(isPriorityValue("P0")).toBe(true);
      expect(isPriorityValue("P1")).toBe(true);
      expect(isPriorityValue("P2")).toBe(true);
      expect(isPriorityValue("P3")).toBe(true);
      expect(isPriorityValue("")).toBe(true);
    });

    it("should return false for invalid priority values", () => {
      expect(isPriorityValue("P4")).toBe(false);
      expect(isPriorityValue("High")).toBe(false);
      expect(isPriorityValue("p0")).toBe(false);
    });
  });

  describe("isStatusValue", () => {
    it("should return true for valid status values", () => {
      expect(isStatusValue("Backlog")).toBe(true);
      expect(isStatusValue("Ready")).toBe(true);
      expect(isStatusValue("In progress")).toBe(true);
      expect(isStatusValue("In review")).toBe(true);
      expect(isStatusValue("Done")).toBe(true);
      expect(isStatusValue("")).toBe(true);
    });

    it("should return false for invalid status values", () => {
      expect(isStatusValue("Pending")).toBe(false);
      expect(isStatusValue("ready")).toBe(false);
      expect(isStatusValue("IN PROGRESS")).toBe(false);
    });
  });

  describe("isSizeValue", () => {
    it("should return true for valid size values", () => {
      expect(isSizeValue("XS")).toBe(true);
      expect(isSizeValue("S")).toBe(true);
      expect(isSizeValue("M")).toBe(true);
      expect(isSizeValue("L")).toBe(true);
      expect(isSizeValue("XL")).toBe(true);
      expect(isSizeValue("")).toBe(true);
    });

    it("should return false for invalid size values", () => {
      expect(isSizeValue("XXL")).toBe(false);
      expect(isSizeValue("xs")).toBe(false);
      expect(isSizeValue("Small")).toBe(false);
    });
  });

  // ============================================================================
  // Board Status Identity (#623)
  // ============================================================================

  describe("BOARD_STATUS", () => {
    it("spells every column exactly as the Go provisioner writes it", () => {
      // Mirrors internal/state/board_state.go's StatusBacklog … StatusDone,
      // which internal/state/board_status_case_test.go cross-checks against
      // DefaultFieldSchema(). Note the lowercase 'p' and 'r': the provisioned
      // columns are "In progress" and "In review", NOT title case. Retyping
      // these literals by hand is exactly how #623 happened.
      expect(BOARD_STATUS).toEqual({
        backlog: "Backlog",
        ready: "Ready",
        inProgress: "In progress",
        inReview: "In review",
        done: "Done",
      });
    });
  });

  describe("boardStatusEquals", () => {
    it("matches the same column regardless of capitalization", () => {
      // The TS counterpart of Go's state.BoardStatus.EqualFold. A board's raw
      // option label is whatever its creator typed, so identity must fold.
      const spellings = ["In progress", "In Progress", "in progress", "IN PROGRESS", "In PrOgReSs"];
      for (const a of spellings) {
        for (const b of spellings) {
          expect(boardStatusEquals(a, b)).toBe(true);
        }
        expect(boardStatusEquals(a, BOARD_STATUS.inProgress)).toBe(true);
      }
    });

    it("still distinguishes DIFFERENT columns", () => {
      // Folding is not "everything matches" — a real column mismatch must
      // still read as a mismatch, in either argument order.
      const distinct = [
        BOARD_STATUS.backlog,
        BOARD_STATUS.ready,
        BOARD_STATUS.inProgress,
        BOARD_STATUS.inReview,
        BOARD_STATUS.done,
      ];
      for (const a of distinct) {
        for (const b of distinct) {
          expect(boardStatusEquals(a, b)).toBe(a === b);
        }
      }
      // Near-misses that share a prefix must not collapse together.
      expect(boardStatusEquals("In review", "In progress")).toBe(false);
      expect(boardStatusEquals("In Review", "In Progress")).toBe(false);
      expect(boardStatusEquals("Ready", "Ready to ship")).toBe(false);
    });

    it("treats a missing status as the empty label", () => {
      expect(boardStatusEquals(undefined, "")).toBe(true);
      expect(boardStatusEquals(null, "")).toBe(true);
      expect(boardStatusEquals(undefined, null)).toBe(true);
      expect(boardStatusEquals(undefined, BOARD_STATUS.inProgress)).toBe(false);
      expect(boardStatusEquals(null, BOARD_STATUS.ready)).toBe(false);
    });

    it("is symmetric", () => {
      expect(boardStatusEquals("in progress", BOARD_STATUS.inProgress)).toBe(
        boardStatusEquals(BOARD_STATUS.inProgress, "in progress")
      );
      expect(boardStatusEquals("Done", BOARD_STATUS.ready)).toBe(
        boardStatusEquals(BOARD_STATUS.ready, "Done")
      );
    });
  });

  describe("canonicalizeBoardStatus", () => {
    // The provenance matrix. Every row is the SAME column, spelled the way one
    // real source spells it; every row must resolve to the one canonical value.
    const PROVENANCE: ReadonlyArray<[string, string[]]> = [
      // nightgauge-provisioned spelling first — DefaultFieldSchema in
      // internal/github/project.go — then the hand-made-board capitalizations.
      ["Backlog", ["Backlog", "backlog", "BACKLOG", "BackLog"]],
      ["Ready", ["Ready", "ready", "READY", "rEaDy"]],
      ["In progress", ["In progress", "In Progress", "in progress", "IN PROGRESS", "iN pRoGrEsS"]],
      ["In review", ["In review", "In Review", "in review", "IN REVIEW", "In ReViEw"]],
      ["Done", ["Done", "done", "DONE", "DoNe"]],
    ];

    it("resolves every provenance spelling to the one canonical label", () => {
      for (const [canonical, spellings] of PROVENANCE) {
        for (const spelling of spellings) {
          expect(canonicalizeBoardStatus(spelling)).toBe(canonical);
        }
      }
    });

    it("returns the provisioner's spelling, not the caller's", () => {
      // The whole point of a canonicalizer over a fold: the value that comes
      // back is safe to store, return and compare exactly. A hand-made board's
      // "In Progress" must not survive the boundary.
      expect(canonicalizeBoardStatus("In Progress")).toBe("In progress");
      expect(canonicalizeBoardStatus("In Progress")).not.toBe("In Progress");
      expect(canonicalizeBoardStatus("In Review")).toBe("In review");
      expect(canonicalizeBoardStatus("In Review")).not.toBe("In Review");
    });

    it("is idempotent — canonical in, same value out", () => {
      for (const canonical of Object.values(BOARD_STATUS)) {
        expect(canonicalizeBoardStatus(canonical)).toBe(canonical);
        expect(canonicalizeBoardStatus(canonicalizeBoardStatus(canonical))).toBe(canonical);
      }
    });

    it("REJECTS an unknown column instead of coercing it", () => {
      // A board may carry columns nightgauge knows nothing about. Rounding one
      // to the nearest known column would assert a status the board never
      // held, which is strictly worse than admitting we cannot name it.
      for (const unknown of [
        "Blocked",
        "Won't do",
        "Triage",
        "Icebox",
        "In progress review", // superset of a real column
        "progress", // substring of a real column
        "In  progress", // double space — not a capitalization difference
        "Readyy",
        "Ready to ship",
      ]) {
        expect(canonicalizeBoardStatus(unknown)).toBeNull();
      }
    });

    it("never collapses two distinct columns onto one another", () => {
      // Folding must not widen the vocabulary: five columns in, five out.
      const canonicalized = Object.values(BOARD_STATUS).map((label) =>
        canonicalizeBoardStatus(label.toUpperCase())
      );
      expect(new Set(canonicalized).size).toBe(Object.values(BOARD_STATUS).length);
      expect(canonicalizeBoardStatus("In Review")).not.toBe(canonicalizeBoardStatus("In Progress"));
    });

    it("treats an absent status as naming no column", () => {
      // "" is StatusValue's absent sentinel, not a column, so it is null here
      // — the same answer as undefined/null. Callers fall through either way.
      expect(canonicalizeBoardStatus(undefined)).toBeNull();
      expect(canonicalizeBoardStatus(null)).toBeNull();
      expect(canonicalizeBoardStatus("")).toBeNull();
      expect(canonicalizeBoardStatus("   ")).toBeNull();
    });

    it("ignores surrounding whitespace", () => {
      expect(canonicalizeBoardStatus("  In Progress  ")).toBe("In progress");
      expect(canonicalizeBoardStatus("\tDone\n")).toBe("Done");
    });

    it("agrees with boardStatusEquals on every known column", () => {
      // The two helpers must not disagree about column identity, or a folded
      // comparison and a canonicalized read would take different branches.
      for (const [, spellings] of PROVENANCE) {
        for (const spelling of spellings) {
          const canonical = canonicalizeBoardStatus(spelling);
          expect(canonical).not.toBeNull();
          expect(boardStatusEquals(spelling, canonical)).toBe(true);
        }
      }
    });
  });

  // ============================================================================
  // Shell Script Parity Tests
  // ============================================================================

  describe("shell script parity", () => {
    describe("matches add-to-project.sh map_priority_label()", () => {
      const shellPriorityMappings: Array<[string, string]> = [
        ["priority:critical", "P0"],
        ["priority:high", "P1"],
        ["priority:medium", "P2"],
        ["priority:low", "P3"],
        ["unknown", ""],
        ["", ""],
      ];

      shellPriorityMappings.forEach(([input, expected]) => {
        it(`map_priority_label("${input}") returns "${expected}"`, () => {
          expect(mapPriorityLabel(input)).toBe(expected);
        });
      });
    });

    describe("matches add-to-project.sh map_size_label()", () => {
      const shellSizeMappings: Array<[string, string]> = [
        ["size:XS", "XS"],
        ["size:S", "S"],
        ["size:M", "M"],
        ["size:L", "L"],
        ["size:XL", "XL"],
        ["unknown", ""],
        ["", ""],
      ];

      shellSizeMappings.forEach(([input, expected]) => {
        it(`map_size_label("${input}") returns "${expected}"`, () => {
          expect(mapSizeLabel(input)).toBe(expected);
        });
      });
    });
  });
});
