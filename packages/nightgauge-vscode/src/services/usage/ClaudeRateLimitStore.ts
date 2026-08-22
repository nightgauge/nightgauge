/**
 * ClaudeRateLimitStore — last-seen `rate_limit_event` readings, per bucket
 * (Issue #709).
 *
 * ## Why a store exists at all
 *
 * The Claude CLI's `rate_limit_event` envelope is only observable **while an
 * invocation is actively streaming**. There is no command that returns the
 * current utilization at rest — `/usage` reports it but is an interactive
 * slash command, and nightgauge dispatches exclusively in print mode
 * (docs/spikes/662-adapter-usage-quota-signals.md §2.2). The status bar,
 * meanwhile, samples on a timer and mostly between runs. Something therefore
 * has to remember the last reading across process restarts, and that is this.
 *
 * ## The three states a reading can be in
 *
 * | State | Meaning | Confidence the provider reports |
 * | --- | --- | --- |
 * | live | observed during the run that is still streaming | `measured` |
 * | cached | observed earlier, its window has not reset yet | `estimated` |
 * | expired | observed earlier, its own `resetsAt` has passed | dropped |
 *
 * The third row is the one that matters and the reason this is not a plain
 * cache. A reading whose `resetsAt` is in the past is not merely stale: the
 * window it describes has since refilled, so the number is **known-wrong**,
 * not merely old. Nightgauge cannot know the post-reset utilization (the user
 * may have spent the refilled window in the Claude Code app, outside
 * nightgauge entirely), and inventing a `0%` would be exactly the fabricated
 * percentage docs/decisions/018-adapter-usage-quota-model.md forbids. Expired
 * readings are therefore dropped on read and pruned on the next write, and
 * the provider degrades to reporting nothing for that bucket.
 *
 * ## Stability
 *
 * The wire format behind these readings is unofficial and reverse-engineered
 * — see the risk note on `ClaudeRateLimitUsageProvider`. This store validates
 * every field it reads back off disk and discards anything it does not
 * recognise, so a shape change downgrades the meter rather than corrupting
 * it.
 *
 * @see Issue #709 - Claude usage provider
 * @see docs/decisions/018-adapter-usage-quota-model.md
 */

import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import type { RateLimitEventData } from "../../utils/tokenParser";

/**
 * Path to the persisted readings, relative to the **account** root (the user's
 * home directory), not a workspace root.
 *
 * Account-scoped because the figure is account-wide. Claude's five-hour and
 * seven-day allowances are consumed by every Claude Code session the operator
 * runs, in every repository, plus nightgauge's own pipeline stages. A
 * workspace-scoped file would give each VS Code window a different partial view
 * of one shared allowance, and would leave the statusline writer's readings
 * (Issue #730 — a process running in whatever directory the operator happens to
 * be in) somewhere no particular workspace would look.
 */
const CLAUDE_RATE_LIMIT_FILE = ".nightgauge/usage/claude-rate-limits.json";

/**
 * On-disk schema version. Bumped when the persisted shape changes; a file
 * carrying any other version is discarded rather than migrated (this is a
 * cache of an unofficial signal, not user data — re-observing it costs
 * nothing).
 */
const STORE_VERSION = 1;

/** One bucket's last-seen reading. */
export interface RateLimitReading {
  /** The vendor's own bucket name, e.g. `"five_hour"` / `"seven_day"` / `"daily"`. */
  rateLimitType: string;
  /** Percentage of the bucket consumed, 0-100, as the vendor reported it. */
  utilization: number;
  /** Unix epoch **seconds** when the bucket refills, or 0 when the event carried none. */
  resetsAt: number;
  /** `"allowed"` / `"allowed_warning"` / `"limited"`, verbatim from the event. */
  status: string;
  /** When nightgauge observed this reading. The "as of" the UI shows. */
  observedAt: Date;
  /**
   * True while the run that produced this reading is still streaming.
   *
   * Never persisted: a reading loaded from disk is by definition not from the
   * current run, so it hydrates as `false`.
   */
  live: boolean;
}

interface PersistedReading {
  rateLimitType: string;
  utilization: number;
  resetsAt: number;
  status: string;
  observedAt: string;
}

interface PersistedStore {
  version: number;
  buckets: Record<string, PersistedReading>;
}

/**
 * Validate one persisted entry. Returns `null` for anything malformed — a
 * corrupted or shape-changed entry is dropped, never coerced, because a
 * coerced percentage is a fabricated one.
 */
