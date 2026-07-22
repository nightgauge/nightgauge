/**
 * AdapterRegistry - Maps adapter names to implementations.
 *
 * Replaces switch/if-chain dispatch in adapterQuery.ts and codexPreflight.ts.
 *
 * @see Issue #627 - Extract ICliAdapter interface & unify types
 */

import type { ICliAdapter, IncrediAdapter } from "./ICliAdapter.js";
import { ClaudeSdkAdapter } from "./ClaudeSdkAdapter.js";
import { ClaudeHeadlessAdapter } from "./ClaudeHeadlessAdapter.js";
import { CodexAdapter } from "./CodexAdapter.js";
import { GeminiAdapter } from "./GeminiAdapter.js";
import { GeminiSdkAdapter } from "./GeminiSdkAdapter.js";
import { LmStudioAdapter } from "./LmStudioAdapter.js";
import { OllamaAdapter } from "./OllamaAdapter.js";
import { CopilotCliAdapter } from "./CopilotCliAdapter.js";

/**
 * Registry that maps adapter names to their implementations.
 */
export class AdapterRegistry {
  private readonly adapters = new Map<IncrediAdapter, ICliAdapter>();

  register(adapter: ICliAdapter): void {
    this.adapters.set(adapter.name, adapter);
  }

  get(name: IncrediAdapter): ICliAdapter {
    const adapter = this.adapters.get(name);
    if (!adapter) {
      throw new Error(
        `Unknown adapter '${name}'. Registered adapters: ${[...this.adapters.keys()].join(", ")}`
      );
    }
    return adapter;
  }

  has(name: IncrediAdapter): boolean {
    return this.adapters.has(name);
  }

  getAll(): ICliAdapter[] {
    return [...this.adapters.values()];
  }

  getNames(): IncrediAdapter[] {
    return [...this.adapters.keys()];
  }
}

/**
 * Default registry with all built-in adapters.
 *
 * Uses lazy initialization to avoid circular dependency issues during
 * module loading. Adapter modules import from codexPreflight → adapter →
 * AdapterRegistry, so eager construction would fail for later adapters.
 */
function createDefaultRegistry(): AdapterRegistry {
  const registry = new AdapterRegistry();
  registry.register(new ClaudeSdkAdapter());
  registry.register(new ClaudeHeadlessAdapter());
  registry.register(new CodexAdapter());
  registry.register(new GeminiAdapter());
  registry.register(new GeminiSdkAdapter());
  registry.register(new LmStudioAdapter());
  registry.register(new OllamaAdapter()); // Issue #2591
  registry.register(new CopilotCliAdapter());
  return registry;
}

let _defaultRegistry: AdapterRegistry | undefined;

/** Singleton default registry instance (lazily initialized). */
export const defaultRegistry: AdapterRegistry = new Proxy({} as AdapterRegistry, {
  get(_target, prop, receiver) {
    if (!_defaultRegistry) {
      _defaultRegistry = createDefaultRegistry();
    }
    return Reflect.get(_defaultRegistry, prop, receiver);
  },
});

/**
 * TRUE when the named adapter drives a real agentic tool loop and may run
 * pipeline stages (#57). Accepts any layer's adapter vocabulary: the VSCode
 * `claude` alias maps to `claude-sdk`; unknown names are non-agentic
 * (fail-closed). Consult this at PIPELINE DISPATCH only — eval/judge/
 * summarization surfaces intentionally accept chat-only adapters.
 */
export function isAgenticAdapter(adapter: string): boolean {
  const canonical = (adapter === "claude" ? "claude-sdk" : adapter) as IncrediAdapter;
  return defaultRegistry.has(canonical) && defaultRegistry.get(canonical).agentic;
}
