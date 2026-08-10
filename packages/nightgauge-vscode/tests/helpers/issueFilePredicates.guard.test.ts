/**
 * Regression guard for #426 — fs-mock predicates must identify issue context
 * files by basename, never by a substring of the ambient checkout path.
 *
 * Two layers:
 *  1. Unit tests on the shared helper, including an executed simulation of the
 *     old predicate class against a colliding checkout path.
 *  2. A meta-scan over the test trees that fails if any test file reintroduces
 *     an inline ambient-path predicate of the banned class.
 */

import * as fs from "fs";
import * as path from "path";
import { fileURLToPath } from "url";
import { describe, expect, it } from "vitest";

import { isIssueJsonPath, isIssueJsonPathFor } from "./issueFilePredicates";

const HERE = path.dirname(fileURLToPath(import.meta.url));
// tests/helpers -> tests -> <package> -> packages
const VSCODE_TESTS_DIR = path.resolve(HERE, "..");
const PACKAGES_DIR = path.resolve(VSCODE_TESTS_DIR, "..", "..");
const SDK_TESTS_DIR = path.join(PACKAGES_DIR, "nightgauge-sdk", "tests");

/**
 * Absolute paths that legitimately mention the banned needles: this guard file
 * (which names them to ban them) and the helper module's explanatory comment.
 */
const EXEMPT_FILES = new Set<string>([
  path.join(HERE, "issueFilePredicates.guard.test.ts"),
  path.join(HERE, "issueFilePredicates.ts"),
]);

// Paths that a real checkout can produce: the directory mentions an issue
// number, the file itself is not an issue context file.
const COLLISION_PATHS = [
  "/x/.nightgauge/worktrees/issue-422/repo/other.json",
  "/home/ci/issue-42/pkg/tests/data.json",
  "/w/issue-429/settings.json",
];

describe("#426 issue-file predicates are basename-anchored", () => {
  it("does not match checkout paths that merely contain an issue directory", () => {
    for (const p of COLLISION_PATHS) {
      expect(isIssueJsonPath(p), p).toBe(false);
      expect(isIssueJsonPathFor(p, 42), p).toBe(false);
      expect(isIssueJsonPathFor(p, 422), p).toBe(false);
      expect(isIssueJsonPathFor(p, 429), p).toBe(false);
    }
  });

  it("matches real issue context files, absolute and relative", () => {
    expect(isIssueJsonPath("/any/dir/issue-42.json")).toBe(true);
    expect(isIssueJsonPath("ctx/issue-7.json")).toBe(true);
    expect(isIssueJsonPath("issue-1.json")).toBe(true);
    expect(isIssueJsonPathFor("/any/dir/issue-42.json", 42)).toBe(true);
    expect(isIssueJsonPathFor("ctx/issue-7.json", 7)).toBe(true);
  });

  it("does not match sibling context types or near-miss basenames", () => {
    expect(isIssueJsonPath("/p/.nightgauge/pipeline/planning-42.json")).toBe(false);
    expect(isIssueJsonPath("/p/.nightgauge/pipeline/issue-42.json.bak")).toBe(false);
    expect(isIssueJsonPath("/p/.nightgauge/pipeline/issue-abc.json")).toBe(false);
    expect(isIssueJsonPath(null)).toBe(false);
    expect(isIssueJsonPath(undefined)).toBe(false);
  });

  it("distinguishes issue numbers instead of prefix-matching them", () => {
    // The old `includes("issue-42")` form also fired on issue-420..issue-429.
    expect(isIssueJsonPathFor("/p/issue-422.json", 42)).toBe(false);
    expect(isIssueJsonPathFor("/p/issue-42.json", 42)).toBe(true);
  });

  it("executes the old predicate class to show it matched the collision path", () => {
    // Verbatim shape of the pre-#426 predicates, inlined here as data so the
    // collision is demonstrated by execution rather than described in prose.
    const oldPredicate = (p: string) => p.includes("issue-42");
    const oldGenericPredicate = (p: string) => p.includes("issue-") && p.endsWith(".json");

    const collision = "/x/.nightgauge/worktrees/issue-422/repo/other.json";

    // OLD: false positive on an unrelated file inside an issue-named checkout.
    expect(oldPredicate(collision)).toBe(true);
    expect(oldGenericPredicate(collision)).toBe(true);

    // NEW: the same path is correctly rejected.
    expect(isIssueJsonPathFor(collision, 42)).toBe(false);
    expect(isIssueJsonPath(collision)).toBe(false);

    // And the genuine target is still matched by both old and new.
    const real = "/x/.nightgauge/pipeline/issue-42.json";
    expect(oldPredicate(real)).toBe(true);
    expect(isIssueJsonPathFor(real, 42)).toBe(true);
  });
});

/**
 * Needles are assembled by concatenation so this guard file never matches
 * itself (and neither does any future copy of the banned expression).
 */
function bannedNeedles(): string[] {
  const methods = ["includes", "indexOf", "startsWith"];
  const openers = ['("', "('", "(`"];
  const literal = "issue-";
  const needles: string[] = [];
  for (const m of methods) {
    for (const o of openers) {
      needles.push("." + m + o + literal);
    }
  }
  return needles;
}

function collectTsFiles(dir: string, out: string[] = []): string[] {
  if (!fs.existsSync(dir)) return out;
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "node_modules" || entry.name === "dist") continue;
      collectTsFiles(full, out);
    } else if (entry.isFile() && full.endsWith(".ts")) {
      out.push(full);
    }
  }
  return out;
}

/**
 * Line-scoped opt-out for the rare case where the receiver is already a
 * basename (not a path), so the substring test cannot collide with the ambient
 * checkout. Put `#426-ok: <reason>` on the offending line or the line above it.
 */
const OPT_OUT_MARKER = "#426" + "-ok";

describe("#426 meta-scan: no inline ambient-path issue predicates in tests", () => {
  it("finds no banned substring predicate in any test file", () => {
    const needles = bannedNeedles();
    const files = [...collectTsFiles(VSCODE_TESTS_DIR), ...collectTsFiles(SDK_TESTS_DIR)];

    expect(files.length).toBeGreaterThan(0);

    const offenders: string[] = [];
    for (const file of files) {
      if (EXEMPT_FILES.has(file)) continue;
      const lines = fs.readFileSync(file, "utf8").split("\n");
      lines.forEach((line, i) => {
        const hits = needles.filter((n) => line.includes(n));
        if (hits.length === 0) return;
        const exempted =
          line.includes(OPT_OUT_MARKER) || (lines[i - 1]?.includes(OPT_OUT_MARKER) ?? false);
        if (exempted) return;
        offenders.push(`${path.relative(PACKAGES_DIR, file)}:${i + 1} → ${hits.join(", ")}`);
      });
    }

    expect(
      offenders,
      [
        "Ambient-path issue predicates found (#426). These match the checkout",
        "path (e.g. .nightgauge/worktrees/issue-422/...) instead of the context",
        "file, so the suite passes or fails based on where it was cloned.",
        "Use the basename-anchored helpers in",
        "packages/nightgauge-vscode/tests/helpers/issueFilePredicates.ts",
        "(isIssueJsonPath / isIssueJsonPathFor) instead.",
        `If the receiver is genuinely a basename, annotate the line with ${OPT_OUT_MARKER}.`,
        "",
        ...offenders.map((o) => `  - ${o}`),
      ].join("\n")
    ).toEqual([]);
  });
});