function parseReading(value: unknown): RateLimitReading | null {
  if (typeof value !== "object" || value === null) {
    return null;
  }
  const raw = value as Partial<PersistedReading>;
  if (typeof raw.rateLimitType !== "string" || raw.rateLimitType.length === 0) {
    return null;
  }
  if (typeof raw.utilization !== "number" || !Number.isFinite(raw.utilization)) {
    return null;
  }
  if (typeof raw.resetsAt !== "number" || !Number.isFinite(raw.resetsAt)) {
    return null;
  }
  if (typeof raw.status !== "string") {
    return null;
  }
  if (typeof raw.observedAt !== "string") {
    return null;
  }
  const observedAt = new Date(raw.observedAt);
  if (Number.isNaN(observedAt.getTime())) {
    return null;
  }
  return {
    rateLimitType: raw.rateLimitType,
    utilization: raw.utilization,
    resetsAt: raw.resetsAt,
    status: raw.status,
    observedAt,
    live: false,
  };
}

/**
 * True when a reading's own window has already refilled, making its
 * utilization known-wrong rather than merely stale.
 *
 * A `resetsAt` of 0 means the event carried no reset time at all. That cannot
 * expire on a clock — there is no clock — so such a reading stays readable and
 * relies on `confidence`/`observedAt` to state its age.
 */
export function readingHasExpired(reading: RateLimitReading, now: Date): boolean {
  if (reading.resetsAt <= 0) {
    return false;
  }
  return reading.resetsAt * 1000 <= now.getTime();
}

/**
 * Last-seen subscription rate-limit readings for the account.
 *
 * ## Two writers, neither authoritative
 *
 * This file has two producers and they cannot see each other's channel:
 *
 * - **This class**, from the `rate_limit_event` envelope on nightgauge's own
 *   `claude -p` stream (Issue #709). Only observable while a pipeline stage is
 *   streaming, but it is the only channel that can be `measured` — the vendor
 *   states the figure as the run is happening.
 * - **`nightgauge hook claude-statusline`** (Issue #730), a separate process
 *   wired in as Claude Code's `statusLine` command. It sees the same
 *   account-wide figure on Claude Code's documented statusline payload, on
 *   every render of every session, including sessions nightgauge knows nothing
 *   about. This is what makes a reading available *at rest*, which is the
 *   whole reason the meter can show a Max allowance rather than falling
 *   through to dollar windows.
 *
 * Since neither writer is authoritative, both merge per bucket on
 * `observedAt`: whoever saw the newer reading wins. A blind rewrite from
 * memory would let whichever process wrote last erase the other's fresher
 * number, and would drop buckets the writer had never heard of.
 *
 * ## Consequences of the second writer
 *
 * `load()` re-reads when the file has changed on disk rather than hydrating
 * once and caching forever. Without that, a VS Code window that had already
 * read the file would serve the same percentage until it was reloaded, no
 * matter how many status lines had since updated it — which is exactly the
 * "stale percentage rendered as current" this model refuses to do.
 *
 * Writes go through a temp file and a rename, so the Go reader never observes
 * a half-written document, and vice versa.
 *
 * Reads stay synchronous and in-memory so `UsageProvider.getSnapshot` never
 * blocks on I/O; the disk is touched in `load()` and once per observed event.
 * Writes are best-effort: a failed write costs the meter its at-rest reading
 * after a restart, and is never allowed to fail a pipeline run.
 */
export class ClaudeRateLimitStore {
  private readonly buckets = new Map<string, RateLimitReading>();
  /** Serialises writes so two events in flight cannot interleave a file rewrite. */
  private writeChain: Promise<void> = Promise.resolve();

  /**
   * @param accountRoot Directory the store path is resolved beneath. Production
   *   passes the user's home directory via {@link forAccount}; tests pass a
   *   temp directory.
   */
  constructor(private readonly accountRoot: string) {}

  /**
   * The store for the current account.
   *
   * Every VS Code window, every workspace, and the `claude-statusline` verb all
   * resolve to this one file — see `CLAUDE_RATE_LIMIT_FILE` for why the figure
   * is account-scoped rather than workspace-scoped.
   */
  static forAccount(): ClaudeRateLimitStore {
    return new ClaudeRateLimitStore(os.homedir());
  }

  /** Absolute path of the backing file. */
  get filePath(): string {
    return path.join(this.accountRoot, CLAUDE_RATE_LIMIT_FILE);
  }

