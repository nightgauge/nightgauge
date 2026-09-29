/**
 * The one-command demo session (#2110): what it writes, where, and with what
 * it launches VS Code. The launch itself is exercised by hand (see the
 * script's header); these tests pin the isolation the security AC needs.
 */

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  DEMO_DAEMON,
  DEMO_WORKSPACE,
  REFERENCE_SCENARIO,
  parseArgs,
  planDemoSession,
  prepareDemoSession,
  sessionEnv,
} from "../../scripts/demo-session";

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
function snapshot(dir: string): Record<string, string> {
  const out: Record<string, string> = {};
  const walk = (at: string) => {
    for (const entry of fs.readdirSync(at, { withFileTypes: true })) {
      const full = path.join(at, entry.name);
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
    prepareDemoSession(p, DEMO_WORKSPACE);
    const first = snapshot(p.workspace);
    // A run changes the workspace, logs events and signals the start.
    fs.writeFileSync(path.join(p.workspace, "scratch.txt"), "left over");
    fs.writeFileSync(p.eventLog, '{"at":0}\n');
    fs.writeFileSync(p.startFile!, "");
    prepareDemoSession(p, DEMO_WORKSPACE);
    expect(snapshot(p.workspace)).toEqual(first);
    expect(first).toEqual(snapshot(DEMO_WORKSPACE));
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
