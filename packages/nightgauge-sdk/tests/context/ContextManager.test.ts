import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import {
  ContextManager,
  ContextNotFoundError,
  ContextValidationError,
} from "../../src/context/ContextManager.js";
import { IssueContextSchema, type IssueContext } from "../../src/context/schemas/index.js";

describe("ContextManager", () => {
  let tempDir: string;
  let contextManager: ContextManager;

  beforeEach(async () => {
    tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "nightgauge-sdk-test-"));
    contextManager = new ContextManager(tempDir);
  });

  afterEach(async () => {
    await fs.rm(tempDir, { recursive: true, force: true });
  });

  describe("write and read", () => {
    it("should write and read a valid context file", async () => {
      const issueContext: IssueContext = {
        schema_version: "1.0",
        issue_number: 42,
        title: "Test Issue",
        type: "feature",
        branch: "feat/42-test",
        base_branch: "main",
        requirements: {
          summary: "Test summary",
        },
        labels: ["feature"],
        created_at: "2026-02-01T12:00:00Z",
      };

      await contextManager.write(IssueContextSchema, "issue-42.json", issueContext);

      const read = await contextManager.read(IssueContextSchema, "issue-42.json");
      expect(read).toEqual(issueContext);
    });

    it("should create directory if it does not exist", async () => {
      const nestedDir = path.join(tempDir, "nested", "context");
      const nestedManager = new ContextManager(nestedDir);

      const issueContext: IssueContext = {
        schema_version: "1.0",
        issue_number: 1,
        title: "Test",
        type: "bug",
        branch: "fix/1-test",
        base_branch: "main",
        requirements: { summary: "Fix bug" },
        labels: [],
        created_at: "2026-02-01T12:00:00Z",
      };

      await nestedManager.write(IssueContextSchema, "issue-1.json", issueContext);

      const exists = await nestedManager.exists("issue-1.json");
      expect(exists).toBe(true);
    });
  });

  describe("read errors", () => {
    it("should throw ContextNotFoundError for missing files", async () => {
      await expect(contextManager.read(IssueContextSchema, "issue-999.json")).rejects.toThrow(
        ContextNotFoundError
      );
    });

    it("should include helpful error message", async () => {
      try {
        await contextManager.read(IssueContextSchema, "issue-999.json");
        expect.fail("Should have thrown");
      } catch (error) {
        expect(error).toBeInstanceOf(ContextNotFoundError);
        const notFoundError = error as ContextNotFoundError;
        expect(notFoundError.filename).toContain("issue-999.json");
        expect(notFoundError.createdBy).toBe("/nightgauge-issue-pickup");
      }
    });

    it("should throw error for invalid JSON", async () => {
      await fs.writeFile(path.join(tempDir, "invalid.json"), "not json", "utf-8");

      await expect(contextManager.read(IssueContextSchema, "invalid.json")).rejects.toThrow(
        "Failed to parse JSON"
      );
    });

    it("should throw ContextValidationError for schema mismatch", async () => {
      // IssueContextSchema uses .catch() defaults for most fields, so
      // only a truly invalid schema_version (required, no default) fails.
      await fs.writeFile(
        path.join(tempDir, "bad-schema.json"),
        JSON.stringify({ schema_version: "not-semver", wrong_field: true }),
        "utf-8"
      );

      await expect(contextManager.read(IssueContextSchema, "bad-schema.json")).rejects.toThrow(
        ContextValidationError
      );
    });
  });

  describe("exists", () => {
    it("should return true for existing files", async () => {
      await fs.writeFile(path.join(tempDir, "exists.json"), "{}", "utf-8");
      expect(await contextManager.exists("exists.json")).toBe(true);
    });

    it("should return false for non-existing files", async () => {
      expect(await contextManager.exists("does-not-exist.json")).toBe(false);
    });
  });

  describe("delete", () => {
    it("should delete existing files", async () => {
      await fs.writeFile(path.join(tempDir, "to-delete.json"), "{}", "utf-8");
      await contextManager.delete("to-delete.json");
      expect(await contextManager.exists("to-delete.json")).toBe(false);
    });

    it("should not throw for non-existing files", async () => {
      await expect(contextManager.delete("does-not-exist.json")).resolves.not.toThrow();
    });
  });

  describe("cleanup", () => {
    it("should delete all context files for an issue", async () => {
      const files = [
        "issue-42.json",
        "planning-42.json",
        "dev-42.json",
        "validate-42.json",
        "pr-42.json",
      ];

      for (const file of files) {
        await fs.writeFile(path.join(tempDir, file), "{}", "utf-8");
      }

      const deleted = await contextManager.cleanup(42);

      expect(deleted).toHaveLength(5);
      for (const file of files) {
        expect(await contextManager.exists(file)).toBe(false);
      }
    });

    it("should only delete existing files", async () => {
      await fs.writeFile(path.join(tempDir, "issue-42.json"), "{}", "utf-8");

      const deleted = await contextManager.cleanup(42);

      expect(deleted).toHaveLength(1);
      expect(deleted[0]).toBe("issue-42.json");
    });
  });

  describe("list", () => {
    it("should list all JSON files", async () => {
      await fs.writeFile(path.join(tempDir, "file1.json"), "{}", "utf-8");
      await fs.writeFile(path.join(tempDir, "file2.json"), "{}", "utf-8");
      await fs.writeFile(path.join(tempDir, "file.txt"), "", "utf-8");

      const files = await contextManager.list();

      expect(files).toHaveLength(2);
      expect(files).toContain("file1.json");
      expect(files).toContain("file2.json");
    });

    it("should filter by pattern", async () => {
      await fs.writeFile(path.join(tempDir, "issue-1.json"), "{}", "utf-8");
      await fs.writeFile(path.join(tempDir, "issue-2.json"), "{}", "utf-8");
      await fs.writeFile(path.join(tempDir, "planning-1.json"), "{}", "utf-8");

      const files = await contextManager.list(/^issue-/);

      expect(files).toHaveLength(2);
      expect(files).toContain("issue-1.json");
      expect(files).toContain("issue-2.json");
    });

    it("should return empty array for non-existing directory", async () => {
      const nonExistentManager = new ContextManager("/non/existent/path");
      const files = await nonExistentManager.list();
      expect(files).toEqual([]);
    });
  });

  describe("getFilename", () => {
    it("should generate correct filenames", () => {
      expect(ContextManager.getFilename("issue", 42)).toBe("issue-42.json");
      expect(ContextManager.getFilename("planning", 42)).toBe("planning-42.json");
      expect(ContextManager.getFilename("dev", 42)).toBe("dev-42.json");
      expect(ContextManager.getFilename("validate", 42)).toBe("validate-42.json");
      expect(ContextManager.getFilename("pr", 42)).toBe("pr-42.json");
    });
  });

  describe("getBasePath", () => {
    it("should return the base path", () => {
      expect(contextManager.getBasePath()).toBe(tempDir);
    });
  });

  describe("atomic writes", () => {
    const validIssueContext: IssueContext = {
      schema_version: "1.0",
      issue_number: 99,
      title: "Atomic write test",
      type: "feature",
      branch: "feat/99-atomic",
      base_branch: "main",
      requirements: { summary: "Test atomic writes" },
      labels: ["feature"],
      created_at: "2026-02-22T12:00:00Z",
    };

    it("should leave no .tmp files after successful write", async () => {
      await contextManager.write(IssueContextSchema, "issue-99.json", validIssueContext);

      const files = await fs.readdir(tempDir);
      const tmpFiles = files.filter((f) => f.endsWith(".tmp"));
      expect(tmpFiles).toHaveLength(0);

      const read = await contextManager.read(IssueContextSchema, "issue-99.json");
      expect(read).toEqual(validIssueContext);
    });

    it("should propagate errors and leave no temp files on write failure", async () => {
      // Make the directory read-only so writeFile to the temp file fails
      const readonlyDir = path.join(tempDir, "readonly-ctx");
      await fs.mkdir(readonlyDir);
      await fs.chmod(readonlyDir, 0o444);

      const readonlyManager = new ContextManager(readonlyDir);

      await expect(
        readonlyManager.write(IssueContextSchema, "issue-99.json", validIssueContext)
      ).rejects.toThrow();

      // Restore permissions for cleanup
      await fs.chmod(readonlyDir, 0o755);

      const files = await fs.readdir(readonlyDir);
      const tmpFiles = files.filter((f) => f.endsWith(".tmp"));
      expect(tmpFiles).toHaveLength(0);
    });

    it("should produce valid JSON under concurrent writes", async () => {
      const writes = Array.from({ length: 5 }, (_, i) =>
        contextManager.write(IssueContextSchema, "issue-99.json", {
          ...validIssueContext,
          title: `Concurrent write ${i}`,
        })
      );

      await Promise.all(writes);

      const content = await fs.readFile(path.join(tempDir, "issue-99.json"), "utf-8");
      const parsed = JSON.parse(content);
      expect(parsed.schema_version).toBe("1.0");
      expect(parsed.issue_number).toBe(99);
      expect(parsed.title).toMatch(/^Concurrent write \d$/);
    });
  });
});
