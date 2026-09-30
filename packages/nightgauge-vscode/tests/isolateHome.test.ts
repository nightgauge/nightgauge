/**
 * The suite never runs against the developer's home or machine state (#2311).
 */
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { describe, expect, it } from "vitest";
import { resolveStateHome } from "../src/utils/machineStateDir";
import { isolatedHome, realHome } from "./isolateHome";

const tmp = fs.realpathSync(os.tmpdir());
const under = (dir: string, root: string) => path.resolve(dir).startsWith(root + path.sep);

describe("test isolation (#2311)", () => {
  it("points HOME at a temporary directory, not the real home", () => {
    expect(os.homedir()).toBe(isolatedHome);
    expect(under(fs.realpathSync(os.homedir()), tmp)).toBe(true);
    expect(os.homedir()).not.toBe(realHome);
  });

  it("resolves machine state, caches and the runtime root inside it", () => {
    const state = resolveStateHome();
    expect(state).toBe(path.join(isolatedHome, "state"));
    expect(under(state!, isolatedHome)).toBe(true);
    expect(under(process.env.NIGHTGAUGE_CACHE_HOME!, isolatedHome)).toBe(true);
    expect(under(fs.realpathSync(process.env.NIGHTGAUGE_RUNTIME_DIR!), tmp)).toBe(true);
    expect(process.env.NIGHTGAUGE_DAEMON_SOCKET).toBeUndefined();
  });
});
