#!/usr/bin/env tsx
/**
 * Cross-provider adapter × stage verification matrix (Issue #2).
 *
 * Runs one representative issue through the six pipeline stages once per
 * adapter (`nightgauge run <issue> --adapter <name>`), each in its own
 * `git clone --no-hardlinks` of the fixture checkout, and writes one evidence
 * row per adapter × stage as JSONL plus a markdown table on stdout.
 *
 * Live only: refuses unless NIGHTGAUGE_SKILL_EVAL_LIVE=1, before any probe or
 * spawn. `nightgauge run` ends in pr-merge, so `--repo` must be a disposable
 * fixture repository.
 *
 * Usage:
 *   NIGHTGAUGE_SKILL_EVAL_LIVE=1 npx tsx scripts/evaluate-adapter-matrix.ts \
 *     --issue 12 --repo <owner>/<fixture> --fixture-dir ~/src/fixture \
 *     [--adapters codex,gemini,copilot,ollama,lm-studio] [--out report.jsonl] \
 *     [--timeout-minutes 90]
 *
 * @see docs/SKILL_EVALUATION.md#cross-provider-adapter--stage-matrix
 */

import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { spawn } from "node:child_process";
import {
  defaultExec,
  historyRunRecordReader,
  isLiveModeEnabled,
  maybeResolveEvalAdapterProfile,
  providerForAdapter,
  renderMarkdownTable,
  runStageMatrix,
  toJsonl,
  type AdapterProbe,
  type StageMatrixRunFn,
} from "../packages/nightgauge-sdk/src/eval/index.js";

const DEFAULT_ADAPTERS = ["codex", "gemini", "copilot", "ollama", "lm-studio"];

interface MatrixArgs {
  adapters: string[];
  issue: number;
  repo: string;
  fixtureDir: string;
  out?: string;
  timeoutMinutes: number;
}

function parseArgs(argv: string[]): MatrixArgs {
  const get = (flag: string): string | undefined => {
    const i = argv.indexOf(flag);
    return i >= 0 ? argv[i + 1] : undefined;
  };
  const issue = Number(get("--issue"));
  const repo = get("--repo");
  const fixtureDir = get("--fixture-dir");
  if (!Number.isInteger(issue) || issue <= 0) throw new Error("--issue <number> is required");
  if (!repo || !/^[\w.-]+\/[\w.-]+$/.test(repo)) throw new Error("--repo <owner/name> is required");
  if (!fixtureDir) throw new Error("--fixture-dir <path to a clone of --repo> is required");
  const timeoutMinutes = Number(get("--timeout-minutes") ?? "90");
  if (!(timeoutMinutes > 0)) throw new Error("--timeout-minutes must be a positive number");
  const adapters = (get("--adapters") ?? DEFAULT_ADAPTERS.join(","))
    .split(",")
    .map((a) => a.trim())
    .filter(Boolean);
  return {
    adapters,
    issue,
    repo,
    fixtureDir: path.resolve(fixtureDir),
    out: get("--out"),
    timeoutMinutes,
  };
}

/** The CLI an adapter spawns: its eval profile's command, else the adapter name. */
function adapterCommand(adapter: string): string {
  return maybeResolveEvalAdapterProfile(providerForAdapter(adapter))?.resolveCommand() ?? adapter;
}

/** PATH probe only — never reads or prints credentials. */
const pathProbe: AdapterProbe = async (adapter) => {
  const cmd = adapterCommand(adapter);
  const r = await defaultExec("sh", ["-c", 'command -v "$1"', "sh", cmd], {});
  return r.code === 0
    ? { available: true }
    : { available: false, reason: `'${cmd}' not found on PATH` };
};

/** Spawn and buffer; on timeout SIGTERM the run (SIGKILL after 10s) so no cell outlives it. */
function runWithTimeout(
  cmd: string,
  args: string[],
  cwd: string,
  timeoutMs: number
): Promise<{ code: number; stdout: string; stderr: string; timedOut: boolean }> {
  return new Promise((resolve) => {
    const child = spawn(cmd, args, { cwd });
    let stdout = "";
    let stderr = "";
    let timedOut = false;
    let killTimer: NodeJS.Timeout | undefined;
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGTERM");
      killTimer = setTimeout(() => child.kill("SIGKILL"), 10_000);
    }, timeoutMs);
    child.stdout?.on("data", (d) => (stdout += d.toString()));
    child.stderr?.on("data", (d) => (stderr += d.toString()));
    const done = (code: number, extra = ""): void => {
      clearTimeout(timer);
      clearTimeout(killTimer);
      resolve({ code, stdout, stderr: stderr + extra, timedOut });
    };
    child.on("error", (err) => done(1, String(err)));
    child.on("close", (code) => done(code ?? 1));
  });
}

async function main(): Promise<number> {
  if (!isLiveModeEnabled()) {
    console.error(
      "ERROR: evaluate-adapter-matrix requires NIGHTGAUGE_SKILL_EVAL_LIVE=1 " +
        "(gated to avoid accidental API cost)."
    );
    return 1;
  }
  let args: MatrixArgs;
  try {
    args = parseArgs(process.argv.slice(2));
  } catch (err) {
    console.error(`evaluate-adapter-matrix: ${(err as Error).message}`);
    return 2;
  }

  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ng-adapter-matrix-"));
  const cellDirs = new Map<string, string>();
  const run: StageMatrixRunFn = async ({ adapter, issueNumber, timeoutMs }) => {
    const dir = path.join(root, adapter);
    const clone = await defaultExec("git", ["clone", "--no-hardlinks", args.fixtureDir, dir], {});
    if (clone.code !== 0) throw new Error(`clone failed: ${clone.stderr.trim()}`);
    cellDirs.set(adapter, dir);
    return runWithTimeout(
      "nightgauge",
      ["run", String(issueNumber), "--adapter", adapter],
      dir,
      timeoutMs
    );
  };
  const readHistory = historyRunRecordReader(defaultExec);

  const rows = await runStageMatrix({
    adapters: args.adapters,
    issueNumber: args.issue,
    repo: args.repo,
    timeoutMs: args.timeoutMinutes * 60_000,
    run,
    probe: pathProbe,
    readRecord: async (adapter, issue) => {
      const dir = cellDirs.get(adapter);
      return dir ? readHistory(dir, issue) : undefined;
    },
  });

  let out = args.out;
  if (!out) {
    const r = await defaultExec(
      "nightgauge",
      ["layout", "path", "pipeline", `adapter-matrix-${args.issue}.jsonl`],
      {}
    );
    out = r.code === 0 ? r.stdout.trim() : `adapter-matrix-${args.issue}.jsonl`;
  }
  await fs.mkdir(path.dirname(path.resolve(out)), { recursive: true });
  await fs.writeFile(out, toJsonl(rows), "utf-8");
  console.log(renderMarkdownTable(rows));
  console.log(`rows → ${out} | cell clones under ${root}`);
  return rows.some((r) => r.status === "fail") ? 1 : 0;
}

main()
  .then((code) => process.exit(code))
  .catch((err) => {
    console.error(err instanceof Error ? (err.stack ?? err.message) : String(err));
    process.exit(1);
  });
