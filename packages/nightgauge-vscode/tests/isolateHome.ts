/**
 * Hermetic home and machine state for the vitest suite (#2311).
 *
 * The TypeScript twin of the Go suite's `internal/hometest.Isolate`. Code
 * under test resolves machine state (the serve claims, usage readings, logs)
 * from `NIGHTGAUGE_STATE_HOME`, or from the platform default under `HOME`,
 * and some tests start a real `nightgauge serve`. Without this, that daemon
 * runs against the developer's real state: its startup prune removed real
 * serve claims, and a CLI started with the real HOME can migrate legacy
 * `~/.nightgauge` data into a directory the test then deletes.
 *
 * It must run before any module under test is evaluated, so tests/setup.ts
 * imports it FIRST: ES modules evaluate in import order. Every value is
 * overwritten, not defaulted, because the developer's environment (or the
 * daemon a suite is started from) may already point them at the real
 * directories.
 */
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterAll } from "vitest";

/**
 * The home the worker had before any isolation, for assertions that name it.
 * Kept in the environment: a worker runs several test files, and from the
 * second one on os.homedir() is already an isolated home.
 */
export const realHome = (process.env.NIGHTGAUGE_TEST_REAL_HOME ??= os.homedir());

/** The isolated home every test in this file runs under. */
export const isolatedHome = fs.realpathSync(
  fs.mkdtempSync(path.join(os.tmpdir(), "ng-vscode-home-"))
);

// A short directory of its own for the daemon socket's runtime root: a socket
// path under the isolated home could exceed sun_path.
const runtimeDir = fs.mkdtempSync(path.join(os.tmpdir(), "ngrt"));

// The Go toolchain keeps its caches and its `go env -w` file under HOME, and
// some tests build the binary (`go build`). Pin them to the real locations
// before HOME moves: a fresh module cache per test file would download every
// module again, and its read-only files cannot be removed by the cleanup below.
// Values the developer set explicitly are kept.
{
  const env = process.env;
  const userCache =
    process.platform === "darwin"
      ? path.join(realHome, "Library", "Caches")
      : process.platform === "win32"
        ? (env.LOCALAPPDATA ?? path.join(realHome, "AppData", "Local"))
        : (env.XDG_CACHE_HOME ?? path.join(realHome, ".cache"));
  const userConfig =
    process.platform === "darwin"
      ? path.join(realHome, "Library", "Application Support")
      : process.platform === "win32"
        ? (env.APPDATA ?? path.join(realHome, "AppData", "Roaming"))
        : (env.XDG_CONFIG_HOME ?? path.join(realHome, ".config"));
  env.GOPATH ??= path.join(realHome, "go");
  env.GOCACHE ??= path.join(userCache, "go-build");
  env.GOMODCACHE ??= path.join(env.GOPATH.split(path.delimiter)[0], "pkg", "mod");
  env.GOENV ??= path.join(userConfig, "go", "env");
}

Object.assign(process.env, {
  HOME: isolatedHome,
  NIGHTGAUGE_STATE_HOME: path.join(isolatedHome, "state"),
  NIGHTGAUGE_CACHE_HOME: path.join(isolatedHome, "cache"),
  NIGHTGAUGE_RUNTIME_DIR: runtimeDir,
});
if (process.platform === "win32") {
  process.env.USERPROFILE = isolatedHome;
}
// A suite run from inside a daemon's child inherits that daemon's socket.
delete process.env.NIGHTGAUGE_DAEMON_SOCKET;

afterAll(() => {
  fs.rmSync(isolatedHome, { recursive: true, force: true });
  fs.rmSync(runtimeDir, { recursive: true, force: true });
});
