import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globals: true,
    environment: "node",
    // The full hosted-runner suite exercises nearly 11,000 orchestration tests.
    // Individual async tests can exceed Vitest's 5-second default under shared
    // runner load even when their assertions are healthy; retain a bounded
    // timeout while avoiding false failures and cascading shared-state errors.
    testTimeout: 15_000,
    include: ["tests/**/*.test.ts"],
    exclude: ["tests/playwright/**", "tests/e2e-playwright/**"],
    setupFiles: ["tests/setup.ts"],
    coverage: {
      provider: "v8",
      reporter: ["text", "json", "html"],
      include: ["src/**/*.ts"],
      exclude: [
        // VSCode API entry point - requires heavy mocking with no coverage value
        "src/extension.ts",
        // HTML template generators - pure string builders with no logic to test
        "src/views/**/*Html.ts",
      ],
      // Baseline thresholds to prevent coverage regression
      thresholds: {
        lines: 50,
        functions: 50,
      },
    },
  },
});
