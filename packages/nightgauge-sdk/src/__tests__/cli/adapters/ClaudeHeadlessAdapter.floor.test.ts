/**
 * ClaudeHeadlessAdapter version-floor tests (#1621).
 *
 * Proves the general compatibility floor `validateAuth()` now applies: a
 * detected `claude --version` below `ADAPTER_COMPAT["claude-headless"].minVersion`
 * warns once (naming the floor), a version at or above it warns not at all,
 * and — mirroring Codex/Gemini/Grok — this never throws or blocks auth.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { ClaudeHeadlessAdapter } from "../../../cli/adapters/ClaudeHeadlessAdapter.js";
import { ADAPTER_COMPAT } from "../../../cli/adapters/adapterCompat.generated.js";
import type {
  PreflightCommandRunner,
  PreflightCommandResult,
} from "../../../cli/codexPreflight.js";

const MIN_VERSION = ADAPTER_COMPAT["claude-headless"].minVersion;

function makeRunner(version: string): PreflightCommandRunner {
  return async (command, args): Promise<PreflightCommandResult> => {
    if (command === "claude" && args[0] === "--version") {
      return { code: 0, stdout: `${version}\n`, stderr: "" };
    }
    if (command === "claude" && args[0] === "auth" && args[1] === "status") {
      // Authenticated — validateAuth must resolve "passed" regardless of the
      // version floor, which only ever warns.
      return { code: 0, stdout: "", stderr: "" };
    }
    return { code: 1, stdout: "", stderr: "unexpected command" };
  };
}

describe("ClaudeHeadlessAdapter version floor", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("warns naming the manifest minVersion when the detected CLI is below it", async () => {
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const adapter = new ClaudeHeadlessAdapter();

    const result = await adapter.validateAuth({ runner: makeRunner("2.1.100"), cwd: "/tmp" });

    expect(result).toBe("passed");
    expect(warnSpy).toHaveBeenCalledTimes(1);
    const [message] = warnSpy.mock.calls[0] as [string];
    expect(message).toContain("2.1.100");
    expect(message).toContain(MIN_VERSION);
    expect(MIN_VERSION).toBe("2.1.223");
  });

  it("does not warn when the detected CLI is at or above minVersion", async () => {
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const adapter = new ClaudeHeadlessAdapter();

    const result = await adapter.validateAuth({ runner: makeRunner("2.1.258"), cwd: "/tmp" });

    expect(result).toBe("passed");
    expect(warnSpy).not.toHaveBeenCalled();
  });

  it("stays a no-op when the version is undetectable", async () => {
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const adapter = new ClaudeHeadlessAdapter();
    const runner: PreflightCommandRunner = async (command, args) => {
      if (command === "claude" && args[0] === "--version") {
        // Installed check passes, but the version cannot be parsed.
        return { code: 0, stdout: "not a version\n", stderr: "" };
      }
      if (command === "claude" && args[0] === "auth" && args[1] === "status") {
        return { code: 0, stdout: "", stderr: "" };
      }
      return { code: 1, stdout: "", stderr: "unexpected command" };
    };

    const result = await adapter.validateAuth({ runner, cwd: "/tmp" });

    expect(result).toBe("passed");
    expect(warnSpy).not.toHaveBeenCalled();
  });
});
