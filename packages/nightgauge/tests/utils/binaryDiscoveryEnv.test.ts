import { afterEach, beforeEach, describe, expect, it } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { resolveBinaryDiscoveryEnv } from "../../src/utils/binaryDiscoveryEnv";

describe("resolveBinaryDiscoveryEnv (#2760)", () => {
  const savedBinaryPath = process.env.NIGHTGAUGE_GO_BINARY_PATH;
  let dir: string;
  let binary: string;

  beforeEach(() => {
    dir = fs.mkdtempSync(path.join(os.tmpdir(), "ng-binary-env-"));
    binary = path.join(dir, "nightgauge");
    fs.writeFileSync(binary, "#!/bin/sh\n", { mode: 0o755 });
    process.env.NIGHTGAUGE_GO_BINARY_PATH = binary;
  });

  afterEach(() => {
    fs.rmSync(dir, { recursive: true, force: true });
    if (savedBinaryPath === undefined) delete process.env.NIGHTGAUGE_GO_BINARY_PATH;
    else process.env.NIGHTGAUGE_GO_BINARY_PATH = savedBinaryPath;
  });

  it("sets NIGHTGAUGE_BIN and puts the binary's directory first on PATH", () => {
    const env = resolveBinaryDiscoveryEnv();

    expect(env.NIGHTGAUGE_BIN).toBe(binary);
    expect(env.PATH.split(path.delimiter)[0]).toBe(dir);
    expect(env.PATH).toContain(process.env.PATH);
  });

  it("exports nothing for a binary it cannot execute", () => {
    fs.chmodSync(binary, 0o644);

    expect(resolveBinaryDiscoveryEnv()).toEqual({});
  });
});
