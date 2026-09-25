/**
 * Adapter names that no longer exist (#2128). Local models run through the
 * `opencode` adapter against any OpenAI-compatible server, and the eval judge
 * uses the `openai-compatible` backend. A config, flag or environment
 * variable that still names one fails loudly instead of falling back to a
 * different adapter. Mirrors RetiredAdapterError in
 * internal/config/retired_adapters.go.
 */
const RETIRED_ADAPTERS: ReadonlySet<string> = new Set([
  "lm-studio",
  "lm_studio",
  "lmstudio",
  "ollama",
]);

/** Whether `name` is a removed adapter name. */
export function isRetiredAdapter(name: string | undefined | null): boolean {
  return typeof name === "string" && RETIRED_ADAPTERS.has(name.trim().toLowerCase());
}

/**
 * The migration message for a retired adapter name, or `undefined` when the
 * name is not retired. `where` names the setting that carried it.
 */
export function retiredAdapterMessage(
  name: string | undefined | null,
  where: string
): string | undefined {
  if (!isRetiredAdapter(name)) return undefined;
  return (
    `${where} names adapter "${name}", which was removed (#2128): local models now run ` +
    `through the opencode adapter against any OpenAI-compatible server ` +
    `(LM Studio, Ollama, llama.cpp, vLLM), declared under opencode.endpoints; ` +
    `the eval judge uses the openai-compatible backend. ` +
    `Set the adapter to opencode (or openai-compatible for the judge)`
  );
}
