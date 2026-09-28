/**
 * Demo-mode logging IPC stub and its committed inventory (#2103).
 */

import { spawn } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { describe, expect, it } from "vitest";
import {
  BEFORE_FIRST_CASE,
  buildInventory,
  parseLog,
  protocolVersionFromClient,
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
const stubSource = fs.readFileSync(STUB, "utf8");

describe("demo/ipc-stub.cjs is inert", () => {
  it("requires only local filesystem and stream builtins", () => {
    const required = [...stubSource.matchAll(/require\(\s*["']([^"']+)["']\s*\)/g)].map(
      (m) => m[1]
    );
    expect(required.sort()).toEqual(["node:fs", "node:os", "node:path", "node:readline"]);
    expect(stubSource).not.toMatch(/\bimport\s*\(/);
  });

  it("uses no network, process-spawning or credential-reading API", () => {
    expect(stubSource).not.toMatch(/\bfetch\s*\(|https?\b|child_process|\bnet\b|tls|dgram/);
    const envReads = [...stubSource.matchAll(/process\.env\.(\w+)/g)].map((m) => m[1]);
    expect(envReads).toEqual(["NIGHTGAUGE_DEMO_IPC_LOG"]);
  });

  it("announces the protocol version the extension expects", () => {
    const stubVersion = /const PROTOCOL_VERSION = (\d+);/.exec(stubSource)?.[1];
    expect(Number(stubVersion)).toBe(protocolVersionFromClient(GENERATED));
  });
});

describe("demo/ipc-stub.cjs protocol", () => {
  it("sends ipc.ready, answers with null and logs the request", async () => {
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
    expect(lines[0]).toEqual({ event: "ipc.ready", data: { protocolVersion: 2 } });
    expect(lines[1]).toEqual({ id: 7, result: null });

    const [entry] = parseLog(fs.readFileSync(log, "utf8"));
    expect(entry.method).toBe("board.list");
    expect(entry.params).toEqual({ owner: "o", token: "<redacted>" });
    expect(entry.paramsShape).toEqual({ owner: "string", token: "string" });
    expect(typeof entry.ts).toBe("string");
    fs.rmSync(dir, { recursive: true, force: true });
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
    expect(text).not.toContain(os.homedir());
    expect(text).not.toMatch(/\/(Users|home)\/|nightgauge-host-/);
    const samples = JSON.stringify(inventory.methods.map((entry) => entry.sampleParams));
    expect(samples).not.toMatch(
      /"(\w*token|\w*secret|\w*password|\w*[kK]ey)":"(?!<redacted>)[^"]+"/
    );
  });
});
