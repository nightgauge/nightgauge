/**
 * Answering "is the Claude usage feed actually working?" (Issue #810).
 *
 * `readStatusLineState` answers a different question — "does some command
 * mention our verb" — and three surfaces were reading it as if it were this
 * one. So an operator whose feed had named a deleted binary for two days was
 * told *"The Claude usage feed is already enabled."* and offered a **Disable**
 * button, while the Dashboard panel and the status-bar tooltip, in the same
 * session, offered to **enable** it. The only offered action made it worse.
 *
 * This module composes the two facts the pure decision needs — is the wired
 * binary executable, and when did the feed last record anything — and hands
 * back one verdict that every surface can share.
 *
 * @see claudeStatusLineSetup.ts — `decideClaudeFeedHealth`, the pure rule
 * @see claudeStatusLineRepair.ts — the repair the broken state offers
 */

import { constants as fsConstants } from "node:fs";
import * as fs from "node:fs/promises";
import { ClaudeRateLimitStore } from "./ClaudeRateLimitStore";
import {
  claudeSettingsPath,
  decideClaudeFeedHealth,
  parseStatusLineBinary,
  readClaudeSettings,
  readStatusLineState,
  resolveSettingsTarget,
  type ClaudeFeedHealth,
} from "./claudeStatusLineSetup";

export interface ClaudeFeedHealthDeps {
  settingsPath?: string;
  /** Defaults to the account store. */
  store?: ClaudeRateLimitStore;
  isExecutable?: (binaryPath: string) => Promise<boolean>;
  now?: Date;
  staleAfterMs?: number;
}

async function defaultIsExecutable(binaryPath: string): Promise<boolean> {
  try {
    await fs.access(binaryPath, fsConstants.X_OK);
    return true;
  } catch {
    return false;
  }
}

/**
 * The feed's health, as one verdict.
 *
 * Never throws. An unreadable settings file is reported as `broken` rather
 * than as `not-wired`: "we could not tell" must not render as "you have not
 * turned it on", which would put an Enable button in front of an operator
 * whose feed may well be wired — the exact confusion this issue is about.
 */
export async function probeClaudeFeedHealth(
  deps: ClaudeFeedHealthDeps = {}
): Promise<ClaudeFeedHealth> {
  try {
    const settingsPath = await resolveSettingsTarget(deps.settingsPath ?? claudeSettingsPath());
    const settings = await readClaudeSettings(settingsPath);
    if (settings === null) {
      return {
        state: "broken",
        reason: "unrecognized-command",
        binary: null,
        lastObservedAt: null,
      };
    }

    // Only probe the filesystem when there is a path to probe: the not-wired
    // case is the common one and costs nothing beyond the settings read.
    const state = readStatusLineState(settings);
    const binary =
      state.wired && state.command !== null ? parseStatusLineBinary(state.command) : null;
    const isExecutable = deps.isExecutable ?? defaultIsExecutable;
    const binaryExecutable = binary === null ? null : await isExecutable(binary);

    const store = deps.store ?? ClaudeRateLimitStore.forAccount();
    await store.load();

    return decideClaudeFeedHealth({
      settings,
      binaryExecutable,
      lastObservedAt: store.lastObservedAt(),
      now: deps.now,
      staleAfterMs: deps.staleAfterMs,
    });
  } catch {
    return { state: "broken", reason: "unrecognized-command", binary: null, lastObservedAt: null };
  }
}

/** Human phrasing for "when did this last work", shared by every surface. */
export function describeLastObserved(health: ClaudeFeedHealth, now: Date = new Date()): string {
  if (health.lastObservedAt === null) {
    return "no reading has ever been recorded";
  }
  const ms = now.getTime() - health.lastObservedAt.getTime();
  const days = Math.floor(ms / (24 * 60 * 60 * 1000));
  const hours = Math.floor(ms / (60 * 60 * 1000));
  const ago =
    days >= 1
      ? `${days} day${days === 1 ? "" : "s"} ago`
      : hours >= 1
        ? `${hours}h ago`
        : "just now";
  return `the last reading arrived ${ago} (${health.lastObservedAt.toISOString()})`;
}
