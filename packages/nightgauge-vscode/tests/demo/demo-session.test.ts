/**
 * The one-command demo session (#2110): what it writes, where, and with what
 * it launches VS Code. The launch itself is exercised by hand (see the
 * script's header); these tests pin the isolation the security AC needs.
 */

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { DEMO_WORKSPACE_NOW, HISTORY_DIR } from "../../demo/workspace-dates";
import {
  DEMO_DAEMON,
  DEMO_WORKSPACE,
  REFERENCE_SCENARIO,
  parseArgs,
  planDemoSession,
  prepareDemoSession,
  sessionEnv,
} from "../../scripts/demo-session";
import { getDefaultAuditFilters } from "../../src/services/AuditLogService";
import { LocalAuditFallbackService } from "../../src/services/LocalAuditFallbackService";
import { pipelineStateDir } from "../../src/utils/cloneLayout";

/** The source stages the demo's per-clone pipeline data in the tree (#2037). */
const STAGED_PIPELINE_DIR = path.join(".nightgauge", "pipeline");

let home: string;
beforeEach(() => {
  home = fs.mkdtempSync(path.join(os.tmpdir(), "demo-session-test-"));
});
afterEach(() => {
  fs.rmSync(home, { recursive: true, force: true });
});

function plan(extra: string[] = []) {
  return planDemoSession(parseArgs(["--home", home, ...extra]));
}

/** Every file in `dir`, relative, with its content. */
function snapshot(
  dir: string,
  skip: (rel: string) => boolean = () => false
): Record<string, string> {
  const out: Record<string, string> = {};
  const walk = (at: string) => {
    for (const entry of fs.readdirSync(at, { withFileTypes: true })) {
      const full = path.join(at, entry.name);
      if (skip(path.relative(dir, full))) continue;
      if (entry.isDirectory()) walk(full);
      else out[path.relative(dir, full)] = fs.readFileSync(full, "utf8");
    }
  };
  walk(dir);
  return out;
}

describe("demo session options", () => {
  it("defaults to the reference scenario at speed 1, playing at once", () => {
    const options = parseArgs([]);
    expect(options.scenario).toBe(REFERENCE_SCENARIO);
    expect(options.speed).toBe(1);
    expect(options.waitForStart).toBe(false);
    expect(options.home).toBe(path.join(os.tmpdir(), "nightgauge-demo"));
  });

  it("rejects a bad speed and an unknown flag", () => {
    expect(() => parseArgs(["--speed", "0"])).toThrow(/--speed must be a positive number/);
    expect(() => parseArgs(["--speed"])).toThrow(/--speed needs a value/);
    expect(() => parseArgs(["--record"])).toThrow(/unknown option --record/);
  });
});

describe("demo session plan", () => {
  it("keeps the profile, workspace and every log under the session home", () => {
    const p = plan(["--wait-for-start"]);
    for (const file of [
      p.userDataDir,
      p.extensionsDir,
      p.workspace,
      p.settingsFile,
      p.eventLog,
      p.startFile!,
      p.env.NIGHTGAUGE_DEMO_IPC_LOG,
      p.env.NIGHTGAUGE_CONFIG_HOME,
      p.env.NIGHTGAUGE_STATE_HOME,
    ]) {
      expect(file.startsWith(home + path.sep), file).toBe(true);
    }
  });

  it("sets the demo daemon as the backend in the demo profile only", () => {
    const p = plan();
    expect(p.settingsFile).toBe(path.join(home, "profile", "User", "settings.json"));
    expect(p.settings["nightgauge.backend.binaryPath"]).toBe(DEMO_DAEMON);
    const at = p.args.indexOf("--user-data-dir");
    expect(p.args[at + 1]).toBe(p.userDataDir);
    expect(p.args[p.args.indexOf("--extensions-dir") + 1]).toBe(p.extensionsDir);
    expect(p.args[0]).toBe(p.workspace);
    expect(p.args).toContain("--new-window");
  });

  it("hands the daemon its scenario through the environment", () => {
    const p = plan(["--speed", "2", "--event-log", path.join(home, "run1.jsonl")]);
    expect(p.env.NIGHTGAUGE_DEMO_SCENARIO).toBe(REFERENCE_SCENARIO);
    expect(p.env.NIGHTGAUGE_DEMO_SPEED).toBe("2");
    expect(p.env.NIGHTGAUGE_DEMO_EVENT_LOG).toBe(path.join(home, "run1.jsonl"));
    expect(p.env.NIGHTGAUGE_DEMO_START_FILE).toBeUndefined();
  });

  it("declares a start file only with --wait-for-start", () => {
    const p = plan(["--wait-for-start"]);
    expect(p.startFile).toBe(path.join(home, "start"));
    expect(p.env.NIGHTGAUGE_DEMO_START_FILE).toBe(p.startFile);
  });

  it("drops variables that would hand the launch to another VS Code", () => {
    const env = sessionEnv(
      { PATH: "/bin", VSCODE_IPC_HOOK_CLI: "/tmp/sock", ELECTRON_RUN_AS_NODE: "1" },
      plan()
    );
    expect(env.PATH).toBe("/bin");
    expect(env.VSCODE_IPC_HOOK_CLI).toBeUndefined();
    expect(env.ELECTRON_RUN_AS_NODE).toBeUndefined();
    expect(env.NIGHTGAUGE_DEMO_SCENARIO).toBe(REFERENCE_SCENARIO);
  });
});

