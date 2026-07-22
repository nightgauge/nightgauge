/**
 * Tests for WorktreeManager Flutter codegen hook
 *
 * Verifies that per-issue worktrees automatically regenerate stale
 * `.g.dart` companion files for Flutter/Dart projects that use
 * `part '*.g.dart';` directives. Without this, `feature-dev`-authored
 * tests routinely fail with "Undefined class <Something>Companion"
 * because the committed .g.dart files don't match the current schema.
 *
 * Detection contract:
 * - pubspec.yaml at worktree root → Flutter project
 * - any .dart file with `part '*.g.dart';` → codegen needed
 * - .fvmrc or .fvm/ present → prefer `fvm flutter`
 *
 * Non-fatal: codegen errors are logged and swallowed so non-Flutter
 * or partially-configured projects don't fail worktree creation.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const { execAsyncMock, execFileAsyncMock, fsMock } = vi.hoisted(() => ({
  execAsyncMock: vi.fn(),
  execFileAsyncMock: vi.fn(),
  fsMock: {
    mkdir: vi.fn().mockResolvedValue(undefined),
    access: vi.fn().mockResolvedValue(undefined),
    rm: vi.fn().mockResolvedValue(undefined),
    readFile: vi.fn().mockResolvedValue(""),
    writeFile: vi.fn().mockResolvedValue(undefined),
    readdir: vi.fn().mockResolvedValue([]),
    cp: vi.fn().mockResolvedValue(undefined),
  },
}));

const execFileSyncMock = vi.hoisted(() => vi.fn().mockReturnValue(Buffer.from("")));

// #2884: WorktreeManager now uses promisify(execFile) for git ops; mocks
// must export execFile with the promisify.custom symbol so awaited calls
// resolve.
vi.mock("node:child_process", () => {
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const execMock = vi.fn();
  (execMock as any)[kCustom] = execAsyncMock;

  const execFileMock = vi.fn();
  (execFileMock as any)[kCustom] = execFileAsyncMock;

  return { exec: execMock, execFile: execFileMock, execFileSync: execFileSyncMock };
});

vi.mock("node:fs/promises", () => fsMock);

import { WorktreeManager } from "../../src/utils/WorktreeManager";

/**
 * Build an fs.access mock that resolves for each path listed in `present`
 * and rejects with ENOENT for everything else. Paths are matched by
 * `endsWith` so call-sites don't need absolute path prefixes.
 */
function mockFsPresent(present: string[]) {
  fsMock.access.mockImplementation((filePath: string) => {
    if (present.some((p) => filePath.endsWith(p))) {
      return Promise.resolve(undefined);
    }
    return Promise.reject(new Error(`ENOENT: ${filePath}`));
  });
}

function flutterCalls() {
  return execAsyncMock.mock.calls.filter(
    ([cmd]: [string]) =>
      typeof cmd === "string" &&
      (cmd.includes("flutter pub get") ||
        cmd.includes("flutter pub run build_runner") ||
        cmd.startsWith("fvm flutter"))
  );
}

/** Helper: matches the WorktreeManager's internal grep command used for
 * detecting whether a Flutter project needs codegen. The command contains
 * escaped literals (`\.g\.dart`) so simple substring checks on ".g.dart"
 * won't match — anchor on the unambiguous fragment `--include="*.dart"`. */
function isCodegenDetectionCommand(cmd: unknown): cmd is string {
  return typeof cmd === "string" && cmd.includes('grep -r --include="*.dart"');
}

