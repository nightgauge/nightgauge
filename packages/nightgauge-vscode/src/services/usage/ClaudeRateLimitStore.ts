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
import * as path from "node:path";
import type { RateLimitEventData } from "../../utils/tokenParser";

/** Relative path from the workspace root to the persisted readings. */
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
 * Last-seen `rate_limit_event` readings for one workspace.
 *
 * Reads are synchronous and in-memory so `UsageProvider.getSnapshot` never
 * blocks on I/O; the disk is touched once at `load()` and once per observed
 * event. Writes are best-effort: a failed write costs the meter its at-rest
 * reading after a restart, and is never allowed to fail a pipeline run.
 */
export class ClaudeRateLimitStore {
  private readonly buckets = new Map<string, RateLimitReading>();
  private loaded = false;
  /** Serialises writes so two events in flight cannot interleave a file rewrite. */
  private writeChain: Promise<void> = Promise.resolve();

  constructor(private readonly workspaceRoot: string) {}

  /** Absolute path of the backing file. */
  get filePath(): string {
    return path.join(this.workspaceRoot, CLAUDE_RATE_LIMIT_FILE);
  }

  /**
   * Hydrate from disk. Safe to call more than once; only the first call reads.
   *
   * Never throws: a missing file is the normal first-run state, and an
   * unreadable or malformed one is treated as "no readings" rather than as an
   * activation failure.
   */
  async load(): Promise<void> {
    if (this.loaded) {
      return;
    }
    this.loaded = true;
    let text: string;
    try {
      text = await fs.readFile(this.filePath, "utf8");
    } catch {
      return;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      return;
    }
    if (typeof parsed !== "object" || parsed === null) {
      return;
    }
    const store = parsed as Partial<PersistedStore>;
    if (store.version !== STORE_VERSION || typeof store.buckets !== "object") {
      return;
    }
    for (const value of Object.values(store.buckets ?? {})) {
      const reading = parseReading(value);
      if (reading === null) {
        continue;
      }
      // An in-memory reading always wins. Hydration is lazy — the first
      // `getSnapshot` triggers it — so a run that streamed a `rate_limit_event`
      // before anything read the meter would otherwise have its live reading
      // overwritten by the older, `live: false` copy the write path had just
      // persisted, silently downgrading a same-run figure to `estimated`.
      if (this.buckets.has(reading.rateLimitType)) {
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
   * Write the current buckets out, dropping expired ones so the file does not
   * accumulate windows that can never be served again.
   *
   * The in-memory update in `record` has already happened by the time this is
   * called, so observing an event stays synchronous on the stream's hot path:
   * the returned promise is there for a caller that needs the file on disk,
   * and the pipeline never awaits it.
   */
  private persist(): Promise<void> {
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
    this.writeChain = this.writeChain
      .then(async () => {
        await fs.mkdir(path.dirname(this.filePath), { recursive: true });
        await fs.writeFile(this.filePath, `${JSON.stringify(snapshot, null, 2)}\n`, "utf8");
      })
      .catch((error) => {
        console.warn("[Nightgauge] failed to persist Claude rate-limit readings:", error);
      });
    return this.writeChain;
  }
}
