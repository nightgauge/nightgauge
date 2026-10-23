/**
 * WorkspaceThrottleSync — keeps this window's dispatch on the throttle the
 * platform holds for the window's own workspace (#2337).
 *
 * A `throttle` command names no workspace, and every window of the machine
 * shares one agent, so the command cannot say whose cap it is: a throttle on
 * one workspace would cap a window serving another, and clearing it would
 * lift a cap the other workspace still has. The command's ack is also the
 * only record of delivery, so a window whose stream was reconnecting when
 * another window acknowledged it, or a machine that was off past the
 * command's expiry, would never hear of it. Each window therefore reads the
 * throttle of its own workspace, found by its manifest's slug in the
 * platform's workspace list (`GET /v1/workspaces`), whenever it may have
 * changed:
 *   - a `throttle` command arrived (ThrottleCommandHandler);
 *   - the command stream (re)connected;
 *   - the session became or stayed authenticated, which includes every token
 *     refresh, so a missed change is also caught within one token lifetime;
 *   - the workspace manifest was reloaded.
 *
 * Reads run one at a time, and a read asked for while one is in flight runs
 * again after it, so the last value applied always comes from a read that
 * started after the last change was signalled; an older read can never undo
 * a newer one. A read that fails changes nothing.
 *
 * The throttle is followed only while the platform session is authenticated:
 * the kept throttle is restored when it becomes so, and signing out, or
 * losing the session, lifts the cap and forgets it. With the platform
 * disabled there is no session, and nothing is applied.
 *
 * @see WorkspaceThrottle — the cap, its parsing and its persistence
 */

import type * as vscode from "vscode";
import type { ITokenStorage } from "../platform/TokenStorage";
import type { IOnDemandTokenRefresher } from "../platform/TokenRefreshManager";
import type { SessionManager } from "../platform/SessionManager";
import type { Logger } from "../utils/logger";
import {
  parseThrottleValue,
  type WorkspaceThrottle,
  type WorkspaceThrottleState,
} from "./WorkspaceThrottle";

/** Reads the throttle the platform holds for a workspace. */
export interface WorkspaceThrottleReader {
  /**
   * The throttle of the workspace with this slug: null when it has none, or
   * when no workspace of the account's team has the slug. Throws when the
   * platform's answer cannot be read, so a failure is never taken for "no
   * throttle".
   */
  read(slug: string): Promise<WorkspaceThrottle | null>;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Reads a workspace's throttle from the platform's workspace list. */
export class PlatformWorkspaceThrottleReader implements WorkspaceThrottleReader {
  constructor(
    private readonly getPlatformUrl: () => string,
    private readonly tokenStorage: Pick<ITokenStorage, "retrieve">,
    private readonly tokenRefresher?: IOnDemandTokenRefresher
  ) {}

  async read(slug: string): Promise<WorkspaceThrottle | null> {
    const token = await this.tokenStorage.retrieve("accessToken");
    if (!token) throw new Error("no access token");
    let response = await this.list(token);
    // An access token past its lifetime is refreshed once, as registration does.
    if ((response.status === 401 || response.status === 403) && this.tokenRefresher) {
      const refreshed = await this.tokenRefresher.forceRefresh();
      if (refreshed) response = await this.list(refreshed);
    }
    if (!response.ok) throw new Error(`the platform returned HTTP ${response.status}`);
    const body: unknown = await response.json();
    const workspaces = isObject(body) ? body.workspaces : undefined;
    if (!Array.isArray(workspaces)) throw new Error("the workspace list is malformed");
    const workspace = workspaces.find((w) => isObject(w) && w.slug === slug);
    if (workspace === undefined) return null;
    const throttle = parseThrottleValue((workspace as Record<string, unknown>).throttle);
    if (throttle === undefined) throw new Error("the workspace's throttle is malformed");
    return throttle;
  }

