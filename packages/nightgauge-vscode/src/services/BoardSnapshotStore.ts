/**
 * BoardSnapshotStore - one board read per (board, scope), shared across every
 * ProjectBoardService instance in the window.
 *
 * A ProjectV2 board read is the most expensive call this product makes: 17
 * GraphQL points per 100-item page against a 5,000-point hourly budget, where
 * almost everything else costs 1 (see internal/forge/boardcache, #845, which
 * solves the same problem for the Go daemon's sweeps).
 *
 * The extension had the matching defect one layer up. `ProjectBoardService`
 * already de-duplicates well - per-status cache, TTL, in-flight coalescing -
 * but every one of those stores is a private instance field, and
 * `RepositoriesTreeProvider` builds one service per repository path. In a
 * workspace where several repositories share a single GitHub Project, each
 * service therefore asks the same board the same question on every refresh, and
 * cost grows linearly with repository count while the upstream data is
 * identical. The IPC call itself proves the data is repo-independent: it takes
 * (owner, project, status, ownerType, githubUser) and no repository at all.
 *
 * So the sharing key is the board, not the workspace path.
 *
 * What is shared is the RAW BoardItem[] as it came off the wire, never the
 * per-repository view. `boardItemsToReadyIssues` filters items down to the
 * service's own repository, so caching that output would hand the second
 * repository the first one's filtered set - a board-wide read turned into a
 * silently wrong answer. Sharing the unfiltered payload and letting each
 * service filter locally is what makes one fetch serve N repositories
 * correctly, and it is the shape the acceptance criteria ask for.
 *
 * @see Issue #11
 */

// Type-only, so it is erased at compile time and creates no runtime import
// cycle with ProjectBoardService, which imports this module for real.
import type { BoardItem } from "./IpcClient";
import type { StatusCounts } from "./IpcClientBase";

/** Identity of one board read. Everything the IPC call varies on appears here. */
export interface BoardSnapshotKey {
  owner: string;
  projectNumber: number;
  /** "org" | "user"; undefined means the server default. */
  ownerType?: string;
  /** Narrows the board query server-side, so it changes the result. */
  githubUser?: string;
  /** A board status, or ALL_ITEMS_SCOPE for the unfiltered read. */
  scope: string;
}

/** Scope value for the unfiltered "every item on the board" read. */
export const ALL_ITEMS_SCOPE = "__all_items__";

/**
 * Scope value for the per-status counts read (`board.counts`).
 *
 * Counts are the read the Repositories tree makes on EVERY root render — one
 * per repository row — and they were the one board read this store did not
 * cover. `ProjectBoardService` cached them in a private instance field, so N
 * repositories on one board still issued N identical `board.counts` queries
 * per cold refresh, in parallel, with nothing to coalesce them (#1277). The
 * data is as repo-independent as the item lists: the IPC call takes (owner,
 * project, ownerType, githubUser) and no repository.
 */
export const COUNTS_SCOPE = "__counts__";

export interface BoardStoreMetrics {
  /** Served from a snapshot inside the caller's TTL. */
  hits: number;
  /** Went to the network. */
  misses: number;
  /** Joined a fetch already in flight. */
  coalesced: number;
}

interface Snapshot<T = unknown> {
  value: T;
  fetchedAt: number;
}

/**
 * Renders a key. Kept separate from the store so tests and callers can talk
 * about identity without holding a store.
 *
 * Parts are joined with a space, which cannot appear in a GitHub login or in a
 * project number, so the owner/project pair stays unambiguous.
 */
export function boardSnapshotKey(key: BoardSnapshotKey): string {
  return [
    key.ownerType ?? "",
    key.owner,
    String(key.projectNumber),
    key.githubUser ?? "",
    key.scope,
  ].join(" ");
}

export class BoardSnapshotStore {
  private snapshots = new Map<string, Snapshot>();
  private inFlight = new Map<string, Promise<unknown>>();
  private metrics: BoardStoreMetrics = { hits: 0, misses: 0, coalesced: 0 };

  /**
   * Returns the snapshot when it is fresher than `ttlMs`, else undefined.
   *
   * Freshness is judged against the caller's TTL rather than one the store
   * owns, because instances legitimately differ (the dashboard passes its own).
   * A short-TTL caller then refetches an entry a long-TTL caller still accepts,
   * and the refetch updates the entry for both - the correct outcome in both
   * directions.
   */
  peek(key: BoardSnapshotKey, ttlMs: number): BoardItem[] | undefined {
    return this.peekEntry<BoardItem[]>(boardSnapshotKey(key), ttlMs);
  }

  /** The snapshot regardless of age - the stale-if-error fallback. */
  stale(key: BoardSnapshotKey): BoardItem[] | undefined {
    return this.snapshots.get(boardSnapshotKey(key))?.value as BoardItem[] | undefined;
  }

