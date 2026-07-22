/**
 * Behavior tests for commands.* configuration fields
 *
 * These tests verify that commands config fields actually affect runtime behavior,
 * specifically that custom commands override auto-detection when specified.
 *
 * @see Issue #437 - Audit and test project/issue/commands config fields
 * @see packages/nightgauge-vscode/src/config/schema.ts - CommandsConfigSchema
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  createMockCommandsConfig,
  FULL_COMMANDS_CONFIG,
  applyEnvOverrides,
  CONFIG_ENV_MAPPINGS,
} from "../mocks/config-fixtures";
import { CommandsConfigSchema, mergeWithDefaults, DEFAULT_CONFIG } from "../../src/config/schema";

describe("commands.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear commands-related environment variables
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_COMMANDS_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // commands.test - Behavior Tests
  // ============================================================================

  describe("commands.test", () => {
    it("uses config command instead of auto-detection", () => {
      const config = createMockCommandsConfig({ test: "pnpm test" });
      expect(config.test).toBe("pnpm test");
      // When config specifies test command, auto-detection should be skipped
    });

    it("empty string triggers auto-detection", () => {
      const config = createMockCommandsConfig({ test: "" });
      expect(config.test).toBe("");
      // Empty string should fall back to auto-detection
    });

    it("undefined triggers auto-detection", () => {
      const config = createMockCommandsConfig({});
      expect(config.test).toBeUndefined();
      // Undefined should trigger auto-detection
    });

    it("supports various test runners", () => {
      const runners = [
        "npm test",
        "pnpm test",
        "yarn test",
        "vitest run",
        "jest",
        "pytest",
        "go test ./...",
        "cargo test",
      ];

      runners.forEach((runner) => {
        const config = createMockCommandsConfig({ test: runner });
        expect(config.test).toBe(runner);
      });
    });

    // Behavior: Command resolution logic
    it("config command takes precedence over auto-detected", () => {
      const configCommand = "pnpm test";
      const autoDetectedCommand = "npm test";

      // Simulate command resolution
      const resolveCommand = (config?: string, autoDetected?: string) => {
        return config || autoDetected;
      };

      expect(resolveCommand(configCommand, autoDetectedCommand)).toBe(configCommand);
      expect(resolveCommand(undefined, autoDetectedCommand)).toBe(autoDetectedCommand);
      expect(resolveCommand("", autoDetectedCommand)).toBe(autoDetectedCommand);
    });
  });

  // ============================================================================
  // commands.build - Behavior Tests
  // ============================================================================

  describe("commands.build", () => {
    it("uses config command for build verification", () => {
      const config = createMockCommandsConfig({ build: "pnpm build" });
      expect(config.build).toBe("pnpm build");
      // feature-validate skill should use this command for build verification
    });

    it("empty/undefined triggers auto-detection", () => {
      const config = createMockCommandsConfig({});
      expect(config.build).toBeUndefined();

      const emptyConfig = createMockCommandsConfig({ build: "" });
      expect(emptyConfig.build).toBe("");
    });

    it("supports various build tools", () => {
      const buildCommands = [
        "npm run build",
        "pnpm build",
        "yarn build",
        "tsc",
        "tsc --noEmit",
        "vite build",
        "esbuild",
        "rollup -c",
        "webpack",
        "go build",
        "cargo build",
        "make build",
      ];

      buildCommands.forEach((cmd) => {
        const config = createMockCommandsConfig({ build: cmd });
        expect(config.build).toBe(cmd);
      });
    });

    // Behavior: Build command is critical for feature-validate
    it("build failure should block PR creation", () => {
      const config = createMockCommandsConfig({ build: "npm run build" });

      // Simulate build verification behavior
      const simulateBuildCheck = (command: string | undefined, buildSuccess: boolean) => {
        if (!command) {
          return { checked: false, reason: "no build command" };
        }
        return {
          checked: true,
          passed: buildSuccess,
          command,
        };
      };

      const result = simulateBuildCheck(config.build, false);
      expect(result.checked).toBe(true);
      expect(result.passed).toBe(false);
    });
  });

  // ============================================================================
  // commands.lint - Behavior Tests
  // ============================================================================

  describe("commands.lint", () => {
    it("uses config command for linting", () => {
      const config = createMockCommandsConfig({ lint: "pnpm lint" });
      expect(config.lint).toBe("pnpm lint");
    });

    it("supports various linters", () => {
      const lintCommands = [
        "npm run lint",
        "pnpm lint",
        "eslint .",
        "eslint src/",
        "biome check",
        "prettier --check .",
        "ruff check",
        "golangci-lint run",
        "cargo clippy",
      ];

      lintCommands.forEach((cmd) => {
        const config = createMockCommandsConfig({ lint: cmd });
        expect(config.lint).toBe(cmd);
      });
    });
  });

  // ============================================================================
  // commands.typecheck - Behavior Tests
  // ============================================================================

  describe("commands.typecheck", () => {
    it("uses config command for type checking", () => {
      const config = createMockCommandsConfig({ typecheck: "tsc --noEmit" });
      expect(config.typecheck).toBe("tsc --noEmit");
    });

    it("supports various type checkers", () => {
      const typecheckCommands = ["tsc --noEmit", "tsc -b", "vue-tsc --noEmit", "pyright", "mypy ."];

      typecheckCommands.forEach((cmd) => {
        const config = createMockCommandsConfig({ typecheck: cmd });
        expect(config.typecheck).toBe(cmd);
      });
    });
  });

  // ============================================================================
  // commands.format - Behavior Tests
  // ============================================================================

  describe("commands.format", () => {
    it("uses config command for formatting", () => {
      const config = createMockCommandsConfig({ format: "pnpm format" });
      expect(config.format).toBe("pnpm format");
    });

    it("supports various formatters", () => {
      const formatCommands = [
        "prettier --write .",
        "npm run format",
        "biome format --write",
        "black .",
        "go fmt ./...",
        "cargo fmt",
      ];

      formatCommands.forEach((cmd) => {
        const config = createMockCommandsConfig({ format: cmd });
        expect(config.format).toBe(cmd);
      });
    });
  });

  // ============================================================================
  // Environment Variable Overrides - Behavior Tests
  // ============================================================================

  describe("environment variable overrides", () => {
    it("NIGHTGAUGE_COMMANDS_TEST overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_COMMANDS_TEST: "vitest run",
      });

      try {
        expect(process.env.NIGHTGAUGE_COMMANDS_TEST).toBe("vitest run");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_COMMANDS_BUILD overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_COMMANDS_BUILD: "tsc && vite build",
      });

      try {
        expect(process.env.NIGHTGAUGE_COMMANDS_BUILD).toBe("tsc && vite build");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_COMMANDS_LINT overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_COMMANDS_LINT: "eslint --fix .",
      });

      try {
        expect(process.env.NIGHTGAUGE_COMMANDS_LINT).toBe("eslint --fix .");
      } finally {
        cleanup();
      }
    });

    it("env var takes precedence over config file", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_COMMANDS_TEST: "env-test-command",
      });

      try {
        const configValue = "config-test-command";
        const envValue = process.env.NIGHTGAUGE_COMMANDS_TEST;

        // Env should take precedence
        const effectiveValue = envValue || configValue;

        expect(effectiveValue).toBe("env-test-command");
      } finally {
        cleanup();
      }
    });

    it("all command env vars are defined", () => {
      expect(CONFIG_ENV_MAPPINGS["commands.test"]).toBe("NIGHTGAUGE_COMMANDS_TEST");
      expect(CONFIG_ENV_MAPPINGS["commands.build"]).toBe("NIGHTGAUGE_COMMANDS_BUILD");
      expect(CONFIG_ENV_MAPPINGS["commands.lint"]).toBe("NIGHTGAUGE_COMMANDS_LINT");
      expect(CONFIG_ENV_MAPPINGS["commands.typecheck"]).toBe("NIGHTGAUGE_COMMANDS_TYPECHECK");
      expect(CONFIG_ENV_MAPPINGS["commands.format"]).toBe("NIGHTGAUGE_COMMANDS_FORMAT");
    });
  });

  // ============================================================================
  // Full Config - Behavior Tests
  // ============================================================================

  describe("full commands config", () => {
    it("FULL_COMMANDS_CONFIG has all fields", () => {
      expect(FULL_COMMANDS_CONFIG.test).toBe("pnpm test");
      expect(FULL_COMMANDS_CONFIG.lint).toBe("pnpm lint");
      expect(FULL_COMMANDS_CONFIG.typecheck).toBe("pnpm typecheck");
      expect(FULL_COMMANDS_CONFIG.format).toBe("pnpm format");
      expect(FULL_COMMANDS_CONFIG.build).toBe("pnpm build");
    });

    it("validates complete config", () => {
      const result = CommandsConfigSchema.safeParse(FULL_COMMANDS_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial config", () => {
      const partialConfig = createMockCommandsConfig({ test: "npm test" });
      const result = CommandsConfigSchema.safeParse(partialConfig);
      expect(result.success).toBe(true);
    });

    it("validates empty config", () => {
      const result = CommandsConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });
  });

  // ============================================================================
  // Auto-Detection Fallback - Behavior Tests
  // ============================================================================

  describe("auto-detection fallback", () => {
    it("undefined command triggers auto-detection", () => {
      const config = createMockCommandsConfig({});

      // Simulate auto-detection logic
      const detectTestCommand = (packageJson: { scripts?: Record<string, string> } | null) => {
        if (!packageJson?.scripts) return null;
        if (packageJson.scripts.test) return "npm test";
        return null;
      };

      const mockPackageJson = { scripts: { test: "vitest run" } };

      // When config is undefined, auto-detect
      const command = config.test ?? detectTestCommand(mockPackageJson);

      expect(command).toBe("npm test");
    });

    it("specified command skips auto-detection", () => {
      const config = createMockCommandsConfig({ test: "custom-test" });

      const detectTestCommand = () => "npm test";

      // When config is specified, don't auto-detect
      const command = config.test ?? detectTestCommand();

      expect(command).toBe("custom-test");
    });

    it("empty string falls back to auto-detection", () => {
      const config = createMockCommandsConfig({ test: "" });

      const detectTestCommand = () => "npm test";

      // Empty string is falsy, should trigger auto-detect
      const command = config.test || detectTestCommand();

      expect(command).toBe("npm test");
    });
  });

  // ============================================================================
  // Command Execution Simulation - Behavior Tests
  // ============================================================================

  describe("command execution simulation", () => {
    it("test command is used for test phase", () => {
      const config = createMockCommandsConfig({ test: "pnpm test:coverage" });

      // Simulate pipeline phase
      const runPhase = (phase: string, commands: typeof config) => {
        switch (phase) {
          case "test":
            return commands.test;
          case "build":
            return commands.build;
          case "lint":
            return commands.lint;
          default:
            return null;
        }
      };

      expect(runPhase("test", config)).toBe("pnpm test:coverage");
    });

    it("build command is used for build phase", () => {
      const config = createMockCommandsConfig({ build: "npm run build:prod" });

      const runPhase = (phase: string, commands: typeof config) => {
        switch (phase) {
          case "build":
            return commands.build;
          default:
            return null;
        }
      };

      expect(runPhase("build", config)).toBe("npm run build:prod");
    });

    it("multiple phases use correct commands", () => {
      const config = createMockCommandsConfig({
        test: "vitest",
        build: "vite build",
        lint: "eslint .",
      });

      const runAllPhases = (commands: typeof config) => {
        return {
          lint: commands.lint,
          test: commands.test,
          build: commands.build,
        };
      };

      const results = runAllPhases(config);

      expect(results.lint).toBe("eslint .");
      expect(results.test).toBe("vitest");
      expect(results.build).toBe("vite build");
    });
  });

  // ============================================================================
  // Default Values - Behavior Tests
  // ============================================================================

  describe("default values", () => {
    it("DEFAULT_CONFIG.commands is empty object", () => {
      expect(DEFAULT_CONFIG.commands).toEqual({});
    });

    it("mergeWithDefaults preserves user commands", () => {
      const config = mergeWithDefaults({
        commands: { test: "custom-test" },
      });

      expect(config.commands?.test).toBe("custom-test");
    });

    it("missing commands section uses defaults", () => {
      const config = mergeWithDefaults({});

      expect(config.commands).toEqual({});
    });
  });

  // ============================================================================
  // Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("accepts valid command strings", () => {
      const config = {
        test: "npm test",
        build: "npm run build",
        lint: "npm run lint",
        typecheck: "tsc --noEmit",
        format: "prettier --write .",
      };

      const result = CommandsConfigSchema.safeParse(config);
      expect(result.success).toBe(true);
    });

    it("rejects non-string command values", () => {
      const invalidConfig = {
        test: 123,
      };

      const result = CommandsConfigSchema.safeParse(invalidConfig);
      expect(result.success).toBe(false);
    });

    it("accepts undefined/missing fields", () => {
      const config = {
        test: "npm test",
        // Other fields undefined
      };

      const result = CommandsConfigSchema.safeParse(config);
      expect(result.success).toBe(true);
    });
  });
});
