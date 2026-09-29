#!/usr/bin/env node
/**
 * Demo daemon entry point (#2105, ADR-026), grown from the #2103 logging stub.
 *
 * A stand-in for `nightgauge serve` that speaks the extension's IPC protocol
 * (line-delimited JSON on stdin/stdout, `src/services/IpcClientBase.ts`):
 *
 *   - on start it emits `ipc.ready` with the protocol version the extension
 *     expects (`IPC_PROTOCOL_VERSION`) and `demo: true`;
 *   - it answers requests from in-memory demo state (`daemon/daemon.cjs`),
 *     seeded from `daemon/seed.json` or the `state` of a scenario file,
 *     rebased so the state's `now` is the scenario start; unknown methods get
 *     `null` and a stderr line;
 *   - with a scenario, it plays the `steps` on a scenario clock
 *     (`daemon/scenario.cjs`): the speed factor scales delays only, and
 *     `--clock <iso>` pins the start for a byte-identical replay. An invalid
 *     scenario exits 1 naming the step before `ipc.ready` is sent;
 *   - it appends one JSONL record per request — timestamp, method, params, a
 *     type-only shape of the params and whether the daemon answers the
 *     method — to the IPC log (#2103, #2109).
 *
 * Every option has a flag and an environment variable. The flag wins; the
 * variable exists because the extension spawns `<binaryPath> serve
 * --workspace <root>` and a setting cannot carry arguments, so a demo window
 * or the `vscode-host` tier configures the daemon through the environment it
 * inherits from the extension host:
 *
 *   --scenario <path>    NIGHTGAUGE_DEMO_SCENARIO    scenario file (default: seed, no steps)
 *   --speed <n>          NIGHTGAUGE_DEMO_SPEED       playback speed factor (default 1)
 *   --start-file <path>  NIGHTGAUGE_DEMO_START_FILE  hold playback until this file exists
 *   --event-log <path>   NIGHTGAUGE_DEMO_EVENT_LOG   append every emitted event, with
 *                                                    timestamps relative to scenario start
 *   (none)               NIGHTGAUGE_DEMO_IPC_LOG     request log (default: OS temp dir)
 *
 * The start file is the start signal (#2110): the window comes up populated
 * with the scenario's initial state, and the first step runs only once the
 * file appears, so a screen recorder can begin capture first. The event log
 * is what two runs of one scenario compare byte-for-byte.
 *
 * Point the extension at it with `nightgauge.backend.binaryPath`. The
 * extension spawns `<binary> serve --workspace <root>`; those arguments are
 * accepted and ignored.
 *
 * Deliberately inert: it makes no network calls, spawns no processes and
 * reads no credentials. The only environment variables it reads are the five
 * above. `tests/demo/ipc-stub.test.ts` asserts the module graph stays that way.
 */
/* global process, __dirname, setTimeout, setInterval, clearInterval */
"use strict";

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const readline = require("node:readline");
const { createDaemon } = require("./daemon/daemon.cjs");
const {
  createPlayer,
  loadScenario,
  rebaseState,
  relativeToStart,
} = require("./daemon/scenario.cjs");
const { createState } = require("./daemon/state.cjs");

const logPath = process.env.NIGHTGAUGE_DEMO_IPC_LOG || path.join(os.tmpdir(), "ipc-stub.jsonl");

/** Params that carry a credential are logged as a marker, never as a value. */
const SECRET_KEY = /token|secret|password|key$/i;

function scrub(value) {
  if (Array.isArray(value)) return value.map(scrub);
  if (value && typeof value === "object") {
    const out = {};
    for (const [key, item] of Object.entries(value)) {
      out[key] = SECRET_KEY.test(key) && item ? "<redacted>" : scrub(item);
    }
    return out;
  }
  return value;
}

/** Replace every leaf with its JSON type so the log records a shape, not data. */
function shapeOf(value) {
  if (value === null) return "null";
  if (Array.isArray(value)) return value.length > 0 ? [shapeOf(value[0])] : [];
  if (typeof value === "object") {
    const out = {};
    for (const key of Object.keys(value).sort()) out[key] = shapeOf(value[key]);
    return out;
  }
  return typeof value;
}

/** Where emitted events are recorded (set once options load), and the scenario start. */
let eventLog;
let scenarioEpochMs = 0;

function send(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
  if (eventLog && message.event) recordEvent(message);
}

/**
 * One line per emitted event: its offset on the scenario clock and its
 * payload with every timestamp made relative to the scenario start, so two
 * runs of one scenario write byte-identical logs whatever the wall clock.
 */
function recordEvent(message) {
  const at = daemon ? Date.parse(daemon.state.now) - scenarioEpochMs : 0;
  const line = { at, event: message.event, data: relativeToStart(message.data, scenarioEpochMs) };
  try {
    fs.appendFileSync(eventLog, JSON.stringify(line) + "\n");
  } catch (err) {
    process.stderr.write(`demo-daemon: cannot write ${eventLog}: ${err.message}\n`);
  }
}