  /**
   * Hydrate from disk. Re-reads every call, because the file has a second
   * writer in another process (Issue #730) and this is how its readings arrive.
   *
   * Unconditional rather than gated on a stat: the document is well under a
   * kilobyte and `load()` runs once per snapshot derivation — every 300s on the
   * refresh timer, or on demand — so a stat costs the same syscall as the read
   * it would be avoiding. Gating on `mtime` would buy nothing and would inherit
   * the mtime granularity of whatever filesystem `$HOME` is on, where two
   * same-second writes of equal length are indistinguishable.
   *
   * Never throws: a missing file is the normal first-run state, and an
   * unreadable or malformed one is treated as "no readings" rather than as an
   * activation failure.
   */
  async load(): Promise<void> {
    let text: string;
    try {
      text = await fs.readFile(this.filePath, "utf8");
    } catch {
      // No file yet, or it went away. Whatever is already in memory stands: a
      // reading this process observed is not invalidated by the absence of its
      // cache.
      return;
    }
    this.mergeFromDisk(parseStore(text));
  }

  /**
   * Merge persisted readings into memory, newest `observedAt` per bucket
   * winning.
   *
   * A tie keeps the in-memory copy, which is how a same-run reading holds its
   * `live` flag across the re-read triggered by its own persist: the two
   * copies carry the same `observedAt`, and only the in-memory one knows the
   * run is still streaming.
   */
  private mergeFromDisk(readings: RateLimitReading[]): void {
    for (const reading of readings) {
      const existing = this.buckets.get(reading.rateLimitType);
      if (existing !== undefined && existing.observedAt >= reading.observedAt) {
        continue;
      }
      this.buckets.set(reading.rateLimitType, reading);
    }
  }

  /**
   * Record one observed `rate_limit_event`.
   *
   * The reading is marked `live` — it was produced by the run currently
   * streaming — until {@link settle} is called at that run's end. Only the
   * newest reading per bucket is kept: an older utilization for the same
   * window is superseded, not history.
   *
   * Events carrying no usable bucket name are ignored. `tokenParser` defaults
   * a missing `rateLimitType` to `"unknown"`, which names no window and must
   * not become one.
   *
   * Returns the queued write. The in-memory update is already done when this
   * returns, so the stream's hot path drops the promise; a caller that needs
   * the file on disk (a test, or any future flush-on-shutdown) can await it.
   * The promise never rejects — a failed write costs the meter its at-rest
   * reading after a restart and is never allowed to fail a pipeline run.
   */
  record(event: RateLimitEventData, observedAt: Date = new Date()): Promise<void> {
    if (typeof event.rateLimitType !== "string" || event.rateLimitType.length === 0) {
      return Promise.resolve();
    }
    if (event.rateLimitType === "unknown") {
      return Promise.resolve();
    }
    if (typeof event.utilization !== "number" || !Number.isFinite(event.utilization)) {
      return Promise.resolve();
    }
    this.buckets.set(event.rateLimitType, {
      rateLimitType: event.rateLimitType,
      utilization: event.utilization,
      resetsAt: Number.isFinite(event.resetsAt) ? event.resetsAt : 0,
      status: typeof event.status === "string" ? event.status : "unknown",
      observedAt,
      live: true,
    });
    return this.persist();
  }

  /**
   * Mark the streaming run as finished: every reading it produced is now a
   * cached one.
   *
   * This is what makes `confidence: "measured"` mean "same-run" rather than
   * "sometime in this VS Code session" — the distinction Issue #709 requires.
   */
  settle(): void {
    for (const reading of this.buckets.values()) {
      reading.live = false;
    }
  }

  /**
   * Every reading whose window has not already refilled, newest bucket state
   * per bucket name. Expired readings are dropped (see the class doc): the
   * window they describe has refilled and their number is known-wrong.
   */
  /**
   * When the feed last recorded anything, across every bucket — INCLUDING
   * readings whose window has already refilled.
   *
   * `readings()` deliberately drops expired readings, because their numbers are
   * known-wrong. Their timestamps are not: "the last thing this feed wrote was
   * two days ago" is exactly the signal that distinguishes a feed which is
   * working from one that stopped (#810), and dropping it is what made a dead
   * feed indistinguishable from a quiet one.
   *
   * Null when nothing has ever been recorded.
   */
  lastObservedAt(): Date | null {
    let newest: Date | null = null;
    for (const reading of this.buckets.values()) {
      if (newest === null || reading.observedAt > newest) {
        newest = reading.observedAt;
      }
    }
    return newest === null ? null : new Date(newest.getTime());
  }

