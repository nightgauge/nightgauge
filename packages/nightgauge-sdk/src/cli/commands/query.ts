/**
 * Query Command - Execute GQL queries against GitHub Project board
 *
 * Usage: nightgauge-sdk query "<query-expression>" [options]
 *
 * Examples:
 *   nightgauge-sdk query "status:ready AND priority:P0"
 *   nightgauge-sdk query "size:M OR size:L" --format json
 *   nightgauge-sdk query "updated<7d" --export results.csv
 *
 * @see docs/QUERY_LANGUAGE.md for query syntax
 */

import type { CAC } from "cac";
import * as fs from "fs";
import * as path from "path";
import * as yaml from "yaml";
import type { CLIConfig } from "../config.js";
import { OutputFormatter } from "../output.js";
import { EXIT_CODES } from "./run.js";
import {
  executeQuery,
  validate as validateQuery,
  type QueryableIssue,
  type QueryResult,
  type SavedQueriesFile,
  SavedQueriesFileSchema,
  EvaluationError,
} from "../../query/index.js";

/**
 * Options for the query command
 */
interface QueryOptions {
  format?: "table" | "json" | "csv";
  export?: string;
  save?: string;
  list?: boolean;
  run?: string;
}

/**
 * Table column configuration
 */
interface TableColumn {
  header: string;
  key: keyof QueryableIssue;
  width: number;
  align?: "left" | "right";
}

/**
 * Default table columns
 */
const TABLE_COLUMNS: TableColumn[] = [
  { header: "#", key: "number", width: 6, align: "right" },
  { header: "Title", key: "title", width: 50 },
  { header: "Priority", key: "priority", width: 8 },
  { header: "Size", key: "size", width: 6 },
  { header: "Status", key: "status", width: 12 },
];

/**
 * Format a value for table display
 */
function formatValue(value: unknown, width: number): string {
  const str = value?.toString() ?? "-";
  if (str.length > width) {
    return str.slice(0, width - 3) + "...";
  }
  return str.padEnd(width);
}

/**
 * Print results as a table
 */
function printTable(result: QueryResult, formatter: OutputFormatter): void {
  // Print header
  const headerLine = TABLE_COLUMNS.map((col) => col.header.padEnd(col.width)).join(" │ ");
  const separator = TABLE_COLUMNS.map((col) => "─".repeat(col.width)).join("─┼─");

  formatter.info("");
  formatter.info(headerLine);
  formatter.info(separator);

  // Print rows
  for (const item of result.items) {
    const row = TABLE_COLUMNS.map((col) => {
      const value = item[col.key];
      return formatValue(value, col.width);
    }).join(" │ ");
    formatter.info(row);
  }

  // Print summary
  formatter.info("");
  formatter.info(
    `Found ${result.matchCount} of ${result.totalCount} issues (${result.executionTimeMs}ms)`
  );
}

/**
 * Print results as JSON
 */
function printJSON(result: QueryResult): void {
  console.log(JSON.stringify(result, null, 2));
}

/**
 * Print results as CSV
 */
function printCSV(result: QueryResult): void {
  // Header
  console.log(TABLE_COLUMNS.map((col) => col.header).join(","));

  // Rows
  for (const item of result.items) {
    const values = TABLE_COLUMNS.map((col) => {
      const value = item[col.key];
      const str = value?.toString() ?? "";
      // Escape quotes and wrap in quotes if contains comma
      if (str.includes(",") || str.includes('"')) {
        return `"${str.replace(/"/g, '""')}"`;
      }
      return str;
    });
    console.log(values.join(","));
  }
}

/**
 * Export results to a file
 */
function exportResults(result: QueryResult, filepath: string, format: "json" | "csv"): void {
  const ext = path.extname(filepath).toLowerCase();
  const actualFormat = format ?? (ext === ".json" ? "json" : "csv");

  let content: string;
  if (actualFormat === "json") {
    content = JSON.stringify(result, null, 2);
  } else {
    // CSV
    const lines = [TABLE_COLUMNS.map((col) => col.header).join(",")];
    for (const item of result.items) {
      const values = TABLE_COLUMNS.map((col) => {
        const value = item[col.key];
        const str = value?.toString() ?? "";
        if (str.includes(",") || str.includes('"')) {
          return `"${str.replace(/"/g, '""')}"`;
        }
        return str;
      });
      lines.push(values.join(","));
    }
    content = lines.join("\n");
  }

  fs.writeFileSync(filepath, content, "utf-8");
}

/**
 * Load saved queries from .nightgauge/saved-queries.yaml
 */
function loadSavedQueries(workingDir: string): SavedQueriesFile | null {
  const filepath = path.join(workingDir, ".nightgauge", "saved-queries.yaml");
  if (!fs.existsSync(filepath)) {
    return null;
  }

  try {
    const content = fs.readFileSync(filepath, "utf-8");
    const data = yaml.parse(content);
    return SavedQueriesFileSchema.parse(data);
  } catch {
    return null;
  }
}

/**
 * Save a query to .nightgauge/saved-queries.yaml
 */