function record(request) {
  const entry = {
    ts: new Date().toISOString(),
    method: request.method,
    params: request.params === undefined ? null : scrub(request.params),
    paramsShape: request.params === undefined ? null : shapeOf(request.params),
    // The drift guard (#2109): false means the extension called a method the
    // daemon has no answer for, and the host tier fails naming it.
    answered: daemon.answers(request.method),
  };
  try {
    fs.appendFileSync(logPath, JSON.stringify(entry) + "\n");
  } catch (err) {
    process.stderr.write(`ipc-stub: cannot write ${logPath}: ${err.message}\n`);
  }
}

function handleLine(line) {
  if (!line.trim()) return;
  let request;
  try {
    request = JSON.parse(line);
  } catch {
    process.stderr.write(`ipc-stub: ignoring non-JSON line\n`);
    return;
  }
  if (!request || typeof request.method !== "string" || request.id === undefined) return;
  record(request);
  daemon.handle(request);
}

/** The value after `flag` in argv, or undefined. */
function flagValue(argv, flag) {
  const at = argv.indexOf(flag);
  if (at < 0) return undefined;
  const value = argv[at + 1];
  if (value === undefined || value.startsWith("--")) throw new Error(`${flag} needs a value`);
  return value;
}

/** A flag's value, else the environment variable's, else undefined. */
function option(argv, flag, envValue) {
  const value = flagValue(argv, flag);
  if (value !== undefined) return value;
  return envValue === undefined || envValue === "" ? undefined : envValue;
}

/**
 * The scenario (else the built-in seed with no steps), the playback speed
 * (default 1), the scenario start (`--clock <iso>`, default now; pin it to
 * replay byte-identically), the start file and the event log. See the file
 * header for each option's flag and environment variable.
 */
function loadOptions(argv, env) {
  const file =
    option(argv, "--scenario", env.NIGHTGAUGE_DEMO_SCENARIO) ||
    path.join(__dirname, "daemon", "seed.json");
  const scenario = loadScenario(JSON.parse(fs.readFileSync(file, "utf8")));
  const speedArg = option(argv, "--speed", env.NIGHTGAUGE_DEMO_SPEED);
  const speed = speedArg === undefined ? 1 : Number(speedArg);
  if (!(speed > 0 && Number.isFinite(speed))) throw new Error("--speed must be a positive number");
  const clockArg = flagValue(argv, "--clock");
  const epochMs = clockArg === undefined ? Date.now() : Date.parse(clockArg);
  if (Number.isNaN(epochMs)) throw new Error("--clock must be an ISO-8601 timestamp");
  return {
    scenario,
    speed,
    epochMs,
    startFile: option(argv, "--start-file", env.NIGHTGAUGE_DEMO_START_FILE),
    eventLog: option(argv, "--event-log", env.NIGHTGAUGE_DEMO_EVENT_LOG),
  };
}

/** Run `fn` now, or once `file` exists when a start file was given. */
function whenStarted(file, fn) {
  if (!file || fs.existsSync(file)) {
    fn();
    return;
  }
  log(`waiting for the start signal: create ${file} to play the scenario`);
  const poll = setInterval(() => {
    if (!fs.existsSync(file)) return;
    clearInterval(poll);
    log("start signal received");
    fn();
  }, 100);
}

const log = (message) => process.stderr.write(`demo-daemon: ${message}\n`);

// The extension also runs one-shot CLI subcommands through the binary path
// (`worktree sweep --json`, for one). The demo has none: fail them at once,
// as an unavailable command, rather than start a second daemon that waits on
// stdin and announces itself.
const subcommand = process.argv[2];
if (subcommand !== "serve") {
  log(`"${subcommand || ""}" is not available in demo mode; only serve runs`);
  process.exit(1);
}

let daemon;
let player;
let startFile;
try {
  const options = loadOptions(process.argv.slice(2), {
    NIGHTGAUGE_DEMO_SCENARIO: process.env.NIGHTGAUGE_DEMO_SCENARIO,
    NIGHTGAUGE_DEMO_SPEED: process.env.NIGHTGAUGE_DEMO_SPEED,
    NIGHTGAUGE_DEMO_START_FILE: process.env.NIGHTGAUGE_DEMO_START_FILE,
    NIGHTGAUGE_DEMO_EVENT_LOG: process.env.NIGHTGAUGE_DEMO_EVENT_LOG,
  });
  const { scenario, speed, epochMs } = options;
  startFile = options.startFile;
  eventLog = options.eventLog;
  scenarioEpochMs = epochMs;
  daemon = createDaemon({ state: createState(rebaseState(scenario.state, epochMs)), send, log });
  player = createPlayer({
    daemon,
    steps: scenario.steps,
    epochMs,
    speed,
    schedule: (fn, delayMs) => setTimeout(fn, delayMs),
    log,
    onDone: () => log("scenario finished"),
  });
} catch (err) {
  process.stderr.write(`demo-daemon: cannot load scenario: ${err.message}\n`);
  process.exit(1);
}

fs.mkdirSync(path.dirname(logPath), { recursive: true });
if (eventLog) fs.mkdirSync(path.dirname(eventLog), { recursive: true });
readline.createInterface({ input: process.stdin }).on("line", handleLine);
process.stdin.on("end", () => process.exit(0));
daemon.ready();
whenStarted(startFile, () => player.start());