describe("preparing a demo session", () => {
  it("writes only under the session home", () => {
    const outside = fs.mkdtempSync(path.join(os.tmpdir(), "demo-session-outside-"));
    try {
      const before = snapshot(outside);
      prepareDemoSession(plan(), DEMO_WORKSPACE);
      expect(snapshot(outside)).toEqual(before);
      expect(fs.existsSync(path.join(home, "profile", "User", "settings.json"))).toBe(true);
    } finally {
      fs.rmSync(outside, { recursive: true, force: true });
    }
  });

  it("starts every run from the same workspace and an empty event log", () => {
    const p = plan(["--wait-for-start"]);
    const epoch = Date.parse("2026-09-29T12:00:00.000Z");
    prepareDemoSession(p, DEMO_WORKSPACE, epoch);
    const first = snapshot(p.workspace);
    // A run changes the workspace, logs events and signals the start.
    fs.writeFileSync(path.join(p.workspace, "scratch.txt"), "left over");
    fs.writeFileSync(p.eventLog, '{"at":0}\n');
    fs.writeFileSync(p.startFile!, "");
    prepareDemoSession(p, DEMO_WORKSPACE, epoch);
    expect(snapshot(p.workspace)).toEqual(first);
    // The working tree holds the source's files less the staged per-clone
    // data, and nothing under .git but the clone's data came from the source.
    const isGit = (rel: string) => rel === ".git";
    const isStaged = (rel: string) => rel === STAGED_PIPELINE_DIR;
    expect(Object.keys(snapshot(p.workspace, isGit))).toEqual(
      Object.keys(snapshot(DEMO_WORKSPACE, isStaged))
    );
    // The per-clone data moved into the clone's pipeline directory: the same
    // files, less the history ones, which are named by their day.
    const notHistory = (files: Record<string, string>) =>
      Object.keys(files).filter((f) => !f.startsWith("history"));
    expect(notHistory(snapshot(pipelineStateDir(p.workspace)))).toEqual(
      notHistory(snapshot(path.join(DEMO_WORKSPACE, STAGED_PIPELINE_DIR)))
    );
    expect(fs.existsSync(path.join(p.workspace, STAGED_PIPELINE_DIR))).toBe(false);
    expect(fs.readFileSync(p.eventLog, "utf8")).toBe("");
    expect(fs.existsSync(p.startFile!)).toBe(false);
  });

  it("keeps the demo profile's other settings when it rewrites the backend", () => {
    const p = plan();
    fs.mkdirSync(path.dirname(p.settingsFile), { recursive: true });
    fs.writeFileSync(p.settingsFile, JSON.stringify({ "editor.fontSize": 18 }));
    prepareDemoSession(p, DEMO_WORKSPACE);
    const settings = JSON.parse(fs.readFileSync(p.settingsFile, "utf8"));
    expect(settings["editor.fontSize"]).toBe(18);
    expect(settings["nightgauge.backend.binaryPath"]).toBe(DEMO_DAEMON);
  });
});

/**
 * Every run record in a workspace's history, oldest file first, in file order:
 * the source's staged history for {@link DEMO_WORKSPACE}, the clone's history
 * (where the extension reads it) for a prepared session workspace.
 */
