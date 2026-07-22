import { describe, it, expect } from "vitest";
import { readFileSync } from "fs";
import { resolve } from "path";

const FIXTURES_DIR = resolve(__dirname, "../../../../tests/fixtures/contracts");

function loadFixture(name: string): Record<string, unknown> {
  return JSON.parse(readFileSync(resolve(FIXTURES_DIR, name), "utf-8"));
}

// ─── Field declarations ────────────────────────────────────────────────────
// Each set of fields must be kept in sync with the corresponding interface
// in src/services/IpcClient.ts. When Go adds a field, the fixture is updated,
// and the "no unknown fields" test below will fail until the field is added
// to either TS_FIELDS or GO_ONLY_FIELDS.

const BOARD_ITEM_TS_FIELDS: Record<string, string> = {
  id: "string",
  number: "number",
  title: "string",
  state: "string",
  status: "string",
  priority: "string",
  size: "string",
  labels: "object", // array
  repo: "string",
  url: "string",
  isEpic: "boolean",
  blockedBy: "object", // array
  blocking: "object", // array
  subIssues: "object", // array
  parentIssueNumber: "number",
  parentIssueTitle: "string",
};

// Fields Go serializes but TS BoardItem does not declare.
// When TS adopts a field, move it from here to BOARD_ITEM_TS_FIELDS.
const BOARD_ITEM_GO_ONLY_FIELDS = new Set([
  "nodeId",
  "pipelineStage",
  "createdAt",
  "updatedAt",
  "isPR",
]);

const BOARD_ITEM_ALL_KNOWN = new Set([
  ...Object.keys(BOARD_ITEM_TS_FIELDS),
  ...BOARD_ITEM_GO_ONLY_FIELDS,
]);

const ISSUE_TS_FIELDS: Record<string, string> = {
  number: "number",
  title: "string",
  body: "string",
  state: "string",
  labels: "object", // array
  assignees: "object", // array
  url: "string",
  blockedBy: "object", // array
  blocking: "object", // array
  subIssues: "object", // array
};

// Optional TS fields that may or may not be in the fixture
const ISSUE_TS_OPTIONAL: Record<string, string> = {
  id: "string",
  stateReason: "string",
  isEpic: "boolean",
  parentIssueId: "string",
  parentIssueNumber: "number",
  milestone: "string",
};

// Fields Go serializes but TS IssueDetail does not declare.
const ISSUE_GO_ONLY_FIELDS = new Set(["nodeId", "repo"]);

const ISSUE_ALL_KNOWN = new Set([
  ...Object.keys(ISSUE_TS_FIELDS),
  ...Object.keys(ISSUE_TS_OPTIONAL),
  ...ISSUE_GO_ONLY_FIELDS,
]);

// ─── Nested ref schemas ────────────────────────────────────────────────────

const BLOCKING_REF_FIELDS: Record<string, string> = {
  number: "number",
  title: "string",
  state: "string",
  repo: "string",
};

const BLOCKING_REF_GO_ONLY = new Set(["nodeId"]);
const BLOCKING_REF_ALL_KNOWN = new Set([
  ...Object.keys(BLOCKING_REF_FIELDS),
  ...BLOCKING_REF_GO_ONLY,
]);

const SUB_ISSUE_REF_FIELDS: Record<string, string> = {
  number: "number",
  title: "string",
  state: "string",
  repo: "string",
  labels: "object", // array
};

const SUB_ISSUE_REF_GO_ONLY = new Set(["nodeId"]);
const SUB_ISSUE_REF_ALL_KNOWN = new Set([
  ...Object.keys(SUB_ISSUE_REF_FIELDS),
  ...SUB_ISSUE_REF_GO_ONLY,
]);

// ─── Helper ────────────────────────────────────────────────────────────────

