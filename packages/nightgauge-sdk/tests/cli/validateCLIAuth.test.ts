/**
 * validateCLIAuth Unit Tests - Direct tests for the shared auth validation
 * utilities used by all CLI-based adapters.
 *
 * @see Issue #2275 - Add CLI adapter tests for multi-tool support validation
 */

import { describe, it, expect } from "vitest";
import { validateCLIAuth, verifyCLIInstalled } from "../../src/cli/adapters/validateCLIAuth.js";
import { AdapterError } from "../../src/cli/adapters/errors.js";
import { type PreflightCommandRunner } from "../../src/cli/codexPreflight.js";

// ---------------------------------------------------------------------------
// Helper: create a deterministic mock command runner
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// verifyCLIInstalled
// ---------------------------------------------------------------------------

describe("verifyCLIInstalled", () => {
  it("should resolve when --version returns exit code 0", async () => {
    const runner = createRunner({
      "mycli --version": { code: 0, stdout: "mycli 1.2.3\n" },
    });

    const result = await verifyCLIInstalled({
      command: "mycli",
      runner,
      cwd: "/tmp",
      adapterName: "MyAdapter",
      installCmd: "install mycli",
    });

    expect(result.code).toBe(0);
    expect(result.stdout).toContain("1.2.3");
  });

  it("should throw AdapterError when --version returns non-zero exit code", async () => {
    const runner = createRunner({
      "mycli --version": { code: 127, stderr: "command not found" },
    });

    await expect(
      verifyCLIInstalled({
        command: "mycli",
        runner,
        cwd: "/tmp",
        adapterName: "MyAdapter",
        installCmd: "install mycli",
      })
    ).rejects.toThrow(AdapterError);

    await expect(
      verifyCLIInstalled({
        command: "mycli",
        runner,
        cwd: "/tmp",
        adapterName: "MyAdapter",
        installCmd: "install mycli",
      })
    ).rejects.toThrow(/not installed or not in PATH/);
  });

  it("should throw AdapterError when runner throws (binary missing)", async () => {
    const runner: PreflightCommandRunner = async () => {
      throw new Error("ENOENT: spawn mycli");
    };

    await expect(
      verifyCLIInstalled({
        command: "mycli",
        runner,
        cwd: "/tmp",
        adapterName: "MyAdapter",
        installCmd: "install mycli",
      })
    ).rejects.toThrow(AdapterError);

    await expect(
      verifyCLIInstalled({
        command: "mycli",
        runner,
        cwd: "/tmp",
        adapterName: "MyAdapter",
        installCmd: "install mycli",
      })
    ).rejects.toThrow(/not installed or not in PATH/);
  });

  it("should include the command name in the error message", async () => {
    const runner = createRunner({
      "some-tool --version": { code: 1 },
    });

    try {
      await verifyCLIInstalled({
        command: "some-tool",
        runner,
        cwd: "/tmp",
        adapterName: "MyAdapter",
        installCmd: "install some-tool",
      });
      expect.fail("should have thrown");
    } catch (err) {
      expect((err as Error).message).toContain("some-tool");
    }
  });
});

// ---------------------------------------------------------------------------
// validateCLIAuth
// ---------------------------------------------------------------------------

