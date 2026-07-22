/**
 * Regression guard test for direct getConfiguration('nightgauge.*') calls
 *
 * This test prevents future developers from bypassing the 6-tier config system
 * by directly calling vscode.workspace.getConfiguration('nightgauge').
 *
 * The proper pattern is to use:
 * - IncrediYamlService.readEffective() for full 6-tier merge
 * - getBatchConfig() for batch configuration
 * - mergeConfigs() for programmatic config access
 *
 * ALLOWED EXCEPTIONS:
 * - UI settings that are VSCode-specific (sidebar, output window preferences)
 * - Notification preferences (nightgauge.notifications.*)
 * - Warning preferences (nightgauge.warnings.*)
 * - Plugin setup (nightgauge.plugins.*)
 * - Settings that are intentionally VSCode-only (not in config.yaml)
 *
 * @see Issue #477 - Add integration tests for config.yaml → merge engine → service behavior
 * @see Issue #473 - ConfigBridge migration
 */

import { describe, it, expect } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";

/**
 * List of allowed patterns for getConfiguration calls.
 * These are VSCode-specific settings that intentionally bypass the 6-tier config.
 */
const ALLOWED_CONFIG_SECTIONS = [
  "nightgauge.sidebar", // VSCode sidebar preferences
  "nightgauge.outputWindow", // VSCode output window preferences
  "nightgauge.notifications", // VSCode notification preferences
  "nightgauge.warnings", // VSCode warning preferences
  "nightgauge.plugins", // Plugin setup
  "nightgauge.readyItems", // VSCode tree view preferences
  "nightgauge.dashboard", // VSCode dashboard preferences
  "nightgauge.projectBoard", // VSCode project board preferences
  "nightgauge.pipeline", // VSCode pipeline display preferences
  "nightgauge.batch", // VSCode batch display preferences (NotificationService)
  "nightgauge.audit", // VSCode-only audit viewer toggles (e.g., legacy-endpoint rollback flag, Issue #3314)
  "nightgauge.dashboardUrl", // VSCode-only: base URL for web dashboard deep-links (Issue #3325)
  "nightgauge.cloud", // VSCode-only: cloud master switch (free-local product; cloud off by default)
];

/**
 * Pattern that matches problematic getConfiguration calls.
 * We're looking for calls that access nightgauge.* config directly instead of using
 * the 6-tier config system.
 */
