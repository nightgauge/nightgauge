import { describe, it, expect, beforeEach } from "vitest";
import { TemplateRegistry } from "../TemplateRegistry.js";
import type { PromptTemplate } from "../PromptTemplate.js";

function makeTemplate(
  name: string,
  version: string,
  content = `Hello from ${name}@${version}`
): PromptTemplate {
  return {
    name,
    version,
    layer: "skill",
    description: `Test template: ${name}`,
    params: [],
    content,
    filePath: `${name}-${version}.handlebars`,
  };
}

describe("TemplateRegistry", () => {
  let registry: TemplateRegistry;

  beforeEach(() => {
    registry = new TemplateRegistry();
  });

  describe("register()", () => {
    it("registers a template and makes it retrievable", () => {
      const t = makeTemplate("my-template", "1.0.0");
      registry.register(t);
      expect(registry.getTemplate("my-template")).toBe(t);
    });

    it("registers multiple versions of the same template", () => {
      const v1 = makeTemplate("my-template", "1.0.0");
      const v2 = makeTemplate("my-template", "2.0.0");
      registry.register(v1);
      registry.register(v2);
      expect(registry.size).toBe(2);
    });

    it("overwrites existing entry when same name@version re-registered", () => {
      const t1 = makeTemplate("t", "1.0.0", "first");
      const t2 = makeTemplate("t", "1.0.0", "second");
      registry.register(t1);
      registry.register(t2);
      // Still only one entry for 1.0.0
      expect(registry.size).toBe(1);
      expect(registry.getTemplate("t")!.content).toBe("second");
    });
  });

  describe("getTemplate()", () => {
    it("returns the latest version when no version specified", () => {
      const v1 = makeTemplate("t", "1.0.0");
      const v2 = makeTemplate("t", "2.0.0");
      const v3 = makeTemplate("t", "1.5.0");
      registry.register(v1);
      registry.register(v3);
      registry.register(v2);
      const result = registry.getTemplate("t");
      expect(result?.version).toBe("2.0.0");
    });

    it("returns a specific version when requested", () => {
      registry.register(makeTemplate("t", "1.0.0"));
      registry.register(makeTemplate("t", "2.0.0"));
      expect(registry.getTemplate("t", "1.0.0")?.version).toBe("1.0.0");
      expect(registry.getTemplate("t", "2.0.0")?.version).toBe("2.0.0");
    });

    it("returns null for unknown template name", () => {
      expect(registry.getTemplate("nonexistent")).toBeNull();
    });

    it("returns null for known name but unknown version", () => {
      registry.register(makeTemplate("t", "1.0.0"));
      expect(registry.getTemplate("t", "9.9.9")).toBeNull();
    });
  });

  describe("listTemplates()", () => {
    it("returns all templates when no name filter specified", () => {
      registry.register(makeTemplate("a", "1.0.0"));
      registry.register(makeTemplate("b", "1.0.0"));
      registry.register(makeTemplate("a", "2.0.0"));
      expect(registry.listTemplates()).toHaveLength(3);
    });

    it("filters by name when specified", () => {
      registry.register(makeTemplate("a", "1.0.0"));
      registry.register(makeTemplate("b", "1.0.0"));
      registry.register(makeTemplate("a", "2.0.0"));
      expect(registry.listTemplates("a")).toHaveLength(2);
      expect(registry.listTemplates("b")).toHaveLength(1);
    });

    it("returns empty array for unknown name", () => {
      expect(registry.listTemplates("does-not-exist")).toHaveLength(0);
    });
  });

  describe("size and isLoaded", () => {
    it("size reflects total registered templates", () => {
      expect(registry.size).toBe(0);
      registry.register(makeTemplate("a", "1.0.0"));
      expect(registry.size).toBe(1);
      registry.register(makeTemplate("a", "2.0.0"));
      expect(registry.size).toBe(2);
    });

    it("isLoaded starts false and does not change from register()", () => {
      expect(registry.isLoaded).toBe(false);
      registry.register(makeTemplate("t", "1.0.0"));
      // register() alone does not set isLoaded — only loadTemplates() does
      expect(registry.isLoaded).toBe(false);
    });
  });

  describe("clear()", () => {
    it("removes all templates and resets state", () => {
      registry.register(makeTemplate("a", "1.0.0"));
      registry.register(makeTemplate("b", "2.0.0"));
      registry.clear();
      expect(registry.size).toBe(0);
      expect(registry.getTemplate("a")).toBeNull();
      expect(registry.isLoaded).toBe(false);
    });
  });

  describe("semver sorting", () => {
    it("resolves 10.0.0 as newer than 9.0.0", () => {
      registry.register(makeTemplate("t", "9.0.0"));
      registry.register(makeTemplate("t", "10.0.0"));
      expect(registry.getTemplate("t")?.version).toBe("10.0.0");
    });

    it("resolves patch version correctly", () => {
      registry.register(makeTemplate("t", "1.0.9"));
      registry.register(makeTemplate("t", "1.0.10"));
      expect(registry.getTemplate("t")?.version).toBe("1.0.10");
    });
  });
});