function validateFields(
  obj: Record<string, unknown>,
  requiredFields: Record<string, string>,
  allKnown: Set<string>,
  label: string
) {
  // Every required TS field must exist with the expected type
  for (const [field, expectedType] of Object.entries(requiredFields)) {
    expect(obj, `${label}: missing required field "${field}"`).toHaveProperty(field);
    expect(typeof obj[field], `${label}: "${field}" type mismatch`).toBe(expectedType);
  }

  // Every fixture key must be in the known set (catches Go additions)
  for (const key of Object.keys(obj)) {
    expect(
      allKnown.has(key),
      `${label}: unknown field "${key}" in fixture — add to TS interface or GO_ONLY_FIELDS`
    ).toBe(true);
  }
}

function validateRefArray(
  arr: unknown,
  refFields: Record<string, string>,
  allKnown: Set<string>,
  label: string
) {
  expect(Array.isArray(arr), `${label}: expected array`).toBe(true);
  for (const [i, item] of (arr as unknown[]).entries()) {
    validateFields(item as Record<string, unknown>, refFields, allKnown, `${label}[${i}]`);
  }
}

// ─── BoardItem contract ────────────────────────────────────────────────────

describe("BoardItem contract (Go → TypeScript)", () => {
  const fixture = loadFixture("board-item.json");

  it("contains all required TS fields with correct types", () => {
    validateFields(fixture, BOARD_ITEM_TS_FIELDS, BOARD_ITEM_ALL_KNOWN, "BoardItem");
  });

  it("has no unknown top-level fields", () => {
    for (const key of Object.keys(fixture)) {
      expect(
        BOARD_ITEM_ALL_KNOWN.has(key),
        `BoardItem: unknown field "${key}" — add to TS interface or GO_ONLY_FIELDS`
      ).toBe(true);
    }
  });

  it("subIssues entries match SubIssueRef schema", () => {
    validateRefArray(
      fixture.subIssues,
      SUB_ISSUE_REF_FIELDS,
      SUB_ISSUE_REF_ALL_KNOWN,
      "BoardItem.subIssues"
    );
  });

  it("blockedBy entries match BlockingRef schema", () => {
    validateRefArray(
      fixture.blockedBy,
      BLOCKING_REF_FIELDS,
      BLOCKING_REF_ALL_KNOWN,
      "BoardItem.blockedBy"
    );
  });

  it("blocking entries match BlockingRef schema", () => {
    validateRefArray(
      fixture.blocking,
      BLOCKING_REF_FIELDS,
      BLOCKING_REF_ALL_KNOWN,
      "BoardItem.blocking"
    );
  });
});

// ─── Issue contract ────────────────────────────────────────────────────────

describe("Issue contract (Go → TypeScript)", () => {
  const fixture = loadFixture("issue.json");

  it("contains all required TS fields with correct types", () => {
    validateFields(fixture, ISSUE_TS_FIELDS, ISSUE_ALL_KNOWN, "IssueDetail");
  });

  it("optional TS fields have correct types when present", () => {
    for (const [field, expectedType] of Object.entries(ISSUE_TS_OPTIONAL)) {
      if (field in fixture) {
        expect(typeof fixture[field], `IssueDetail: optional field "${field}" type mismatch`).toBe(
          expectedType
        );
      }
    }
  });

  it("has no unknown top-level fields", () => {
    for (const key of Object.keys(fixture)) {
      expect(
        ISSUE_ALL_KNOWN.has(key),
        `IssueDetail: unknown field "${key}" — add to TS interface or GO_ONLY_FIELDS`
      ).toBe(true);
    }
  });

  it("subIssues entries match SubIssueRef schema", () => {
    validateRefArray(
      fixture.subIssues,
      SUB_ISSUE_REF_FIELDS,
      SUB_ISSUE_REF_ALL_KNOWN,
      "IssueDetail.subIssues"
    );
  });

  it("blockedBy entries match BlockingRef schema", () => {
    validateRefArray(
      fixture.blockedBy,
      BLOCKING_REF_FIELDS,
      BLOCKING_REF_ALL_KNOWN,
      "IssueDetail.blockedBy"
    );
  });

  it("blocking entries match BlockingRef schema", () => {
    validateRefArray(
      fixture.blocking,
      BLOCKING_REF_FIELDS,
      BLOCKING_REF_ALL_KNOWN,
      "IssueDetail.blocking"
    );
  });
});
