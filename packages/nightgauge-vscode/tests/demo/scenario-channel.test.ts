/**
 * The demo daemon's scenario channel (#2105, #2106, #2110, ADR-026).
 *
 * The extension spawns `<binaryPath> serve --workspace <root>` and a setting
 * cannot carry arguments, so a demo window and the `vscode-host` tier
 * configure the daemon through the environment: the scenario, its speed, a
 * start file that holds playback until a recorder is ready, and an event log
 * that two runs of one scenario compare byte-for-byte.
 */

import { spawn } from "node:child_process";
import * as fs from "node:fs";
import { createRequire } from "node:module";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

const packageRoot = path.resolve(__dirname, "..", "..");
const ENTRY = path.join(packageRoot, "demo", "ipc-stub.cjs");
const REFERENCE = path.join(packageRoot, "demo", "scenarios", "reference.json");

const load = createRequire(__filename);
const { relativeToStart } = load("../../demo/daemon/scenario.cjs") as {
  relativeToStart(value: unknown, epochMs: number): unknown;
};

let dir: string;
beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), "demo-channel-"));
});
afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

interface Run {
  code: number | null;
  stdout: string;
  stderr: string;
}

/**
 * Run the daemon with `env`, send `requests` once `ready` matches stderr (or
 * at once), and close stdin once `until` matches stderr (or after sending).
 */
function runDaemon(opts: {
  args?: string[];
  env?: Record<string, string>;
  requests?: Array<{ method: string; params?: unknown }>;
  until?: RegExp;
  onStderr?: (stderr: string) => void;
}): Promise<Run> {
  return new Promise((resolve) => {
    const proc = spawn(process.execPath, [ENTRY, "serve", ...(opts.args ?? [])], {
      env: {
        PATH: process.env.PATH,
        NIGHTGAUGE_DEMO_IPC_LOG: path.join(dir, "ipc.jsonl"),
        ...opts.env,
      },
    });
    let stdout = "";
    let stderr = "";
    let closed = false;
    const close = () => {
      if (!closed) {
        closed = true;
        proc.stdin.end();
      }
    };
    proc.stdout.on("data", (c: Buffer) => (stdout += c.toString()));
    proc.stderr.on("data", (c: Buffer) => {
      stderr += c.toString();
      opts.onStderr?.(stderr);
      if (opts.until && opts.until.test(stderr)) close();
    });
    (opts.requests ?? []).forEach((r, i) =>
      proc.stdin.write(JSON.stringify({ id: i + 1, ...r }) + "\n")
    );
    if (!opts.until) close();
    proc.on("exit", (code) => resolve({ code, stdout, stderr }));
  });
}

function scenarioFile(name: string, owner: string, steps: unknown[] = []): string {
  const file = path.join(dir, `${name}.json`);
  const reference = JSON.parse(fs.readFileSync(REFERENCE, "utf8"));
  fs.writeFileSync(
    file,
    JSON.stringify({ ...reference, state: { ...reference.state, owner }, steps })
  );
  return file;
}

function responses(stdout: string): Array<Record<string, unknown>> {
  return stdout
    .trim()
    .split("\n")
    .map((line) => JSON.parse(line) as Record<string, unknown>)
    .filter((m) => m.id !== undefined);
}

