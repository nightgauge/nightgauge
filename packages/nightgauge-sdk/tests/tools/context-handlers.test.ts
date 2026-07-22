/**
 * Context File Tool Handler Tests
 *
 * Tests for ReadContextFileHandler and ListContextFilesHandler.
 * fs module is mocked at module level so no real filesystem access occurs.
 *
 * @see Issue #1070 - Optimize context file and git batch operations
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("fs", () => ({
  readFileSync: vi.fn(),
  readdirSync: vi.fn(),
  statSync: vi.fn(),
}));

import { readFileSync, readdirSync, statSync } from "fs";
import {
  ReadContextFileHandler,
  ListContextFilesHandler,
  createContextHandlers,
} from "../../src/tools/context-handlers.js";

const mockReadFileSync = vi.mocked(readFileSync);
const mockReaddirSync = vi.mocked(readdirSync);
const mockStatSync = vi.mocked(statSync);

const CWD = "/project";

// ---------------------------------------------------------------------------
// ReadContextFileHandler
// ---------------------------------------------------------------------------

describe("ReadContextFileHandler", () => {
  let handler: ReadContextFileHandler;

  beforeEach(() => {
    handler = new ReadContextFileHandler();
    vi.clearAllMocks();
  });

  it('has name "read_context_file"', () => {
    expect(handler.name).toBe("read_context_file");
  });

  it("reads and parses a valid JSON context file", async () => {
    const content = { schema_version: "1.1", issue_number: 42 };
    mockReadFileSync.mockReturnValueOnce(JSON.stringify(content));

    const result = await handler.execute({ filename: "issue-42.json" }, CWD);

    expect(result.success).toBe(true);
    expect(result.output["filename"]).toBe("issue-42.json");
    expect(result.output["content"]).toEqual(content);
    expect(result.output["schema_version"]).toBe("1.1");
  });

  it("returns error when filename is missing", async () => {
    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(false);
    expect(result.output["error"]).toContain("Missing required parameter");
  });

  it("returns error when file is not found", async () => {
    const err = Object.assign(new Error("ENOENT"), { code: "ENOENT" });
    mockReadFileSync.mockImplementationOnce(() => {
      throw err;
    });

    const result = await handler.execute({ filename: "missing-42.json" }, CWD);

    expect(result.success).toBe(false);
    expect(result.output["error"]).toContain("not found");
    expect(result.output["filename"]).toBe("missing-42.json");
  });

  it("returns error when file contains invalid JSON", async () => {
    mockReadFileSync.mockReturnValueOnce("not json at all");

    const result = await handler.execute({ filename: "bad-42.json" }, CWD);

    expect(result.success).toBe(false);
    expect(result.output["error"]).toContain("Failed to read context file");
  });

  it('defaults schema_version to "unknown" when not present', async () => {
    const content = { issue_number: 42 };
    mockReadFileSync.mockReturnValueOnce(JSON.stringify(content));

    const result = await handler.execute({ filename: "issue-42.json" }, CWD);

    expect(result.success).toBe(true);
    expect(result.output["schema_version"]).toBe("unknown");
  });

  it("prevents path traversal attacks", async () => {
    const result = await handler.execute({ filename: "../../../etc/passwd" }, CWD);

    expect(result.success).toBe(false);
    expect(result.output["error"]).toContain("path traversal");
  });

  it("reads from correct pipeline directory path", async () => {
    const content = { schema_version: "1.0" };
    mockReadFileSync.mockReturnValueOnce(JSON.stringify(content));

    await handler.execute({ filename: "dev-42.json" }, CWD);

    expect(mockReadFileSync).toHaveBeenCalledWith(
      expect.stringContaining(".nightgauge/pipeline/dev-42.json"),
      "utf-8"
    );
  });

  it("handles non-string filename input gracefully", async () => {
    const result = await handler.execute({ filename: 42 }, CWD);
    expect(result.success).toBe(false);
    expect(result.output["error"]).toContain("Missing required parameter");
  });
});

// ---------------------------------------------------------------------------
// ListContextFilesHandler
// ---------------------------------------------------------------------------

describe("ListContextFilesHandler", () => {
  let handler: ListContextFilesHandler;

  beforeEach(() => {
    handler = new ListContextFilesHandler();
    vi.clearAllMocks();
  });

  it('has name "list_context_files"', () => {
    expect(handler.name).toBe("list_context_files");
  });

  it("lists all JSON files in the pipeline directory", async () => {
    mockReaddirSync.mockReturnValueOnce([
      "issue-42.json",
      "planning-42.json",
      "readme.txt",
    ] as unknown as ReturnType<typeof readdirSync>);
    mockStatSync.mockReturnValue({
      size: 1024,
      mtime: new Date("2026-01-15T10:00:00Z"),
    } as ReturnType<typeof statSync>);

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(true);
    const files = result.output["files"] as Array<Record<string, unknown>>;
    expect(files).toHaveLength(2);
    expect(files[0]["filename"]).toBe("issue-42.json");
    expect(files[1]["filename"]).toBe("planning-42.json");
    expect(result.output["count"]).toBe(2);
  });

  it("filters files by regex pattern", async () => {
    mockReaddirSync.mockReturnValueOnce([
      "issue-42.json",
      "planning-42.json",
      "dev-42.json",
    ] as unknown as ReturnType<typeof readdirSync>);
    mockStatSync.mockReturnValue({
      size: 512,
      mtime: new Date("2026-01-15T10:00:00Z"),
    } as ReturnType<typeof statSync>);

    const result = await handler.execute({ pattern: "issue-.*\\.json" }, CWD);

    expect(result.success).toBe(true);
    const files = result.output["files"] as Array<Record<string, unknown>>;
    expect(files).toHaveLength(1);
    expect(files[0]["filename"]).toBe("issue-42.json");
  });

  it("returns empty array when pipeline directory does not exist", async () => {
    const err = Object.assign(new Error("ENOENT"), { code: "ENOENT" });
    mockReaddirSync.mockImplementationOnce(() => {
      throw err;
    });

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(true);
    expect(result.output["files"]).toEqual([]);
    expect(result.output["count"]).toBe(0);
  });

  it("returns error for invalid regex pattern", async () => {
    mockReaddirSync.mockReturnValueOnce(["issue-42.json"] as unknown as ReturnType<
      typeof readdirSync
    >);

    const result = await handler.execute({ pattern: "[invalid" }, CWD);

    expect(result.success).toBe(false);
    expect(result.output["error"]).toContain("Invalid regex pattern");
  });

  it("includes file size and modification time", async () => {
    mockReaddirSync.mockReturnValueOnce(["dev-42.json"] as unknown as ReturnType<
      typeof readdirSync
    >);
    mockStatSync.mockReturnValueOnce({
      size: 2048,
      mtime: new Date("2026-02-20T12:00:00Z"),
    } as ReturnType<typeof statSync>);

    const result = await handler.execute({}, CWD);

    const files = result.output["files"] as Array<Record<string, unknown>>;
    expect(files[0]["size_bytes"]).toBe(2048);
    expect(files[0]["modified_at"]).toBe("2026-02-20T12:00:00.000Z");
  });

  it("handles stat failure gracefully for individual files", async () => {
    mockReaddirSync.mockReturnValueOnce(["dev-42.json"] as unknown as ReturnType<
      typeof readdirSync
    >);
    mockStatSync.mockImplementationOnce(() => {
      throw new Error("permission denied");
    });

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(true);
    const files = result.output["files"] as Array<Record<string, unknown>>;
    expect(files[0]["size_bytes"]).toBe(0);
    expect(files[0]["modified_at"]).toBe("");
  });

  it("returns error for non-ENOENT directory read failures", async () => {
    const err = Object.assign(new Error("EACCES"), { code: "EACCES" });
    mockReaddirSync.mockImplementationOnce(() => {
      throw err;
    });

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(false);
    expect(result.output["error"]).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// createContextHandlers()
// ---------------------------------------------------------------------------

describe("createContextHandlers()", () => {
  it("returns a Map", () => {
    const handlers = createContextHandlers();
    expect(handlers).toBeInstanceOf(Map);
  });

  it("contains exactly 2 handlers", () => {
    const handlers = createContextHandlers();
    expect(handlers.size).toBe(2);
  });

  it("contains read_context_file handler", () => {
    const handlers = createContextHandlers();
    expect(handlers.has("read_context_file")).toBe(true);
    expect(handlers.get("read_context_file")).toBeInstanceOf(ReadContextFileHandler);
  });

  it("contains list_context_files handler", () => {
    const handlers = createContextHandlers();
    expect(handlers.has("list_context_files")).toBe(true);
    expect(handlers.get("list_context_files")).toBeInstanceOf(ListContextFilesHandler);
  });

  it("returns a new Map on each call", () => {
    const a = createContextHandlers();
    const b = createContextHandlers();
    expect(a).not.toBe(b);
  });

  it("each handler name matches its map key", () => {
    const handlers = createContextHandlers();
    for (const [key, handler] of handlers) {
      expect(handler.name).toBe(key);
    }
  });
});
