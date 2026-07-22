import { describe, it, expect } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import {
  type PreflightCommandRunner,
  CodexPreflightError,
  runAdapterPreflightChecks,
  runCodexPreflightChecks,
} from "../../src/cli/codexPreflight.js";
import { isAgenticAdapter } from "../../src/cli/adapters/AdapterRegistry.js";
import { AdapterError } from "../../src/cli/adapters/errors.js";
import { isCodexAdapterEnabled } from "../../src/cli/adapter.js";

function createRunner(
  responses: Record<string, { code: number; stdout?: string; stderr?: string }>
): PreflightCommandRunner {
  return async (command, args) => {
    const key = `${command} ${args.join(" ")}`;
    const response = responses[key];
    if (!response) {
      return { code: 1, stdout: "", stderr: `unexpected command: ${key}` };
    }
    return {
      code: response.code,
      stdout: response.stdout ?? "",
      stderr: response.stderr ?? "",
    };
  };
}

async function createTempWorkspace(): Promise<string> {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "codex-preflight-"));
  await fs.mkdir(path.join(dir, "docs"), { recursive: true });
  await fs.mkdir(path.join(dir, "standards"), { recursive: true });
  await fs.writeFile(path.join(dir, "docs/README.md"), "# Docs\n", "utf-8");
  await fs.writeFile(path.join(dir, "standards/security.md"), "# Security\n", "utf-8");
  return dir;
}

/** Standard codex --version response used by most tests. */
const CODEX_VERSION_OK = { code: 0, stdout: "codex 0.112.0\n" };

