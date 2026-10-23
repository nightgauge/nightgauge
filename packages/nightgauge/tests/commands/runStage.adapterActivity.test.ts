/**
 * runStage.adapterActivity.test.ts (Issue #1657)
 *
 * The SDK stage CLI prints an "adapter activity" log line for each OpenCode
 * step and tool call while the process runs. It exists to move the idle
 * clock; Run Stage and the slot output channels must never render it, while
 * every other `{"level","message"}` log line still shows.
 */
import { describe, it, expect, vi } from "vitest";
import { OutputFormatter } from "@nightgauge/sdk/dist/cli/output";
import { parseStreamOutput } from "../../src/commands/runStage";
import { isAdapterActivityLine, stripAdapterActivityLines } from "../../src/utils/streamJsonFilter";

/** Lines exactly as the SDK's own formatter prints them in JSON mode. */
function sdkLines(write: (f: InstanceType<typeof OutputFormatter>) => void): string[] {
  const log = vi.spyOn(console, "log").mockImplementation(() => {});
  try {
    write(new OutputFormatter("json", "info"));
    return log.mock.calls.map((c) => c[0] as string);
  } finally {
    log.mockRestore();
  }
}

const ACTIVITY = sdkLines((f) => {
  f.activity({ adapter: "opencode", event: "step_start" });
  f.activity({ adapter: "opencode", event: "tool_use" });
  f.activity({ adapter: "opencode", event: "step_finish" });
});
const [INFO] = sdkLines((f) => f.info("Running stage 'feature-dev' for issue #42..."));

describe("adapter activity lines are never rendered (#1657)", () => {
  it("Run Stage's parser drops them and still shows other log lines", () => {
    const items = parseStreamOutput([...ACTIVITY, INFO, ...ACTIVITY].join("\n"));
    expect(items).toEqual([{ type: "text", text: "Running stage 'feature-dev' for issue #42..." }]);
  });

  it("the slot-output strip removes only them", () => {
    const data = [ACTIVITY[0], "plain text line", ACTIVITY[1], INFO].join("\n");
    expect(stripAdapterActivityLines(data)).toBe(["plain text line", INFO].join("\n"));
    expect(stripAdapterActivityLines(ACTIVITY.join("\n")).trim()).toBe("");
  });

  it("matches on the exact message with a string data.event, nothing looser", () => {
    for (const line of ACTIVITY) expect(isAdapterActivityLine(line)).toBe(true);
    expect(isAdapterActivityLine(INFO)).toBe(false);
    expect(
      isAdapterActivityLine(JSON.stringify({ level: "info", message: "adapter activity" }))
    ).toBe(false);
    expect(
      isAdapterActivityLine(
        JSON.stringify({ level: "info", message: "adapter activity seen", data: { event: "x" } })
      )
    ).toBe(false);
    expect(isAdapterActivityLine('{"level":"debug","message":"adapter activity"')).toBe(false);
  });
});
