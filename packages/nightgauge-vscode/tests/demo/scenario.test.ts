/**
 * Demo scenario format and deterministic player (#2106, ADR-026 decision 3).
 */

import { spawn } from "node:child_process";
import * as fs from "node:fs";
import { createRequire } from "node:module";
import * as os from "node:os";
import * as path from "node:path";
import { describe, expect, it } from "vitest";

const packageRoot = path.resolve(__dirname, "..", "..");
const ENTRY = path.join(packageRoot, "demo", "ipc-stub.cjs");
const REFERENCE = path.join(packageRoot, "demo", "scenarios", "reference.json");
const EPOCH = Date.parse("2026-03-02T10:00:00.000Z");

interface Step {
  at: number;
  kind: string;
  [key: string]: unknown;
}
interface Scenario {
  seed: number;
  state: Record<string, unknown>;
  steps: Step[];
}
type Obj = Record<string, unknown>;

const load = createRequire(__filename);
const { createDaemon } = load("../../demo/daemon/daemon.cjs") as {
  createDaemon(opts: { state: unknown; send: (m: unknown) => void; log?: (m: string) => void }): {
    handle(request: { id: number; method: string; params?: unknown }): void;
    state: Obj;
  };
};
const { createState } = load("../../demo/daemon/state.cjs") as {
  createState(raw: unknown): Obj;
};
const { createPlayer, loadScenario, rebaseState } = load("../../demo/daemon/scenario.cjs") as {
  loadScenario(raw: unknown): Scenario;
  rebaseState(raw: unknown, epochMs: number): Obj;
  createPlayer(opts: {
    daemon: unknown;
    steps: Step[];
    epochMs: number;
    speed?: number;
    schedule: (fn: () => void, delayMs: number) => void;
    onDone?: () => void;
  }): { start(): void };
};

const reference = (): Scenario => JSON.parse(fs.readFileSync(REFERENCE, "utf8")) as Scenario;

/**
 * Play a scenario on a virtual clock: each scheduled callback runs in order
 * and advances virtual time by its delay. Returns every line the daemon
 * sent, prefixed with its virtual time relative to scenario start.
 */
function play(raw: unknown, speed = 1): { lines: string[]; state: Obj } {
  const scenario = loadScenario(raw);
  const lines: string[] = [];
  let now = 0;
  const daemon = createDaemon({
    state: createState(rebaseState(scenario.state, EPOCH)),
    send: (m) => lines.push(`${now} ${JSON.stringify(m)}`),
  });
  const queue: Array<{ fn: () => void; delay: number }> = [];
  createPlayer({
    daemon,
    steps: scenario.steps,
    epochMs: EPOCH,
    speed,
    schedule: (fn, delay) => queue.push({ fn, delay }),
  }).start();
  while (queue.length) {
    const { fn, delay } = queue.shift()!;
    now += delay;
    fn();
  }
  return { lines, state: daemon.state };
}

function runProcess(args: string[], untilStderr?: RegExp) {
  return new Promise<{ code: number | null; stdout: string; stderr: string }>((resolve) => {
    const proc = spawn(process.execPath, [ENTRY, "serve", ...args], {
      env: {
        PATH: process.env.PATH,
        NIGHTGAUGE_DEMO_IPC_LOG: path.join(os.tmpdir(), "demo-scenario-test.jsonl"),
      },
    });
    let stdout = "";
    let stderr = "";
    proc.stdout.on("data", (c: Buffer) => (stdout += c.toString()));
    proc.stderr.on("data", (c: Buffer) => {
      stderr += c.toString();
      if (untilStderr && untilStderr.test(stderr)) proc.stdin.end();
    });
    if (!untilStderr) proc.stdin.end();
    proc.on("exit", (code) => resolve({ code, stdout, stderr }));
  });
}

