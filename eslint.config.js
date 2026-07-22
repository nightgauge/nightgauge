// @ts-check
import js from "@eslint/js";
import tsParser from "@typescript-eslint/parser";
import tsPlugin from "@typescript-eslint/eslint-plugin";

// Node.js built-in globals
const nodeGlobals = {
  process: "readonly",
  Buffer: "readonly",
  __dirname: "readonly",
  __filename: "readonly",
  require: "readonly",
  module: "writeable",
  exports: "writeable",
  global: "readonly",
  console: "readonly",
  setTimeout: "readonly",
  clearTimeout: "readonly",
  setInterval: "readonly",
  clearInterval: "readonly",
  setImmediate: "readonly",
  clearImmediate: "readonly",
  URL: "readonly",
  URLSearchParams: "readonly",
  TextEncoder: "readonly",
  TextDecoder: "readonly",
  fetch: "readonly",
  Response: "readonly",
  Request: "readonly",
  Headers: "readonly",
  AbortController: "readonly",
  AbortSignal: "readonly",
  NodeJS: "readonly",
  queueMicrotask: "readonly",
  structuredClone: "readonly",
  crypto: "readonly",
  performance: "readonly",
};

// Vitest/Jest test globals
const testGlobals = {
  describe: "readonly",
  it: "readonly",
  test: "readonly",
  expect: "readonly",
  beforeEach: "readonly",
  afterEach: "readonly",
  beforeAll: "readonly",
  afterAll: "readonly",
  vi: "readonly",
  jest: "readonly",
};

