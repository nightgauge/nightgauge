/**
 * Demo scenarios and their deterministic player (#2106, ADR-026 decision 3).
 *
 * A scenario is `{ seed, state, steps }`. `loadScenario(raw)` validates the
 * whole file up front and throws `scenario step <i>: <reason>` (or
 * `scenario: <reason>`) on the first problem, so a bad file never reaches
 * `ipc.ready`.
 *
 * Time is a scenario clock starting at 0. `rebaseState(raw, epochMs)` shifts
 * every timestamp in the seed state so the state's `now` lands on `epochMs`:
 * a demo always looks current, and the relative spacing never changes.
 * `createPlayer` schedules each step at `at / speed` through an injected
 * `schedule(fn, delayMs)` and stamps the state clock as `epochMs + at`, never
 * the wall clock, so a fixed epoch gives a byte-identical stream.
 *
 * Pure: no I/O and no timers of its own; the entry point owns both.
 */
"use strict";

const { BOARD_STATUSES, moveBoardItem } = require("./state.cjs");

const MUTATIONS = [
  "board.move",
  "queue.set",
  "attention.raise",
  "attention.resolve",
  "history.append",
];
const KINDS = new Set([...MUTATIONS, "event", "ui"]);
/** Events the daemon never emits: they would make the extension start or stop a run. */
const REFUSED_EVENTS = new Set(["pipeline.runStage", "pipeline.abort"]);
/** Placeholder a step payload uses for "the scenario clock, now". */
const NOW_TOKEN = "{{now}}";
/**
 * `ui` actions and the targets each accepts (null: no target). Mirrors the
 * extension's allowlist in src/services/DemoModeController.ts (#2108), which
 * drops anything else; rejecting here keeps a typo from failing silently.
 */
const UI_ACTIONS = {
  "dashboard.open": null,
  "dashboard.tab": [
    "overview",
    "pipeline",
    "analytics",
    "history",
    "epics",
    "audit",
    "discovery",
    "cost",
    "health",
    "runs",
    "trends",
    "compliance",
    "dependencies",
  ],
  "view.focus": ["pipeline-tree", "repositories", "attention", "knowledge", "query-results"],
  "pipeline.expandActiveIssue": null,
};
const ISO = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const DAY_MS = 86400000;

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function checkStep(step, previousAt) {
  if (!isObject(step)) return "must be an object";
  if (!Number.isInteger(step.at) || step.at < 0) return '"at" must be a non-negative integer (ms)';
  if (step.at < previousAt) return `"at" ${step.at} is before the previous step's ${previousAt}`;
  if (!KINDS.has(step.kind)) return `unknown kind ${JSON.stringify(step.kind)}`;
  switch (step.kind) {
    case "board.move":
      if (!Number.isInteger(step.number)) return 'board.move needs an integer "number"';
      if (!BOARD_STATUSES.includes(step.status)) {
        return `board.move has unknown status ${JSON.stringify(step.status)}`;
      }
      return null;
    case "queue.set":
      if (!Array.isArray(step.items)) return 'queue.set needs an "items" array';
      for (const item of step.items) {
        if (
          !isObject(item) ||
          typeof item.repo !== "string" ||
          !Number.isInteger(item.issueNumber)
        ) {
          return 'queue.set items need a "repo" and an integer "issueNumber"';
        }
      }
      return null;
    case "attention.raise":
      if (!isObject(step.item) || typeof step.item.id !== "string") {
        return 'attention.raise needs an "item" with a string "id"';
      }
      for (const key of ["kind", "severity", "title", "repo"]) {
        if (typeof step.item[key] !== "string") return `attention.raise item needs "${key}"`;
      }
      return null;
    case "attention.resolve":
      if (typeof step.id !== "string") return 'attention.resolve needs a string "id"';
      if (typeof step.option !== "string") return 'attention.resolve needs a string "option"';
      return null;
    case "history.append":
      if (!isObject(step.entry) || !Number.isInteger(step.entry.number)) {
        return 'history.append needs an "entry" with an integer "number"';
      }
      if (typeof step.entry.repo !== "string" || typeof step.entry.outcome !== "string") {
        return 'history.append entry needs "repo" and "outcome"';
      }
      return null;
    case "event":
      if (typeof step.method !== "string" || !step.method) return 'event needs a "method"';
      if (REFUSED_EVENTS.has(step.method))
        return `event ${step.method} is never emitted in demo mode`;
      if (step.payload !== undefined && !isObject(step.payload)) {
        return 'event "payload" must be an object';
      }
      return null;
    case "ui":
      if (typeof step.action !== "string" || !step.action) return 'ui needs an "action"';
      if (!Object.prototype.hasOwnProperty.call(UI_ACTIONS, step.action)) {
        return `ui has unknown action "${step.action}"`;
      }
      if (UI_ACTIONS[step.action] === null) {
        if (step.target !== undefined) return `ui ${step.action} takes no "target"`;
      } else if (!UI_ACTIONS[step.action].includes(step.target)) {
        return `ui ${step.action} has unknown target "${step.target}"`;
      }
      return null;
    default:
      return null;
  }
}