  readings(now: Date = new Date()): RateLimitReading[] {
    // Copies, so a caller holding a reading cannot see `settle()` flip its
    // `live` flag underneath it mid-render.
    const usable: RateLimitReading[] = [];
    for (const reading of this.buckets.values()) {
      if (!readingHasExpired(reading, now)) {
        usable.push({ ...reading });
      }
    }
    return usable;
  }

  /**
   * Merge the current buckets with whatever is on disk and write the result
   * out atomically, dropping expired ones so the file does not accumulate
   * windows that can never be served again.
   *
   * The re-read inside the write chain is not belt-and-braces: the statusline
   * writer may have recorded a bucket this process has never seen, or a newer
   * reading for one it has, between the last `load()` and now. Serialising
   * from memory alone would delete the first and overwrite the second.
   *
   * The in-memory update in `record` has already happened by the time this is
   * called, so observing an event stays synchronous on the stream's hot path:
   * the returned promise is there for a caller that needs the file on disk,
   * and the pipeline never awaits it.
   */
  private persist(): Promise<void> {
    this.writeChain = this.writeChain
      .then(async () => {
        let onDisk: RateLimitReading[] = [];
        try {
          onDisk = parseStore(await fs.readFile(this.filePath, "utf8"));
        } catch {
          // Missing or unreadable: this process's readings are the whole file.
        }
        this.mergeFromDisk(onDisk);

        const snapshot: PersistedStore = { version: STORE_VERSION, buckets: {} };
        const now = new Date();
        for (const reading of this.buckets.values()) {
          if (readingHasExpired(reading, now)) {
            continue;
          }
          snapshot.buckets[reading.rateLimitType] = {
            rateLimitType: reading.rateLimitType,
            utilization: reading.utilization,
            resetsAt: reading.resetsAt,
            status: reading.status,
            observedAt: reading.observedAt.toISOString(),
          };
        }
        await writeStoreAtomically(this.filePath, snapshot);
      })
      .catch((error) => {
        console.warn("[Nightgauge] failed to persist Claude rate-limit readings:", error);
      });
    return this.writeChain;
  }
}

/**
 * Parse a persisted store document into readings, discarding anything
 * unrecognised.
 *
 * A file carrying a different `version` is discarded whole rather than
 * migrated: this is a cache of a figure that will be re-observed within
 * minutes, not user data.
 */
function parseStore(text: string): RateLimitReading[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return [];
  }
  if (typeof parsed !== "object" || parsed === null) {
    return [];
  }
  const store = parsed as Partial<PersistedStore>;
  if (store.version !== STORE_VERSION || typeof store.buckets !== "object") {
    return [];
  }
  const readings: RateLimitReading[] = [];
  for (const value of Object.values(store.buckets ?? {})) {
    const reading = parseReading(value);
    if (reading !== null) {
      readings.push(reading);
    }
  }
  return readings;
}

/**
 * Monotonic suffix for temp filenames.
 *
 * A counter rather than `Date.now()`: the process id makes the name unique
 * across processes, and this makes it unique within one. A clock does neither
 * reliably — two writes in the same millisecond collide, and under a test's
 * fake timers *every* write collides, which turns a queued write into a file
 * another queued write has already renamed away.
 */
let tempCounter = 0;
function nextTempId(): number {
  tempCounter += 1;
  return tempCounter;
}

/**
 * Write via a temp file in the target directory plus a rename, so a concurrent
 * reader — the Go `claude-statusline` verb reads this same file — never
 * observes a partially written document. The temp file is a sibling so the
 * rename stays atomic on every platform.
 *
 * Two-space indent and a trailing newline, matched byte for byte by the Go
 * writer in `internal/usagestore`.
 */
async function writeStoreAtomically(filePath: string, snapshot: PersistedStore): Promise<void> {
  const dir = path.dirname(filePath);
  await fs.mkdir(dir, { recursive: true });
  const tempPath = path.join(dir, `.claude-rate-limits-${process.pid}-${nextTempId()}.json`);
  try {
    await fs.writeFile(tempPath, `${JSON.stringify(snapshot, null, 2)}\n`, "utf8");
    await fs.rename(tempPath, filePath);
  } catch (error) {
    await fs.rm(tempPath, { force: true }).catch(() => undefined);
    throw error;
  }
}
