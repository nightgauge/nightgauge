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
 *     seeded from `daemon/seed.json` or the `state` of the JSON file named by
 *     `--scenario <path>`, rebased so the state's `now` is the scenario start;
 *     unknown methods get `null` and a stderr line;
 *   - with a scenario, it plays the `steps` on a scenario clock
 *     (`daemon/scenario.cjs`): `--speed <n>` scales delays only, and
 *     `--clock <iso>` pins the start for a byte-identical replay. An invalid
 *     scenario exits 1 naming the step before `ipc.ready` is sent;
 *   - it appends one JSONL record per request — timestamp, method, params and
 *     a type-only shape of the params — to the file named by
 *     `NIGHTGAUGE_DEMO_IPC_LOG`, or to `ipc-stub.jsonl` in the OS temp dir.
 *
 * Point the extension at it with `nightgauge.backend.binaryPath`. The
 * extension spawns `<binary> serve --workspace <root>`; those arguments are
 * accepted and ignored.
 *
 * Deliberately inert: it makes no network calls, spawns no processes and
 * reads no credentials. The only environment variable it reads is the log
 * path. `tests/demo/ipc-stub.test.ts` asserts the module graph stays that way.
 */
/* global process, __dirname, setTimeout */
"use strict";

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const readline = require("node:readline");
const { createDaemon } = require("./daemon/daemon.cjs");
const { createPlayer, loadScenario, rebaseState } = require("./daemon/scenario.cjs");
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

function send(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
}

function record(request) {
  const entry = {
    ts: new Date().toISOString(),
    method: request.method,
    params: request.params === undefined ? null : scrub(request.params),
    paramsShape: request.params === undefined ? null : shapeOf(request.params),
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

/**
 * The scenario (`--scenario <path>`, else the built-in seed with no steps),
 * the playback speed (`--speed <n>`, default 1) and the scenario start
 * (`--clock <iso>`, default now; pin it to replay byte-identically).
 */
function loadOptions(argv) {
  const file = flagValue(argv, "--scenario") || path.join(__dirname, "daemon", "seed.json");
  const scenario = loadScenario(JSON.parse(fs.readFileSync(file, "utf8")));
  const speedArg = flagValue(argv, "--speed");
  const speed = speedArg === undefined ? 1 : Number(speedArg);
  if (!(speed > 0 && Number.isFinite(speed))) throw new Error("--speed must be a positive number");
  const clockArg = flagValue(argv, "--clock");
  const epochMs = clockArg === undefined ? Date.now() : Date.parse(clockArg);
  if (Number.isNaN(epochMs)) throw new Error("--clock must be an ISO-8601 timestamp");
  return { scenario, speed, epochMs };
}

const log = (message) => process.stderr.write(`demo-daemon: ${message}\n`);
let daemon;
let player;
try {
  const { scenario, speed, epochMs } = loadOptions(process.argv.slice(2));
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
readline.createInterface({ input: process.stdin }).on("line", handleLine);
process.stdin.on("end", () => process.exit(0));
daemon.ready();
player.start();
