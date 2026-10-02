/**
 * #2357: the windows of one machine share an agent, so every one of them
 * receives a run verb, and the platform keeps the first acknowledgement.
 *
 * When a window holds the run, the platform receives that window's answer and
 * no refusal from another window, even when the holder takes a while (a cancel
 * waits for the run to stop). When no window holds the run, the verb is still
 * refused, `no-active-run`, once, within the grace.
 *
 * The windows share a real RemoteRunLedger directory, as the windows of a
 * machine share its machine-state directory.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({}));

import {
  RunVerbCommandHandler,
  type RunVerbTarget,
} from "../../src/services/RunVerbCommandHandler";
import { RemoteRunLedger } from "../../src/services/RemoteRunLedger";
import type { ReceivedCommand } from "../../src/services/AgentCommandStreamService";
import type { RemoteVerbResult } from "../../src/services/ConcurrentPipelineManager";

/** The grace a window that does not hold the run waits, kept short here. */
const GRACE_MS = 30;

let dir: string;

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), "run-verb-unheld-"));
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
}

/** The platform: it records every acknowledgement, in arrival order. */
function makePlatform() {
  return { agentAcknowledgeCommand: vi.fn().mockResolvedValue({ runId: "" }) };
}

type Runs = { [K in keyof RunVerbTarget]: ReturnType<typeof vi.fn> };

/** A window's runs: it holds `held`, and every verb answers `result`. */
function makeRuns(held: boolean, result: RemoteVerbResult = "applied"): Runs {
  return {
    holdsRemoteRun: vi.fn().mockResolvedValue(held),
    cancelByRemoteRunId: vi.fn().mockResolvedValue(result),
    pauseByRemoteRunId: vi.fn().mockResolvedValue(result),
    resumeByRemoteRunId: vi.fn().mockResolvedValue(result),
  };
}

/** One editor window: its runs, its handler, its view of the machine's ledger. */
function makeWindow(pid: number, runs: Runs, platform: ReturnType<typeof makePlatform>) {
  const ledger = new RemoteRunLedger(dir, { windowId: pid, isAlive: () => true });
  const handler = new RunVerbCommandHandler(
    runs as never,
    platform,
    makeLogger() as never,
    undefined,
    undefined,
    { ledger, graceMs: GRACE_MS }
  );
  handler.setAgentId("agent-machine");
  return { runs, ledger, handler };
}

function verb(
  type: "cancel" | "pause" | "resume" | "approve",
  id = `cmd-${type}`
): ReceivedCommand {
  return {
    id,
    type,
    payload: { runId: "run-1" },
    createdAt: "2026-10-02T00:00:00.000Z",
    owner: "acme",
    repo: "api",
  };
}

