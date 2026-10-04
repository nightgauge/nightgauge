/**
 * An epic named by repository and number (#2382).
 *
 * An issue number names an issue only within one repository, so in a
 * multi-repository workspace two epics can share a number: stopping
 * `example-org/platform#20` must not stop `example-org/app#20`. Mirrors the Go
 * scheduler, which records each queued sub-issue's `epicRepo` beside its
 * `epicNumber`.
 */
export interface EpicRef {
  /** The epic's repository, `owner/name`. Absent only when it is unknown. */
  repo?: string;
  number: number;
}

/** Whether two `owner/name` names are one repository; GitHub ignores case. */
export function sameRepo(a: string | undefined, b: string | undefined): boolean {
  return (a ?? "").toLowerCase() === (b ?? "").toLowerCase();
}

/** Whether an item's epic (`epicRepo`, `epicNumber`) is `epic`. */
export function isEpic(
  epic: EpicRef,
  epicRepo: string | undefined,
  epicNumber: number | undefined
): boolean {
  return epicNumber === epic.number && sameRepo(epicRepo, epic.repo);
}

/** `owner/name#N`, or `#N` when the repository is unknown. */
export function formatEpicRef(epic: EpicRef): string {
  return epic.repo ? `${epic.repo}#${epic.number}` : `#${epic.number}`;
}

/** A case-insensitive map key for an epic. */
export function epicRefKey(epic: EpicRef): string {
  return `${(epic.repo ?? "").toLowerCase()}#${epic.number}`;
}

/** An issue named by repository (`owner/name`) and number (#2382). */
export interface RepoIssueRef {
  repo: string;
  number: number;
}

/** A case-insensitive map key for an issue: `owner/name#N`. */
export function repoIssueKey(repo: string, number: number): string {
  return `${repo.toLowerCase()}#${number}`;
}

/** The `owner/name` a GitHub issue or pull request URL names, if any. */
export function repoFromIssueUrl(url: string | undefined): string | undefined {
  const m = url?.match(/github\.com\/([^/]+)\/([^/]+)\/(?:issues|pull)\//);
  return m ? `${m[1]}/${m[2]}` : undefined;
}

/**
 * The key a running issue's per-slot state is held under: repository
 * (`owner/name`, "" when unknown) and issue number, case-insensitive (#2403).
 * Two repositories' issues with one number run in two slots.
 */
export function slotKey(repo: string | undefined, issueNumber: number): string {
  return repoIssueKey(repo ?? "", issueNumber);
}