describe("WorktreeManager — Flutter codegen hook", () => {
  let manager: WorktreeManager;
  const repoRoot = "/repo";

  beforeEach(() => {
    vi.clearAllMocks();
    manager = new WorktreeManager(repoRoot, ".worktrees");
    execAsyncMock.mockResolvedValue({ stdout: "", stderr: "" });
    execFileAsyncMock.mockResolvedValue({ stdout: "", stderr: "" });
    execFileSyncMock.mockReturnValue(Buffer.from(""));
  });

  it("runs `flutter pub get` then `build_runner build` when pubspec.yaml + part '*.g.dart' present", async () => {
    // pubspec.yaml exists; package.json does not; no .fvmrc / .fvm
    mockFsPresent(["pubspec.yaml", ".gitignore"]);

    // grep returns a match → codegen needed
    execAsyncMock.mockImplementation((cmd: string) => {
      if (isCodegenDetectionCommand(cmd)) {
        return Promise.resolve({ stdout: "lib/db.dart\n", stderr: "" });
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    });

    await manager.create(42, "feat/42-flutter-test", {
      _adapterResolver: () => "claude",
    });

    const pubGet = execAsyncMock.mock.calls.find(
      ([cmd]: [string]) => typeof cmd === "string" && cmd === "flutter pub get"
    );
    const buildRunner = execAsyncMock.mock.calls.find(
      ([cmd]: [string]) =>
        typeof cmd === "string" &&
        cmd === "flutter pub run build_runner build --delete-conflicting-outputs"
    );

    expect(pubGet).toBeDefined();
    expect(buildRunner).toBeDefined();
    expect(pubGet?.[1]).toEqual(expect.objectContaining({ cwd: "/repo/.worktrees/issue-42" }));
  });

  it("uses `fvm flutter` when .fvmrc is present", async () => {
    mockFsPresent(["pubspec.yaml", ".fvmrc", ".gitignore"]);
    execAsyncMock.mockImplementation((cmd: string) => {
      if (isCodegenDetectionCommand(cmd)) {
        return Promise.resolve({ stdout: "lib/db.dart\n", stderr: "" });
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    });

    await manager.create(42, "feat/42-fvm-test", {
      _adapterResolver: () => "claude",
    });

    const pubGet = execAsyncMock.mock.calls.find(
      ([cmd]: [string]) => typeof cmd === "string" && cmd === "fvm flutter pub get"
    );
    const buildRunner = execAsyncMock.mock.calls.find(
      ([cmd]: [string]) =>
        typeof cmd === "string" &&
        cmd === "fvm flutter pub run build_runner build --delete-conflicting-outputs"
    );

    expect(pubGet).toBeDefined();
    expect(buildRunner).toBeDefined();
  });

  it("uses `fvm flutter` when .fvm/ directory is present", async () => {
    mockFsPresent(["pubspec.yaml", ".fvm", ".gitignore"]);
    execAsyncMock.mockImplementation((cmd: string) => {
      if (isCodegenDetectionCommand(cmd)) {
        return Promise.resolve({ stdout: "lib/db.dart\n", stderr: "" });
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    });

    await manager.create(42, "feat/42-fvm-dir-test", {
      _adapterResolver: () => "claude",
    });

    expect(
      execAsyncMock.mock.calls.some(
        ([cmd]: [string]) => typeof cmd === "string" && cmd === "fvm flutter pub get"
      )
    ).toBe(true);
  });

  it("skips codegen when pubspec.yaml is present but no .dart files reference .g.dart", async () => {
    mockFsPresent(["pubspec.yaml", ".gitignore"]);
    // grep returns empty → no codegen needed
    execAsyncMock.mockImplementation((cmd: string) => {
      if (isCodegenDetectionCommand(cmd)) {
        return Promise.resolve({ stdout: "", stderr: "" });
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    });

    await manager.create(42, "feat/42-no-codegen", {
      _adapterResolver: () => "claude",
    });

    expect(flutterCalls()).toHaveLength(0);
  });

  it("does not run flutter commands when pubspec.yaml is absent (non-Flutter project)", async () => {
    // Only .gitignore exists — no pubspec.yaml, no package.json
    mockFsPresent([".gitignore"]);

    await manager.create(42, "feat/42-plain", {
      _adapterResolver: () => "claude",
    });

    // No flutter commands issued at all, and grep should not run either
    expect(flutterCalls()).toHaveLength(0);
    expect(execAsyncMock.mock.calls.some(([cmd]: [string]) => isCodegenDetectionCommand(cmd))).toBe(
      false
    );
  });

  it("logs a warning and does NOT throw when `flutter pub run build_runner build` fails", async () => {
    mockFsPresent(["pubspec.yaml", ".gitignore"]);
    execAsyncMock.mockImplementation((cmd: string) => {
      if (isCodegenDetectionCommand(cmd)) {
        return Promise.resolve({ stdout: "lib/db.dart\n", stderr: "" });
      }
      if (typeof cmd === "string" && cmd.includes("build_runner")) {
        return Promise.reject(new Error("build_runner: target pattern failed"));
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    });

    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

    const result = await manager.create(42, "feat/42-codegen-fails", {
      _adapterResolver: () => "claude",
    });

    expect(result.path).toBe("/repo/.worktrees/issue-42");
    expect(result.exists).toBe(true);
    expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining("Flutter codegen failed"));

    warnSpy.mockRestore();
  });

  it("logs a warning and does NOT throw when `flutter pub get` itself fails (flutter not installed)", async () => {
    mockFsPresent(["pubspec.yaml", ".gitignore"]);
    execAsyncMock.mockImplementation((cmd: string) => {
      if (isCodegenDetectionCommand(cmd)) {
        return Promise.resolve({ stdout: "lib/db.dart\n", stderr: "" });
      }
      if (typeof cmd === "string" && cmd === "flutter pub get") {
        return Promise.reject(new Error("flutter: command not found"));
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    });

    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

    await expect(
      manager.create(42, "feat/42-no-flutter", {
        _adapterResolver: () => "claude",
      })
    ).resolves.toBeDefined();

    expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining("Flutter codegen failed"));

    warnSpy.mockRestore();
  });

  it("skips Flutter codegen entirely when npmInstall is false", async () => {
    mockFsPresent(["pubspec.yaml", ".gitignore"]);
    execAsyncMock.mockImplementation((cmd: string) => {
      if (isCodegenDetectionCommand(cmd)) {
        return Promise.resolve({ stdout: "lib/db.dart\n", stderr: "" });
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    });

    await manager.create(42, "feat/42-no-install", {
      npmInstall: false,
      _adapterResolver: () => "claude",
    });

    expect(flutterCalls()).toHaveLength(0);
  });
});
