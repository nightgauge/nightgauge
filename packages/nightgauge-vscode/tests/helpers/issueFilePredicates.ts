/**
 * Basename-anchored predicates for `fs` mocks that intercept issue context
 * files (#426).
 *
 * Production writes and reads issue context as
 * `<workspaceRoot>/.nightgauge/pipeline/issue-<n>.json`
 * (`RepositoryContextLoader.getContextFile("issue", n)` and
 * `ContextAssembler.getContextPath("issue", n)`; `ActiveIssueKnowledgeProvider`
 * composes the same basename by hand). The identity of that file is therefore
 * its **basename**, never a substring of its absolute path.
 *
 * Mocks that matched with `String(p).includes("issue-42")` matched the ambient
 * checkout path too: any run whose working copy lives under a directory such as
 * `.nightgauge/worktrees/issue-422/` made every unrelated `existsSync` /
 * `readFileSync` call hit the mock, and the suite failed spuriously depending
 * on where it was checked out. Anchor on the basename so the predicate answers
 * "is this the issue context file?" instead of "does this path mention an
 * issue?".
 */

import * as path from "path";

/** `issue-<digits>.json`, anchored at both ends. */
const ISSUE_JSON_BASENAME = /^issue-\d+\.json$/;

/**
 * True iff `p` names an issue context file for *any* issue number —
 * i.e. its basename is exactly `issue-<digits>.json`.
 */
export function isIssueJsonPath(p: unknown): boolean {
  // Buffer/URL and other PathLike shapes stringify usefully; nullish must not
  // match (String(null) would otherwise be compared).
  if (p === null || p === undefined) return false;
  return ISSUE_JSON_BASENAME.test(path.basename(String(p)));
}

/**
 * True iff `p` names the issue context file for exactly `issueNumber` —
 * i.e. its basename is exactly `issue-<issueNumber>.json`.
 *
 * Use this when a test's semantics depend on the specific issue under test
 * (e.g. counting how many times issue 42's context file is probed).
 */
export function isIssueJsonPathFor(p: unknown, issueNumber: number): boolean {
  if (p === null || p === undefined) return false;
  return path.basename(String(p)) === `issue-${issueNumber}.json`;
}
