/**
 * Honest health for the Claude usage feed (#810).
 *
 * `readStatusLineState` answers "does a command mention our verb". Three
 * surfaces read it as if it answered "is the feed working", so an operator
 * whose wiring named a deleted binary and had recorded nothing for two days was
 * told *"The Claude usage feed is already enabled."* — with **Disable** as the
 * only offered action — while the Dashboard panel and the status-bar tooltip
 * offered to *enable* it, in the same session.
 *
 * Every case here fails against the pre-#810 code, which had no health notion
 * at all.
 */

import { describe, expect, it } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { ClaudeRateLimitStore } from "../../../src/services/usage/ClaudeRateLimitStore";
import {
  buildStatusLineCommand,
  CLAUDE_FEED_STALE_AFTER_MS,
  decideClaudeFeedHealth,
  STATUS_LINE_VERB,
} from "../../../src/services/usage/claudeStatusLineSetup";
import {
  describeLastObserved,
  probeClaudeFeedHealth,
} from "../../../src/services/usage/claudeFeedHealth";

const DEAD =
  "/Users/x/.vscode/extensions/nightgauge.nightgauge-vscode-0.1.1787175119/dist/bin/nightgauge";
const LIVE =
  "/Users/x/.vscode/extensions/nightgauge.nightgauge-vscode-0.1.1787399523/dist/bin/nightgauge";
const NOW = new Date("2026-08-22T12:00:00.000Z");
/** The maintainer's observed reading, two days stale. */
const TWO_DAYS_AGO = new Date("2026-08-20T04:34:11.326Z");

function wired(command: string): Record<string, unknown> {
  return { statusLine: { type: "command", command } };
}

describe("decideClaudeFeedHealth", () => {
  it("reports not-wired when no nightgauge status line is configured", () => {
    expect(
      decideClaudeFeedHealth({
        settings: {},
        binaryExecutable: null,
        lastObservedAt: null,
        now: NOW,
      })
    ).toMatchObject({ state: "not-wired", reason: "not-wired", binary: null });
  });

  it("reports not-wired for someone else's status line", () => {
    const health = decideClaudeFeedHealth({
      settings: wired("~/bin/my-own-status-line"),
      binaryExecutable: null,
      lastObservedAt: null,
      now: NOW,
    });
    expect(health.state).toBe("not-wired");
  });

  it("reports healthy when the binary runs and a reading is recent", () => {
    const health = decideClaudeFeedHealth({
      settings: wired(buildStatusLineCommand(LIVE, null)),
      binaryExecutable: true,
      lastObservedAt: new Date(NOW.getTime() - 60 * 60 * 1000),
      now: NOW,
    });
    expect(health).toMatchObject({ state: "healthy", reason: "producing", binary: LIVE });
  });

  it("reports broken when the wired binary is gone — the reported case", () => {
    const health = decideClaudeFeedHealth({
      settings: wired(buildStatusLineCommand(DEAD, null)),
      binaryExecutable: false,
      lastObservedAt: TWO_DAYS_AGO,
      now: NOW,
    });
    expect(health).toMatchObject({ state: "broken", reason: "binary-missing", binary: DEAD });
    expect(health.lastObservedAt).toEqual(TWO_DAYS_AGO);
  });

  it("refuses to call a command it cannot parse healthy", () => {
    const health = decideClaudeFeedHealth({
      settings: wired(`/usr/bin/env FOO=1 '${DEAD}' ${STATUS_LINE_VERB}`),
      binaryExecutable: null,
      lastObservedAt: new Date(NOW.getTime() - 60_000),
      now: NOW,
    });
    // "We cannot tell" must never render as "it is fine".
    expect(health).toMatchObject({ state: "broken", reason: "unrecognized-command" });
  });

  it("reports silent, not broken, when nothing has been recorded yet", () => {
    // The state immediately after enabling: readings only arrive on the next
    // Claude Code render. Calling that broken would cry wolf on every enable.
    const health = decideClaudeFeedHealth({
      settings: wired(buildStatusLineCommand(LIVE, null)),
      binaryExecutable: true,
      lastObservedAt: null,
      now: NOW,
    });
    expect(health).toMatchObject({ state: "silent", reason: "no-readings" });
  });

  it("reports silent once readings pass the staleness window", () => {
    const health = decideClaudeFeedHealth({
      settings: wired(buildStatusLineCommand(LIVE, null)),
      binaryExecutable: true,
      lastObservedAt: new Date(NOW.getTime() - CLAUDE_FEED_STALE_AFTER_MS - 1),
      now: NOW,
    });
    expect(health).toMatchObject({ state: "silent", reason: "stale-readings" });
  });

  it("does not call a two-day-old reading stale on its own", () => {
    // Only the missing binary makes the reported case broken. A quiet couple of
    // days is an ordinary week, and flagging it would train the operator to
    // ignore the signal.
    const health = decideClaudeFeedHealth({
      settings: wired(buildStatusLineCommand(LIVE, null)),
      binaryExecutable: true,
      lastObservedAt: TWO_DAYS_AGO,
      now: NOW,
    });
    expect(health.state).toBe("healthy");
  });

  it("never reports a utilization figure in any state", () => {
    const states = [
      {
        binaryExecutable: null as boolean | null,
        lastObservedAt: null as Date | null,
        settings: {},
      },
      {
        binaryExecutable: false,
        lastObservedAt: null,
        settings: wired(buildStatusLineCommand(DEAD, null)),
      },
      {
        binaryExecutable: true,
        lastObservedAt: null,
        settings: wired(buildStatusLineCommand(LIVE, null)),
      },
    ];
    for (const s of states) {
      const health = decideClaudeFeedHealth({ ...s, now: NOW });
      // The verdict carries a state, a reason, a path and a timestamp — and no
      // number anyone could render as an allowance (AC 7).
      expect(Object.keys(health).sort()).toEqual(["binary", "lastObservedAt", "reason", "state"]);
    }
  });
});

