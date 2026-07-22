/**
 * Context File Tool Handlers
 *
 * Server-side handlers for reading and listing pipeline context files
 * in `.nightgauge/pipeline/`. These handlers execute when Claude's
 * Python code invokes `read_context_file` or `list_context_files` via PTC.
 *
 * @see Issue #1070 - Optimize context file and git batch operations
 * @see packages/nightgauge-sdk/src/tools/definitions/context.ts
 */

import { readFileSync, readdirSync, statSync } from "fs";
import { join, resolve } from "path";
import type { ToolHandler, ToolResult } from "./tool-handlers.js";

const PIPELINE_DIR = ".nightgauge/pipeline";

/** Handler for `read_context_file` tool */
export class ReadContextFileHandler implements ToolHandler {
  readonly name = "read_context_file";

  async execute(input: Record<string, unknown>, cwd: string): Promise<ToolResult> {
    const filename = typeof input.filename === "string" ? input.filename : "";
    if (!filename) {
      return {
        success: false,
        output: {
          success: false,
          error: "Missing required parameter: filename",
        },
      };
    }

    // Prevent path traversal
    const pipelineDir = resolve(cwd, PIPELINE_DIR);
    const filePath = resolve(pipelineDir, filename);
    if (!filePath.startsWith(pipelineDir)) {
      return {
        success: false,
        output: {
          success: false,
          error: "Invalid filename: path traversal not allowed",
        },
      };
    }

    try {
      const raw = readFileSync(filePath, "utf-8");
      const content = JSON.parse(raw);
      const schemaVersion =
        typeof content.schema_version === "string" ? content.schema_version : "unknown";

      return {
        success: true,
        output: {
          success: true,
          filename,
          content,
          schema_version: schemaVersion,
        },
      };
    } catch (err: unknown) {
      const errMsg = err instanceof Error ? err.message : "Failed to read context file";
      const isNotFound =
        err instanceof Error && "code" in err && (err as NodeJS.ErrnoException).code === "ENOENT";

      return {
        success: false,
        output: {
          success: false,
          filename,
          error: isNotFound
            ? `Context file not found: ${filename}`
            : `Failed to read context file: ${errMsg}`,
        },
      };
    }
  }
}

/** Handler for `list_context_files` tool */
export class ListContextFilesHandler implements ToolHandler {
  readonly name = "list_context_files";

  async execute(input: Record<string, unknown>, cwd: string): Promise<ToolResult> {
    const pattern = typeof input.pattern === "string" ? input.pattern : undefined;

    const pipelineDir = resolve(cwd, PIPELINE_DIR);

    let entries: string[];
    try {
      entries = readdirSync(pipelineDir);
    } catch (err: unknown) {
      const isNotFound =
        err instanceof Error && "code" in err && (err as NodeJS.ErrnoException).code === "ENOENT";
      if (isNotFound) {
        return {
          success: true,
          output: { success: true, files: [], count: 0 },
        };
      }
      const errMsg = err instanceof Error ? err.message : "Failed to list directory";
      return {
        success: false,
        output: { success: false, error: errMsg },
      };
    }

    // Filter by regex pattern if provided
    let filtered = entries.filter((f) => f.endsWith(".json"));
    if (pattern) {
      try {
        const regex = new RegExp(pattern);
        filtered = filtered.filter((f) => regex.test(f));
      } catch {
        return {
          success: false,
          output: {
            success: false,
            error: `Invalid regex pattern: ${pattern}`,
          },
        };
      }
    }

    const files = filtered.map((filename) => {
      const filePath = join(pipelineDir, filename);
      try {
        const stat = statSync(filePath);
        return {
          filename,
          size_bytes: stat.size,
          modified_at: stat.mtime.toISOString(),
        };
      } catch {
        return { filename, size_bytes: 0, modified_at: "" };
      }
    });

    return {
      success: true,
      output: { success: true, files, count: files.length },
    };
  }
}

/**
 * Create the context file tool handler map.
 * Maps tool names to their server-side handler implementations.
 */
export function createContextHandlers(): Map<string, ToolHandler> {
  const handlers = new Map<string, ToolHandler>();
  const instances: ToolHandler[] = [new ReadContextFileHandler(), new ListContextFilesHandler()];
  for (const handler of instances) {
    handlers.set(handler.name, handler);
  }
  return handlers;
}
