/**
 * Naming a running slot by repository and issue number (#2412).
 *
 * An issue number names an issue only within one repository, so two
 * repositories' issues with one number are two runs in two slots. A caller
 * or a record without a repository matches by number, and only when that
 * names exactly one run.
 */
import type { ActiveSlot } from "../types/queue";
import { sameRepo, slotKey } from "./epicRef";

/** The set of pipeline runs in flight, named by repository and number. */
export class ActiveRunSet {
  /** slotKey(repo, issueNumber) -> issueNumber */
  private readonly runs = new Map<string, number>();

  /** Record a run's start; false when it is already in flight. */
  start(issueNumber: number, repo?: string): boolean {
    if (this.find(issueNumber, repo) !== undefined) return false;
    this.runs.set(slotKey(repo, issueNumber), issueNumber);
    return true;
  }

  /** Record a run's end; false when no single run in flight matches. */
  end(issueNumber: number, repo?: string): boolean {
    const key = this.find(issueNumber, repo);
    return key !== undefined && this.runs.delete(key);
  }

  get size(): number {
    return this.runs.size;
  }

  private find(issueNumber: number, repo?: string): string | undefined {
    const unknownRepo = slotKey(undefined, issueNumber);
    if (repo) {
      const exact = slotKey(repo, issueNumber);
      if (this.runs.has(exact)) return exact;
      return this.runs.has(unknownRepo) ? unknownRepo : undefined;
    }
    if (this.runs.has(unknownRepo)) return unknownRepo;
    let found: string | undefined;
    for (const [key, number] of this.runs) {
      if (number !== issueNumber) continue;
      if (found !== undefined) return undefined;
      found = key;
    }
    return found;
  }
}

/**
 * The active slot running `issueNumber` in `repo` (`owner/name`) (#2412). A
 * slot or a caller without a repository matches by number, and only when
 * that names exactly one slot.
 */
export function findActiveSlot(
  slots: readonly ActiveSlot[],
  issueNumber: number,
  repo?: string
): ActiveSlot | undefined {
  const candidates: ActiveSlot[] = [];
  for (const slot of slots) {
    if (slot.issueNumber !== issueNumber) continue;
    if (repo && slot.repo) {
      if (sameRepo(slot.repo, repo)) return slot;
      continue;
    }
    candidates.push(slot);
  }
  return candidates.length === 1 ? candidates[0] : undefined;
}