  /** Counts counterpart of `stale`: the last successful counts, any age. */
  staleCounts(key: BoardSnapshotKey): StatusCounts | undefined {
    return this.snapshots.get(boardSnapshotKey(key))?.value as StatusCounts | undefined;
  }

  private peekEntry<T>(id: string, ttlMs: number): T | undefined {
    const snapshot = this.snapshots.get(id);
    if (!snapshot) return undefined;
    if (Date.now() - snapshot.fetchedAt >= ttlMs) return undefined;
    return snapshot.value as T;
  }

  /**
   * Fresh snapshot, a join onto the in-flight fetch, or `fetcher()`.
   *
   * A rejected fetch is never stored, so one repository's auth failure or
   * transient error cannot become another repository's cached answer. The
   * rejection propagates to every joined caller, which each apply their own
   * fallback.
   */
  async fetch(
    key: BoardSnapshotKey,
    ttlMs: number,
    fetcher: () => Promise<BoardItem[]>
  ): Promise<BoardItem[]> {
    return this.fetchEntry(boardSnapshotKey(key), ttlMs, fetcher);
  }

  /**
   * `fetch` for the counts read. Same freshness, coalescing and never-cache-a-
   * rejection rules; the only difference is the payload type. Callers pass a
   * key whose scope is {@link COUNTS_SCOPE}, so `invalidateBoard` and
   * `expireBoard` sweep counts together with the item lists.
   */
  async fetchCounts(
    key: BoardSnapshotKey,
    ttlMs: number,
    fetcher: () => Promise<StatusCounts>
  ): Promise<StatusCounts> {
    return this.fetchEntry(boardSnapshotKey(key), ttlMs, fetcher);
  }

  private async fetchEntry<T>(id: string, ttlMs: number, fetcher: () => Promise<T>): Promise<T> {
    const fresh = this.peekEntry<T>(id, ttlMs);
    if (fresh) {
      this.metrics.hits++;
      return fresh;
    }

    const existing = this.inFlight.get(id);
    if (existing) {
      this.metrics.coalesced++;
      return existing as Promise<T>;
    }

    this.metrics.misses++;
    const promise = fetcher()
      .then((value) => {
        this.snapshots.set(id, { value, fetchedAt: Date.now() });
        return value;
      })
      .finally(() => {
        if (this.inFlight.get(id) === promise) this.inFlight.delete(id);
      });

    this.inFlight.set(id, promise);
    return promise;
  }

  /**
   * Drops every snapshot for one board, across all scopes.
   *
   * Scoped to the board rather than global so a manual refresh in one
   * repository does not discard unrelated boards' data. In-flight fetches are
   * deliberately left alone: their results are still written, and cancelling
   * them would only make the joined callers fail.
   */
  invalidateBoard(owner: string, projectNumber: number): void {
    const marker = ` ${owner} ${String(projectNumber)} `;
    for (const id of [...this.snapshots.keys()]) {
      if (id.includes(marker)) this.snapshots.delete(id);
    }
  }

  /**
   * Expires one board's snapshots without discarding them.
   *
   * The counterpart to `invalidateBoard` for a user-triggered refresh: the next
   * read must go to the network, but the old payload has to survive as the
   * stale-if-error fallback. Deleting instead would turn a failed refresh into
   * an empty board.
   */
  expireBoard(owner: string, projectNumber: number): void {
    const marker = ` ${owner} ${String(projectNumber)} `;
    for (const [id, snapshot] of this.snapshots) {
      if (id.includes(marker)) snapshot.fetchedAt = 0;
    }
  }

  /**
   * Expires exactly one (board, scope) entry, keeping it as the fallback.
   *
   * The counts path needs this on every pipeline status move: the numbers
   * changed, so the next render must refetch, but the item lists on the same
   * board are invalidated separately by the caller and must not be expired
   * here — that would turn one status move into a 17-point board re-read.
   */
  expireScope(key: BoardSnapshotKey): void {
    const snapshot = this.snapshots.get(boardSnapshotKey(key));
    if (snapshot) snapshot.fetchedAt = 0;
  }

  /** Drops everything. For workspace or auth changes, and for test isolation. */
  clear(): void {
    this.snapshots.clear();
    this.inFlight.clear();
    this.metrics = { hits: 0, misses: 0, coalesced: 0 };
  }

  /**
   * Counters only - never board contents, which would put issue titles into
   * logs for a surface whose whole value is being cheap and quiet.
   */
  getMetrics(): BoardStoreMetrics {
    return { ...this.metrics };
  }

  /** Number of live snapshots. Lets a bound be asserted in tests. */
  size(): number {
    return this.snapshots.size;
  }
}

/**
 * The window-wide store every ProjectBoardService shares by default.
 *
 * A module singleton is the right shape here precisely because the services are
 * constructed independently, in several places, with no common owner to thread
 * an instance through. Tests inject their own store instead.
 */
export const sharedBoardSnapshots = new BoardSnapshotStore();
