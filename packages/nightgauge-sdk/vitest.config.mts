import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globals: true,
    environment: "node",
    include: ["tests/**/*.test.ts", "src/__tests__/**/*.test.ts", "src/**/__tests__/**/*.test.ts"],
    coverage: {
      provider: "v8",
      reporter: ["text", "json", "html"],
      include: ["src/**/*.ts"],
      exclude: ["src/index.ts"],
      // Baseline thresholds to prevent coverage regression
      thresholds: {
        lines: 50,
        functions: 50,
      },
    },
  },
});