  private list(token: string): Promise<Response> {
    return fetch(`${this.getPlatformUrl()}/v1/workspaces`, {
      headers: { Authorization: `Bearer ${token}`, Accept: "application/json" },
    });
  }
}

/** What a refresh did: applied the throttle read, could not read it, or was not following one. */
export type ThrottleRefreshResult = "applied" | "failed" | "inactive";

export class WorkspaceThrottleSync {
  /** Whether the throttle is followed: the platform session is authenticated. */
  private active = false;
  /** Bumped when following stops, so a read in flight then applies nothing. */
  private generation = 0;
  private inFlight: Promise<ThrottleRefreshResult> | null = null;
  private readAgain = false;

  constructor(
    private readonly reader: WorkspaceThrottleReader,
    private readonly state: Pick<WorkspaceThrottleState, "restore" | "apply" | "clear">,
    /** The slug of the platform workspace this window serves, or null for none. */
    private readonly getSlug: () => string | null,
    private readonly logger: Logger,
    /**
     * Settles once the workspace manifest has been read, so `getSlug` answers.
     * Nothing is restored or read before it: an activation that raced the
     * manifest would take the window for one with no workspace.
     */
    private readonly ready: Promise<unknown> = Promise.resolve()
  ) {}

  /** Whether the window follows the throttle now. */
  isActive(): boolean {
    return this.active;
  }

  /**
   * Follow the throttle: on the first call restore the kept one, so dispatch
   * is held before the first read lands, then read the current one.
   */
  activate(): Promise<ThrottleRefreshResult> {
    if (!this.active) {
      this.active = true;
      const generation = this.generation;
      // Registered on `ready` before the read below, so it runs first.
      void this.ready.then(() => {
        if (this.active && generation === this.generation) this.state.restore(this.getSlug());
      });
    }
    return this.refresh();
  }

  /** Stop following the throttle: lift the cap and forget the kept one. */
  async deactivate(): Promise<void> {
    this.active = false;
    this.generation += 1;
    await this.state.clear();
  }

  /**
   * Read this workspace's throttle and apply it. Resolves once a read that
   * started after this call has been applied, or has failed.
   */
  refresh(): Promise<ThrottleRefreshResult> {
    if (!this.active) return Promise.resolve("inactive");
    if (this.inFlight) {
      this.readAgain = true;
      return this.inFlight;
    }
    const run = this.drain().finally(() => {
      this.inFlight = null;
    });
    this.inFlight = run;
    return run;
  }

  /**
   * Follow the throttle while `session` is authenticated: now when it already
   * is, and on every later authenticated event (sign-in, restore, token
   * refresh); stop when it signs out or fails.
   */
  followSession(session: Pick<SessionManager, "state" | "onSessionChanged">): vscode.Disposable {
    if (session.state === "authenticated") void this.activate();
    return session.onSessionChanged((event) => {
      if (event.current === "authenticated") {
        void this.activate();
      } else if (event.current === "unauthenticated" || event.current === "error") {
        void this.deactivate();
      }
    });
  }

  private async drain(): Promise<ThrottleRefreshResult> {
    let result: ThrottleRefreshResult;
    do {
      this.readAgain = false;
      result = await this.readAndApply();
    } while (this.readAgain && this.active);
    return result;
  }

  private async readAndApply(): Promise<ThrottleRefreshResult> {
    await this.ready;
    const generation = this.generation;
    const slug = this.getSlug();
    let throttle: WorkspaceThrottle | null;
    try {
      throttle = slug === null ? null : await this.reader.read(slug);
    } catch (err) {
      this.logger.warn("WorkspaceThrottleSync: could not read the workspace throttle", {
        slug,
        err: err instanceof Error ? err.message : String(err),
      });
      return "failed";
    }
    if (!this.active || generation !== this.generation) return "inactive";
    try {
      await this.state.apply(slug, throttle);
    } catch (err) {
      this.logger.error("WorkspaceThrottleSync: could not apply the workspace throttle", {
        slug,
        err: err instanceof Error ? err.message : String(err),
      });
      return "failed";
    }
    this.logger.debug("WorkspaceThrottleSync: applied the workspace throttle", { slug, throttle });
    return "applied";
  }
}
