/**
 * The parent epic a sub-issue's branch may be based on (#2377).
 *
 * An epic branch, `epic/<N>-<slug>`, names issue #N of the repository it is
 * pushed to. A sub-issue whose epic lives in another repository therefore has
 * no epic branch in its own: `epic/<N>-*` there is the branch of that
 * repository's own #N, and creating one strands the sub-issue's work on a
 * branch the epic's completion PR, opened in the epic's repository, never
 * merges. Such a sub-issue is based on, and merges into, its own repository's
 * default branch.
 *
 * Mirrors the Go `git.EpicBranchParent`, which the scheduler, its pickup runner
 * and `nightgauge git branch-create` use.
 *
 * @param subRepo - The sub-issue's repository, `owner/name`.
 * @param parentNumber - The parent epic's number, if any.
 * @param parentRepo - The parent's repository, `owner/name`. Empty or absent
 *   means the sub-issue's own repository, the one place a bare number names an
 *   issue.
 * @returns The parent's number when it lives in `subRepo` (compared
 *   case-insensitively), otherwise `undefined`.
 */
export function epicBranchParent(
  subRepo: string | undefined,
  parentNumber: number | null | undefined,
  parentRepo: string | null | undefined
): number | undefined {
  if (!parentNumber || parentNumber <= 0) return undefined;
  if (parentRepo && parentRepo.toLowerCase() !== (subRepo ?? "").toLowerCase()) {
    return undefined;
  }
  return parentNumber;
}
