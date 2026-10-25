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
    const outcome = await moveToInReviewIfPRBacked(312, "/repo", makeLogger(), "acme/app", d);
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
    const outcome = await moveToInReviewIfPRBacked(312, "/repo", logger, "acme/app", d);
    expect(outcome).toBe("refused");
    expect(d.exec).toHaveBeenCalledTimes(1);
    expect(logger.warn).not.toHaveBeenCalled();
  });

  it("reports other CLI errors as failed, not refused", async () => {
    const d = deps(async () => {
      throw Object.assign(new Error("Command failed"), { stderr: "Error: network down" });
    });
    const logger = makeLogger();
    expect(await moveToInReviewIfPRBacked(312, "/repo", logger, undefined, d)).toBe("failed");
    expect(d.exec).toHaveBeenCalledWith(
      "/bin/nightgauge",
      ["project", "sync-status", "312", "in-review"],
      "/repo"
    );
    expect(logger.warn).toHaveBeenCalled();
  });

  it("writes nothing when the binary cannot be found", async () => {
    const d = deps(async () => ({}), null);
    expect(await moveToInReviewIfPRBacked(312, "/repo", makeLogger(), undefined, d)).toBe("failed");
    expect(d.exec).not.toHaveBeenCalled();
  });
});
