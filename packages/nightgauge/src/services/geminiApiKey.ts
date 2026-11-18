/**
 * The Gemini API key from SecretStorage, held in memory for the processes
 * that need it.
 *
 * It used to be copied into the extension's own `process.env`, so every
 * process the extension started (any adapter's stage, git, gh, the daemon and
 * every stage the daemon started) inherited it. Now it is handed only to the
 * daemon, whose stage environment gives it to the Gemini adapters alone, and
 * to a Gemini stage this extension spawns itself (ADR-023 Decision 7).
 */

let storedKey: string | undefined;

/** Reads the key from SecretStorage once at activation. Never writes `process.env`. */
export async function loadGeminiApiKey(secrets: {
  getApiKey(name: "gemini"): Promise<string | undefined>;
}): Promise<void> {
  storedKey = (await secrets.getApiKey("gemini")) || undefined;
}

/** Whether a Gemini key is available, from SecretStorage or the environment. */
export function hasGeminiApiKey(): boolean {
  return Boolean(storedKey || process.env.GEMINI_API_KEY || process.env.GOOGLE_API_KEY);
}

/**
 * The variable a process that runs Gemini needs: the stored key as
 * GEMINI_API_KEY, unless the environment already sets one (an operator's
 * exported key wins, as it did before).
 */
export function geminiApiKeyEnv(): Record<string, string> {
  if (!storedKey || process.env.GEMINI_API_KEY) return {};
  return { GEMINI_API_KEY: storedKey };
}

/** Test hook: forget the loaded key. */
export function resetGeminiApiKeyForTest(): void {
  storedKey = undefined;
}
