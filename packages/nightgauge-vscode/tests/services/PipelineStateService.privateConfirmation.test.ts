/**
 * The private badge is keyed by run (#2400): the hosted service's answer for
 * one run never marks another run private, a late answer for an earlier run
 * is dropped, and a missing or non-private answer shows no badge.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const ipcCalls: Array<{ method: string; params: Record<string, unknown> }> = [];

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      call: vi.fn((method: string, params: Record<string, unknown>) => {
        ipcCalls.push({ method, params });
        return Promise.resolve({ status: "ok" });
      }),
    }),
  },
}));

vi.mock("vscode", () => ({
  EventEmitter: class {
    private _handlers: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value: unknown) {
      for (const h of this._handlers) h(value);
    }
    dispose() {}
  },
  Disposable: class {
    dispose() {}
  },
  window: {
    showWarningMessage: vi.fn(() => Promise.resolve(undefined)),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

async function makeService(issueNumber: number) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  return PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
}

/** Real identities, minted by the production minter — never hand-authored (#166). */
async function mint(): Promise<string> {
  const { uuidV7 } = await import("@nightgauge/sdk");
  return uuidV7();
}

import * as vscode from "vscode";
import { confirmAndReportPrivateRun } from "../../src/services/RunVisibility";

const noSleep = { sleep: async () => {}, delaysMs: [1] };

describe("private confirmation is keyed by run (#2400)", () => {
  it("two runs started back to back: only the one the service confirmed private shows it", async () => {
    // Two slots, each with its own state service, started one after the
    // other; the service answers private for the first and team for the
    // second, and the answers arrive in the opposite order.
    const a = await makeService(10);
    const b = await makeService(11);
    const runA = await mint();
    const runB = await mint();
    a.beginRun(runA, "acme/app", 10, undefined, "private");
    b.beginRun(runB, "acme/app", 11, undefined, "private");
    const answers: Record<string, { found: boolean; visibility?: string }> = {
      [runA]: { found: true, visibility: "private" },
      [runB]: { found: true, visibility: "team" },
    };
    const read = vi.fn(async (_issue: number, runId: string) => answers[runId]);

    await confirmAndReportPrivateRun(b, 11, runB, "acme/app", read, noSleep);
    await confirmAndReportPrivateRun(a, 10, runA, "acme/app", read, noSleep);

    expect(a.isPrivateConfirmed()).toBe(true);
    expect(b.isPrivateConfirmed()).toBe(false);
  });

  it("a late answer for the previous run never marks the next run private", async () => {
    const svc = await makeService(12);
    const first = await mint();
    svc.beginRun(first, "acme/app", 12, undefined, "private");
    svc.endRun(first);
    const second = await mint();
    svc.beginRun(second, "acme/app", 12); // a team run on the same service

    // The first run's read finishes now, saying private.
    expect(svc.setPrivateConfirmation(first, "confirmed")).toBe(false);
    expect(svc.isPrivateConfirmed()).toBe(false);

    // Even an answer naming the team run cannot badge it.
    expect(svc.setPrivateConfirmation(second, "confirmed")).toBe(false);
    expect(svc.isPrivateConfirmed()).toBe(false);
  });

  it("the choice does not carry over: the next run starts unconfirmed", async () => {
    const svc = await makeService(13);
    const first = await mint();
    svc.beginRun(first, "acme/app", 13, undefined, "private");
    expect(svc.setPrivateConfirmation(first, "confirmed")).toBe(true);
    expect(svc.isPrivateConfirmed()).toBe(true);
    svc.endRun(first);
    expect(svc.isPrivateConfirmed()).toBe(false);

    const second = await mint();
    svc.beginRun(second, "acme/app", 13, undefined, "private");
    expect(svc.getVisibility()).toBe("private");
    expect(svc.isPrivateConfirmed()).toBe(false);
  });

  it("fails closed: no answer, or an answer without private, shows no badge", async () => {
    const svc = await makeService(14);
    const runId = await mint();
    svc.beginRun(runId, "acme/app", 14, undefined, "private");
    expect(svc.isPrivateConfirmed()).toBe(false);

    vi.mocked(vscode.window.showWarningMessage).mockClear();
    const neverFound = vi.fn(async () => ({ found: false }));
    await expect(
      confirmAndReportPrivateRun(svc, 14, runId, "acme/app", neverFound, noSleep)
    ).resolves.toBe("unconfirmed");
    expect(svc.isPrivateConfirmed()).toBe(false);

    // Fail closed means no badge PLUS the notice.
    expect(vscode.window.showWarningMessage).toHaveBeenCalledTimes(1);

    const noVisibility = vi.fn(async () => ({ found: true }));
    await expect(
      confirmAndReportPrivateRun(svc, 14, runId, "acme/app", noVisibility, noSleep)
    ).resolves.toBe("unconfirmed");
    expect(svc.isPrivateConfirmed()).toBe(false);
  });
});