describe("codexPreflight", () => {
  it("should pass all checks when auth, branch, and docs are valid", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 0 },
      "gh auth status": { code: 0 },
      "gh api rate_limit": { code: 0 },
      "git branch --show-current": { code: 0, stdout: "feat/553-tests\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    const result = await runCodexPreflightChecks({ cwd, runner });
    expect(result.githubAuth).toBe("passed");
    expect(result.branchState).toBe("passed");
    expect(result.docsPreconditions).toBe("passed");
  });

  it("fails model-validation when NIGHTGAUGE_CODEX_MODEL is invalid (#4021)", async () => {
    const cwd = await createTempWorkspace();
    // Model validation runs after branch/docs but before auth, so only git
    // commands are needed — auth/version are never reached.
    const runner = createRunner({
      "git branch --show-current": { code: 0, stdout: "feat/4021-test\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    await expect(
      runCodexPreflightChecks({
        cwd,
        runner,
        env: { NIGHTGAUGE_CODEX_MODEL: "gpt-5.4-typo" } as NodeJS.ProcessEnv,
      })
    ).rejects.toMatchObject({
      name: "CodexPreflightError",
      check: "model-validation",
    });
  });

  it("passes model-validation for a valid model id and a tier alias (#4021)", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 0 },
      "gh auth status": { code: 0 },
      "gh api rate_limit": { code: 0 },
      "git branch --show-current": { code: 0, stdout: "feat/4021-ok\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    for (const model of ["gpt-5.5", "fable"]) {
      const result = await runCodexPreflightChecks({
        cwd,
        runner,
        env: {
          NIGHTGAUGE_CODEX_MODEL: model,
          GH_TOKEN: "test-token",
        } as NodeJS.ProcessEnv,
      });
      expect(result.adapterAuth).toBe("passed");
    }
  });

  it("should fail when codex login status returns non-zero (Issue #628)", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 1, stderr: "not logged in" },
      "git branch --show-current": { code: 0, stdout: "feat/628-test\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    await expect(runCodexPreflightChecks({ cwd, runner })).rejects.toThrow(AdapterError);
  });

  it("should fail when codex CLI is not installed (Issue #628)", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": { code: 1, stderr: "command not found" },
      "git branch --show-current": { code: 0, stdout: "feat/628-test\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    await expect(runCodexPreflightChecks({ cwd, runner })).rejects.toThrow(
      /not installed or not in PATH/
    );
  });

  it("should allow missing gh auth for non-github stages", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 0 },
      "git branch --show-current": { code: 0, stdout: "feat/dev\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    const result = await runCodexPreflightChecks({
      cwd,
      runner,
      stage: "feature-dev",
    });

    expect(result.adapterAuth).toBe("passed");
    expect(result.githubAuth).toBe("passed");
  });

  it("should require gh auth for github-dependent stages", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 0 },
      "gh auth status": { code: 1, stderr: "not logged in" },
      "git branch --show-current": { code: 0, stdout: "feat/pr\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    await expect(
      runCodexPreflightChecks({
        cwd,
        runner,
        stage: "pr-create",
        env: {},
      })
    ).rejects.toThrow(/GitHub auth is unavailable in this execution environment/);
  });

  it("should fail when gh auth exists but GitHub API is unreachable", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 0 },
      "gh auth status": { code: 0 },
      "gh api rate_limit": {
        code: 1,
        stderr: "error connecting to api.github.com",
      },
      "git branch --show-current": { code: 0, stdout: "feat/pr\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    await expect(
      runCodexPreflightChecks({
        cwd,
        runner,
        stage: "pr-create",
        env: {},
      })
    ).rejects.toThrow(/GitHub API is unreachable/);
  });

  it("should allow token-based auth for github-dependent stages", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 0 },
      "git branch --show-current": { code: 0, stdout: "feat/token-auth\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    const result = await runCodexPreflightChecks({
      cwd,
      runner,
      stage: "pr-create",
      env: { GH_TOKEN: "test-token" },
    });

    expect(result.adapterAuth).toBe("passed");
    expect(result.githubAuth).toBe("passed");
  });

  it("should fail for invalid branch state", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 0 },
      "gh auth status": { code: 0 },
      "gh api rate_limit": { code: 0 },
      "git branch --show-current": { code: 0, stdout: "main\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    await expect(runCodexPreflightChecks({ cwd, runner })).rejects.toThrow(/feature branch/);
  });

  it("should allow main branch for issue-pickup stage", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 0 },
      "gh auth status": { code: 0 },
      "gh api rate_limit": { code: 0 },
      "git branch --show-current": { code: 0, stdout: "main\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    const result = await runCodexPreflightChecks({
      cwd,
      runner,
      stage: "issue-pickup",
    });

    expect(result.branchState).toBe("passed");
    expect(result.docsPreconditions).toBe("passed");
  });

  it("should still fail on main branch for non-issue-pickup stage", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 0 },
      "gh auth status": { code: 0 },
      "git branch --show-current": { code: 0, stdout: "main\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    await expect(
      runCodexPreflightChecks({
        cwd,
        runner,
        stage: "feature-dev",
      })
    ).rejects.toThrow(/feature branch/);
  });

  it("should fail for dirty working tree on issue-pickup stage", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 0 },
      "gh auth status": { code: 0 },
      "git branch --show-current": { code: 0, stdout: "feat/work\n" },
      "git status --porcelain": { code: 0, stdout: " M README.md\n" },
    });

    await expect(runCodexPreflightChecks({ cwd, runner, stage: "issue-pickup" })).rejects.toThrow(
      /working tree must be clean/
    );
  });

  it("should allow dirty working tree for non-issue-pickup stage", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 0 },
      "git branch --show-current": { code: 0, stdout: "feat/work\n" },
      "git status --porcelain": { code: 0, stdout: " M README.md\n" },
    });

    const result = await runCodexPreflightChecks({
      cwd,
      runner,
      stage: "feature-dev",
    });

    expect(result.branchState).toBe("passed");
    expect(result.docsPreconditions).toBe("passed");
  });

  it("should fail when documentation prerequisites are missing", async () => {
    const cwd = await createTempWorkspace();
    await fs.rm(path.join(cwd, "docs/README.md"));
    const runner = createRunner({
      "codex --version": CODEX_VERSION_OK,
      "codex login status": { code: 0 },
      "gh auth status": { code: 0 },
      "git branch --show-current": { code: 0, stdout: "feat/docs\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    await expect(runCodexPreflightChecks({ cwd, runner })).rejects.toThrow(
      /missing required documentation prerequisites/
    );
  });

  it("should detect codex adapter mode from env", () => {
    expect(isCodexAdapterEnabled({ NIGHTGAUGE_ADAPTER: "codex" })).toBe(true);
    expect(isCodexAdapterEnabled({ NIGHTGAUGE_ADAPTER: "Codex" })).toBe(true);
    expect(isCodexAdapterEnabled({ NIGHTGAUGE_ADAPTER: "claude" })).toBe(false);
    expect(isCodexAdapterEnabled({})).toBe(false);
  });

  it("should validate claude-headless adapter auth without provider API keys", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "claude --version": { code: 0, stdout: "claude 2.1.38\n" },
      "claude auth status": { code: 0 },
      "git branch --show-current": { code: 0, stdout: "feat/585-adapter\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    const result = await runAdapterPreflightChecks({
      adapter: "claude-headless",
      cwd,
      runner,
    });

    expect(result.adapterAuth).toBe("passed");
    expect(result.branchState).toBe("passed");
    expect(result.docsPreconditions).toBe("passed");
  });

  it("should fail when claude CLI is not installed (Issue #626)", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "claude --version": { code: 1, stderr: "command not found" },
      "git branch --show-current": { code: 0, stdout: "feat/626-test\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    await expect(
      runAdapterPreflightChecks({
        adapter: "claude-headless",
        cwd,
        runner,
      })
    ).rejects.toThrow(/not installed or not in PATH/);
  });

  it("should fail when claude auth status returns non-zero (Issue #626)", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "claude --version": { code: 0, stdout: "claude 2.1.38\n" },
      "claude auth status": { code: 1, stderr: "not authenticated" },
      "git branch --show-current": { code: 0, stdout: "feat/626-test\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    await expect(
      runAdapterPreflightChecks({
        adapter: "claude-headless",
        cwd,
        runner,
      })
    ).rejects.toThrow(/not authenticated.*Run `claude auth login`/);
  });

  it("should validate gemini adapter preflight when CLI is available (Issue #629)", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "gemini --version": { code: 0, stdout: "gemini 0.2.0\n" },
      "gcloud auth print-access-token": { code: 0, stdout: "ya29.fake\n" },
      "git branch --show-current": { code: 0, stdout: "feat/629-gemini\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    const result = await runAdapterPreflightChecks({
      adapter: "gemini",
      cwd,
      runner,
    });

    expect(result.adapterAuth).toBe("passed");
    expect(result.branchState).toBe("passed");
    expect(result.docsPreconditions).toBe("passed");
  });

  it("should fail gemini preflight when CLI is not installed (Issue #629)", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "gemini --version": { code: 1, stderr: "command not found" },
      "git branch --show-current": { code: 0, stdout: "feat/629-test\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    await expect(
      runAdapterPreflightChecks({
        adapter: "gemini",
        cwd,
        runner,
      })
    ).rejects.toThrow(/not installed or not in PATH/);
  });

  it("should not require GitHub auth for gemini adapter (Issue #629)", async () => {
    const cwd = await createTempWorkspace();
    const runner = createRunner({
      "gemini --version": { code: 0, stdout: "gemini 0.2.0\n" },
      "gcloud auth print-access-token": { code: 0, stdout: "ya29.fake\n" },
      "git branch --show-current": { code: 0, stdout: "feat/629-no-gh\n" },
      "git status --porcelain": { code: 0, stdout: "" },
    });

    // Should pass without any gh auth commands in runner
    const result = await runAdapterPreflightChecks({
      adapter: "gemini",
      cwd,
      runner,
      stage: "pr-create",
    });

    expect(result.adapterAuth).toBe("passed");
    expect(result.githubAuth).toBeUndefined();
  });
});

