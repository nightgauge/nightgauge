/**
 * Scan machinery for the #426 regression guard.
 *
 * #426: `fs` mocks in vitest decided "is this the issue context file?" with a
 * substring test against the *whole path*. Any working copy living under a
 * directory such as `.nightgauge/worktrees/issue-422/` then matched every
 * unrelated `existsSync` / `readFileSync` call, so the suite passed or failed
 * based on where it happened to be cloned. The fix anchors on the basename
 * (see `issueFilePredicates.ts`); this module is how we keep the bug class
 * from growing back.
 *
 * Three pieces, kept out of the test file so the detectors can be unit-tested
 * in isolation:
 *
 *  - {@link discoverScanRoots} — the scanned surface. Not a hardcoded pair of
 *    directories: every package under `packages/` contributes its `tests/`
 *    tree *and* every in-`src/` `__tests__` file, which is what the SDK's
 *    vitest `include` actually runs.
 *  - {@link collectTsFiles} — the file collector.
 *  - {@link scanLine} — the detector. Returns the matched shape, or `null`.
 *
 * ## Self-exemption is by construction, not by allowlist
 *
 * An earlier revision carried a whole-file `EXEMPT_FILES` set so the guard and
 * the helper would not flag themselves. A whole-file exemption is a hole: any
 * *future* offender added to an exempt file is invisible. Instead, every
 * needle here is assembled by string concatenation and every regex is built
 * with `new RegExp`, so no source line in this module (or in the guard's test
 * fixtures) contains the banned character sequence verbatim. Both files stay
 * fully under the scanner; the only opt-out left is line-scoped.
 */

import * as fs from "fs";
import * as path from "path";

/** A named collection of files the guard scans. */
export interface ScanRoot {
  /** Stable, human-readable name, e.g. `nightgauge-sdk/tests`. */
  label: string;
  /** Absolute path of the directory this root was discovered under. */
  dir: string;
  /** Absolute paths of the TypeScript files belonging to this root. */
  files: string[];
}

/** Directories that never contain first-party test sources. */
const SKIP_DIRS = new Set(["node_modules", "dist", "out", "build", "coverage", ".git"]);

/** Path segment that marks an in-`src/` test file (SDK layout). */
const IN_SRC_TEST_SEGMENT = "__tests__";

/**
 * Recursively collect `.ts` files under `dir` (sorted, vendor dirs skipped).
 * A missing directory yields no files rather than throwing — callers assert
 * non-emptiness per root, which is the signal that actually matters.
 */
export function collectTsFiles(dir: string, out: string[] = []): string[] {
  if (!fs.existsSync(dir)) return out;
  const entries = fs
    .readdirSync(dir, { withFileTypes: true })
    .sort((a, b) => a.name.localeCompare(b.name));
  for (const entry of entries) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (SKIP_DIRS.has(entry.name)) continue;
      collectTsFiles(full, out);
    } else if (entry.isFile() && full.endsWith(".ts")) {
      out.push(full);
    }
  }
  return out;
}

/** True iff `file` sits inside a `__tests__` directory at any depth. */
function isInSrcTestFile(file: string): boolean {
  return file.split(path.sep).includes(IN_SRC_TEST_SEGMENT);
}

/**
 * Discover the live test surface under `packagesDir`.
 *
 * For every directory that carries a `package.json`:
 *  - its `tests/` tree, if present (`<pkg>/tests`);
 *  - every file under `src/` whose path contains a `__tests__` segment
 *    (labelled `<pkg>/src/__tests__`) — the SDK runs ~99 such files via its
 *    vitest `include`, and they were entirely outside the original scan.
 *
 * Roots with no files are omitted, so a package rename or a layout move makes
 * the guard's per-root non-empty assertions fail loudly instead of silently
 * shrinking the scanned surface to nothing.
 */
export function discoverScanRoots(packagesDir: string): ScanRoot[] {
  const roots: ScanRoot[] = [];
  if (!fs.existsSync(packagesDir)) return roots;

  const packages = fs
    .readdirSync(packagesDir, { withFileTypes: true })
    .filter((e) => e.isDirectory() && !SKIP_DIRS.has(e.name))
    .sort((a, b) => a.name.localeCompare(b.name));

  for (const pkg of packages) {
    const pkgDir = path.join(packagesDir, pkg.name);
    if (!fs.existsSync(path.join(pkgDir, "package.json"))) continue;

    const testsDir = path.join(pkgDir, "tests");
    const testsFiles = collectTsFiles(testsDir);
    if (testsFiles.length > 0) {
      roots.push({ label: `${pkg.name}/tests`, dir: testsDir, files: testsFiles });
    }

    const srcDir = path.join(pkgDir, "src");
    const inSrcFiles = collectTsFiles(srcDir).filter(isInSrcTestFile);
    if (inSrcFiles.length > 0) {
      roots.push({
        label: `${pkg.name}/src/${IN_SRC_TEST_SEGMENT}`,
        dir: srcDir,
        files: inSrcFiles,
      });
    }
  }

  return roots;
}

/**
 * Line-scoped opt-out. Put this marker on the offending line when the receiver
 * is genuinely a basename (or the line is prose/fixture data), and say why.
 *
 * Built by concatenation for the same reason the needles are: a verbatim
 * occurrence would silently exempt every line of this file.
 */
export const OPT_OUT_MARKER = "#426" + "-ok";

/** The substring that makes a path predicate an *issue* path predicate. */
const ISSUE_TOKEN = "issue" + "-";

/**
 * String methods whose receiver, in these tests, is a path — and whose
 * argument therefore gets tested against the ambient checkout directory.
 * `endsWith` is deliberately absent: it anchors at the end, so it cannot match
 * a parent directory.
 */
export const SCANNED_METHODS = [
  "includes",
  "indexOf",
  "lastIndexOf",
  "search",
  "startsWith",
  "match",
  "split",
] as const;

/**
 * Arm 1 — method call: a dot, one of the scanned methods, an open paren, then
 * any run of non-`)` characters containing the issue token. Catches
 * `p.includes("<token>")`, `String(p).lastIndexOf('/<token>')`,
 * `p.match(/<token>\d+/)`, `p.split("<token>")`, and every quoting variant,
 * without caring which quote character or leading slash was used.
 */
const METHOD_ARM = new RegExp("\\.(?:" + SCANNED_METHODS.join("|") + ")\\(\\s*[^)]*" + ISSUE_TOKEN);

/**
 * Arm 2 — regex literal executed against a path: `/…<token>…/flags.test(`.
 * The method arm cannot see this shape because the call site is `.test(` on a
 * regex receiver rather than a string method on a path receiver.
 */
const REGEX_LITERAL_ARM = new RegExp(
  "/[^/\\n]*" + ISSUE_TOKEN + "[^/\\n]*/[a-z]*" + "\\." + "test" + "\\("
);

/**
 * Return the offending shape found in `line`, or `null` if the line is clean
 * or carries the line-scoped opt-out marker.
 */
export function scanLine(line: string): string | null {
  if (line.includes(OPT_OUT_MARKER)) return null;
  const hit = METHOD_ARM.exec(line) ?? REGEX_LITERAL_ARM.exec(line);
  return hit ? hit[0] : null;
}
