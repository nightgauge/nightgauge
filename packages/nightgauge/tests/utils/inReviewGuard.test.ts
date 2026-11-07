import { describe, it, expect, vi } from "vitest";
import { moveToInReviewIfPRBacked, type ParkInReviewDeps } from "../../src/utils/inReviewGuard";
import type { Logger } from "../../src/utils/logger";

function makeLogger(): Logger {
  return { info: vi.fn(), warn: vi.fn(), debug: vi.fn(), error: vi.fn() } as unknown as Logger;
}

function deps(exec: ParkInReviewDeps["exec"], binary: string | null = "/bin/nightgauge") {
  return { resolveBinary: vi.fn(async () => binary), exec: vi.fn(exec) };
}

// #1496: every extension "In review" write goes through the guarded CLI, which
// refuses without an open PR. A refusal writes nothing and is not retried.
describe("moveToInReviewIfPRBacked", () => {
  it("writes In review through the guarded CLI when an open PR backs the issue", async () => {
    const d = deps(async () => ({ stdout: "Synced" }));
    const outcome = await moveToInReviewIfPRBacked(312, "/repo", makeLogger(), "acme/app", {
      deps: d,
    });
    expect(outcome).toBe("moved");
    expect(d.exec).toHaveBeenCalledTimes(1);
    expect(d.exec).toHaveBeenCalledWith(
      "/bin/nightgauge",
      ["project", "sync-status", "312", "in-review", "--repo", "acme/app"],
      "/repo"
    );
  });

  it("treats the no-open-PR refusal as no write, without retrying", async () => {
    const d = deps(async () => {
      throw Object.assign(new Error("Command failed"), {
        stderr:
          "Error: refusing In review: no open PR whose head branch names the issue (acme/app#312)",
      });
    });
    const logger = makeLogger();
    const outcome = await moveToInReviewIfPRBacked(312, "/repo", logger, "acme/app", { deps: d });
    expect(outcome).toBe("refused");
    expect(d.exec).toHaveBeenCalledTimes(1);
    expect(logger.warn).not.toHaveBeenCalled();
  });

  it("reports other CLI errors as failed, not refused", async () => {
    const d = deps(async () => {
      throw Object.assign(new Error("Command failed"), { stderr: "Error: network down" });
    });
    const logger = makeLogger();
    expect(await moveToInReviewIfPRBacked(312, "/repo", logger, undefined, { deps: d })).toBe(
      "failed"
    );
    expect(d.exec).toHaveBeenCalledWith(
      "/bin/nightgauge",
      ["project", "sync-status", "312", "in-review"],
      "/repo"
    );
    expect(logger.warn).toHaveBeenCalled();
  });

  it("passes --only-from and reports the CLI's yield", async () => {
    const d = deps(async () => {
      throw Object.assign(new Error("Command failed"), {
        stderr: 'Error: yielding In review for o/r#312: it is "Ready", not "In progress"',
      });
    });
    const outcome = await moveToInReviewIfPRBacked(312, "/repo", makeLogger(), undefined, {
      onlyFrom: "In progress",
      deps: d,
    });
    expect(outcome).toBe("yielded");
    expect(d.exec).toHaveBeenCalledWith(
      "/bin/nightgauge",
      ["project", "sync-status", "312", "in-review", "--only-from", "In progress"],
      "/repo"
    );
  });

  it("writes nothing when the binary cannot be found", async () => {
    const d = deps(async () => ({}), null);
    expect(await moveToInReviewIfPRBacked(312, "/repo", makeLogger(), undefined, { deps: d })).toBe(
      "failed"
    );
    expect(d.exec).not.toHaveBeenCalled();
  });
});

// #2706: the park and the scheduler's revert race. The fake applies the CLI's
// --only-from rule to one board, so a park that drops the flag ends In review.
describe("failed-run park against the scheduler's revert", () => {
  function board(start: string) {
    const state = { status: start };
    const cli = (args: string[], status: string) => {
      const from = args[args.indexOf("--only-from") + 1];
      if (args.includes("--only-from") && state.status !== from) {
        throw Object.assign(new Error("Command failed"), {
          stderr: `Error: yielding ${status}: it is "${state.status}"`,
        });
      }
      state.status = status;
    };
    const park = () =>
      moveToInReviewIfPRBacked(312, "/repo", makeLogger(), undefined, {
        onlyFrom: "In progress",
        deps: deps(async (_binary, args) => {
          cli(args, "In review");
          return {};
        }),
      });
    // revertFailedIssueStatus is an unconditional MoveStatus: no --only-from.
    const revert = () => cli(["project", "move-status", "312", "ready"], "Ready");
    return { state, park, revert };
  }

  it("ends Ready when the park lands first", async () => {
    const b = board("In progress");
    expect(await b.park()).toBe("moved");
    b.revert();
    expect(b.state.status).toBe("Ready");
  });

  it("ends Ready when the revert lands first", async () => {
    const b = board("In progress");
    b.revert();
    expect(await b.park()).toBe("yielded");
    expect(b.state.status).toBe("Ready");
  });

  it("keeps the In review park on a TS-only run with no revert", async () => {
    const b = board("In progress");
    expect(await b.park()).toBe("moved");
    expect(b.state.status).toBe("In review");
  });
});
