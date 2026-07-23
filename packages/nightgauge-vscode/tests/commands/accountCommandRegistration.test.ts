import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("vscode", () => ({
  commands: {
    registerCommand: vi.fn((_id: string, _handler: unknown) => ({
      dispose: vi.fn(),
    })),
  },
  window: {
    showQuickPick: vi.fn(),
    showErrorMessage: vi.fn(),
    showInformationMessage: vi.fn(),
  },
  Disposable: { from: vi.fn() },
  TreeItem: class {},
  EventEmitter: class {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
}));

import * as vscode from "vscode";
import { registerAccountCommands } from "../../src/commands/accountCommands";
import type { OAuthDeviceFlowService } from "../../src/services/OAuthDeviceFlowService";
import type { GitHubAuthService } from "../../src/services/GitHubAuthService";
import type { TrialStateStore } from "../../src/platform/TrialState";
import type { Logger } from "../../src/utils/logger";

describe("registerAccountCommands", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("always registers every contributed account command", () => {
    const oauth = {
      startDeviceFlow: vi.fn(),
      signOut: vi.fn(),
    } as unknown as OAuthDeviceFlowService;
    const github = {
      signInWithGitHub: vi.fn(),
    } as unknown as GitHubAuthService;
    const logger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
    const trialStore = { clear: vi.fn() } as unknown as TrialStateStore;

    const disposables = registerAccountCommands(oauth, github, logger, trialStore);

    expect(disposables).toHaveLength(3);
    expect(vi.mocked(vscode.commands.registerCommand).mock.calls.map(([id]) => id)).toEqual([
      "nightgauge.signIn",
      "nightgauge.signOut",
      "nightgauge.signInWithGitHub",
    ]);
  });
});