/** Validate a parsed scenario in full; returns `{ seed, state, steps }` or throws. */
function loadScenario(raw) {
  if (!isObject(raw)) throw new Error("scenario: must be a JSON object");
  if (!Number.isInteger(raw.seed)) throw new Error('scenario: "seed" must be an integer');
  if (!isObject(raw.state)) throw new Error('scenario: "state" must be an object');
  if (raw.state.now !== undefined && !ISO.test(raw.state.now)) {
    throw new Error('scenario: "state.now" must be an ISO-8601 UTC timestamp');
  }
  const steps = raw.steps === undefined ? [] : raw.steps;
  if (!Array.isArray(steps)) throw new Error('scenario: "steps" must be an array');
  let previousAt = 0;
  steps.forEach((step, index) => {
    const reason = checkStep(step, previousAt);
    if (reason) throw new Error(`scenario step ${index}: ${reason}`);
    previousAt = step.at;
  });
  return { seed: raw.seed, state: raw.state, steps };
}

function shiftIso(value, deltaMs) {
  return new Date(Date.parse(value) + deltaMs).toISOString();
}

/**
 * Copy `value`, shifting ISO timestamps by `deltaMs` and calendar dates by
 * whole days, and replacing `{{now}}` with `nowIso`.
 */
function rebaseValue(value, deltaMs, nowIso) {
  if (Array.isArray(value)) return value.map((v) => rebaseValue(v, deltaMs, nowIso));
  if (isObject(value)) {
    const out = {};
    for (const [key, item] of Object.entries(value)) out[key] = rebaseValue(item, deltaMs, nowIso);
    return out;
  }
  if (typeof value !== "string") return value;
  if (value === NOW_TOKEN) return nowIso;
  if (ISO.test(value)) return shiftIso(value, deltaMs);
  if (DATE.test(value)) {
    const days = Math.round(deltaMs / DAY_MS);
    return new Date(Date.parse(`${value}T00:00:00.000Z`) + days * DAY_MS)
      .toISOString()
      .slice(0, 10);
  }
  return value;
}

/** The raw state with every timestamp moved so `state.now` is `epochMs`. */
function rebaseState(rawState, epochMs) {
  const nowIso = new Date(epochMs).toISOString();
  const reference = rawState.now ? Date.parse(rawState.now) : epochMs;
  const rebased = rebaseValue(rawState, epochMs - reference, nowIso);
  rebased.now = nowIso;
  return rebased;
}

/**
 * Copy `value` with every ISO timestamp replaced by its offset from
 * `epochMs` (`"T+1500ms"`, `"T-3600000ms"`) and every calendar date by its
 * day offset (`"D-2"`). The event log (#2110) records payloads this way, so
 * two runs of one scenario compare byte-for-byte whatever the wall clock.
 */
