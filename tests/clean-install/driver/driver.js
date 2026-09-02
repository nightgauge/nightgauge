"use strict";
/**
 * Clean-install gate driver — loaded by VS Code as `--extensionTestsPath`.
 *
 * Runs INSIDE a real extension host that has the packaged Nightgauge VSIX
 * installed into a fresh --extensions-dir. It walks the Marketplace README's
 * Quick Start as a first-time user would, using only public VS Code API
 * surface (commands, the extensions registry, the file system) plus xdotool
 * for the one step the product only offers as an input box.
 *
 * Plain CommonJS on purpose: nothing to compile inside the container, and no
 * dependency on the product's test bundles (the product must come from the
 * VSIX, never from a development path).
 *
 * Inputs (environment):
 *   CLEAN_INSTALL_REPORT          path the JSON report is written to
 *   CLEAN_INSTALL_ISSUE           issue number to drive (default 1)
 *   CLEAN_INSTALL_WALL_CLOCK_MS   give up after this long (default 90 min)
 *   CLEAN_INSTALL_COST_CAP_USD    stop the run past this spend (default 15)
 *   CLEAN_INSTALL_EXTENSIONS_DIR  the fresh --extensions-dir (for provenance)
 *   CLEAN_INSTALL_SMOKE           "1": stop after activation + binary checks
 *
 * The report is the contract with scripts/clean-install-e2e.sh: every
 * assertion lands there with evidence, and the host script treats a missing
 * report as a failure (a window that dies before this module runs can still
 * exit 0 — the same lesson tests/vscode-host/launch.ts records).
 */

const fs = require("node:fs");
const path = require("node:path");
const { execFileSync, execFile } = require("node:child_process");
const vscode = require("vscode");

const EXTENSION_ID = "nightgauge.nightgauge-vscode";
const POLL_MS = 15_000;

const report = {
  status: "running",
  startedAt: new Date().toISOString(),
  assertions: [],
  findings: [],
  extension: {},
  binary: {},
  run: {},
  stages: [],
};

function log(line) {
  const stamped = `[driver ${new Date().toISOString()}] ${line}`;
  console.log(stamped);
}

function assert(name, ok, evidence) {
  report.assertions.push({ name, ok: Boolean(ok), evidence });
  log(`${ok ? "PASS" : "FAIL"} ${name} — ${evidence}`);
  if (!ok) {
    throw new Error(`assertion failed: ${name} — ${evidence}`);
  }
}

function finding(text) {
  report.findings.push(text);
  log(`FINDING ${text}`);
}

function writeReport() {
  const target = process.env.CLEAN_INSTALL_REPORT;
  if (!target) return;
  report.finishedAt = new Date().toISOString();
  fs.writeFileSync(target, JSON.stringify(report, null, 2));
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function readJson(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return undefined;
  }
}

function xdotool(args) {
  return new Promise((resolve) => {
    execFile("xdotool", args, (err, stdout, stderr) => {
      if (err) log(`xdotool ${args.join(" ")} failed: ${stderr || err.message}`);
      resolve(!err);
    });
  });
}

