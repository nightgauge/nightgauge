import { afterEach, beforeEach, describe, expect, it } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { installBinaryShim, userShimPath } from "../../src/utils/binaryShim";

describe.skipIf(process.platform === "win32")("installBinaryShim (#2765)", () => {
  let dir: string;
  let home: string;
  let bundled: string;

  const makeBinary = (p: string, mode = 0o755): string => {
    fs.mkdirSync(path.dirname(p), { recursive: true });
    fs.writeFileSync(p, "#!/bin/sh\n", { mode });
    return p;
  };

  beforeEach(() => {
    dir = fs.mkdtempSync(path.join(os.tmpdir(), "ng-shim-"));
    home = path.join(dir, "home");
    fs.mkdirSync(home);
    bundled = makeBinary(path.join(dir, "ext-0.8.0", "dist", "bin", "nightgauge"));
  });

  afterEach(() => {
    fs.rmSync(dir, { recursive: true, force: true });
  });

  const install = (bundledBinary = bundled) =>
    installBinaryShim({ bundledBinary, homeDir: home, platform: process.platform });

  it("creates ~/.nightgauge/bin/nightgauge pointing at the bundled binary", async () => {
    await expect(install()).resolves.toBe("created");
    const shim = userShimPath(home);
    expect(shim).toBe(path.join(home, ".nightgauge", "bin", "nightgauge"));
    expect(fs.readlinkSync(shim)).toBe(bundled);
    expect(fs.readdirSync(path.dirname(shim))).toEqual(["nightgauge"]);
  });

  it("is a no-op when the shim already points at this bundle", async () => {
    await install();
    await expect(install()).resolves.toBe("unchanged");
  });

  it("replaces a link to a superseded bundle", async () => {
    const old = makeBinary(path.join(dir, "ext-0.7.0", "dist", "bin", "nightgauge"));
    await install(old);
    await expect(install()).resolves.toBe("created");
    expect(fs.readlinkSync(userShimPath(home))).toBe(bundled);
  });

  it("replaces a regular file at the shim path", async () => {
    makeBinary(userShimPath(home));
    await expect(install()).resolves.toBe("created");
    expect(fs.readlinkSync(userShimPath(home))).toBe(bundled);
  });

  it("skips a missing or non-executable bundled binary", async () => {
    await expect(install(path.join(dir, "absent"))).resolves.toBe("skipped");
    fs.chmodSync(bundled, 0o644);
    await expect(install()).resolves.toBe("skipped");
    expect(fs.existsSync(path.join(home, ".nightgauge"))).toBe(false);
  });

  it("skips win32", async () => {
    await expect(
      installBinaryShim({ bundledBinary: bundled, homeDir: home, platform: "win32" })
    ).resolves.toBe("skipped");
    expect(fs.existsSync(path.join(home, ".nightgauge"))).toBe(false);
  });
});
