import fs from "fs";
import os from "os";
import path from "path";
import { afterEach, describe, expect, it } from "vitest";
import { listCodexModels } from "@nightgauge/sdk";
import { CodexModelCatalogService } from "../../src/services/CodexModelCatalogService";

const tempDirs: string[] = [];

afterEach(() => {
  for (const dir of tempDirs.splice(0)) {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

function createCodexHome(contents?: unknown): string {
  const codexHome = fs.mkdtempSync(path.join(os.tmpdir(), "codex-model-catalog-"));
  tempDirs.push(codexHome);

  if (contents !== undefined) {
    fs.writeFileSync(
      path.join(codexHome, "models_cache.json"),
      JSON.stringify(contents, null, 2),
      "utf-8"
    );
  }

  return codexHome;
}

describe("CodexModelCatalogService", () => {
  it("returns visible, registry-valid models ordered by priority", () => {
    const codexHome = createCodexHome({
      models: [
        { slug: "gpt-5.4-mini", visibility: "list", priority: 4 },
        { slug: "codex-auto-review", visibility: "hide", priority: 1 },
        { slug: "gpt-5.4", visibility: "list", priority: 2 },
        { slug: "gpt-5.5", visibility: "list", priority: 1 },
      ],
    });

    const service = new CodexModelCatalogService(codexHome);
    expect(service.listModels()).toEqual(["gpt-5.5", "gpt-5.4", "gpt-5.4-mini"]);
  });

  it("filters deprecated/invalid cached slugs through the registry", () => {
    // gpt-5.3-codex is deprecated in the canonical registry — a stale daemon
    // cache must never surface it (Issue #4018).
    const codexHome = createCodexHome({
      models: [
        { slug: "gpt-5.4", visibility: "list", priority: 2 },
        { slug: "gpt-5.3-codex", visibility: "list", priority: 1 },
        { slug: "gpt-5-mini", visibility: "list", priority: 3 },
      ],
    });

    const service = new CodexModelCatalogService(codexHome);
    const models = service.listModels();
    expect(models).toEqual(["gpt-5.4"]);
    expect(models).not.toContain("gpt-5.3-codex");
    expect(models).not.toContain("gpt-5-mini");
  });

  it("falls back to the registry list when the cache file is missing", () => {
    const service = new CodexModelCatalogService(createCodexHome());
    expect(service.listModels()).toEqual(listCodexModels());
  });

  it("falls back to the registry list when the cache file is invalid", () => {
    const codexHome = createCodexHome();
    fs.writeFileSync(path.join(codexHome, "models_cache.json"), "{bad json", "utf-8");

    const service = new CodexModelCatalogService(codexHome);
    expect(service.listModels()).toEqual(listCodexModels());
  });

  it("falls back to the registry list when no cached slug survives filtering", () => {
    const codexHome = createCodexHome({
      models: [{ slug: "gpt-5.3-codex", visibility: "list", priority: 1 }],
    });

    const service = new CodexModelCatalogService(codexHome);
    expect(service.listModels()).toEqual(listCodexModels());
  });
});