describe("describeLastObserved", () => {
  it("says plainly when nothing has ever been recorded", () => {
    expect(
      describeLastObserved(
        { state: "silent", reason: "no-readings", binary: LIVE, lastObservedAt: null },
        NOW
      )
    ).toBe("no reading has ever been recorded");
  });

  it("names when the last reading arrived, with an exact timestamp", () => {
    const text = describeLastObserved(
      { state: "broken", reason: "binary-missing", binary: DEAD, lastObservedAt: TWO_DAYS_AGO },
      NOW
    );
    expect(text).toContain("2 days ago");
    expect(text).toContain(TWO_DAYS_AGO.toISOString());
  });
});

describe("probeClaudeFeedHealth — composing the real facts", () => {
  async function withSettings(settings: unknown): Promise<string> {
    const dir = await fs.mkdtemp(path.join(os.tmpdir(), "ng-810-"));
    const settingsPath = path.join(dir, "settings.json");
    await fs.writeFile(settingsPath, `${JSON.stringify(settings, null, 2)}\n`, "utf8");
    return settingsPath;
  }

  /** A store rooted in a temp dir, so no reading exists. */
  async function emptyStore(): Promise<ClaudeRateLimitStore> {
    return new ClaudeRateLimitStore(await fs.mkdtemp(path.join(os.tmpdir(), "ng-810-store-")));
  }

  it("reports broken for a wired-but-missing binary, off the real filesystem", async () => {
    const settingsPath = await withSettings(wired(buildStatusLineCommand(DEAD, null)));

    const health = await probeClaudeFeedHealth({
      settingsPath,
      store: await emptyStore(),
      isExecutable: async (p) => p === LIVE,
      now: NOW,
    });

    expect(health).toMatchObject({ state: "broken", reason: "binary-missing", binary: DEAD });
  });

  it("reports not-wired when there is no settings file at all", async () => {
    const dir = await fs.mkdtemp(path.join(os.tmpdir(), "ng-810-none-"));

    const health = await probeClaudeFeedHealth({
      settingsPath: path.join(dir, "settings.json"),
      store: await emptyStore(),
      now: NOW,
    });

    expect(health.state).toBe("not-wired");
  });

  it("does not report not-wired for a settings file it cannot parse", async () => {
    const dir = await fs.mkdtemp(path.join(os.tmpdir(), "ng-810-bad-"));
    const settingsPath = path.join(dir, "settings.json");
    await fs.writeFile(settingsPath, "{ not json", "utf8");

    const health = await probeClaudeFeedHealth({
      settingsPath,
      store: await emptyStore(),
      now: NOW,
    });

    // "We could not tell" must not put an Enable button in front of an operator
    // whose feed may well be wired.
    expect(health.state).toBe("broken");
  });

  it("does not touch the filesystem for a binary when nothing is wired", async () => {
    const settingsPath = await withSettings({ model: "opus" });
    let probes = 0;

    await probeClaudeFeedHealth({
      settingsPath,
      store: await emptyStore(),
      isExecutable: async () => {
        probes += 1;
        return true;
      },
      now: NOW,
    });

    expect(probes).toBe(0);
  });
});
