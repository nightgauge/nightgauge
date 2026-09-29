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
 *     `--scenario <path>`; unknown methods get `null` and a stderr line;
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
/* global process, __dirname */
"use strict";

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const readline = require("node:readline");
const { createDaemon } = require("./daemon/daemon.cjs");
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

/** The seed state: `--scenario <path>` if given, else the built-in seed. */
function loadState(argv) {
  const at = argv.indexOf("--scenario");
  const file = at >= 0 ? argv[at + 1] : path.join(__dirname, "daemon", "seed.json");
  if (!file) throw new Error("--scenario needs a path");
  const parsed = JSON.parse(fs.readFileSync(file, "utf8"));
  return createState(parsed.state);
}

let daemon;
try {
  daemon = createDaemon({
    state: loadState(process.argv.slice(2)),
    send,
    log: (message) => process.stderr.write(`demo-daemon: ${message}\n`),
  });
} catch (err) {
  process.stderr.write(`demo-daemon: cannot load state: ${err.message}\n`);
  process.exit(1);
}

fs.mkdirSync(path.dirname(logPath), { recursive: true });
readline.createInterface({ input: process.stdin }).on("line", handleLine);
process.stdin.on("end", () => process.exit(0));
daemon.ready();
