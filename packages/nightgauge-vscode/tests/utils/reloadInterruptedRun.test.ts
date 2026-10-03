/**
 * #2339: which paused snapshot is a platform run a window reload ended.
 */

import { describe, it, expect } from "vitest";

import { reloadInterruptedRemoteRun } from "../../src/utils/reloadInterruptedRun";

const dead = () => false;
const alive = () => true;

describe("reloadInterruptedRemoteRun (#2339)", () => {
  it("names the platform run of a paused snapshot whose owner is gone", () => {
    expect(
      reloadInterruptedRemoteRun(
        { paused: true, issueNumber: 7, remoteRunId: "run-7", ownerPid: 4242 },
        dead
      )
    ).toEqual({ remoteRunId: "run-7", issueNumber: 7 });
    // A snapshot that records no owner refuses nothing.
    expect(
      reloadInterruptedRemoteRun({ paused: true, issueNumber: 7, remoteRunId: "run-7" }, alive)
    ).toEqual({ remoteRunId: "run-7", issueNumber: 7 });
  });

  it("leaves a run whose owner is alive to the window that runs it", () => {
    expect(
      reloadInterruptedRemoteRun(
        { paused: true, issueNumber: 7, remoteRunId: "run-7", ownerPid: 4242 },
        alive
      )
    ).toBeNull();
  });

  it("ignores a snapshot that is not paused, names no platform run, or no issue", () => {
    expect(
      reloadInterruptedRemoteRun({ paused: false, issueNumber: 7, remoteRunId: "run-7" }, dead)
    ).toBeNull();
    expect(reloadInterruptedRemoteRun({ paused: true, issueNumber: 7 }, dead)).toBeNull();
    expect(
      reloadInterruptedRemoteRun({ paused: true, issueNumber: 7, remoteRunId: "" }, dead)
    ).toBeNull();
    expect(
      reloadInterruptedRemoteRun({ paused: true, issueNumber: 7, remoteRunId: 9 }, dead)
    ).toBeNull();
    expect(reloadInterruptedRemoteRun({ paused: true, remoteRunId: "run-7" }, dead)).toBeNull();
  });

  it("checks the owner with the real process table by default", () => {
    expect(
      reloadInterruptedRemoteRun({
        paused: true,
        issueNumber: 7,
        remoteRunId: "run-7",
        ownerPid: process.pid,
      })
    ).toBeNull();
  });
});