/** @type {import("eslint").Linter.Config[]} */
export default [
  // Global ignores
  {
    ignores: [
      "node_modules/**",
      "**/dist/**",
      "**/build/**",
      "**/out/**",
      "**/*.generated.ts",
      ".nightgauge/**",
      // Per-issue pipeline worktrees — linting happens inside each worktree's
      // own CI run, not from the main checkout. Without this ignore, every
      // file in an active pipeline slot shows up as a lint failure in the
      // main workspace. Covers both historical `.worktrees/` locations and
      // the current `.claude/worktrees/` layout.
      ".worktrees/**",
      ".claude/worktrees/**",
      "api/generated/**",
      "skills/*/dist/**",
      "packages/*/dist/**",
      "coverage/**",
    ],
  },

  // Base JS rules for all files
  js.configs.recommended,

  // TypeScript source files
  {
    files: ["packages/*/src/**/*.ts", "packages/*/src/**/*.tsx", "scripts/**/*.ts"],
    languageOptions: {
      parser: tsParser,
      parserOptions: {
        ecmaVersion: 2022,
        sourceType: "module",
      },
      globals: nodeGlobals,
    },
    plugins: {
      "@typescript-eslint": tsPlugin,
    },
    rules: {
      ...tsPlugin.configs.recommended.rules,

      // Ban deprecated .substr() method — use .slice() or .substring() instead
      "no-restricted-syntax": [
        "error",
        {
          selector: "CallExpression[callee.property.name='substr']",
          message: "String.prototype.substr() is deprecated. Use .slice() or .substring() instead.",
        },
      ],

      // Warn on unused vars — initial setup: existing code has many type-only imports.
      // Fix incrementally: use `import type` or remove unused imports.
      "@typescript-eslint/no-unused-vars": [
        "warn",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
          caughtErrorsIgnorePattern: "^_",
        },
      ],

      // Allow explicit any — common in platform integration and generic callbacks
      "@typescript-eslint/no-explicit-any": "warn",

      // Allow require() — common in Node.js dynamic loading patterns
      "@typescript-eslint/no-require-imports": "warn",

      // Warn on Function type — common as parameter/callback type in existing code
      // Prefer: () => void, (...args: unknown[]) => unknown, etc.
      "@typescript-eslint/no-unsafe-function-type": "warn",

      // Allow @ts-ignore with description (legacy suppressions)
      "@typescript-eslint/ban-ts-comment": [
        "warn",
        {
          "ts-ignore": "allow-with-description",
          "ts-expect-error": "allow-with-description",
        },
      ],

      // Disable rules handled by Prettier
      "no-extra-semi": "off",

      // Disable no-undef for TypeScript files — TS compiler handles this
      "no-undef": "off",

      // Allow multiple spaces in regex — common in format-string patterns
      "no-regex-spaces": "warn",
    },
  },

  // Test files — add test framework globals and more permissive rules
  {
    files: [
      "**/*.test.ts",
      "**/*.test.tsx",
      "**/*.spec.ts",
      "**/*.spec.tsx",
      "**/__tests__/**/*.ts",
      "**/tests/**/*.ts",
    ],
    languageOptions: {
      parser: tsParser,
      parserOptions: {
        ecmaVersion: 2022,
        sourceType: "module",
      },
      globals: {
        ...nodeGlobals,
        ...testGlobals,
      },
    },
    plugins: {
      "@typescript-eslint": tsPlugin,
    },
    rules: {
      ...tsPlugin.configs.recommended.rules,

      // Warn on unused imports in tests — common with vitest fixture imports
      "@typescript-eslint/no-unused-vars": [
        "warn",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
          caughtErrorsIgnorePattern: "^_",
        },
      ],

      // More permissive in tests
      "@typescript-eslint/no-explicit-any": "off",
      "@typescript-eslint/no-non-null-assertion": "off",
      "@typescript-eslint/no-require-imports": "warn",
      "@typescript-eslint/no-unsafe-function-type": "warn",
      "@typescript-eslint/ban-ts-comment": "off",

      // Disable no-undef for TypeScript files — TS compiler handles this
      "no-undef": "off",

      "no-extra-semi": "off",
      "no-regex-spaces": "warn",
    },
  },

  // JavaScript config files (eslint.config.js, vite.config.js, etc.)
  {
    files: ["*.js", "*.mjs", "*.cjs"],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: "module",
      globals: nodeGlobals,
    },
  },

  // Strict no-explicit-any for entire VSCode extension src/ — Part 3 of tech-debt elimination (#2776)
  // Supersedes the per-file rules added in Parts 1 (#2774) and 2 (#2775).
  {
    files: ["packages/nightgauge-vscode/src/**/*.ts"],
    ignores: ["packages/nightgauge-vscode/src/**/*.test.ts"],
    rules: {
      "@typescript-eslint/no-explicit-any": "error",
    },
  },

  // #2884: Block sync subprocess calls anywhere under the VSCode extension
  // src/ tree — they freeze the extension host event loop while gh/git hangs
  // on rate-limit, manifesting as "Window is not responding". Use
  // promisify(execFile)/promisify(exec) instead. See HeadlessOrchestrator.ts
  // and ContextAssembler.ts for the canonical execFileAsync pattern.
  //
  // Scope chosen broadly to catch regressions before they ship — the
  // original #2884 fix scoped the rule to src/services/ only and missed
  // the worst offenders (npm build / vitest run) in src/orchestrator/context/.
  {
    files: ["packages/nightgauge-vscode/src/**/*.ts"],
    ignores: [
      "packages/nightgauge-vscode/src/**/*.test.ts",
      "packages/nightgauge-vscode/src/services/IpcClient*.ts",
      // Cold-path leaf files where converting cascades widely and the call
      // is bounded by a short (≤5s) timeout. Tracked for follow-up.
      "packages/nightgauge-vscode/src/utils/skillRunner.ts",
      "packages/nightgauge-vscode/src/bootstrap/services.ts",
      "packages/nightgauge-vscode/src/commands/checkEpicCompletion.ts",
      "packages/nightgauge-vscode/src/commands/fixAutoMergeSetting.ts",
    ],
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector: "CallExpression[callee.property.name='substr']",
          message: "String.prototype.substr() is deprecated. Use .slice() or .substring() instead.",
        },
        {
          selector: "CallExpression[callee.name='execFileSync']",
          message:
            "execFileSync blocks the VSCode extension host event loop (#2884 'Window is not responding'). Use `await execFileAsync(...)` instead — see ContextAssembler.ts or HeadlessOrchestrator.ts for the promisify(execFile) pattern.",
        },
        {
          selector: "CallExpression[callee.name='execSync']",
          message:
            "execSync blocks the VSCode extension host event loop (#2884 'Window is not responding'). Use `await execAsync(...)` instead.",
        },
      ],
    },
  },

  // CommonJS Node.js scripts (postinstall, platform detection, etc.)
  {
    files: ["packages/*/scripts/**/*.js"],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: "commonjs",
      globals: nodeGlobals,
    },
  },

  // ESM Node.js scripts under a package's scripts/ dir (.mjs). Mirrors the
  // root `scripts/**/*.mjs` block below — without this, an .mjs here falls
  // through to js.configs.recommended (no Node globals, no-undef on) and every
  // `console`/`process` reference errors. e.g. check-engine-types.mjs.
  {
    files: ["packages/*/scripts/**/*.mjs"],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: "module",
      globals: nodeGlobals,
    },
  },

  // Root-level scripts directory — CommonJS Node.js scripts
  {
    files: ["scripts/**/*.js"],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: "commonjs",
      globals: nodeGlobals,
    },
  },

  // Root-level scripts directory — ESM Node.js scripts (.mjs)
  {
    files: ["scripts/**/*.mjs"],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: "module",
      globals: nodeGlobals,
    },
  },
];
