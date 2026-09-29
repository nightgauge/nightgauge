/**
 * One command starts a demo session (#2110, ADR-026).
 *
 *   npm run -w nightgauge-vscode demo -- [--scenario <file>] [--speed <n>]
 *     [--wait-for-start] [--event-log <file>] [--home <dir>] [--code <path>]
 *
 * It opens a new VS Code window, with a profile of its own, on a fresh copy of
 * the demo workspace (`demo/workspace/`), with the demo daemon as the backend,
 * and plays the scenario (default: `demo/scenarios/reference.json`).
 *
 * Isolation (the security AC). Every file the session writes lives under
 * `--home` (default `<os temp dir>/nightgauge-demo`): the VS Code user-data
 * directory (so `nightgauge.backend.binaryPath` is set in that profile's
 * `settings.json` and nowhere else), an empty extensions directory (the only
 * extension is this package, loaded as the development extension), the
 * workspace copy, and the machine-tier config and state roots the extension
 * would otherwise read from the operator's home. The operator's own VS Code
 * profile and settings are never opened. The profile is reused between runs;
 * the workspace is copied fresh each time, so every run starts identically.
 *
 * VS Code is the build `@vscode/test-electron` downloads and caches (as the
 * `vscode-host` tier uses), or the executable named by `--code`. It is started
 * directly, not through a running instance, so the demo environment reaches
 * the extension host and from it the daemon.
 *
 * Start signal: with `--wait-for-start` the window opens populated with the
 * scenario's initial state and holds playback until the file
 * `<home>/start` exists, so a screen recorder can begin capture first:
 *
 *   touch "$TMPDIR/nightgauge-demo/start"
 *
 * Replays: the daemon writes every event it emits to the event log (default
 * `<home>/events.jsonl`, emptied at launch) with timestamps relative to the
 * scenario start. Two runs of one scenario write byte-identical logs:
 *
 *   npm run -w nightgauge-vscode demo -- --event-log /tmp/run1.jsonl   # close the window
 *   npm run -w nightgauge-vscode demo -- --event-log /tmp/run2.jsonl
 *   cmp /tmp/run1.jsonl /tmp/run2.jsonl
 *
 * Close the previous demo window before starting another: VS Code hands a
 * second launch on the same profile to the running window.
 */

import { spawn } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
export const PACKAGE_ROOT = path.resolve(here, "..");

export interface DemoSessionOptions {
  /** Where the session's profile, workspace, logs and start file live. */
  home: string;
  /** Scenario file the daemon plays. */
  scenario: string;
  /** Playback speed factor. */
  speed: number;
  /** Hold playback until `<home>/start` exists. */
  waitForStart: boolean;
  /** Event log path; `<home>/events.jsonl` when unset. */
  eventLog?: string;
  /** VS Code executable; the cached test-electron build when unset. */
  code?: string;
}

export interface DemoSessionPlan {
  userDataDir: string;
  extensionsDir: string;
  workspace: string;
  settingsFile: string;
  /** Keys written into the demo profile's settings.json. */
  settings: Record<string, unknown>;
  /** Added to the environment VS Code starts with. */
  env: Record<string, string>;
  /** VS Code arguments. */
  args: string[];
  startFile?: string;
  eventLog: string;
}

export const DEMO_DAEMON = path.join(PACKAGE_ROOT, "demo", "ipc-stub.cjs");
export const DEMO_WORKSPACE = path.join(PACKAGE_ROOT, "demo", "workspace");
export const REFERENCE_SCENARIO = path.join(PACKAGE_ROOT, "demo", "scenarios", "reference.json");

export function defaultOptions(): DemoSessionOptions {
  return {
    home: path.join(os.tmpdir(), "nightgauge-demo"),
    scenario: REFERENCE_SCENARIO,
    speed: 1,
    waitForStart: false,
  };
}

/** Parse the command line; throws naming the bad flag. */
export function parseArgs(argv: readonly string[]): DemoSessionOptions {
  const options = defaultOptions();
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    const value = (): string => {
      const next = argv[++i];
      if (next === undefined || next.startsWith("--")) throw new Error(`${flag} needs a value`);
      return next;
    };
    switch (flag) {
      case "--scenario":
        options.scenario = path.resolve(value());
        break;
      case "--speed":
        options.speed = Number(value());
        if (!(options.speed > 0 && Number.isFinite(options.speed))) {
          throw new Error("--speed must be a positive number");
        }
        break;
      case "--wait-for-start":
        options.waitForStart = true;
        break;
      case "--event-log":
        options.eventLog = path.resolve(value());
        break;
      case "--home":
        options.home = path.resolve(value());
        break;
      case "--code":
        options.code = path.resolve(value());
        break;
      default:
        throw new Error(`unknown option ${flag}`);
    }
  }
  return options;
}