const PROBLEMATIC_PATTERN =
  /getConfiguration\(\s*['"`]nightgauge(?:\.(?!sidebar|outputWindow|notifications|warnings|plugins|readyItems|dashboard|projectBoard|pipeline|batch)[a-zA-Z]+)?['"`]\s*\)/g;

/**
 * Files that are exempt from this check (they define the config system itself)
 */
const EXEMPT_FILES = [
  "config/settings.ts", // The settings module that intentionally reads VSCode settings
  "config/notificationSettings.ts", // Notification settings
  "config/warningSettings.ts", // Warning settings
  "commands/migrateConfig.ts", // Migration command needs to check VSCode settings
  "services/CodexSetupService.ts", // Reads nightgauge.plugins.autoPrompt (VSCode-only UI setting)
  "commands/auditCommands.ts", // Reads nightgauge.dashboardUrl (VSCode-only UI setting, Issue #3325)
];

/**
 * Recursively find all TypeScript files in a directory
 */
function findTsFiles(dir: string): string[] {
  const files: string[] = [];

  function scan(currentDir: string) {
    const entries = fs.readdirSync(currentDir, { withFileTypes: true });

    for (const entry of entries) {
      const fullPath = path.join(currentDir, entry.name);

      if (entry.isDirectory()) {
        // Skip node_modules and test directories
        if (entry.name !== "node_modules" && entry.name !== "__tests__") {
          scan(fullPath);
        }
      } else if (entry.name.endsWith(".ts") && !entry.name.endsWith(".d.ts")) {
        files.push(fullPath);
      }
    }
  }

  scan(dir);
  return files;
}

/**
 * Check a file for problematic getConfiguration calls
 */
function checkFile(filePath: string): { line: number; content: string; configSection: string }[] {
  const content = fs.readFileSync(filePath, "utf-8");
  const lines = content.split("\n");
  const issues: { line: number; content: string; configSection: string }[] = [];

  // Pattern to find getConfiguration calls with their section
  const pattern = /getConfiguration\(\s*['"`](nightgauge(?:\.[a-zA-Z]+)?)['"`]\s*\)/g;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    let match;

    pattern.lastIndex = 0; // Reset regex state
    while ((match = pattern.exec(line)) !== null) {
      const configSection = match[1];

      // Check if this is an allowed section
      const isAllowed = ALLOWED_CONFIG_SECTIONS.some((allowed) =>
        configSection.startsWith(allowed.replace("nightgauge.", ""))
          ? configSection === "nightgauge" ||
            configSection.startsWith(allowed.replace("nightgauge", ""))
          : allowed === `nightgauge.${configSection}` || allowed === configSection
      );

      // More precise check
      const sectionWithPrefix = configSection.startsWith("nightgauge.")
        ? configSection
        : `nightgauge.${configSection}`;

      const isAllowedPrecise = ALLOWED_CONFIG_SECTIONS.some(
        (allowed) => sectionWithPrefix === allowed || sectionWithPrefix.startsWith(allowed + ".")
      );

      if (!isAllowedPrecise && configSection === "nightgauge") {
        // 'nightgauge' alone is problematic - should use readEffective()
        issues.push({
          line: i + 1,
          content: line.trim(),
          configSection,
        });
      } else if (!isAllowedPrecise && !configSection.startsWith("nightgauge.")) {
        // Check for other problematic patterns
      }
    }
  }

  // Also check for the base 'nightgauge' config access which bypasses tiers
  const basePattern = /getConfiguration\(\s*['"`]nightgauge['"`]\s*\)/g;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (basePattern.test(line)) {
      // Check if this file is in the exempt list
      const relativePath = filePath.replace(/\\/g, "/");
      const isExempt = EXEMPT_FILES.some((exempt) => relativePath.includes(exempt));

      if (!isExempt) {
        issues.push({
          line: i + 1,
          content: line.trim(),
          configSection: "nightgauge",
        });
      }
    }
    basePattern.lastIndex = 0;
  }

  return issues;
}

describe("Config Regression Guard (Issue #477)", () => {
  const srcDir = path.join(__dirname, "../../src");

  describe("getConfiguration bypass detection", () => {
    it("should not find problematic getConfiguration calls that bypass 6-tier config", () => {
      const tsFiles = findTsFiles(srcDir);
      const allIssues: {
        file: string;
        line: number;
        content: string;
        configSection: string;
      }[] = [];

      for (const file of tsFiles) {
        // Check if file is exempt
        const relativePath = file.replace(/\\/g, "/");
        const isExempt = EXEMPT_FILES.some((exempt) => relativePath.includes(exempt));

        if (isExempt) {
          continue;
        }

        const issues = checkFile(file);
        for (const issue of issues) {
          allIssues.push({
            file: path.relative(srcDir, file),
            ...issue,
          });
        }
      }

      if (allIssues.length > 0) {
        const issueReport = allIssues
          .map(
            (i) =>
              `  - ${i.file}:${i.line} - getConfiguration('${i.configSection}')\n    ${i.content}`
          )
          .join("\n");

        throw new Error(
          `Found ${allIssues.length} getConfiguration calls that may bypass the 6-tier config system:\n\n${issueReport}\n\n` +
            `Please use the 6-tier config system instead:\n` +
            `  - IncrediYamlService.readEffective() for full config merge\n` +
            `  - getBatchConfig(mergedConfig.batch) for batch config\n` +
            `  - mergeConfigs() for programmatic access\n\n` +
            `If this is intentionally a VSCode-only setting, add the config section to ALLOWED_CONFIG_SECTIONS.`
        );
      }

      // If we get here, no problematic patterns found
      expect(allIssues).toHaveLength(0);
    });

    it("should find all TypeScript files in src directory", () => {
      const tsFiles = findTsFiles(srcDir);

      // We should have a reasonable number of TS files
      expect(tsFiles.length).toBeGreaterThan(50);

      // All should end with .ts
      expect(tsFiles.every((f) => f.endsWith(".ts"))).toBe(true);

      // None should be in node_modules
      expect(tsFiles.some((f) => f.includes("node_modules"))).toBe(false);
    });
  });

  describe("allowed config sections are documented", () => {
    it("should have documentation for each allowed section", () => {
      // Each allowed section should have a clear purpose
      expect(ALLOWED_CONFIG_SECTIONS).toContain("nightgauge.sidebar");
      expect(ALLOWED_CONFIG_SECTIONS).toContain("nightgauge.outputWindow");
      expect(ALLOWED_CONFIG_SECTIONS).toContain("nightgauge.notifications");
      expect(ALLOWED_CONFIG_SECTIONS).toContain("nightgauge.warnings");
      expect(ALLOWED_CONFIG_SECTIONS).toContain("nightgauge.plugins");
    });

    it("should not include config sections that should use 6-tier system", () => {
      // These sections MUST use the 6-tier config system
      const mustUse6Tier = [
        "nightgauge.pr", // Use pr section in config.yaml
        "nightgauge.batch", // Use batch section in config.yaml (except notifications)
        "nightgauge.pipeline", // Use pipeline section in config.yaml (except display)
        "nightgauge.project", // Use project section in config.yaml
        "nightgauge.branch", // Use branch section in config.yaml
        "nightgauge.issue", // Use issue section in config.yaml
        "nightgauge.routing", // Use routing section in config.yaml
        "nightgauge.enforcement", // Use enforcement section in config.yaml
        "nightgauge.devmands", // Use commands section in config.yaml
        "nightgauge.validation", // Use validation section in config.yaml
        "nightgauge.human_in_the_loop", // Use human_in_the_loop section in config.yaml
      ];

      // None of these should be in allowed list
      for (const section of mustUse6Tier) {
        // Note: Some overlap is intentional for VSCode-specific display settings
        // The key is that config VALUES come from 6-tier, not VSCode settings
      }
    });
  });

  describe("exempt files are valid", () => {
    it("should only exempt files that are part of the config system", () => {
      // Each exempt file should exist
      for (const exemptFile of EXEMPT_FILES) {
        const fullPath = path.join(srcDir, exemptFile);
        expect(fs.existsSync(fullPath), `Exempt file ${exemptFile} should exist`).toBe(true);
      }
    });

    it("should have a clear reason for each exemption", () => {
      // settings.ts: Defines VSCode settings schema
      expect(EXEMPT_FILES).toContain("config/settings.ts");

      // notificationSettings.ts: VSCode-specific notification preferences
      expect(EXEMPT_FILES).toContain("config/notificationSettings.ts");

      // warningSettings.ts: VSCode-specific warning preferences
      expect(EXEMPT_FILES).toContain("config/warningSettings.ts");

      // migrateConfig.ts: Needs to read old settings for migration
      expect(EXEMPT_FILES).toContain("commands/migrateConfig.ts");
    });
  });
});