describe("scenario player", () => {
  it("emits a byte-identical stream on every run", () => {
    const first = play(reference()).lines.join("\n");
    const second = play(reference()).lines.join("\n");
    expect(first.length).toBeGreaterThan(0);
    expect(second).toBe(first);
  });

  it("scales delays with speed and never changes order", () => {
    const normal = play(reference(), 1).lines;
    const fast = play(reference(), 4).lines;
    const body = (l: string) => l.slice(l.indexOf(" ") + 1);
    const time = (l: string) => Number(l.slice(0, l.indexOf(" ")));
    expect(fast.map(body)).toEqual(normal.map(body));
    fast.forEach((line, i) => expect(time(line)).toBeCloseTo(time(normal[i]) / 4, 6));
  });

  it("stamps timestamps from the scenario clock, rebased to its start", () => {
    const { state } = play(reference());
    const steps = reference().steps;
    const lastAt = steps[steps.length - 1].at;
    expect(state.now).toBe(new Date(EPOCH + lastAt).toISOString());
    const merged = (state.history as Obj[]).find((h) => h.number === 133);
    expect(Date.parse(merged!.completedAt as string)).toBeGreaterThanOrEqual(EPOCH);
    // Seed history keeps its spacing before `now` instead of reading as months old.
    const seeded = (state.history as Obj[]).find((h) => h.number === 101);
    const age = EPOCH - Date.parse(seeded!.completedAt as string);
    expect(age).toBe(
      Date.parse(reference().state.now as string) - Date.parse("2026-01-14T16:20:00.000Z")
    );
  });

  it("runs equal-`at` steps in file order", () => {
    const raw = reference();
    raw.steps = [
      { at: 5, kind: "event", method: "b.first", payload: {} },
      { at: 5, kind: "event", method: "a.second", payload: {} },
    ];
    const events = play(raw).lines.map((l) => JSON.parse(l.slice(l.indexOf(" ") + 1)).event);
    expect(events).toEqual(["b.first", "a.second"]);
  });
});