/** Everything the session will write and run, derived from the options alone. */
export function planDemoSession(options: DemoSessionOptions): DemoSessionPlan {
  const { home } = options;
  const userDataDir = path.join(home, "profile");
  const eventLog = options.eventLog ?? path.join(home, "events.jsonl");
  const startFile = options.waitForStart ? path.join(home, "start") : undefined;
  const workspace = path.join(home, "workspace");
  return {
    userDataDir,
    extensionsDir: path.join(home, "extensions"),
    workspace,
    settingsFile: path.join(userDataDir, "User", "settings.json"),
    settings: {
      // The demo daemon, for this profile only (ADR-026 decision 1).
      "nightgauge.backend.binaryPath": DEMO_DAEMON,
      // A recording shows no first-run notice, and the demo sends nothing.
      "telemetry.telemetryLevel": "off",
      "nightgauge.telemetry.enabled": false,
      "workbench.startupEditor": "none",
      "security.workspace.trust.enabled": false,
    },
    env: {
      NIGHTGAUGE_DEMO_SCENARIO: options.scenario,
      NIGHTGAUGE_DEMO_SPEED: String(options.speed),
      NIGHTGAUGE_DEMO_EVENT_LOG: eventLog,
      NIGHTGAUGE_DEMO_IPC_LOG: path.join(home, "ipc.jsonl"),
      ...(startFile ? { NIGHTGAUGE_DEMO_START_FILE: startFile } : {}),
      // Keep the extension off the operator's machine-tier config and state.
      NIGHTGAUGE_CONFIG_HOME: path.join(home, "config"),
      NIGHTGAUGE_STATE_HOME: path.join(home, "state"),
      NIGHTGAUGE_SKIP_AUTH_PREFLIGHT: "1",
    },
    args: [
      workspace,
      "--new-window",
      "--user-data-dir",
      userDataDir,
      "--extensions-dir",
      path.join(home, "extensions"),
      `--extensionDevelopmentPath=${PACKAGE_ROOT}`,
      "--disable-extensions",
      "--disable-workspace-trust",
      "--skip-welcome",
      "--skip-release-notes",
    ],
    startFile,
    eventLog,
  };
}

/**
 * Lay the session out on disk: a fresh workspace copy, the demo profile's
 * settings merged over whatever that profile already holds, no stale start
 * signal, and an empty event log.
 */
export function prepareDemoSession(plan: DemoSessionPlan, workspaceSource: string): void {
  fs.rmSync(plan.workspace, { recursive: true, force: true });
  fs.cpSync(workspaceSource, plan.workspace, { recursive: true });
  fs.mkdirSync(plan.extensionsDir, { recursive: true });
  fs.mkdirSync(path.dirname(plan.settingsFile), { recursive: true });
  let existing: Record<string, unknown> = {};
  if (fs.existsSync(plan.settingsFile)) {
    existing = JSON.parse(fs.readFileSync(plan.settingsFile, "utf8")) as Record<string, unknown>;
  }
  fs.writeFileSync(
    plan.settingsFile,
    `${JSON.stringify({ ...existing, ...plan.settings }, null, 2)}\n`
  );
  if (plan.startFile) fs.rmSync(plan.startFile, { force: true });
  fs.mkdirSync(path.dirname(plan.eventLog), { recursive: true });
  fs.writeFileSync(plan.eventLog, "");
}

/**
 * The environment VS Code starts with: the operator's, minus anything that
 * would hand the launch to another VS Code instance or run Electron as
 * plain Node, plus the demo's.
 */
export function sessionEnv(base: NodeJS.ProcessEnv, plan: DemoSessionPlan): Record<string, string> {
  const env: Record<string, string> = {};
  for (const [key, value] of Object.entries(base)) {
    if (value === undefined || key.startsWith("VSCODE_") || key === "ELECTRON_RUN_AS_NODE") {
      continue;
    }
    env[key] = value;
  }
  return { ...env, ...plan.env };
}

async function resolveCode(options: DemoSessionOptions): Promise<string> {
  if (options.code) return options.code;
  const { downloadAndUnzipVSCode } = await import("@vscode/test-electron");
  const { acquireVSCode } = await import("../tests/launcher/acquireVSCode");
  return acquireVSCode({
    download: () => downloadAndUnzipVSCode(),
    sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
    log: (message) => console.log(message),
    warn: (message) => console.error(`WARN: ${message}`),
  });
}

async function main(): Promise<void> {
  const options = parseArgs(process.argv.slice(2));
  const bundle = path.join(PACKAGE_ROOT, "dist", "extension.cjs");
  if (!fs.existsSync(bundle)) {
    throw new Error(
      "dist/extension.cjs is missing: run `npm run -w nightgauge-vscode build` first."
    );
  }
  const plan = planDemoSession(options);
  prepareDemoSession(plan, DEMO_WORKSPACE);
  const code = await resolveCode(options);

  console.log(`Demo session: ${path.relative(process.cwd(), options.scenario)}`);
  console.log(`  profile     ${plan.userDataDir}`);
  console.log(`  workspace   ${plan.workspace}`);
  console.log(`  event log   ${plan.eventLog}`);
  if (plan.startFile) {
    console.log(`  waiting for the start signal: touch "${plan.startFile}"`);
  }

  const child = spawn(code, plan.args, { env: sessionEnv(process.env, plan), stdio: "ignore" });
  console.log(`  VS Code pid ${child.pid}; close the window to end the session.`);
  const exitCode = await new Promise<number>((resolve) => {
    child.on("error", (err) => {
      console.error(`ERROR: could not start VS Code at ${code}: ${err.message}`);
      resolve(1);
    });
    child.on("exit", (status) => resolve(status ?? 0));
  });
  process.exit(exitCode);
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  main().catch((err: unknown) => {
    console.error(`ERROR: ${err instanceof Error ? err.message : String(err)}`);
    process.exit(1);
  });
}