/** A promise and the function that settles it. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

describe("RunVerbCommandHandler — a verb no window of the machine holds (#2357)", () => {
  it.each(["cancel", "pause"] as const)(
    "of two windows sharing an agent, the platform gets only the holder's applied %s, though the holder is slow",
    async (type) => {
      const platform = makePlatform();
      const holderRuns = makeRuns(true);
      const applied = deferred<RemoteVerbResult>();
      // The holder's verb takes longer than the other window's grace: a
      // cancel waits for the run to stop.
      holderRuns[type === "cancel" ? "cancelByRemoteRunId" : "pauseByRemoteRunId"].mockReturnValue(
        applied.promise
      );
      const holder = makeWindow(101, holderRuns, platform);
      const other = makeWindow(102, makeRuns(false), platform);
      await holder.ledger.publish(["run-1"]);

      // The window without the run sees the command first.
      const otherDone = other.handler.consume(verb(type), type);
      const holderDone = holder.handler.consume(verb(type), type);
      await otherDone; // its grace has passed: it stayed silent
      expect(platform.agentAcknowledgeCommand).not.toHaveBeenCalled();

      applied.resolve("applied");
      await holderDone;
      expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
      expect(platform.agentAcknowledgeCommand).toHaveBeenCalledWith(
        "agent-machine",
        `cmd-${type}`,
        "applied",
        undefined
      );
      expect(other.runs.cancelByRemoteRunId).not.toHaveBeenCalled();
      expect(other.runs.pauseByRemoteRunId).not.toHaveBeenCalled();
    }
  );

  // The holder's listing can drop the run before its ack is out (a cancelled
  // run's slot is cleaned up first); its claim on the answer still keeps the
  // other window quiet.
  it("keeps quiet once the holder claimed the answer, even when no window lists the run", async () => {
    const platform = makePlatform();
    const applied = deferred<RemoteVerbResult>();
    const holderRuns = makeRuns(true);
    holderRuns.cancelByRemoteRunId.mockReturnValue(applied.promise);
    const holder = makeWindow(101, holderRuns, platform);
    const other = makeWindow(102, makeRuns(false), platform);
    // The other window looks only once the holder's claim is on disk, however
    // long the filesystem takes: the claim, not a timing, keeps it quiet.
    const claimed = deferred<void>();
    const claimAnswer = holder.ledger.claimAnswer.bind(holder.ledger);
    vi.spyOn(holder.ledger, "claimAnswer").mockImplementation(async (id) => {
      const won = await claimAnswer(id);
      claimed.resolve();
      return won;
    });

    const holderDone = holder.handler.consume(verb("cancel"), "cancel");
    await claimed.promise;
    await other.handler.consume(verb("cancel"), "cancel");
    expect(platform.agentAcknowledgeCommand).not.toHaveBeenCalled();

    applied.resolve("applied");
    await holderDone;
    expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    expect(platform.agentAcknowledgeCommand.mock.calls[0][2]).toBe("applied");
  });

  it.each(["cancel", "pause", "resume", "approve"] as const)(
    "refuses a %s no window holds once, no-active-run, within the grace",
    async (type) => {
      const platform = makePlatform();
      const a = makeWindow(101, makeRuns(false), platform);
      const b = makeWindow(102, makeRuns(false), platform);

      const started = Date.now();
      await Promise.all([a.handler.consume(verb(type), type), b.handler.consume(verb(type), type)]);

      expect(Date.now() - started).toBeGreaterThanOrEqual(GRACE_MS - 5);
      expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
      const [agentId, id, outcome, detail] = platform.agentAcknowledgeCommand.mock.calls[0];
      expect([agentId, id, outcome]).toEqual(["agent-machine", `cmd-${type}`, "rejected"]);
      expect(detail).toMatch(/^no-active-run: /);
      // Nothing was applied anywhere.
      for (const w of [a, b]) {
        expect(w.runs.cancelByRemoteRunId).not.toHaveBeenCalled();
        expect(w.runs.pauseByRemoteRunId).not.toHaveBeenCalled();
        expect(w.runs.resumeByRemoteRunId).not.toHaveBeenCalled();
      }
    }
  );

  it("refuses it from a window that is alone on the machine", async () => {
    const platform = makePlatform();
    const only = makeWindow(101, makeRuns(false), platform);
    await only.handler.consume(verb("cancel"), "cancel");
    expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    expect(platform.agentAcknowledgeCommand.mock.calls[0][2]).toBe("rejected");
  });

  it("does not refuse a run another live window lists, which answers it itself", async () => {
    const platform = makePlatform();
    const other = makeWindow(101, makeRuns(false), platform);
    // The holding window lists the run; its stream has not delivered yet.
    await new RemoteRunLedger(dir, { windowId: 102 }).publish(["run-1"]);
    await other.handler.consume(verb("pause"), "pause");
    expect(platform.agentAcknowledgeCommand).not.toHaveBeenCalled();
  });

  it("refuses past the listing of a window whose process is gone", async () => {
    const platform = makePlatform();
    const ledger = new RemoteRunLedger(dir, { windowId: 101, isAlive: (pid) => pid !== 102 });
    await new RemoteRunLedger(dir, { windowId: 102 }).publish(["run-1"]);
    const handler = new RunVerbCommandHandler(
      makeRuns(false) as never,
      platform,
      makeLogger() as never,
      undefined,
      undefined,
      { ledger, graceMs: GRACE_MS }
    );
    handler.setAgentId("agent-machine");

    await handler.consume(verb("cancel"), "cancel");
    expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    expect(platform.agentAcknowledgeCommand.mock.calls[0][2]).toBe("rejected");
  });

  // A trigger this window was accepting queued the run during the grace.
  it("answers as the holder when it comes to hold the run during the grace", async () => {
    const platform = makePlatform();
    const runs = makeRuns(false);
    runs.holdsRemoteRun.mockResolvedValueOnce(false).mockResolvedValue(true);
    const w = makeWindow(101, runs, platform);

    await w.handler.consume(verb("cancel"), "cancel");
    expect(runs.cancelByRemoteRunId).toHaveBeenCalledWith("run-1");
    expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    expect(platform.agentAcknowledgeCommand.mock.calls[0][2]).toBe("applied");
  });

  it("refuses a command delivered twice during the grace once, and re-sends only a failed refusal", async () => {
    const platform = makePlatform();
    platform.agentAcknowledgeCommand.mockRejectedValueOnce(new Error("offline"));
    const w = makeWindow(101, makeRuns(false), platform);

    await Promise.all([
      w.handler.consume(verb("cancel"), "cancel"),
      w.handler.consume(verb("cancel"), "cancel"),
    ]);
    expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);

    // A later copy re-sends the refusal that did not reach the platform.
    await w.handler.consume(verb("cancel"), "cancel");
    expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(2);
    expect(platform.agentAcknowledgeCommand.mock.calls[1][2]).toBe("rejected");
    await w.handler.consume(verb("cancel"), "cancel");
    expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(2);
  });

  it("leaves the verb unanswered, as before, without a ledger", async () => {
    const platform = makePlatform();
    const handler = new RunVerbCommandHandler(
      makeRuns(false) as never,
      platform,
      makeLogger() as never
    );
    handler.setAgentId("agent-machine");
    await handler.consume(verb("cancel"), "cancel");
    expect(platform.agentAcknowledgeCommand).not.toHaveBeenCalled();
  });
});
