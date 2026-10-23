/**
 * Demo-mode logging IPC stub and its committed inventory (#2103).
 */

import { spawn } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { describe, expect, it } from "vitest";
import { realHome } from "../isolateHome";
import {
  BEFORE_FIRST_CASE,
  buildInventory,
  driftProblems,
  parseLog,
  protocolVersionFromClient,
  protocolVersionFromDaemon,
  resultTypesFromClient,
  type IpcInventory,
} from "../../demo/ipc-inventory";

const packageRoot = path.resolve(__dirname, "..", "..");
const STUB = path.join(packageRoot, "demo", "ipc-stub.cjs");
const INVENTORY = path.join(packageRoot, "demo", "ipc-inventory.json");
const GENERATED = fs.readFileSync(
  path.join(packageRoot, "src", "services", "IpcClient.generated.ts"),
  "utf8"
);
const MANUAL = fs.readFileSync(path.join(packageRoot, "src", "services", "IpcClient.ts"), "utf8");

/**
 * The daemon's whole module graph: the entry plus every relative module it
 * requires, transitively. Only builtins may appear outside `demo/`.
 */
function moduleGraph(entry: string): Map<string, string> {
  const seen = new Map<string, string>();
  const visit = (file: string) => {
    if (seen.has(file)) return;
    const source = fs.readFileSync(file, "utf8");
    seen.set(file, source);
    for (const [, spec] of source.matchAll(/require\(\s*["']([^"']+)["']\s*\)/g)) {
      if (spec.startsWith(".")) visit(path.resolve(path.dirname(file), spec));
    }
  };
  visit(entry);
  return seen;
}
const graph = moduleGraph(STUB);
const DAEMON = path.join(packageRoot, "demo", "daemon", "daemon.cjs");

describe("demo daemon module graph is inert", () => {
  it("covers the entry and the daemon modules", () => {
    const files = [...graph.keys()].map((f) => path.relative(packageRoot, f)).sort();
    expect(files).toEqual([
      "demo/daemon/daemon.cjs",
      "demo/daemon/scenario.cjs",
      "demo/daemon/state.cjs",
      "demo/ipc-stub.cjs",
    ]);
  });

  it("requires only local filesystem and stream builtins", () => {
    const builtins = new Set<string>();
    for (const source of graph.values()) {
      for (const [, spec] of source.matchAll(/require\(\s*["']([^"']+)["']\s*\)/g)) {
        if (!spec.startsWith(".")) builtins.add(spec);
      }
      expect(source).not.toMatch(/\bimport\s*\(/);
    }
    expect([...builtins].sort()).toEqual(["node:fs", "node:os", "node:path", "node:readline"]);
  });

  it("uses no network, process-spawning or credential-reading API", () => {
    const envReads: string[] = [];
    for (const [file, source] of graph) {
      expect(source, file).not.toMatch(
        /\bfetch\s*\(|\bhttps?\b(?!:\/\/example\.invalid)|child_process|\bnet\b|tls|dgram|keychain|keytar/
      );
      envReads.push(...[...source.matchAll(/process\.env\.(\w+)/g)].map((m) => m[1]));
      expect(source, file).not.toMatch(/process\.env\[/);
    }
    // The request log, plus the scenario channel a binaryPath launch needs.
    expect(envReads.sort()).toEqual([
      "NIGHTGAUGE_DEMO_EVENT_LOG",
      "NIGHTGAUGE_DEMO_IPC_LOG",
      "NIGHTGAUGE_DEMO_SCENARIO",
      "NIGHTGAUGE_DEMO_SPEED",
      "NIGHTGAUGE_DEMO_START_FILE",
    ]);
  });

  it("announces the protocol version the extension expects", () => {
    const version = /const PROTOCOL_VERSION = (\d+);/.exec(graph.get(DAEMON) ?? "")?.[1];
    expect(Number(version)).toBe(protocolVersionFromClient(GENERATED));
  });
});

describe("demo/ipc-stub.cjs protocol", () => {
  it("sends ipc.ready, answers from demo state and logs the request", async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "ipc-stub-test-"));
    const log = path.join(dir, "log.jsonl");
    const proc = spawn(process.execPath, [STUB, "serve", "--workspace", dir], {
      env: { PATH: process.env.PATH, NIGHTGAUGE_DEMO_IPC_LOG: log },
    });
    let stdout = "";
    proc.stdout.on("data", (chunk: Buffer) => (stdout += chunk.toString()));
    proc.stdin.write(
      JSON.stringify({ id: 7, method: "board.list", params: { owner: "o", token: "t" } }) + "\n"
    );
    proc.stdin.end();
    await new Promise((resolve) => proc.on("exit", resolve));

    const lines = stdout
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line));
    expect(lines[0]).toEqual({ event: "ipc.ready", data: { protocolVersion: 2, demo: true } });
    expect(lines[1].id).toBe(7);
    expect(Array.isArray(lines[1].result)).toBe(true);

    const [entry] = parseLog(fs.readFileSync(log, "utf8"));
    expect(entry.method).toBe("board.list");
    expect(entry.params).toEqual({ owner: "o", token: "<redacted>" });
    expect(entry.paramsShape).toEqual({ owner: "string", token: "string" });
    expect(entry.answered).toBe(true);
    expect(typeof entry.ts).toBe("string");
    fs.rmSync(dir, { recursive: true, force: true });
  });

  it("fails a one-shot CLI subcommand at once instead of serving", async () => {
    const proc = spawn(process.execPath, [STUB, "worktree", "sweep", "--json"], {
      env: { PATH: process.env.PATH, NIGHTGAUGE_DEMO_IPC_LOG: path.join(os.tmpdir(), "x.jsonl") },
    });
    let stdout = "";
    let stderr = "";
    proc.stdout.on("data", (c: Buffer) => (stdout += c.toString()));
    proc.stderr.on("data", (c: Buffer) => (stderr += c.toString()));
    const code = await new Promise((resolve) => proc.on("exit", resolve));
    expect(code).toBe(1);
    expect(stdout).toBe("");
    expect(stderr).toMatch(/"worktree" is not available in demo mode/);
  });
});