describe("reference scenario", () => {
  const { lines, state } = play(reference());
  const events = lines.map((l) => JSON.parse(l.slice(l.indexOf(" ") + 1)) as Obj);
  const byEvent = (name: string) =>
    events.filter((e) => e.event === name).map((e) => e.data as Obj);

  it("runs one issue through every stage to merge, in order", () => {
    const stages = byEvent("stage.start")
      .filter((d) => d.issueNumber === 133)
      .map((d) => d.stage);
    expect(stages).toEqual([
      "pipeline-start",
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
      "pipeline-finish",
    ]);
    expect(byEvent("pipeline.complete")).toEqual([
      expect.objectContaining({ issueNumber: 133, success: true }),
    ]);
    const item = (state.board as Obj[]).find((i) => i.number === 133);
    expect(item!.status).toBe("Done");
  });

  it("starts a second run", () => {
    expect(byEvent("stage.start").some((d) => d.issueNumber === 131)).toBe(true);
    expect((state.board as Obj[]).find((i) => i.number === 131)!.status).toBe("In Progress");
  });

  it("raises one attention item and resolves it", () => {
    const attention = byEvent("attention.event");
    expect(attention.map((a) => a.action)).toEqual(["raised", "resolved"]);
    const states = attention.map((a) => ((a.request as Obj).lifecycle as Obj).state);
    expect(states).toEqual(["open", "resolved"]);
  });

  it("uses only fictional names", () => {
    const text = fs.readFileSync(REFERENCE, "utf8");
    expect(text).not.toMatch(/nightgauge|edibu|github\.com|anthropic/i);
    const repos = new Set([...text.matchAll(/"repo": "([^"]+)"/g)].map((m) => m[1]));
    for (const repo of repos) {
      expect([
        "harbor-api",
        "tide-web",
        "lanternworks/harbor-api",
        "lanternworks/tide-web",
      ]).toContain(repo);
    }
  });
});

describe("scenario validation", () => {
  const withSteps = (steps: unknown[]) => ({ ...reference(), steps });

  it.each([
    [
      "an unknown kind",
      withSteps([
        { at: 0, kind: "ui", action: "dashboard.open" },
        { at: 10, kind: "board.teleport" },
      ]),
      'scenario step 1: unknown kind "board.teleport"',
    ],
    [
      "a step that goes back in time",
      withSteps([
        { at: 100, kind: "ui", action: "dashboard.open" },
        { at: 50, kind: "event", method: "stage.start", payload: {} },
      ]),
      'scenario step 1: "at" 50 is before the previous step\'s 100',
    ],
    [
      "a board move to an unknown status",
      withSteps([{ at: 0, kind: "board.move", number: 131, status: "Shipped" }]),
      'scenario step 0: board.move has unknown status "Shipped"',
    ],
  ])("rejects %s", (_name, raw, message) => {
    expect(() => loadScenario(raw)).toThrow(message);
  });

  it("rejects an event that would start a run", () => {
    expect(() =>
      loadScenario(withSteps([{ at: 0, kind: "event", method: "pipeline.runStage" }]))
    ).toThrow(/step 0: event pipeline.runStage is never emitted/);
  });

  it("rejects a missing seed", () => {
    expect(() => loadScenario({ state: {}, steps: [] })).toThrow(/"seed" must be an integer/);
  });
});

describe("demo daemon process with a scenario", () => {
  it("replays byte-identically with a pinned clock", async () => {
    const args = ["--scenario", REFERENCE, "--speed", "200", "--clock", "2026-03-02T10:00:00Z"];
    const a = await runProcess(args, /scenario finished/);
    const b = await runProcess(args, /scenario finished/);
    expect(a.code, a.stderr).toBe(0);
    expect(a.stdout.split("\n")[0]).toContain('"ipc.ready"');
    expect(a.stdout.split("\n").length).toBeGreaterThan(40);
    expect(b.stdout).toBe(a.stdout);
  }, 30000);

  it("exits non-zero before ipc.ready on an invalid scenario", async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "demo-scenario-"));
    const bad = path.join(dir, "bad.json");
    fs.writeFileSync(
      bad,
      JSON.stringify({ ...reference(), steps: [{ at: 0, kind: "board.move", number: 1 }] })
    );
    const result = await runProcess(["--scenario", bad]);
    fs.rmSync(dir, { recursive: true, force: true });
    expect(result.code).toBe(1);
    expect(result.stdout).toBe("");
    expect(result.stderr).toMatch(/scenario step 0: board.move has unknown status/);
  });
});

describe("ui steps (#2108)", () => {
  const withSteps = (steps: unknown[]) => ({ ...reference(), steps });

  it("emits demo.uiStep with the action and its target", () => {
    const { lines } = play(
      withSteps([
        { at: 0, kind: "ui", action: "dashboard.tab", target: "history" },
        { at: 10, kind: "ui", action: "pipeline.expandActiveIssue" },
      ])
    );
    expect(lines).toEqual([
      '0 {"event":"demo.uiStep","data":{"command":"dashboard.tab","args":{"target":"history"}}}',
      '10 {"event":"demo.uiStep","data":{"command":"pipeline.expandActiveIssue","args":{}}}',
    ]);
  });

  it.each([
    [{ at: 0, kind: "ui", action: "workbench.action.terminal.new" }, /unknown action/],
    [{ at: 0, kind: "ui", action: "dashboard.tab", target: "settings" }, /unknown target/],
    [{ at: 0, kind: "ui", action: "dashboard.open", target: "x" }, /takes no "target"/],
  ])("rejects %j", (step, message) => {
    expect(() => loadScenario(withSteps([step]))).toThrow(message);
  });

  it("the reference switches through four dashboard tabs and expands the active issue", () => {
    const ui = reference().steps.filter((s) => s.kind === "ui");
    const tabs = new Set(ui.filter((s) => s.action === "dashboard.tab").map((s) => s.target));
    expect(tabs.size).toBeGreaterThanOrEqual(4);
    expect(ui.some((s) => s.action === "pipeline.expandActiveIssue")).toBe(true);
  });
});