function relativeToStart(value, epochMs) {
  if (Array.isArray(value)) return value.map((v) => relativeToStart(v, epochMs));
  if (isObject(value)) {
    const out = {};
    for (const [key, item] of Object.entries(value)) out[key] = relativeToStart(item, epochMs);
    return out;
  }
  if (typeof value !== "string") return value;
  const sign = (n) => (n < 0 ? `${n}` : `+${n}`);
  if (ISO.test(value)) return `T${sign(Date.parse(value) - epochMs)}ms`;
  if (DATE.test(value)) {
    const startDay = Date.parse(`${new Date(epochMs).toISOString().slice(0, 10)}T00:00:00.000Z`);
    return `D${sign(Math.round((Date.parse(`${value}T00:00:00.000Z`) - startDay) / DAY_MS))}`;
  }
  return value;
}

/**
 * The player. `daemon` is `createDaemon(...)`; `schedule(fn, delayMs)` is
 * `setTimeout` in production and a recorder in tests. `onDone` runs after
 * the last step.
 */
function createPlayer({ daemon, steps, epochMs, speed = 1, schedule, log, onDone }) {
  if (!(typeof speed === "number" && speed > 0 && Number.isFinite(speed))) {
    throw new Error("scenario: --speed must be a positive number");
  }
  const state = daemon.state;
  const warn = log || (() => {});
  const clock = (at) => new Date(epochMs + at).toISOString();

  function attentionView(id) {
    const all = daemon.query("attention.list", { includeTerminal: true }).requests;
    return all.find((r) => r.id === id);
  }

  function apply(step, index) {
    const nowIso = clock(step.at);
    state.now = nowIso;
    switch (step.kind) {
      case "board.move":
        if (!moveBoardItem(state, step.number, step.status)) {
          warn(`step ${index}: no board item #${step.number}`);
        }
        return;
      case "queue.set":
        state.queue = step.items.map((q) => ({ ...q, repo: `${state.owner}/${q.repo}` }));
        daemon.emit("queue.changed", { count: state.queue.length });
        return;
      case "attention.raise":
        state.attention.push({ ...step.item, repo: `${state.owner}/${step.item.repo}` });
        daemon.emit("attention.event", { action: "raised", request: attentionView(step.item.id) });
        return;
      case "attention.resolve": {
        const item = state.attention.find((a) => a.id === step.id);
        if (!item) {
          warn(`step ${index}: no attention item ${step.id}`);
          return;
        }
        item.resolved = step.option;
        daemon.emit("attention.event", { action: "resolved", request: attentionView(step.id) });
        return;
      }
      case "history.append":
        state.history.unshift({
          ...rebaseValue(step.entry, 0, nowIso),
          repo: `${state.owner}/${step.entry.repo}`,
        });
        return;
      case "event":
        daemon.emit(step.method, rebaseValue(step.payload || {}, 0, nowIso));
        return;
      case "ui":
        // Decision 5: the extension runs it only in demo mode and only from
        // its own allowlist.
        daemon.emit("demo.uiStep", {
          command: step.action,
          args: step.target === undefined ? {} : { target: step.target },
        });
        return;
    }
  }

  /** Steps run one group at a time, in file order; a group shares one `at`. */
  function start() {
    let index = 0;
    let previousAt = 0;
    const next = () => {
      if (index >= steps.length) {
        if (onDone) onDone();
        return;
      }
      const at = steps[index].at;
      schedule(
        () => {
          while (index < steps.length && steps[index].at === at) {
            apply(steps[index], index);
            index += 1;
          }
          next();
        },
        (at - previousAt) / speed
      );
      previousAt = at;
    };
    next();
  }

  return { start, apply };
}

module.exports = {
  KINDS,
  NOW_TOKEN,
  UI_ACTIONS,
  createPlayer,
  loadScenario,
  rebaseState,
  rebaseValue,
  relativeToStart,
};
