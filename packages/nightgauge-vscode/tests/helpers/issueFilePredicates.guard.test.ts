/**
 * Regression guard for #426 — fs-mock predicates must identify issue context
 * files by basename, never by a substring of the ambient checkout path.
 *
 * Three layers:
 *  1. Unit tests on the shared helper, including an executed simulation of the
 *     old predicate class against a colliding checkout path.
 *  2. Unit tests on the detector itself (`scanLine`), so the guard's own
 *     sensitivity is pinned: it must flag the three original offenders and the
 *     shapes that escaped the first, literal-needle revision of this scan.
 *  3. A meta-scan over the *live* test surface — every package's `tests/` tree
 *     plus every in-`src/` `__tests__` file — that fails if any of those files
 *     reintroduces an inline ambient-path predicate.
 *
 * Both this file and `issueFilePredicates.ts` are scanned like everything else.
 * There is no whole-file exemption list: the scan machinery builds its needles
 * by concatenation (see `ambientPathScan.ts`) so it cannot flag itself, and the
 * handful of legitimate lines below carry the line-scoped opt-out instead.
 */

import * as fs from "fs";
import * as path from "path";
import { fileURLToPath } from "url";
import { describe, expect, it } from "vitest";

import { discoverScanRoots, OPT_OUT_MARKER, scanLine } from "./ambientPathScan";
import { isIssueJsonPath, isIssueJsonPathFor } from "./issueFilePredicates";

const HERE = path.dirname(fileURLToPath(import.meta.url));
// tests/helpers -> tests -> <package> -> packages
const VSCODE_TESTS_DIR = path.resolve(HERE, "..");
const PACKAGES_DIR = path.resolve(VSCODE_TESTS_DIR, "..", "..");

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
    // These two lines are the banned class on purpose; the opt-out is
    // line-scoped, so each of them carries its own marker.
    const oldPredicate = (p: string) => p.includes("issue-42"); // #426-ok: demo
    const oldGeneric = (p: string) => p.includes("issue-") && p.endsWith(".json"); // #426-ok

    const collision = "/x/.nightgauge/worktrees/issue-422/repo/other.json";

    // OLD: false positive on an unrelated file inside an issue-named checkout.
    expect(oldPredicate(collision)).toBe(true);
    expect(oldGeneric(collision)).toBe(true);

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
 * Fixtures for the detector tests.
 *
 * Every fixture is assembled from `TOKEN` rather than written out, so no line
 * in this file contains the banned character sequence verbatim and the
 * meta-scan below can run over this file unexempted.
 */
const TOKEN = "issue" + "-";

/** The three predicates that actually shipped in this repo before #426. */
const ORIGINAL_OFFENDERS = [
  'String(p).includes("' + TOKEN + '42")',
  'filePath.includes("' + TOKEN + '") && filePath.endsWith(".json")',
  'p.includes("' + TOKEN + '")',
];

/**
 * Shapes that are the same bug but which the first revision of this guard —
 * a fixed list of `.<method>("<token>` string needles — did not see.
 */
const ESCAPE_SHAPES = [
  'p.includes("/' + TOKEN + '")',
  "/" + TOKEN + "/.test(p)",
  "String(p).match(/" + TOKEN + ".../)",
  'String(p).lastIndexOf("' + TOKEN + '") > -1',
  'String(p).search("' + TOKEN + '") >= 0',
  'p.split("' + TOKEN + '")',
];

/** Lines that mention an issue file but are not path-substring predicates. */
const CLEAN_SHAPES = [
  // Exact basename comparison — no scanned method call at all.
  'name === "' + TOKEN + '42.json"',
  // The banned shape, explicitly opted out on its own line.
  'p.includes("' + TOKEN + '") // ' + OPT_OUT_MARKER + ": receiver is a basename",
  // Prose that merely names the issue.
  "// the ambient checkout under " + TOKEN + "426 collides with this class",
  // `join` composes a path, it does not test one.
  "path.join(dir, `" + TOKEN + "${n}.json`)",
];

describe("#426 scanLine detects the bug class, not a fixed needle list", () => {
  it("flags all three predicates that shipped before the fix", () => {
    for (const shape of ORIGINAL_OFFENDERS) {
      expect(scanLine(shape) ?? "", shape).toContain(TOKEN);
    }
  });

  it("flags the shapes that escaped the literal-needle revision", () => {
    for (const shape of ESCAPE_SHAPES) {
      expect(scanLine(shape) ?? "", shape).toContain(TOKEN);
    }
  });

  it("does not flag lines outside the bug class", () => {
    for (const shape of CLEAN_SHAPES) {
      expect(scanLine(shape), shape).toBeNull();
    }
  });
});

/**
 * Roots that must exist and must contain files today. Naming them here means a
 * package rename, a `tests/` move, or the SDK's in-`src` suites relocating
 * turns this guard red instead of silently shrinking the scanned surface.
 */
const REQUIRED_ROOTS = [
  "nightgauge-vscode/tests",
  "nightgauge-sdk/tests",
  "nightgauge-sdk/src/__tests__",
];

describe("#426 meta-scan: no inline ambient-path issue predicates in tests", () => {
  const roots = discoverScanRoots(PACKAGES_DIR);

  it.each(REQUIRED_ROOTS)("scans the %s root and finds test files there", (label) => {
    const root = roots.find((r) => r.label === label);
    expect(
      root?.files.length ?? 0,
      [
        `Scan root "${label}" contributed no files to the #426 meta-scan.`,
        "Either the package/directory moved or was renamed, or the discovery",
        "in tests/helpers/ambientPathScan.ts no longer matches the layout.",
        `Discovered roots: ${roots.map((r) => `${r.label}(${r.files.length})`).join(", ") || "none"}`,
      ].join("\n")
    ).toBeGreaterThan(0);
  });

  it("finds no banned path predicate in any scanned test file", () => {
    const offenders: string[] = [];

    for (const root of roots) {
      for (const file of root.files) {
        const lines = fs.readFileSync(file, "utf8").split("\n");
        lines.forEach((line, i) => {
          const shape = scanLine(line);
          if (shape === null) return;
          offenders.push(`${path.relative(PACKAGES_DIR, file)}:${i + 1} → ${shape}`);
        });
      }
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
