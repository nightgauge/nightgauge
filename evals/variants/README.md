# Prompt Variants

Named overlays on the prompt text an eval cell executes under (#72). The
on-disk task instruction is the implicit `baseline` variant; each file here
defines one named variant that transforms that text **without editing anything
on disk**. Running `scripts/evaluate-models.ts --variants <name>` executes
`{baseline, <name>} × {models}` and prints a per-`(variant, model)`
composite-score delta — negative Δ means the variant **regressed** against the
unmodified prompt.

## File format

One `<name>.json` per variant, validated against `PromptVariantSchema`
(`packages/nightgauge-sdk/src/eval/promptVariants.ts`). The `name` field must
match the filename. At least one overlay op is required:

```json
{
  "name": "example-concise-preamble",
  "description": "Does a brevity preamble change quality or cost?",
  "prepend": "Be concise. Prefer the smallest correct change.",
  "append": "Before finishing, re-check the requirements once.",
  "replacements": [{ "find": "exact text in the instruction", "replace_with": "new text" }]
}
```

- `prepend` / `append` wrap the instruction.
- `replacements` are exact-match substitutions applied first, in order. A
  `find` that does not occur in the instruction **fails the run** — a variant
  that silently fails to apply measures nothing while claiming to.
- `baseline` is reserved and never defined as a file.

## Usage

```bash
# A/B one variant against baseline across the default model set (mock wiring):
npx tsx scripts/evaluate-models.ts --variants example-concise-preamble

# The real measurement (live models + judge; costs API money):
npx tsx scripts/evaluate-models.ts --mode live --judge --variants example-concise-preamble
```

Records persist to `.nightgauge/model-evals/*.jsonl` with the variant on
`cell.prompt_variant`, so a variant can also be compared against a baseline
captured on an earlier run (`computeVariantDeltas` accepts concatenated record
sets).

See `docs/MODEL_EVALUATION.md` § Prompt-variant axis for design and rationale.
