/**
 * PromptRenderer - Handlebars-based template renderer
 *
 * Wraps Handlebars compilation with caching, safe error handling,
 * and support for nested objects, arrays, and conditionals.
 *
 * @see docs/PROMPT_TEMPLATES.md for template syntax reference
 */

import Handlebars from "handlebars";
import type { PromptTemplate } from "./PromptTemplate.js";

/** Context object passed to template rendering */
export type TemplateContext = Record<string, unknown>;

/** A compiled Handlebars template function */
type CompiledTemplate = HandlebarsTemplateDelegate<TemplateContext>;

/**
 * PromptRenderer - renders PromptTemplate instances with a given context.
 *
 * Instances maintain a compiled template cache keyed by
 * `{name}@{version}` to avoid re-compiling on repeated calls.
 *
 * @example
 * ```typescript
 * const renderer = new PromptRenderer();
 * const output = renderer.render(template, { issueNumber: 42, repoName: 'my-repo' });
 * ```
 */
export class PromptRenderer {
  private readonly cache = new Map<string, CompiledTemplate>();

  /**
   * Render a template with the given context variables.
   *
   * Missing variables render as empty string (Handlebars default).
   * Use `{{{variable}}}` in templates to skip HTML escaping.
   *
   * @param template - The PromptTemplate to render
   * @param context - Variable bindings for Handlebars substitution
   * @returns Rendered string
   * @throws Error when Handlebars compilation or rendering fails
   */
  render(template: PromptTemplate, context: TemplateContext = {}): string {
    const compiled = this.getCompiled(template);
    try {
      return compiled(context);
    } catch (error) {
      throw new Error(
        `Failed to render template "${template.name}@${template.version}": ${
          error instanceof Error ? error.message : String(error)
        }`,
        { cause: error }
      );
    }
  }

  /**
   * Render raw Handlebars content (without a PromptTemplate wrapper).
   * Useful for one-off rendering without a registered template.
   *
   * @param content - Raw Handlebars template string
   * @param context - Variable bindings
   * @returns Rendered string
   */
  renderRaw(content: string, context: TemplateContext = {}): string {
    try {
      const compiled = Handlebars.compile(content, { noEscape: true });
      return compiled(context);
    } catch (error) {
      throw new Error(
        `Failed to render template content: ${
          error instanceof Error ? error.message : String(error)
        }`,
        { cause: error }
      );
    }
  }

  /**
   * Pre-warm the cache by compiling a template without rendering it.
   * Useful during startup to avoid first-render latency.
   */
  precompile(template: PromptTemplate): void {
    this.getCompiled(template);
  }

  /**
   * Clear the compiled template cache.
   * Typically only needed in tests or after template hot-reloading.
   */
  clearCache(): void {
    this.cache.clear();
  }

  /**
   * Return the number of cached compiled templates.
   */
  get cacheSize(): number {
    return this.cache.size;
  }

  private getCompiled(template: PromptTemplate): CompiledTemplate {
    const cacheKey = `${template.name}@${template.version}`;
    const cached = this.cache.get(cacheKey);
    if (cached) return cached;

    let compiled: CompiledTemplate;
    try {
      // noEscape: true — prompts are plain text, not HTML
      compiled = Handlebars.compile(template.content, { noEscape: true });
    } catch (error) {
      throw new Error(
        `Failed to compile template "${template.name}@${template.version}" ` +
          `(${template.filePath}): ${error instanceof Error ? error.message : String(error)}`,
        { cause: error }
      );
    }

    this.cache.set(cacheKey, compiled);
    return compiled;
  }
}

/**
 * Shared default renderer instance.
 * Use this unless you need isolated cache behaviour (e.g., in tests).
 */
export const defaultRenderer = new PromptRenderer();