/** README step 1 (Install) — the extension must come from the VSIX. */
async function assertInstalledFromVsix() {
  const ext = vscode.extensions.getExtension(EXTENSION_ID);
  assert(
    "extension is installed",
    Boolean(ext),
    ext ? ext.extensionPath : `no extension ${EXTENSION_ID}`
  );

  const extensionsDir = process.env.CLEAN_INSTALL_EXTENSIONS_DIR;
  if (extensionsDir) {
    const inside = path
      .resolve(ext.extensionPath)
      .startsWith(path.resolve(extensionsDir) + path.sep);
    assert("extension path is inside the fresh --extensions-dir", inside, ext.extensionPath);
  }
  assert(
    "extension is not the driver's development path",
    ext.extensionKind !== undefined && !ext.extensionPath.includes("/harness/"),
    ext.extensionPath
  );

  if (!ext.isActive) {
    // A user activates it by opening the Nightgauge sidebar; the API call is
    // the same activation path.
    await ext.activate();
  }
  assert("extension activated", ext.isActive, `version ${ext.packageJSON.version}`);
  report.extension = {
    id: ext.id,
    version: ext.packageJSON.version,
    path: ext.extensionPath,
  };

  // The binary the extension resolves at tier 3 of its cascade (no setting,
  // no env override, so the bundled one is what runs).
  const bundled = path.join(ext.extensionPath, "dist", "bin", "nightgauge");
  assert("bundled binary exists and is executable", isExecutable(bundled), bundled);
  let version = "";
  try {
    version = execFileSync(bundled, ["version"], { encoding: "utf8", timeout: 20_000 }).trim();
  } catch (err) {
    assert("bundled binary reports a version", false, String(err.stderr || err.message));
  }
  assert("bundled binary reports a version", version.length > 0, version.split("\n")[0]);
  report.binary = { path: bundled, version: version.split("\n")[0] };
}

function isExecutable(file) {
  try {
    fs.accessSync(file, fs.constants.X_OK);
    return fs.statSync(file).isFile();
  } catch {
    return false;
  }
}

function workspaceRoot() {
  const folders = vscode.workspace.workspaceFolders;
  if (!folders || folders.length === 0) throw new Error("no workspace folder open");
  return folders[0].uri.fsPath;
}

/** README step 4 (Initialize the repo). */
async function assertInitialized(root) {
  const config = path.join(root, ".nightgauge", "config.yaml");
  assert("README step 4: .nightgauge/config.yaml exists", fs.existsSync(config), config);
  // The product's own predicate for "initialized" is this file; the welcome
  // view flips on the context key this command refreshes.
  await vscode.commands.executeCommand("nightgauge.refreshRepoInitializedContext");
  finding(
    "README step 4 was NOT walked through the product: `Initialize Repository` " +
      "(nightgauge.quickstartRepoInit) opens an interactive `claude /nightgauge:repo-init` " +
      "terminal that a headless run cannot drive. The container provisioned the repository " +
      "with the VSIX's own binary verbs instead (config init, label ensure, project ensure-fields, " +
      "forge graphql link) — the same mutations the skill makes, minus its questions."
  );
}

/** README step 5 (Claim an issue). */
async function pickUpIssue(issueNumber) {
  const commands = await vscode.commands.getCommands(true);
  assert(
    "README step 5: nightgauge.pickupIssue is registered",
    commands.includes("nightgauge.pickupIssue"),
    `${commands.filter((c) => c.startsWith("nightgauge.")).length} nightgauge.* commands registered`
  );

  finding(
    "README step 5 has no programmatic path: nightgauge.pickupIssue accepts only a live " +
      "ReadyIssueTreeItem and otherwise opens an input box; nightgauge.startPipelineForIssue " +
      "returns silently without one. The driver types the issue number into the input box " +
      "with xdotool, exactly as a user would."
  );

  // Fire the command (it blocks on the input box) and type into the box.
  const invoked = vscode.commands.executeCommand("nightgauge.pickupIssue");
  await sleep(2500);
  await xdotool(["type", "--delay", "120", String(issueNumber)]);
  await sleep(500);
  await xdotool(["key", "Return"]);

  const outcome = await Promise.race([
    invoked.then(
      () => "resolved",
      (err) => `rejected: ${err && err.message}`
    ),
    sleep(120_000).then(() => "timeout"),
  ]);
  assert("pickupIssue accepted the issue number", outcome === "resolved", outcome);
}

