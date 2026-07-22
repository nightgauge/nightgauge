/**
 * Prompt-variant axis for the model-eval matrix (Issue #72).
 *
 * A prompt variant is a NAMED OVERLAY on the prompt text a cell executes
 * under: the on-disk task instruction is the implicit `baseline` variant, and
 * a variant transforms that text (prepend / append / targeted replacements)
 * without editing anything on disk. Running the suite over
 * `{baseline, variant} × {model}` yields a composite-score delta per pair —
 * the measurement that turns "this skill text is better" from an opinion into
 * a number (and a REGRESSION into a negative delta).
 *
 * Variants are deliberately attached to the executed instruction, not the
 * Handlebars template registry (`src/templates/`): the eval executor builds
 * its prompt from `task.instruction` and never renders those templates, so
 * routing variants through the registry would measure text the cells do not
 * actually run under. A variant CAN carry template-derived text; the overlay
 * is where it must land either way.
 *
 * Definitions live as one JSON file per variant (default `evals/variants/`),
 * validated against {@link PromptVariantSchema}. Overlay application is
 * strict: a replacement whose `find` text is absent throws, because a variant
 * that silently fails to apply measures nothing while claiming to.
 *
 * @see docs/MODEL_EVALUATION.md — prompt-variant axis
 */

import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { z } from "zod";
import { BASELINE_PROMPT_VARIANT } from "./modelEvalSchemas.js";

/** One targeted text substitution inside the task instruction. */
export const VariantReplacementSchema = z
  .object({
    /** Exact text that must exist in the instruction. */
    find: z.string().min(1),
    /** Text it is replaced with (may be empty to delete). */
    replace_with: z.string(),
  })
  .strict();
export type VariantReplacement = z.infer<typeof VariantReplacementSchema>;

/** A named overlay on the executed prompt text. */
export const PromptVariantSchema = z
  .object({
    /** Kebab-case identity carried on the matrix cell (`cell.prompt_variant`). */
    name: z
      .string()
      .min(1)
      .regex(/^[a-z0-9-]+$/, "variant name must be kebab-case ([a-z0-9-])"),
    /** What this variant tests, for humans reading the results. */
    description: z.string().optional(),
    /** Text inserted before the task instruction. */
    prepend: z.string().optional(),
    /** Text appended after the task instruction. */
    append: z.string().optional(),
    /** Targeted substitutions applied to the instruction, in order. */
    replacements: z.array(VariantReplacementSchema).optional(),
  })
  .strict()
  .superRefine((v, ctx) => {
    if (v.name === BASELINE_PROMPT_VARIANT) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        message: `"${BASELINE_PROMPT_VARIANT}" is reserved for the unmodified on-disk text and cannot be defined as a variant file`,
      });
    }
    if (!v.prepend && !v.append && !(v.replacements && v.replacements.length > 0)) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        message:
          "a variant must declare at least one overlay op (prepend, append, or replacements)",
      });
    }
  });
export type PromptVariant = z.infer<typeof PromptVariantSchema>;

/**
 * Apply a variant overlay to an instruction. `undefined` (or baseline) returns
 * the instruction untouched. Replacements are applied first (each `find` MUST
 * be present — a silent no-op would fake a measurement), then prepend/append
 * wrap the result.
 */
export function applyPromptVariant(instruction: string, variant?: PromptVariant): string {
  if (!variant) return instruction;
  let text = instruction;
  for (const r of variant.replacements ?? []) {
    if (!text.includes(r.find)) {
      throw new Error(
        `prompt variant "${variant.name}": replacement target not found in instruction: ${JSON.stringify(r.find)}`
      );
    }
    text = text.split(r.find).join(r.replace_with);
  }
  if (variant.prepend) text = `${variant.prepend}\n\n${text}`;
  if (variant.append) text = `${text}\n\n${variant.append}`;
  return text;
}

/**
 * Resolve a cell's variant name against the loaded definitions. Baseline maps
 * to `undefined` (no overlay); an unknown name throws — that is a harness
 * configuration error, not a model-quality signal.
 */
export function resolveVariant(
  variants: ReadonlyMap<string, PromptVariant> | undefined,
  name: string
): PromptVariant | undefined {
  if (name === BASELINE_PROMPT_VARIANT) return undefined;
  const variant = variants?.get(name);
  if (!variant) {
    throw new Error(
      `prompt variant "${name}" is not loaded — pass its definition to the executor (--variants / variants option)`
    );
  }
  return variant;
}

/**
 * Load named variant definitions from a directory of `<name>.json` files.
 * The file's `name` field must match its filename so records stay greppable.
 */
export async function loadPromptVariants(
  dir: string,
  names: string[]
): Promise<Map<string, PromptVariant>> {
  const loaded = new Map<string, PromptVariant>();
  for (const name of names) {
    if (name === BASELINE_PROMPT_VARIANT) continue; // implicit, never a file
    const file = join(dir, `${name}.json`);
    let raw: string;
    try {
      raw = await readFile(file, "utf-8");
    } catch {
      throw new Error(`prompt variant "${name}": cannot read ${file}`);
    }
    const variant = PromptVariantSchema.parse(JSON.parse(raw));
    if (variant.name !== name) {
      throw new Error(
        `prompt variant file ${file} declares name "${variant.name}" — must match its filename`
      );
    }
    loaded.set(name, variant);
  }
  return loaded;
}
