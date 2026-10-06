/**
 * RunVisibility — the private-run choice and its confirmation (#2400).
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import {
  chooseRunVisibility,
  confirmAndReportPrivateRun,
  confirmPrivateRun,
  visibilityOption,
  PRIVATE_RUN_DETAIL,
  type PrivateConfirmation,
} from "../../src/services/RunVisibility";

const noSleep = { sleep: async () => {} };

describe("chooseRunVisibility", () => {
  let showQuickPick: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    showQuickPick = vi.fn();
    (vscode.window as unknown as { showQuickPick: unknown }).showQuickPick = showQuickPick;
  });

  it("offers no choice when the window is signed out: the run is team", async () => {
    await expect(chooseRunVisibility(async () => false)).resolves.toBe("team");
    expect(showQuickPick).not.toHaveBeenCalled();
  });

  it("defaults to team, states what private hides, and asks again on every run", async () => {
    showQuickPick.mockImplementation(async (items: Array<{ visibility: string }>) => items[0]);
    await expect(chooseRunVisibility(async () => true)).resolves.toBe("team");

    const items = showQuickPick.mock.calls[0][0] as Array<{ visibility: string; detail?: string }>;
    expect(items.map((i) => i.visibility)).toEqual(["team", "private"]);
    // Next to the choice: what private does not hide, and what owners and
    // admins still see.
    expect(items[1].detail).toBe(PRIVATE_RUN_DETAIL);
    expect(PRIVATE_RUN_DETAIL).toMatch(/GitHub/);
    expect(PRIVATE_RUN_DETAIL).toMatch(/[Oo]wners and admins still see/);
    expect(PRIVATE_RUN_DETAIL).toMatch(/what it cost/);

    // A private choice is not remembered: the next run asks again, from team.
    showQuickPick.mockImplementationOnce(async (it: Array<{ visibility: string }>) => it[1]);
    await expect(chooseRunVisibility(async () => true)).resolves.toBe("private");
    showQuickPick.mockImplementationOnce(async (it: Array<{ visibility: string }>) => it[0]);
    await expect(chooseRunVisibility(async () => true)).resolves.toBe("team");
    expect(showQuickPick).toHaveBeenCalledTimes(3);
    for (const call of showQuickPick.mock.calls) {
      expect((call[0] as Array<{ visibility: string }>)[0].visibility).toBe("team");
    }
  });

  it("resolves undefined when the member dismisses the choice", async () => {
    showQuickPick.mockResolvedValue(undefined);
    await expect(chooseRunVisibility(async () => true)).resolves.toBeUndefined();
  });
});

describe("visibilityOption", () => {
  it("names only private", () => {
    expect(visibilityOption("private")).toEqual({ visibility: "private" });
    expect(visibilityOption("team")).toEqual({});
    expect(visibilityOption(undefined)).toEqual({});
  });
});

describe("confirmPrivateRun", () => {
  it("confirms only on the service's private answer for the run", async () => {
    const read = vi
      .fn()
      .mockResolvedValueOnce({ found: false })
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce({ found: true, visibility: "private" });
    await expect(confirmPrivateRun(read, 912, "run-1", noSleep)).resolves.toBe("confirmed");
    expect(read).toHaveBeenCalledTimes(3);
    expect(read).toHaveBeenCalledWith(912, "run-1");
  });

  it("is unconfirmed at once when the service answers for the run without private", async () => {
    const read = vi.fn().mockResolvedValue({ found: true, visibility: "team" });
    await expect(confirmPrivateRun(read, 912, "run-1", noSleep)).resolves.toBe("unconfirmed");
    expect(read).toHaveBeenCalledTimes(1);
  });

  it("is unconfirmed when the run never reaches the service", async () => {
    const read = vi.fn().mockResolvedValue({ found: false });
    await expect(
      confirmPrivateRun(read, 912, "run-1", { ...noSleep, delaysMs: [1, 1, 1] })
    ).resolves.toBe("unconfirmed");
    expect(read).toHaveBeenCalledTimes(3);
  });
});

describe("confirmAndReportPrivateRun", () => {
  beforeEach(() => {
    vi.mocked(vscode.window.showWarningMessage).mockClear();
  });

  it("a service answer without private shows the notice and records no confirmation", async () => {
    const outcomes: PrivateConfirmation[] = [];
    const target = { setPrivateConfirmation: (o: PrivateConfirmation) => outcomes.push(o) };
    const read = vi.fn().mockResolvedValue({ found: true, visibility: "team" });

    await confirmAndReportPrivateRun(target, 912, "run-1", "acme/app", read, noSleep);

    expect(outcomes).toEqual(["unconfirmed"]);
    expect(vscode.window.showWarningMessage).toHaveBeenCalledTimes(1);
    expect(String(vi.mocked(vscode.window.showWarningMessage).mock.calls[0][0])).toContain(
      "did not confirm run acme/app#912 as private"
    );
  });

  it("a private answer confirms the run without a notice", async () => {
    const outcomes: PrivateConfirmation[] = [];
    const target = { setPrivateConfirmation: (o: PrivateConfirmation) => outcomes.push(o) };
    const read = vi.fn().mockResolvedValue({ found: true, visibility: "private" });

    await confirmAndReportPrivateRun(target, 912, "run-1", "acme/app", read, noSleep);

    expect(outcomes).toEqual(["confirmed"]);
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
  });
});
