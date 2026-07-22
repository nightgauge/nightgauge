import { readFileSync, existsSync } from "fs";
import type { DocEndpoint } from "../types.js";

/**
 * Parses markdown tables from ECOSYSTEM.md or similar documentation files.
 * Extracts endpoint rows with columns: Method, Path, Status, Notes.
 */

const ENDPOINT_TABLE_HEADER_RE = /\|\s*method\s*\|\s*(?:path|endpoint)\s*\|\s*status\s*/i;

/** Parse a pipe-delimited markdown table row into trimmed cells. */
function parseTableRow(line: string): string[] {
  return line
    .split("|")
    .map((cell) => cell.trim())
    .filter((_, i, arr) => i > 0 && i < arr.length - 1); // drop empty first/last
}

/** Detect if a row is a separator row (e.g., `|---|---|---|`). */
function isSeparatorRow(cells: string[]): boolean {
  return cells.every((c) => /^[-: ]+$/.test(c));
}

/**
 * Find the index positions of "method", "path/endpoint", "status", and
 * "notes" columns in a header row (case-insensitive).
 */
function detectColumnIndices(
  headers: string[]
): { method: number; path: number; status: number; notes: number } | null {
  const lower = headers.map((h) => h.toLowerCase());
  const method = lower.findIndex((h) => h.includes("method"));
  const path = lower.findIndex((h) => h.includes("path") || h.includes("endpoint"));
  const status = lower.findIndex((h) => h.includes("status"));
  const notes = lower.findIndex((h) => h.includes("notes") || h.includes("description"));

  if (method === -1 || path === -1 || status === -1) return null;
  return { method, path, status, notes };
}

/**
 * Extract all documented endpoints from a markdown file.
 * Skips malformed tables with a warning and continues.
 */
export function parseDocumentationEndpoints(filePath: string): DocEndpoint[] {
  if (!existsSync(filePath)) {
    return [];
  }

  const content = readFileSync(filePath, "utf-8");
  const lines = content.split("\n");
  const endpoints: DocEndpoint[] = [];

  let inTable = false;
  let columnIndices: ReturnType<typeof detectColumnIndices> | null = null;
  let headerFound = false;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];

    if (!inTable) {
      // Detect a table header line matching our pattern
      if (ENDPOINT_TABLE_HEADER_RE.test(line)) {
        headerFound = true;
        inTable = true;
        const cells = parseTableRow(line);
        columnIndices = detectColumnIndices(cells);
        if (!columnIndices) {
          // Can't parse this table — skip it
          inTable = false;
          headerFound = false;
          console.warn(
            `[documentation-parser] Could not detect columns in table header at ${filePath}:${i + 1}`
          );
        }
      }
    } else {
      const cells = parseTableRow(line);

      if (cells.length === 0 || !line.includes("|")) {
        // Empty line ends the table
        inTable = false;
        columnIndices = null;
        headerFound = false;
        continue;
      }

      if (isSeparatorRow(cells)) {
        // Separator between header and data
        continue;
      }

      if (!columnIndices) continue;

      const method = cells[columnIndices.method] ?? "";
      const path = cells[columnIndices.path] ?? "";
      const status = cells[columnIndices.status] ?? "";
      const notes = columnIndices.notes >= 0 ? (cells[columnIndices.notes] ?? "") : "";

      // Skip empty rows or rows with no meaningful content
      if (!method && !path) continue;

      endpoints.push({
        method: method.toUpperCase(),
        path,
        status,
        notes,
        file: filePath,
        line: i + 1,
      });
    }
  }

  return endpoints;
}

/**
 * Check whether a documented endpoint path exists in a set of known routes.
 * Returns true when the path is found (exact match or prefix match).
 */
export function endpointExistsInRoutes(path: string, knownRoutes: string[]): boolean {
  const normalizedPath = path.replace(/\/+$/, "").toLowerCase();
  return knownRoutes.some((route) => {
    const normalizedRoute = route.replace(/\/+$/, "").toLowerCase();
    return (
      normalizedRoute === normalizedPath ||
      normalizedRoute.startsWith(normalizedPath) ||
      normalizedPath.startsWith(normalizedRoute)
    );
  });
}

/**
 * Extract route paths from a Hono/Express route file by scanning for
 * common route registration patterns.
 */
export function extractRoutesFromFile(filePath: string): string[] {
  if (!existsSync(filePath)) return [];

  const content = readFileSync(filePath, "utf-8");
  const routes: string[] = [];

  // Match patterns like: .get('/path', ...) .post("/path", ...) app.route('/path')
  const routeRe = /\.(get|post|put|patch|delete|all|route)\s*\(\s*['"`]([^'"`]+)['"`]/gi;
  let match: RegExpExecArray | null;

  while ((match = routeRe.exec(content)) !== null) {
    routes.push(match[2]);
  }

  return routes;
}
