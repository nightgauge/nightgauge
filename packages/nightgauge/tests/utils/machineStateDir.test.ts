/**
 * resolveStateHome must answer exactly as the Go resolver
 * (internal/layout.StateHomePathFrom, TestStateHomePathResolution) does: the
 * Go statusline writer and the extension's rate-limit reader meet at the file
 * this root names (#2032).
 */
import { describe, it, expect } from "vitest";
import * as path from "node:path";
import { resolveStateHome } from "../../src/utils/machineStateDir";

describe("resolveStateHome — the extension's STATE resolver (ADR-024 § 8)", () => {
  const home = "/home/op";
  const override = "/srv/override";
  const xdg = "/srv/xdg";
  const winHome = "C:\\Users\\op";
  const local = "C:\\Users\\op\\AppData\\Local";

  const posix: Array<[string, NodeJS.Platform, NodeJS.ProcessEnv, string | undefined]> = [
    [
      "override beats xdg (linux)",
      "linux",
      { NIGHTGAUGE_STATE_HOME: override, XDG_STATE_HOME: xdg },
      override,
    ],
    [
      "override beats xdg (darwin)",
      "darwin",
      { NIGHTGAUGE_STATE_HOME: override, XDG_STATE_HOME: xdg },
      override,
    ],
    ["override is cleaned", "darwin", { NIGHTGAUGE_STATE_HOME: "/srv/override/" }, override],
    ["xdg on linux", "linux", { XDG_STATE_HOME: xdg }, path.posix.join(xdg, "nightgauge")],
    ["xdg on darwin", "darwin", { XDG_STATE_HOME: xdg }, path.posix.join(xdg, "nightgauge")],
    ["linux default", "linux", {}, path.posix.join(home, ".local", "state", "nightgauge")],
    ["darwin default", "darwin", {}, path.posix.join(home, ".nightgauge", "state")],
    [
      "relative xdg is ignored",
      "linux",
      { XDG_STATE_HOME: "rel/xdg" },
      path.posix.join(home, ".local", "state", "nightgauge"),
    ],
    [
      "a relative override resolves nothing",
      "linux",
      { NIGHTGAUGE_STATE_HOME: "relative/state" },
      undefined,
    ],
  ];
  for (const [name, platform, env, want] of posix) {
    it(name, () => {
      expect(resolveStateHome(env, platform, home)).toBe(want);
    });
  }

  it("override beats xdg (windows)", () => {
    expect(
      resolveStateHome(
        { NIGHTGAUGE_STATE_HOME: "D:\\state", XDG_STATE_HOME: "D:\\xdg", LOCALAPPDATA: local },
        "win32",
        winHome
      )
    ).toBe("D:\\state");
  });

  it("windows default is LOCALAPPDATA, never roaming APPDATA", () => {
    expect(
      resolveStateHome({ LOCALAPPDATA: local, APPDATA: "C:\\Roaming" }, "win32", winHome)
    ).toBe(path.win32.join(local, "nightgauge", "state"));
  });

  it("windows without LOCALAPPDATA", () => {
    expect(resolveStateHome({}, "win32", winHome)).toBe(
      path.win32.join(winHome, "AppData", "Local", "nightgauge", "state")
    );
  });

  it("no home and no override resolves nothing, never a workspace path", () => {
    expect(resolveStateHome({}, "linux", "")).toBeUndefined();
  });
});