/** Send `methods` to a fresh daemon and return its request log. */
async function logFor(methods: string[]): Promise<ReturnType<typeof parseLog>> {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "ipc-drift-test-"));
  const log = path.join(dir, "log.jsonl");
  const proc = spawn(process.execPath, [STUB, "serve"], {
    env: { PATH: process.env.PATH, NIGHTGAUGE_DEMO_IPC_LOG: log },
  });
  methods.forEach((method, i) => proc.stdin.write(JSON.stringify({ id: i + 1, method }) + "\n"));
  proc.stdin.end();
  await new Promise((resolve) => proc.on("exit", resolve));
  const entries = parseLog(fs.readFileSync(log, "utf8"));
  fs.rmSync(dir, { recursive: true, force: true });
  return entries;
}

describe("drift guard (#2109)", () => {
  const version = protocolVersionFromClient(GENERATED);
  const committed = JSON.parse(fs.readFileSync(INVENTORY, "utf8")) as IpcInventory;

  it("passes when the daemon answers every method the extension called", async () => {
    const entries = await logFor(committed.methods.map((m) => m.method));
    expect(
      driftProblems({
        entries,
        inventory: committed,
        clientProtocolVersion: version,
        daemonProtocolVersion: protocolVersionFromDaemon(graph.get(DAEMON) ?? ""),
      })
    ).toEqual([]);
  });

  it("names a method the daemon does not answer, and passes once it is gone", async () => {
    // Stands in for the extension calling a method nobody taught the daemon.
    const entries = await logFor(["board.list", "demo.unregisteredMethod"]);
    expect(entries.find((e) => e.method === "demo.unregisteredMethod")?.answered).toBe(false);
    const inventory = {
      ...committed,
      methods: [
        ...committed.methods,
        { ...committed.methods[0], method: "demo.unregisteredMethod" },
      ],
    };
    const inputs = { clientProtocolVersion: version, daemonProtocolVersion: version };
    const problems = driftProblems({ ...inputs, entries, inventory });
    expect(problems).toHaveLength(1);
    expect(problems[0]).toMatch(
      /called demo\.unregisteredMethod, which the demo daemon does not answer/
    );
    expect(
      driftProblems({
        ...inputs,
        entries: entries.filter((e) => e.method !== "demo.unregisteredMethod"),
        inventory,
      })
    ).toEqual([]);
  });

  it("names a called method the committed inventory does not list", () => {
    const problems = driftProblems({
      entries: [{ method: "board.counts", answered: true }],
      inventory: { ...committed, methods: [] },
      clientProtocolVersion: version,
      daemonProtocolVersion: version,
    });
    expect(problems).toEqual([
      expect.stringMatching(/board\.counts, which demo\/ipc-inventory\.json does not list/),
    ]);
  });

  it("names a protocol version that differs from the extension's", () => {
    const problems = driftProblems({
      entries: [],
      inventory: { ...committed, protocolVersion: version + 1 },
      clientProtocolVersion: version,
      daemonProtocolVersion: version - 1,
    });
    expect(problems).toHaveLength(2);
    expect(problems.join("\n")).toMatch(/inventory.*records protocol/);
    expect(problems.join("\n")).toMatch(/daemon announces protocol/);
  });
});

