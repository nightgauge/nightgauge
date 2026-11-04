/**
 * check-runtime-assets.sh refuses a package whose resources name a script it
 * does not ship (#2762). The Codex commands under resources/codex/commands
 * called scripts/run-stage.sh, which the VSIX never carried.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { spawnSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

const SCRIPT = path.join(__dirname, "../../scripts/check-runtime-assets.sh");

let tmp: string;

function write(file: string, text: string, mode?: number): void {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, text);
  if (mode !== undefined) {
    fs.chmodSync(file, mode);
  }
}

/** A package directory whose dist/ carries every runtime asset the check requires. */
function makePackage(): string {
  const pkg = path.join(tmp, "pkg");
  write(path.join(pkg, "dist", "bin", "nightgauge"), "#!/bin/sh\n", 0o755);
  write(path.join(pkg, "dist", "sdk-cli.cjs"), "");
  write(path.join(pkg, "dist", "extension.cjs"), "");
  write(path.join(pkg, "dist", "vendor", "marked.umd.js"), "");
  write(path.join(pkg, "resources", "icon.svg"), "<svg/>\n");
  return pkg;
}

function check(target: string): { status: number | null; stderr: string } {
  const res = spawnSync("bash", [SCRIPT, target], { encoding: "utf8" });
  if (res.error) {
    throw new Error(`HARNESS ERROR: bash could not run: ${res.error.message}`);
  }
  return { status: res.status, stderr: res.stderr };
}

describe("check-runtime-assets.sh resource references (#2762)", () => {
  beforeEach(() => {
    tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ng-runtime-assets-"));
  });

  afterEach(() => {
    fs.rmSync(tmp, { recursive: true, force: true });
  });

  it("passes a package whose resources name no script", () => {
    const pkg = makePackage();
    expect(check(path.join(pkg, "dist")).status).toBe(0);
  });

  it("fails a resource that names a script the VSIX does not ship", () => {
    const pkg = makePackage();
    write(path.join(pkg, "resources", "cmd.md"), "Run `bash scripts/run-stage.sh pickup`.\n");
    const res = check(path.join(pkg, "dist"));
    expect(res.status).toBe(1);
    expect(res.stderr).toContain("cmd.md names scripts/run-stage.sh");
  });

  it("fails while resources/codex/commands exists", () => {
    const pkg = makePackage();
    write(path.join(pkg, "resources", "codex", "commands", "README.md"), "# commands\n");
    const res = check(path.join(pkg, "dist"));
    expect(res.status).toBe(1);
    expect(res.stderr).toContain("codex/commands exists");
  });
});
