/**
 * PromptTemplate - Template definition and file loader
 *
 * Defines the structure of a prompt template and provides utilities
 * for loading templates from `.handlebars` files with YAML frontmatter.
 *
 * @see docs/PROMPT_TEMPLATES.md for template format specification
 */

import { load as yamlLoad } from "js-yaml";

/**
 * Layer that a template targets
 */
export type TemplateLayer = "skill" | "sdk" | "extension" | "platform";

/**
 * Parameter documentation for a template variable
 */
export interface TemplateParam {
  /** Variable name used in Handlebars syntax: {{name}} */
  name: string;
  /** TypeScript-style type hint */
  type: "string" | "number" | "boolean" | "object" | "array";
  /** Human-readable description */
  description: string;
  /** Whether the variable must be provided for rendering */
  required?: boolean;
  /** Default value when the variable is absent */
  default?: string | number | boolean;
}

/**
 * Metadata stored in the YAML frontmatter of a template file
 */
export interface TemplateMetadata {
  /** Unique name identifying this template (e.g., "issue-pickup-system") */
  name: string;
  /** Semantic version string (e.g., "1.0.0") */
  version: string;
  /** Product layer this template belongs to */
  layer: TemplateLayer;
  /** Short human-readable description */
  description: string;
  /** Parameter definitions (documentation only — not enforced at runtime) */
  params?: TemplateParam[];
  /** Original file path this template was loaded from */
  filePath?: string;
}

/**
 * A loaded and parsed prompt template
 */
export interface PromptTemplate {
  /** Unique identifier (from frontmatter `name` field) */
  name: string;
  /** Semantic version (from frontmatter `version` field) */
  version: string;
  /** Product layer (from frontmatter `layer` field) */
  layer: TemplateLayer;
  /** Human-readable description */
  description: string;
  /** Parameter documentation */
  params: TemplateParam[];
  /** Raw Handlebars template content (without frontmatter) */
  content: string;
  /** Original file path */
  filePath: string;
}

/**
 * Frontmatter separator used in template files
 */
const FRONTMATTER_DELIMITER = "---";

/**
 * Parse YAML frontmatter using js-yaml (already a project dependency).
 */
function parseFrontmatter(raw: string): Record<string, unknown> {
  const parsed = yamlLoad(raw);
  if (parsed === null || parsed === undefined) return {};
  if (typeof parsed !== "object" || Array.isArray(parsed)) return {};
  return parsed as Record<string, unknown>;
}

/**
 * Parse a `.handlebars` template file into a PromptTemplate.
 *
 * File format:
 * ```
 * ---
 * name: "my-template"
 * version: "1.0.0"
 * layer: "skill"
 * description: "Short description"
 * params:
 *   - name: "issueNumber"
 *     type: "number"
 *     description: "GitHub issue number"
 *     required: true
 * ---
 *
 * Template content using {{variable}} Handlebars syntax.
 * ```
 *
 * @param content - Raw file content
 * @param filePath - Path the file was loaded from (for error messages)
 * @returns Parsed PromptTemplate
 * @throws Error when required frontmatter fields are missing
 */
export function parseTemplateFile(content: string, filePath: string): PromptTemplate {
  const trimmed = content.trimStart();

  if (!trimmed.startsWith(FRONTMATTER_DELIMITER)) {
    throw new Error(
      `Template file ${filePath} is missing YAML frontmatter. ` + `File must start with ---`
    );
  }

  // Find closing ---
  const afterFirst = trimmed.slice(3);
  const closeIdx = afterFirst.indexOf(`\n${FRONTMATTER_DELIMITER}`);
  if (closeIdx === -1) {
    throw new Error(`Template file ${filePath} has unclosed frontmatter (missing closing ---)`);
  }

  const frontmatterRaw = afterFirst.slice(0, closeIdx).trim();
  const templateContent = afterFirst.slice(closeIdx + 4).trimStart(); // +4 for \n---

  const meta = parseFrontmatter(frontmatterRaw);

  // Validate required fields
  for (const field of ["name", "version", "layer", "description"] as const) {
    if (!meta[field]) {
      throw new Error(`Template file ${filePath} is missing required frontmatter field: ${field}`);
    }
  }

  const validLayers: TemplateLayer[] = ["skill", "sdk", "extension", "platform"];
  const layer = meta["layer"] as string;
  if (!validLayers.includes(layer as TemplateLayer)) {
    throw new Error(
      `Template file ${filePath} has invalid layer "${layer}". Must be one of: ${validLayers.join(", ")}`
    );
  }

  // Parse params array (optional)
  const rawParams = meta["params"];
  const params: TemplateParam[] = Array.isArray(rawParams)
    ? (rawParams as Record<string, unknown>[]).map((p) => ({
        name: String(p["name"] ?? ""),
        type: (p["type"] ?? "string") as TemplateParam["type"],
        description: String(p["description"] ?? ""),
        required: p["required"] === true || p["required"] === "true",
        default: p["default"] as TemplateParam["default"],
      }))
    : [];

  return {
    name: String(meta["name"]),
    version: String(meta["version"]),
    layer: layer as TemplateLayer,
    description: String(meta["description"]),
    params,
    content: templateContent,
    filePath,
  };
}
