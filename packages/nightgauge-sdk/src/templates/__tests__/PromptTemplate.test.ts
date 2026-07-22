import { describe, it, expect } from "vitest";
import { parseTemplateFile } from "../PromptTemplate.js";

const VALID_TEMPLATE = `---
name: "test-template"
version: "1.0.0"
layer: "skill"
description: "A test template"
params:
  - name: "issueNumber"
    type: "number"
    description: "GitHub issue number"
    required: true
  - name: "title"
    type: "string"
    description: "Issue title"
    required: false
---
Hello issue #{{issueNumber}}{{#if title}}: {{title}}{{/if}}
`;

describe("parseTemplateFile", () => {
  it("parses a valid template with frontmatter and content", () => {
    const result = parseTemplateFile(VALID_TEMPLATE, "test.handlebars");

    expect(result.name).toBe("test-template");
    expect(result.version).toBe("1.0.0");
    expect(result.layer).toBe("skill");
    expect(result.description).toBe("A test template");
    expect(result.content).toContain("Hello issue #{{issueNumber}}");
    expect(result.filePath).toBe("test.handlebars");
  });

  it("parses params array from frontmatter", () => {
    const result = parseTemplateFile(VALID_TEMPLATE, "test.handlebars");

    expect(result.params).toHaveLength(2);
    expect(result.params[0].name).toBe("issueNumber");
    expect(result.params[0].type).toBe("number");
    expect(result.params[0].required).toBe(true);
    expect(result.params[1].name).toBe("title");
    expect(result.params[1].required).toBe(false);
  });

  it("handles template with no params", () => {
    const noParams = `---
name: "simple"
version: "1.0.0"
layer: "sdk"
description: "No params"
---
Static content here.
`;
    const result = parseTemplateFile(noParams, "simple.handlebars");
    expect(result.params).toHaveLength(0);
    expect(result.content).toContain("Static content here.");
  });

  it("throws when frontmatter delimiter is missing", () => {
    const noFrontmatter = "Just plain content without frontmatter";
    expect(() => parseTemplateFile(noFrontmatter, "bad.handlebars")).toThrow(
      "missing YAML frontmatter"
    );
  });

  it("throws when closing --- is absent", () => {
    const unclosed = `---
name: "test"
version: "1.0.0"
layer: "skill"
description: "unclosed"
Content without closing delimiter`;
    expect(() => parseTemplateFile(unclosed, "unclosed.handlebars")).toThrow(
      "unclosed frontmatter"
    );
  });

  it("throws when required frontmatter field name is missing", () => {
    const missingName = `---
version: "1.0.0"
layer: "skill"
description: "Missing name"
---
Content
`;
    expect(() => parseTemplateFile(missingName, "missing.handlebars")).toThrow(
      "missing required frontmatter field: name"
    );
  });

  it("throws when required frontmatter field version is missing", () => {
    const missingVersion = `---
name: "test"
layer: "skill"
description: "Missing version"
---
Content
`;
    expect(() => parseTemplateFile(missingVersion, "missing.handlebars")).toThrow(
      "missing required frontmatter field: version"
    );
  });

  it("throws on invalid layer value", () => {
    const invalidLayer = `---
name: "test"
version: "1.0.0"
layer: "invalid-layer"
description: "Bad layer"
---
Content
`;
    expect(() => parseTemplateFile(invalidLayer, "bad-layer.handlebars")).toThrow("invalid layer");
  });

  it("accepts all valid layer values", () => {
    const layers = ["skill", "sdk", "extension", "platform"] as const;
    for (const layer of layers) {
      const tmpl = `---\nname: "t"\nversion: "1.0.0"\nlayer: "${layer}"\ndescription: "test"\n---\ncontent\n`;
      const result = parseTemplateFile(tmpl, `${layer}.handlebars`);
      expect(result.layer).toBe(layer);
    }
  });

  it("preserves multi-line template content", () => {
    const multiline = `---
name: "multi"
version: "1.0.0"
layer: "skill"
description: "Multi-line"
---
Line one
Line two

Line four after blank
`;
    const result = parseTemplateFile(multiline, "multi.handlebars");
    expect(result.content).toContain("Line one");
    expect(result.content).toContain("Line two");
    expect(result.content).toContain("Line four after blank");
  });
});