describe("validateCLIAuth", () => {
  it('should return "passed" when the first auth subcommand succeeds', async () => {
    const runner = createRunner({
      "mycli auth status": { code: 0, stdout: "Authenticated\n" },
    });

    const result = await validateCLIAuth({
      command: "mycli",
      authSubcommands: [{ args: ["auth", "status"] }],
      runner,
      cwd: "/tmp",
      adapterName: "MyAdapter",
      loginHint: "mycli auth login",
    });

    expect(result).toBe("passed");
  });

  it("should try subsequent subcommands if the first one fails", async () => {
    const runner = createRunner({
      "mycli auth status": { code: 1, stderr: "not authenticated" },
      "mycli login status": { code: 0, stdout: "Logged in\n" },
    });

    const result = await validateCLIAuth({
      command: "mycli",
      authSubcommands: [{ args: ["auth", "status"] }, { args: ["login", "status"] }],
      runner,
      cwd: "/tmp",
      adapterName: "MyAdapter",
      loginHint: "mycli auth login",
    });

    expect(result).toBe("passed");
  });

  it("should throw AdapterError when all subcommands fail", async () => {
    const runner = createRunner({
      "mycli auth status": { code: 1, stderr: "not authenticated" },
      "mycli login status": { code: 1, stderr: "not logged in" },
    });

    await expect(
      validateCLIAuth({
        command: "mycli",
        authSubcommands: [{ args: ["auth", "status"] }, { args: ["login", "status"] }],
        runner,
        cwd: "/tmp",
        adapterName: "MyAdapter",
        loginHint: "mycli auth login",
      })
    ).rejects.toThrow(AdapterError);
  });

  it("should include the loginHint in the error message", async () => {
    const runner = createRunner({
      "mycli auth status": { code: 1, stderr: "not authenticated" },
    });

    try {
      await validateCLIAuth({
        command: "mycli",
        authSubcommands: [{ args: ["auth", "status"] }],
        runner,
        cwd: "/tmp",
        adapterName: "MyAdapter",
        loginHint: "mycli auth login",
      });
      expect.fail("should have thrown");
    } catch (err) {
      expect((err as Error).message).toContain("mycli auth login");
    }
  });

  it("should include the last error details in the error message", async () => {
    const runner = createRunner({
      "mycli auth status": { code: 1, stderr: "specific error detail" },
    });

    try {
      await validateCLIAuth({
        command: "mycli",
        authSubcommands: [{ args: ["auth", "status"] }],
        runner,
        cwd: "/tmp",
        adapterName: "MyAdapter",
        loginHint: "mycli auth login",
      });
      expect.fail("should have thrown");
    } catch (err) {
      expect((err as Error).message).toContain("specific error detail");
    }
  });

  it("should handle empty authSubcommands array by throwing", async () => {
    const runner = createRunner({});

    await expect(
      validateCLIAuth({
        command: "mycli",
        authSubcommands: [],
        runner,
        cwd: "/tmp",
        adapterName: "MyAdapter",
        loginHint: "mycli auth login",
      })
    ).rejects.toThrow(AdapterError);
  });

  it("should omit details when last error output is empty", async () => {
    const runner = createRunner({
      "mycli auth status": { code: 1, stdout: "", stderr: "" },
    });

    try {
      await validateCLIAuth({
        command: "mycli",
        authSubcommands: [{ args: ["auth", "status"] }],
        runner,
        cwd: "/tmp",
        adapterName: "MyAdapter",
        loginHint: "mycli login",
      });
      expect.fail("should have thrown");
    } catch (err) {
      expect((err as Error).message).not.toContain("Details:");
    }
  });

  it('should set error category to "AUTH_MISSING"', async () => {
    const runner = createRunner({
      "mycli auth status": { code: 1, stderr: "fail" },
    });

    try {
      await validateCLIAuth({
        command: "mycli",
        authSubcommands: [{ args: ["auth", "status"] }],
        runner,
        cwd: "/tmp",
        adapterName: "MyAdapter",
        loginHint: "mycli login",
      });
      expect.fail("should have thrown");
    } catch (err) {
      expect(err).toBeInstanceOf(AdapterError);
      expect((err as AdapterError).category).toBe("AUTH_MISSING");
    }
  });
});

// ---------------------------------------------------------------------------
// Multi-tool auth validation (simulating different backends)
// ---------------------------------------------------------------------------

describe("multi-tool auth validation", () => {
  const backends = [
    {
      label: "Claude Headless",
      command: "claude",
      authSubcommands: [{ args: ["auth", "status"] }],
      loginHint: "claude auth login",
    },
    {
      label: "Codex",
      command: "codex",
      authSubcommands: [{ args: ["login", "status"] }],
      loginHint: "codex login",
    },
    {
      label: "Copilot",
      command: "copilot",
      authSubcommands: [{ args: ["auth", "status"] }],
      loginHint: "gh auth login",
    },
  ];

  it.each(backends)(
    "$label: should pass when auth subcommand returns exit code 0",
    async ({ command, authSubcommands, loginHint }) => {
      const key = `${command} ${authSubcommands[0].args.join(" ")}`;
      const runner = createRunner({
        [key]: { code: 0, stdout: "Authenticated\n" },
      });

      const result = await validateCLIAuth({
        command,
        authSubcommands,
        runner,
        cwd: "/tmp",
        adapterName: `${command} adapter`,
        loginHint,
      });

      expect(result).toBe("passed");
    }
  );

  it.each(backends)(
    "$label: should throw when auth subcommand fails",
    async ({ command, authSubcommands, loginHint }) => {
      const key = `${command} ${authSubcommands[0].args.join(" ")}`;
      const runner = createRunner({
        [key]: { code: 1, stderr: "not authenticated" },
      });

      await expect(
        validateCLIAuth({
          command,
          authSubcommands,
          runner,
          cwd: "/tmp",
          adapterName: `${command} adapter`,
          loginHint,
        })
      ).rejects.toThrow(AdapterError);
    }
  );
});