describe("agentic truth-gate (#57)", () => {
  it("rejects chat-completion-only adapters before any other check", async () => {
    for (const adapter of ["ollama", "lm-studio", "gemini-sdk"] as const) {
      // No runner/cwd needed: the gate fires before branch/docs/model checks.
      await expect(runAdapterPreflightChecks({ adapter })).rejects.toThrow(/chat-completion-only/);
    }
  });

  it("names remediation adapters in the rejection", async () => {
    await expect(runAdapterPreflightChecks({ adapter: "ollama" })).rejects.toThrow(
      /claude-sdk, claude-headless, codex, gemini, copilot/
    );
  });
});

describe("isAgenticAdapter (#57)", () => {
  it("declares the tool-loop truth per adapter, with the vscode claude alias", () => {
    for (const agentic of [
      "claude",
      "claude-sdk",
      "claude-headless",
      "codex",
      "gemini",
      "copilot",
    ]) {
      expect(isAgenticAdapter(agentic), agentic).toBe(true);
    }
    for (const chatOnly of ["gemini-sdk", "ollama", "lm-studio"]) {
      expect(isAgenticAdapter(chatOnly), chatOnly).toBe(false);
    }
  });

  it("fails closed on unknown adapter names", () => {
    expect(isAgenticAdapter("mystery-adapter")).toBe(false);
  });
});
