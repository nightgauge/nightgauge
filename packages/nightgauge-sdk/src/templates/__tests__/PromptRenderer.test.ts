import { describe, it, expect, beforeEach } from "vitest";
import { PromptRenderer } from "../PromptRenderer.js";
import type { PromptTemplate } from "../PromptTemplate.js";

function makeTemplate(content: string, name = "test", version = "1.0.0"): PromptTemplate {
  return {
    name,
    version,
    layer: "skill",
    description: "test template",
    params: [],
    content,
    filePath: `${name}.handlebars`,
  };
}

describe("PromptRenderer", () => {
  let renderer: PromptRenderer;

  beforeEach(() => {
    renderer = new PromptRenderer();
  });

  it("renders a template with all variables provided", () => {
    const template = makeTemplate("Issue #{{issueNumber}}: {{title}}");
    const result = renderer.render(template, {
      issueNumber: 42,
      title: "Add dark mode",
    });
    expect(result).toBe("Issue #42: Add dark mode");
  });

  it("renders missing variables as empty string (Handlebars default)", () => {
    const template = makeTemplate("Hello {{name}}!");
    const result = renderer.render(template, {});
    expect(result).toBe("Hello !");
  });

  it("renders nested object property access", () => {
    const template = makeTemplate("Repo: {{issue.repo}}, Title: {{issue.title}}");
    const result = renderer.render(template, {
      issue: { repo: "my-repo", title: "Fix bug" },
    });
    expect(result).toBe("Repo: my-repo, Title: Fix bug");
  });

  it("renders {{#if}} conditional blocks", () => {
    const template = makeTemplate("{{#if showExtra}}Extra content{{/if}} Base");
    expect(renderer.render(template, { showExtra: true })).toBe("Extra content Base");
    expect(renderer.render(template, { showExtra: false })).toBe(" Base");
    expect(renderer.render(template, {})).toBe(" Base");
  });

  it("renders {{#unless}} conditional blocks", () => {
    const template = makeTemplate("{{#unless hidden}}Visible{{/unless}}");
    expect(renderer.render(template, { hidden: false })).toBe("Visible");
    expect(renderer.render(template, { hidden: true })).toBe("");
  });

  it("renders {{#each}} array iteration", () => {
    const template = makeTemplate("{{#each items}}{{this}},{{/each}}");
    const result = renderer.render(template, { items: ["a", "b", "c"] });
    expect(result).toBe("a,b,c,");
  });

  it("does not HTML-escape content (noEscape: true)", () => {
    const template = makeTemplate("Code: {{code}}");
    const result = renderer.render(template, { code: "<b>bold</b>" });
    // Should NOT escape < > to &lt; &gt;
    expect(result).toBe("Code: <b>bold</b>");
  });

  it("caches compiled templates and reuses them", () => {
    const template = makeTemplate("{{value}}", "cached-template");
    renderer.render(template, { value: "first" });
    expect(renderer.cacheSize).toBe(1);
    renderer.render(template, { value: "second" });
    // Still 1 — same template was reused from cache
    expect(renderer.cacheSize).toBe(1);
  });

  it("uses separate cache entries for different template versions", () => {
    const v1 = makeTemplate("v1: {{value}}", "versioned", "1.0.0");
    const v2 = makeTemplate("v2: {{value}}", "versioned", "2.0.0");
    renderer.render(v1, { value: "x" });
    renderer.render(v2, { value: "y" });
    expect(renderer.cacheSize).toBe(2);
  });

  it("clearCache() empties the compiled template cache", () => {
    const template = makeTemplate("{{value}}");
    renderer.render(template, { value: "x" });
    expect(renderer.cacheSize).toBe(1);
    renderer.clearCache();
    expect(renderer.cacheSize).toBe(0);
  });

  it("precompile() populates the cache without rendering", () => {
    const template = makeTemplate("Hello {{name}}");
    expect(renderer.cacheSize).toBe(0);
    renderer.precompile(template);
    expect(renderer.cacheSize).toBe(1);
  });

  it("renderRaw() renders content directly without a PromptTemplate", () => {
    const result = renderer.renderRaw("Value: {{x}}", { x: 99 });
    expect(result).toBe("Value: 99");
  });

  it("throws a descriptive error for invalid Handlebars syntax in template", () => {
    const bad = makeTemplate("{{#unclosed}}");
    // Handlebars throws at compile or render time — either message is acceptable
    expect(() => renderer.render(bad, {})).toThrow(/Failed to (compile|render) template/);
  });
});