describe("scenario channel", () => {
  it("loads the scenario named by NIGHTGAUGE_DEMO_SCENARIO", async () => {
    const run = await runDaemon({
      env: { NIGHTGAUGE_DEMO_SCENARIO: scenarioFile("alpha", "alpha-harbour") },
      requests: [{ method: "config.getProjectConfig" }],
    });
    expect(run.code, run.stderr).toBe(0);
    expect(responses(run.stdout)[0].result).toMatchObject({ owner: "alpha-harbour" });
  });

  it("lets the --scenario flag win over the environment", async () => {
    const run = await runDaemon({
      args: ["--scenario", scenarioFile("bravo", "bravo-harbour")],
      env: { NIGHTGAUGE_DEMO_SCENARIO: scenarioFile("alpha", "alpha-harbour") },
      requests: [{ method: "config.getProjectConfig" }],
    });
    expect(responses(run.stdout)[0].result).toMatchObject({ owner: "bravo-harbour" });
  });

  it("rejects an invalid NIGHTGAUGE_DEMO_SPEED before ipc.ready", async () => {
    const run = await runDaemon({ env: { NIGHTGAUGE_DEMO_SPEED: "fast" } });
    expect(run.code).toBe(1);
    expect(run.stdout).toBe("");
    expect(run.stderr).toMatch(/--speed must be a positive number/);
  });

  it("holds playback until the start file exists, then plays every step", async () => {
    const startFile = path.join(dir, "start");
    const eventLog = path.join(dir, "events.jsonl");
    let waitingSeenAt = 0;
    let beforeSignal: string[] = [];
    const run = await runDaemon({
      env: {
        NIGHTGAUGE_DEMO_SCENARIO: REFERENCE,
        NIGHTGAUGE_DEMO_SPEED: "400",
        NIGHTGAUGE_DEMO_START_FILE: startFile,
        NIGHTGAUGE_DEMO_EVENT_LOG: eventLog,
      },
      until: /scenario finished/,
      onStderr: (stderr) => {
        if (!waitingSeenAt && /waiting for the start signal/.test(stderr)) {
          waitingSeenAt = Date.now();
          // Long enough that an unheld player (60 s / 400 = 150 ms) would
          // have finished before the signal.
          setTimeout(() => {
            beforeSignal = fs
              .readFileSync(eventLog, "utf8")
              .trim()
              .split("\n")
              .map((l) => JSON.parse(l).event as string);
            fs.writeFileSync(startFile, "");
          }, 400);
        }
      },
    });
    expect(run.code, run.stderr).toBe(0);
    expect(beforeSignal).toEqual(["ipc.ready"]);
    expect(run.stderr).toMatch(/start signal received/);
    const events = fs
      .readFileSync(eventLog, "utf8")
      .trim()
      .split("\n")
      .map((l) => JSON.parse(l).event as string);
    expect(events[0]).toBe("ipc.ready");
    expect(events).toContain("pipeline.complete");
    expect(events.filter((e) => e === "demo.uiStep").length).toBeGreaterThanOrEqual(4);
  });

  it("writes a byte-identical event log on every run, whatever the wall clock", async () => {
    const play = async (name: string) => {
      const eventLog = path.join(dir, `${name}.jsonl`);
      const run = await runDaemon({
        env: {
          NIGHTGAUGE_DEMO_SCENARIO: REFERENCE,
          NIGHTGAUGE_DEMO_SPEED: "400",
          NIGHTGAUGE_DEMO_EVENT_LOG: eventLog,
        },
        until: /scenario finished/,
      });
      expect(run.code, run.stderr).toBe(0);
      return fs.readFileSync(eventLog, "utf8");
    };
    const first = await play("first");
    await new Promise((resolve) => setTimeout(resolve, 25));
    const second = await play("second");
    expect(first.split("\n").length).toBeGreaterThan(40);
    expect(second).toBe(first);
    // Timestamps are offsets from the scenario start, never wall-clock times.
    expect(first).not.toMatch(/\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/);
  });
});

describe("relativeToStart", () => {
  it("replaces timestamps and dates with offsets from the scenario start", () => {
    const epoch = Date.parse("2026-03-02T10:00:00.000Z");
    expect(
      relativeToStart(
        {
          at: "2026-03-02T10:00:01.500Z",
          before: "2026-03-02T09:00:00Z",
          day: "2026-02-28",
          list: ["2026-03-02", "label", 3],
        },
        epoch
      )
    ).toEqual({
      at: "T+1500ms",
      before: "T-3600000ms",
      day: "D-2",
      list: ["D+0", "label", 3],
    });
  });
});
