/**
 * Regression test for #901 — the extension warned "project config incomplete
 * — missing project.number" on a repository that had never been initialized.
 *
 * Found walking the clean-install gate: a fresh user opens a repo with no
 * Nightgauge state, the welcome view invites them to click **Initialize
 * Repository**, and simultaneously a warning notification tells them their
 * config is incomplete. There is no config yet — that is what the button is
 * for — and the README promises "nothing is written until you opt in".
 *
 * The distinction the fix has to hold is "config absent" (expected, silent)
 * vs "config present but missing keys" (a real misconfiguration, warn). Both
 * are asserted below against a real filesystem, so the check is exercised
 * rather than mocked.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as os from "node:os";
import * as path from "node:path";
import * as fs from "node:fs/promises";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";

const mockConfigGetProjectConfig = vi.fn();

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      boardList: vi.fn(),
      boardCounts: vi.fn(),
      configGetProjectConfig: mockConfigGetProjectConfig,
      githubRateLimit: vi.fn(),
    }),
  },
}));

// `vi.hoisted` because the vscode factory below is hoisted above this file's
// top-level statements — see docs/TESTING.md.
const { showWarningMessage } = vi.hoisted(() => ({ showWarningMessage: vi.fn() }));

vi.mock("vscode", () => ({
  EventEmitter: class {
    event = () => ({ dispose: () => {} });
    fire() {}
    dispose() {}
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage,
  },
  Disposable: class {
    dispose() {}
  },
}));

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getGitHubUser: vi.fn().mockReturnValue("test-user"),
}));

describe("ProjectBoardService — incomplete-config warning (#901)", () => {
  let tmpRoot: string;

  beforeEach(async () => {
    vi.clearAllMocks();
    tmpRoot = await fs.mkdtemp(path.join(os.tmpdir(), "ng-901-"));
    // Config the pipeline cannot use: no owner, no project number.
    mockConfigGetProjectConfig.mockResolvedValue({
      owner: "",
      defaultRepo: "",
      projectNumber: 0,
    });
  });

  afterEach(async () => {
    await fs.rm(tmpRoot, { recursive: true, force: true });
  });

  it("stays silent when the repo has never been initialized", async () => {
    // No .nightgauge/config.yaml — the state every first-time user starts in.
    await new ProjectBoardService(tmpRoot).loadConfig();

    expect(showWarningMessage).not.toHaveBeenCalled();
  });

  it("still warns when config.yaml exists but is missing keys", async () => {
    await fs.mkdir(path.join(tmpRoot, ".nightgauge"), { recursive: true });
    await fs.writeFile(path.join(tmpRoot, ".nightgauge", "config.yaml"), "version: 2\n");

    await new ProjectBoardService(tmpRoot).loadConfig();

    expect(showWarningMessage).toHaveBeenCalledTimes(1);
    expect(showWarningMessage.mock.calls[0][0]).toContain("project config incomplete");
  });
});