/** README step 6 (Watch it run) — poll the run records until terminal. */
async function watchRun(root, issueNumber, wallClockMs, costCapUsd) {
  const pipelineDir = path.join(root, ".nightgauge", "pipeline");
  const historyDir = path.join(pipelineDir, "history");
  const deadline = Date.now() + wallClockMs;
  let lastStage = "";
  let runtime;
  let runtimeFile;
  let history;
  let sawRuntime = false;

  while (Date.now() < deadline) {
    await sleep(POLL_MS);

    // Per-run snapshot (ADR-017: runtime-<issue>-<runId>.json).
    let files = [];
    try {
      files = fs
        .readdirSync(pipelineDir)
        .filter((f) => f.startsWith(`runtime-${issueNumber}-`) && f.endsWith(".json"));
    } catch {
      // not created yet
    }
    for (const f of files) {
      const parsed = readJson(path.join(pipelineDir, f));
      if (parsed) {
        runtime = parsed;
        runtimeFile = path.join(pipelineDir, f);
        sawRuntime = true;
      }
    }
    const sidecar = readJson(path.join(pipelineDir, "current-run.json"));
    const stage = (sidecar && sidecar.stage) || (runtime && runtime.stage) || "";
    if (stage && stage !== lastStage) {
      lastStage = stage;
      report.stages.push({ stage, at: new Date().toISOString() });
      log(`stage → ${stage}`);
    }

    if (runtime && typeof runtime.totalCostUsd === "number" && runtime.totalCostUsd > costCapUsd) {
      log(`cost ${runtime.totalCostUsd} exceeds cap ${costCapUsd}; stopping the pipeline`);
      try {
        await vscode.commands.executeCommand("nightgauge.stopPipeline");
      } catch (err) {
        log(`stopPipeline failed: ${err && err.message}`);
      }
      assert(
        "run stayed under the cost cap",
        false,
        `${runtime.totalCostUsd} USD > ${costCapUsd} USD`
      );
    }

    history = findHistoryRecord(historyDir, issueNumber);
    const terminal = (runtime && runtime.terminal === true) || Boolean(history);
    if (terminal) break;
  }

  assert("a run record was created for the issue", sawRuntime, runtimeFile || "no runtime-*.json");
  const terminal = (runtime && runtime.terminal === true) || Boolean(history);
  assert(
    "run reached a terminal state within the wall clock",
    terminal,
    terminal
      ? `outcome ${runtime && runtime.terminalOutcome}`
      : `last stage ${lastStage || "(none)"}`
  );

  // Give the history writer a moment after the runtime record flips.
  for (let i = 0; i < 6 && !history; i += 1) {
    await sleep(5_000);
    history = findHistoryRecord(historyDir, issueNumber);
  }

  // The per-run snapshot is removed once the run latches terminal, so the last
  // poll can predate the fields pr-merge writes (prUrl, mergedCommitSha). The
  // survival record (pipeline/survival-records.jsonl) is written by the merge
  // itself and names the PR and merge commit, so it is the durable evidence.
  const survival = findSurvivalRecord(pipelineDir, issueNumber);
  const prUrl =
    (runtime && runtime.prUrl) ||
    (survival && survival.repo && survival.pr_number
      ? `https://github.com/${survival.repo}/pull/${survival.pr_number}`
      : undefined);
  const mergedCommitSha =
    (runtime && runtime.mergedCommitSha) || (survival && survival.merge_commit_sha);

  report.run = {
    runId: runtime && runtime.runId,
    runtimeFile,
    terminalOutcome: runtime && runtime.terminalOutcome,
    prUrl,
    mergedCommitSha,
    prEvidence: runtime && runtime.prUrl ? "runtime" : survival ? "survival-record" : "none",
    totalCostUsd: runtime && runtime.totalCostUsd,
    startedAt: runtime && runtime.startedAt,
    terminalAt: runtime && runtime.terminalAt,
    completedStages: runtime && runtime.completedStages,
    stageErrors: runtime && runtime.stageErrors,
    history: history && {
      outcome: history.outcome,
      outcomeType: history.outcome_type,
      totalDurationMs: history.total_duration_ms,
      estimatedCostUsd: history.tokens && history.tokens.estimated_cost_usd,
      terminalFailureKind: history.terminal_failure_kind,
      stageErrors: Object.fromEntries(
        Object.entries(history.stages || {})
          .filter(([, v]) => v && v.error)
          .map(([k, v]) => [k, v.error])
      ),
    },
  };

  assert(
    "history record exists for the run",
    Boolean(history),
    history ? `outcome ${history.outcome}` : "none"
  );
  assert(
    "run outcome is complete",
    (history && history.outcome === "complete") ||
      (runtime && runtime.terminalOutcome === "complete"),
    `history=${history && history.outcome} runtime=${runtime && runtime.terminalOutcome} errors=${JSON.stringify(
      (runtime && runtime.stageErrors) || {}
    )}`
  );
  assert(
    "run record carries a PR (runtime prUrl or survival record)",
    Boolean(prUrl),
    prUrl
      ? `${prUrl} (${report.run.prEvidence})`
      : "none in runtime snapshot or survival-records.jsonl"
  );
  const cost = Number(
    (history && history.tokens && history.tokens.estimated_cost_usd) ||
      (runtime && runtime.totalCostUsd) ||
      0
  );
  assert("recorded cost is non-zero", cost > 0, `${cost} USD`);
  const duration = Number((history && history.total_duration_ms) || 0);
  assert("recorded duration is non-zero", duration > 0, `${duration} ms`);
}