function saveQuery(workingDir: string, name: string, query: string): void {
  const filepath = path.join(workingDir, ".nightgauge", "saved-queries.yaml");
  const incrediDir = path.join(workingDir, ".nightgauge");

  // Ensure .nightgauge directory exists
  if (!fs.existsSync(incrediDir)) {
    fs.mkdirSync(incrediDir, { recursive: true });
  }

  // Load existing or create new
  let savedQueries = loadSavedQueries(workingDir);
  if (!savedQueries) {
    savedQueries = { version: "1.0", queries: [] };
  }

  // Check if query with same name exists
  const existingIndex = savedQueries.queries.findIndex((q) => q.name === name);
  const now = new Date().toISOString();

  if (existingIndex >= 0) {
    // Update existing
    savedQueries.queries[existingIndex] = {
      ...savedQueries.queries[existingIndex],
      query,
      lastUsedAt: now,
    };
  } else {
    // Add new
    savedQueries.queries.push({
      name,
      query,
      createdAt: now,
    });
  }

  // Write back
  const content = yaml.stringify(savedQueries);
  fs.writeFileSync(filepath, content, "utf-8");
}

/**
 * Get a saved query by name
 */
function getSavedQuery(workingDir: string, name: string): string | null {
  const savedQueries = loadSavedQueries(workingDir);
  if (!savedQueries) {
    return null;
  }

  const query = savedQueries.queries.find((q) => q.name === name);
  return query?.query ?? null;
}

/**
 * List all saved queries
 */
function listSavedQueries(workingDir: string, formatter: OutputFormatter): void {
  const savedQueries = loadSavedQueries(workingDir);

  if (!savedQueries || savedQueries.queries.length === 0) {
    formatter.info("No saved queries found.");
    formatter.info('Save a query with: nightgauge-sdk query "..." --save <name>');
    return;
  }

  formatter.info("Saved queries:");
  formatter.info("");

  for (const query of savedQueries.queries) {
    formatter.info(`  ${query.name}`);
    formatter.info(`    Query: ${query.query}`);
    if (query.description) {
      formatter.info(`    Description: ${query.description}`);
    }
    formatter.info("");
  }
}

/**
 * Fetch issues from GitHub Project board
 * This is a placeholder - actual implementation would use gh CLI
 */
async function fetchIssues(workingDir: string): Promise<QueryableIssue[]> {
  // TODO: Integrate with ProjectBoardService or gh CLI
  // For now, return empty array - actual fetching should be done
  // by the VSCode extension's ProjectBoardService

  // Try to read from a cache file if it exists
  const cachePath = path.join(workingDir, ".nightgauge", "pipeline", "project-items.json");
  if (fs.existsSync(cachePath)) {
    try {
      const content = fs.readFileSync(cachePath, "utf-8");
      const data = JSON.parse(content);
      return data.items ?? [];
    } catch {
      // Ignore cache errors
    }
  }

  return [];
}

/**
 * Register the query command
 */
export function registerQueryCommand(cli: CAC, config: CLIConfig): void {
  cli
    .command("query [expression]", "Query project board issues")
    .option("--format <format>", "Output format: table, json, csv", {
      default: "table",
    })
    .option("--export <file>", "Export results to file")
    .option("--save <name>", "Save query with a name")
    .option("--list", "List saved queries")
    .option("--run <name>", "Run a saved query")
    .action(async (expression: string | undefined, options: QueryOptions) => {
      const formatter = new OutputFormatter(config.outputFormat, config.logLevel);
      const workingDir = process.cwd();

      // Handle --list flag
      if (options.list) {
        listSavedQueries(workingDir, formatter);
        return;
      }

      // Handle --run flag
      let queryExpression = expression;
      if (options.run) {
        const savedQuery = getSavedQuery(workingDir, options.run);
        if (!savedQuery) {
          formatter.error(`Saved query not found: ${options.run}`);
          process.exit(EXIT_CODES.CONFIG_ERROR);
        }
        queryExpression = savedQuery;
        formatter.info(`Running saved query "${options.run}": ${savedQuery}`);
      }

      // Require query expression
      if (!queryExpression) {
        formatter.error("Query expression required");
        formatter.info('Usage: nightgauge-sdk query "<expression>"');
        formatter.info('Example: nightgauge-sdk query "status:ready AND priority:P0"');
        process.exit(EXIT_CODES.CONFIG_ERROR);
      }

      // Validate query
      const errors = validateQuery(queryExpression);
      if (errors.length > 0) {
        formatter.error("Invalid query:");
        for (const error of errors) {
          formatter.error(`  Position ${error.position}: ${error.message}`);
        }
        process.exit(EXIT_CODES.CONFIG_ERROR);
      }

      // Handle --save flag
      if (options.save) {
        saveQuery(workingDir, options.save, queryExpression);
        formatter.info(`Query saved as "${options.save}"`);
      }

      // Fetch issues
      formatter.debug("Fetching project board issues...");
      const issues = await fetchIssues(workingDir);

      if (issues.length === 0) {
        formatter.warn("No issues found. Run from VSCode extension for full project board access.");
        return;
      }

      // Execute query
      try {
        const result = executeQuery(queryExpression, issues);

        // Export if requested
        if (options.export) {
          const exportFormat = options.format === "json" ? "json" : "csv";
          exportResults(result, options.export, exportFormat);
          formatter.info(`Results exported to ${options.export}`);
        }

        // Print results
        switch (options.format) {
          case "json":
            printJSON(result);
            break;
          case "csv":
            printCSV(result);
            break;
          default:
            printTable(result, formatter);
        }
      } catch (error) {
        if (error instanceof EvaluationError) {
          formatter.error(`Query error: ${error.message}`);
        } else {
          formatter.error(
            `Unexpected error: ${error instanceof Error ? error.message : String(error)}`
          );
        }
        process.exit(EXIT_CODES.PIPELINE_FAILED);
      }
    });
}
