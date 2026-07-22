import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { KnowledgeService } from "../services/KnowledgeService.js";
import type { RepoTopicType } from "../context/schemas/knowledge.js";

async function makeTempRoot(): Promise<string> {
  return fs.mkdtemp(path.join(os.tmpdir(), "kb-repo-topic-test-"));
}

async function removeTempRoot(dir: string): Promise<void> {
  await fs.rm(dir, { recursive: true, force: true });
}

describe("KnowledgeService.createRepoTopicEntry", () => {
  let tempRoot: string;
  let service: KnowledgeService;

  beforeEach(async () => {
    tempRoot = await makeTempRoot();
    service = new KnowledgeService(tempRoot);
  });

  afterEach(async () => {
    await removeTempRoot(tempRoot);
  });

  const allTypes: RepoTopicType[] = ["architecture", "glossary", "runbook", "post-mortem"];

  it.each(allTypes)("creates entry file for type %s", async (type) => {
    const result = await service.createRepoTopicEntry(type, "test-entry");

    expect(result.created).toBe(true);
    expect(result.files_created).toContain("test-entry.md");

    // Entry file must exist and contain the slug
    const content = await fs.readFile(result.file_path, "utf-8");
    expect(content).toContain("test-entry");
    expect(content).toContain("---"); // YAML frontmatter
    expect(content).toContain(`type: ${type}`);
  });

  it.each(allTypes)("creates README.md and _template.md for new category %s", async (type) => {
    const result = await service.createRepoTopicEntry(type, "test-entry");

    expect(result.files_created).toContain("README.md");
    expect(result.files_created).toContain("_template.md");

    const readmeContent = await fs.readFile(path.join(result.knowledge_path, "README.md"), "utf-8");
    expect(readmeContent).toContain(`# Knowledge Base — ${type}`);

    const templateContent = await fs.readFile(
      path.join(result.knowledge_path, "_template.md"),
      "utf-8"
    );
    expect(templateContent).toContain("---"); // YAML frontmatter
  });

  it("is idempotent — second call returns created=false", async () => {
    await service.createRepoTopicEntry("glossary", "knowledge-path");

    const second = await service.createRepoTopicEntry("glossary", "knowledge-path");

    expect(second.created).toBe(false);
    expect(second.files_created).toHaveLength(0);
  });

  it("does not overwrite existing entry on second call", async () => {
    const first = await service.createRepoTopicEntry("glossary", "wave");
    const _originalContent = await fs.readFile(first.file_path, "utf-8");

    // Overwrite with custom content to verify it is not clobbered
    await fs.writeFile(first.file_path, "# CUSTOM CONTENT\n", "utf-8");

    await service.createRepoTopicEntry("glossary", "wave");
    const afterContent = await fs.readFile(first.file_path, "utf-8");

    expect(afterContent).toBe("# CUSTOM CONTENT\n");
  });

  it("second entry in existing category does not recreate README or _template", async () => {
    await service.createRepoTopicEntry("runbook", "first-runbook");
    const second = await service.createRepoTopicEntry("runbook", "second-runbook");

    expect(second.created).toBe(true);
    // Only the entry file — not README or _template
    expect(second.files_created).toEqual(["second-runbook.md"]);
  });

  it("rejects unknown type", async () => {
    await expect(
      // @ts-expect-error — intentionally invalid type
      service.createRepoTopicEntry("invalid-type", "test")
    ).rejects.toThrow();
  });

  it("glossary entry contains domain-term tag", async () => {
    const result = await service.createRepoTopicEntry("glossary", "epic");
    const content = await fs.readFile(result.file_path, "utf-8");
    expect(content).toContain("domain-term");
  });

  it("architecture entry contains architecture tag", async () => {
    const result = await service.createRepoTopicEntry("architecture", "sse-transport");
    const content = await fs.readFile(result.file_path, "utf-8");
    expect(content).toContain("architecture");
  });

  it("runbook entry contains procedure heading structure", async () => {
    const result = await service.createRepoTopicEntry("runbook", "restart-pipeline");
    const content = await fs.readFile(result.file_path, "utf-8");
    expect(content).toContain("## Purpose");
    expect(content).toContain("## Steps");
  });

  it("post-mortem entry contains incident structure", async () => {
    const result = await service.createRepoTopicEntry("post-mortem", "pipeline-crash-2026");
    const content = await fs.readFile(result.file_path, "utf-8");
    expect(content).toContain("## Summary");
    expect(content).toContain("## Root Cause");
    expect(content).toContain("## Action Items");
  });
});