/** The survival record the merge writes (kind=survival, one line per merged PR). */
function findSurvivalRecord(pipelineDir, issueNumber) {
  let text;
  try {
    text = fs.readFileSync(path.join(pipelineDir, "survival-records.jsonl"), "utf8");
  } catch {
    return undefined;
  }
  let found;
  for (const line of text.split("\n")) {
    if (!line.trim()) continue;
    try {
      const rec = JSON.parse(line);
      if (rec && rec.kind === "survival" && Number(rec.issue_number) === Number(issueNumber)) {
        found = rec;
      }
    } catch {
      // partial line
    }
  }
  return found;
}

function findHistoryRecord(historyDir, issueNumber) {
  let files;
  try {
    // Daily run files only — history/ also holds outcomes.jsonl, whose
    // records carry the same issue number in a different shape.
    files = fs.readdirSync(historyDir).filter((f) => /^\d{4}-\d{2}-\d{2}\.jsonl$/.test(f));
  } catch {
    return undefined;
  }
  let found;
  for (const f of files) {
    const lines = fs.readFileSync(path.join(historyDir, f), "utf8").split("\n").filter(Boolean);
    for (const line of lines) {
      try {
        const rec = JSON.parse(line);
        const num = rec.issue_number ?? rec.issueNumber;
        if (Number(num) === Number(issueNumber)) found = rec; // last one wins
      } catch {
        // skip malformed line
      }
    }
  }
  return found;
}

async function run() {
  const issueNumber = Number(process.env.CLEAN_INSTALL_ISSUE || 1);
  const wallClockMs = Number(process.env.CLEAN_INSTALL_WALL_CLOCK_MS || 90 * 60_000);
  const costCapUsd = Number(process.env.CLEAN_INSTALL_COST_CAP_USD || 15);
  const smoke = process.env.CLEAN_INSTALL_SMOKE === "1";

  try {
    log(
      `clean-install driver: issue #${issueNumber}, wall clock ${wallClockMs} ms, cost cap ${costCapUsd} USD, smoke=${smoke}`
    );
    await assertInstalledFromVsix();
    const root = workspaceRoot();
    log(`workspace ${root}`);
    if (smoke) {
      report.status = "pass";
      writeReport();
      return;
    }
    await assertInitialized(root);
    await pickUpIssue(issueNumber);
    await watchRun(root, issueNumber, wallClockMs, costCapUsd);
    report.status = "pass";
    writeReport();
  } catch (err) {
    report.status = "fail";
    report.error = err && (err.stack || err.message);
    writeReport();
    throw err;
  }
}

module.exports = { run };
