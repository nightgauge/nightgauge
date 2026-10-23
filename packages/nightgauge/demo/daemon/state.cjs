/**
 * In-memory demo state (#2105, ADR-026 decisions 1 and 3).
 *
 * `createState(raw)` turns the `state` object of a seed or scenario file into
 * the daemon's working state. Seed entries are terse; every field the typed
 * IPC results need is filled here, so handlers only read. The mutation
 * helpers are the operations the scenario player (#2106) applies.
 */
"use strict";

const BOARD_STATUSES = ["Backlog", "Ready", "In Progress", "In Review", "Done"];

function fail(message) {
  throw new Error(`demo state: ${message}`);
}

function boardItem(owner, status, entry) {
  if (!Number.isInteger(entry.number) || typeof entry.title !== "string") {
    fail(`board item in "${status}" needs an integer number and a title`);
  }
  const repo = `${owner}/${entry.repo}`;
  return {
    id: `demo-item-${entry.number}`,
    number: entry.number,
    title: entry.title,
    state: status === "Done" ? "CLOSED" : "OPEN",
    status,
    priority: entry.priority || "",
    size: entry.size || "",
    labels: entry.labels || [],
    assignees: [],
    repo,
    url: `https://example.invalid/${repo}/issues/${entry.number}`,
    isEpic: false,
    blockedBy: [],
    blocking: [],
  };
}

function createState(raw) {
  if (!raw || typeof raw !== "object") fail("state must be an object");
  const owner = raw.owner;
  if (typeof owner !== "string" || !owner) fail("owner is required");
  const now = raw.now || "2026-01-15T09:30:00.000Z";
  const board = [];
  for (const [status, entries] of Object.entries(raw.board || {})) {
    if (!BOARD_STATUSES.includes(status)) fail(`unknown board status "${status}"`);
    for (const entry of entries) board.push(boardItem(owner, status, entry));
  }
  return {
    owner,
    projectNumber: raw.projectNumber || 1,
    now,
    repositories: (raw.repositories || []).map((r) => ({
      name: r.name,
      role: r.role || "app",
    })),
    board,
    activeRuns: (raw.activeRuns || []).map((r) => ({ ...r, repo: `${owner}/${r.repo}` })),
    queue: (raw.queue || []).map((q) => ({ ...q, repo: `${owner}/${q.repo}` })),
    attention: (raw.attention || []).map((a) => ({ ...a, repo: `${owner}/${a.repo}` })),
    history: (raw.history || []).map((h) => ({ ...h, repo: `${owner}/${h.repo}` })),
    knowledge: raw.knowledge || [],
    platform: {
      costByModel: (raw.platform && raw.platform.costByModel) || [],
      trends: (raw.platform && raw.platform.trends) || [],
    },
  };
}

/** Move a board item to another status; returns the item or null. */
function moveBoardItem(state, number, status) {
  if (!BOARD_STATUSES.includes(status)) fail(`unknown board status "${status}"`);
  const item = state.board.find((i) => i.number === number);
  if (!item) return null;
  item.status = status;
  item.state = status === "Done" ? "CLOSED" : "OPEN";
  return item;
}

module.exports = { BOARD_STATUSES, createState, moveBoardItem };