function historyRecords(root: string): Array<{ file: string; recorded_at: string }> {
  const dir =
    root === DEMO_WORKSPACE
      ? path.join(root, HISTORY_DIR)
      : path.join(pipelineStateDir(root), "history");
  return fs
    .readdirSync(dir)
    .filter((f) => f.endsWith(".jsonl"))
    .sort()
    .flatMap((file) =>
      fs
        .readFileSync(path.join(dir, file), "utf8")
        .split("\n")
        .filter(Boolean)
        .map((line) => ({ file, ...(JSON.parse(line) as { recorded_at: string }) }))
    );
}

/** Every ISO-8601 UTC timestamp string anywhere in the workspace's JSON files. */
function timestamps(root: string): string[] {
  const out: string[] = [];
  const walk = (value: unknown): void => {
    if (Array.isArray(value)) value.forEach(walk);
    else if (value && typeof value === "object") Object.values(value).forEach(walk);
    else if (typeof value === "string" && /^\d{4}-\d{2}-\d{2}T[\d:.]+Z$/.test(value)) {
      out.push(value);
    }
  };
  for (const [file, text] of Object.entries(snapshot(root))) {
    if (file.endsWith(".jsonl"))
      text
        .split("\n")
        .filter(Boolean)
        .forEach((l) => walk(JSON.parse(l)));
    else if (file.endsWith(".json")) walk(JSON.parse(text));
  }
  return out;
}

describe("demo workspace dates (#2285)", () => {
  it("puts the newest history event inside the Audit Trail's default range", async () => {
    const p = plan();
    prepareDemoSession(p, DEMO_WORKSPACE);
    const filters = getDefaultAuditFilters();
    const newest = historyRecords(p.workspace)
      .map((r) => Date.parse(r.recorded_at))
      .reduce((a, b) => Math.max(a, b));
    expect(newest).toBeGreaterThanOrEqual(Date.parse(filters.dateFrom));
    expect(newest).toBeLessThanOrEqual(Date.parse(filters.dateTo));

    // What the tab reads with the platform off: every seeded run is in range.
    const audit = await new LocalAuditFallbackService(p.workspace).buildLocalAuditData(filters);
    expect(audit.entries.length).toBe(historyRecords(DEMO_WORKSPACE).length);
    expect(audit.entries.length).toBeGreaterThan(0);
  });

  it("keeps every event's order and spacing, moved by one offset", () => {
    const p = plan();
    const epoch = Date.parse("2026-09-29T12:00:00.000Z");
    prepareDemoSession(p, DEMO_WORKSPACE, epoch);
    const offset = epoch - Date.parse(DEMO_WORKSPACE_NOW);
    const source = historyRecords(DEMO_WORKSPACE).map((r) => Date.parse(r.recorded_at));
    const redated = historyRecords(p.workspace).map((r) => Date.parse(r.recorded_at));
    expect(redated).toEqual(source.map((t) => t + offset));
    expect(timestamps(p.workspace)).toEqual(
      expect.arrayContaining(
        timestamps(DEMO_WORKSPACE).map((t) => new Date(Date.parse(t) + offset).toISOString())
      )
    );
  });

  it("files each run under its re-dated day, as the history writer names files", () => {
    const p = plan();
    prepareDemoSession(p, DEMO_WORKSPACE, Date.parse("2026-09-29T02:00:00.000Z"));
    const records = historyRecords(p.workspace);
    expect(records.length).toBe(historyRecords(DEMO_WORKSPACE).length);
    for (const record of records) {
      expect(record.file).toBe(`${record.recorded_at.slice(0, 10)}.jsonl`);
    }
  });

  it("dates nothing after the session start", () => {
    const scenario = JSON.parse(fs.readFileSync(REFERENCE_SCENARIO, "utf8")) as {
      state: { now: string };
    };
    expect(DEMO_WORKSPACE_NOW).toBe(scenario.state.now);
    const p = plan();
    const epoch = Date.parse("2026-09-29T12:00:00.000Z");
    prepareDemoSession(p, DEMO_WORKSPACE, epoch);
    const stamps = timestamps(p.workspace).map((t) => Date.parse(t));
    expect(stamps.length).toBeGreaterThan(0);
    expect(Math.max(...stamps)).toBeLessThanOrEqual(epoch);
  });
});