describe("buildInventory", () => {
  it("attributes requests to the case running when they arrived", () => {
    const inventory = buildInventory(
      [
        { method: "git.root", params: { workDir: "/tmp/ws/a" }, paramsShape: {} },
        { marker: "tree > repos" },
        { method: "board.list", params: { apiKey: "k" } },
        { method: "git.root", params: {} },
        { method: "pr.list", params: {} },
      ],
      GENERATED,
      MANUAL,
      [["/tmp/ws", "<workspace>"]]
    );
    const byMethod = new Map(inventory.methods.map((m) => [m.method, m]));
    expect(byMethod.get("git.root")?.surfaces).toEqual([BEFORE_FIRST_CASE, "tree > repos"]);
    expect(byMethod.get("git.root")?.sampleParams).toEqual({ workDir: "<workspace>/a" });
    expect(byMethod.get("board.list")?.sampleParams).toEqual({ apiKey: "<redacted>" });
    expect(byMethod.get("board.list")?.client).toBe("generated");
    expect(byMethod.get("pr.list")?.client).toBe("manual");
  });
});

describe("demo/ipc-inventory.json", () => {
  const inventory = JSON.parse(fs.readFileSync(INVENTORY, "utf8")) as IpcInventory;
  const generated = resultTypesFromClient(GENERATED);
  const manual = resultTypesFromClient(MANUAL);

  it("parses and lists at least one method", () => {
    expect(inventory.methods.length).toBeGreaterThan(0);
    expect(inventory.protocolVersion).toBe(protocolVersionFromClient(GENERATED));
  });

  it("names only methods the typed client declares, with their result type", () => {
    for (const entry of inventory.methods) {
      const declared = entry.client === "manual" ? manual : generated;
      expect(declared.has(entry.method), `${entry.method} (${entry.client})`).toBe(true);
      expect(entry.resultType).toBe(declared.get(entry.method));
    }
  });

  it("lists every method the generated client declares as generated", () => {
    const misfiled = inventory.methods.filter(
      (entry) => entry.client === "manual" && generated.has(entry.method)
    );
    expect(misfiled).toEqual([]);
  });

  it("carries no machine-local path or credential", () => {
    const text = JSON.stringify(inventory);
    expect(text).not.toContain(realHome);
    expect(text).not.toMatch(/\/(Users|home)\/|nightgauge-host-/);
    const samples = JSON.stringify(inventory.methods.map((entry) => entry.sampleParams));
    expect(samples).not.toMatch(
      /"(\w*token|\w*secret|\w*password|\w*[kK]ey)":"(?!<redacted>)[^"]+"/
    );
  });
});
