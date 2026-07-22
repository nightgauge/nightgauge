import { readFileSync, existsSync } from "fs";
import type { ReadmeCommandResult } from "../types.js";

/** Recognized command prefixes to validate. */
const COMMAND_PATTERNS: { re: RegExp; type: string }[] = [
  { re: /`(npm run \S+)`/, type: "npm-run" },
  { re: /`(npm install[^`]*)`/, type: "npm-install" },
  { re: /`(npx [^`]+)`/, type: "npx" },
  { re: /`(go (?:build|test|run|generate)[^`]*)`/, type: "go" },
  { re: /`(make \S+)`/, type: "make" },
  { re: /`(pnpm [^`]+)`/, type: "pnpm" },
  { re: /`(yarn [^`]+)`/, type: "yarn" },
];

/** Extract all command strings from a markdown file with line numbers. */
function extractCommands(content: string): Array<{ command: string; type: string; line: number }> {
  const lines = content.split("\n");
  const commands: Array<{ command: string; type: string; line: number }> = [];

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    for (const { re, type } of COMMAND_PATTERNS) {
      const match = line.match(re);
      if (match && match[1]) {
        commands.push({ command: match[1], type, line: i + 1 });
      }
    }
  }

  return commands;
}

/**
 * Load package.json scripts from a file.
 * Returns an empty set if the file doesn't exist.
 */
function loadPackageScripts(packageJsonPath: string): Set<string> {
  if (!existsSync(packageJsonPath)) return new Set();

  try {
    const content = readFileSync(packageJsonPath, "utf-8");
    const parsed = JSON.parse(content) as { scripts?: Record<string, string> };
    return new Set(Object.keys(parsed.scripts ?? {}));
  } catch (err) {
    console.warn(
      `[readme-validator] Could not parse ${packageJsonPath}: ${(err as Error).message}`
    );
    return new Set();
  }
}

/**
 * Validate a single `npm run <script>` command against the package.json scripts.
 */
function validateNpmRunCommand(
  command: string,
  packageScripts: Set<string>
): { valid: boolean; reason?: string } {
  const match = command.match(/^npm run (\S+)/);
  if (!match || !match[1]) return { valid: true }; // Can't extract script name

  const scriptName = match[1];
  if (packageScripts.size === 0) {
    // No package.json found — can't validate, assume valid
    return { valid: true };
  }

  if (!packageScripts.has(scriptName)) {
    return {
      valid: false,
      reason: `Script "${scriptName}" not found in package.json scripts`,
    };
  }

  return { valid: true };
}

/**
 * Validate README commands in a file against the project's package.json.
 *
 * @param readmePath - Path to the README.md file
 * @param packageJsonPath - Path to the nearest package.json (optional)
 */
export function validateReadmeCommands(
  readmePath: string,
  packageJsonPath?: string
): ReadmeCommandResult[] {
  if (!existsSync(readmePath)) return [];

  const content = readFileSync(readmePath, "utf-8");
  const commands = extractCommands(content);
  const packageScripts = packageJsonPath ? loadPackageScripts(packageJsonPath) : new Set<string>();

  const results: ReadmeCommandResult[] = [];

  for (const { command, type, line } of commands) {
    let valid = true;
    let reason: string | undefined;

    if (type === "npm-run") {
      const validation = validateNpmRunCommand(command, packageScripts);
      valid = validation.valid;
      reason = validation.reason;
    }
    // go, make, npx, pnpm, yarn commands — basic syntax check only
    else if (type === "go") {
      const validGoCommands = ["build", "test", "run", "generate", "mod"];
      const subcommand = command.split(" ")[1];
      if (subcommand && !validGoCommands.some((c) => subcommand.startsWith(c))) {
        valid = false;
        reason = `Unknown go subcommand: ${subcommand}`;
      }
    }

    results.push({
      command,
      valid,
      reason,
      file: readmePath,
      line,
    });
  }

  return results;
}

/**
 * Summarize validation results: count valid vs invalid commands.
 */
export function summarizeValidation(results: ReadmeCommandResult[]): {
  total: number;
  valid: number;
  invalid: number;
  invalidCommands: ReadmeCommandResult[];
} {
  const invalid = results.filter((r) => !r.valid);
  return {
    total: results.length,
    valid: results.length - invalid.length,
    invalid: invalid.length,
    invalidCommands: invalid,
  };
}
