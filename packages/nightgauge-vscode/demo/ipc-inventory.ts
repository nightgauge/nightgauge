/**
 * Builds the IPC inventory (#2103) from the logging stub's JSONL log.
 *
 * The VSCode host tier runs the real extension against `demo/ipc-stub.cjs`.
 * The stub logs every request; the in-host harness appends a `marker` line
 * before each case, so every request is attributed to the case (activation,
 * a tree view, a dashboard tab) that was running when it arrived. Async work
 * started by one case can land during the next, so `surfaces` means "observed
 * during", not "caused by".
 *
 * The result shape each caller expects is read from the typed client, not
 * from the stub (which answers everything with `null`): the generated
 * `src/services/IpcClient.generated.ts` first, then the hand-written wrappers
 * in `src/services/IpcClient.ts`. `client` records which one declares it.
 */

export interface IpcLogEntry {
  ts?: string;
  method?: string;
  params?: unknown;
  paramsShape?: unknown;
  /** Written by the host harness, not the stub: the case now running. */
  marker?: string;
}

export interface InventoryEntry {
  method: string;
  /** The type argument of `this.call<…>` in the typed client. */
  resultType: string | null;
  /** Which typed client declares the call; null when neither does. */
  client: "generated" | "manual" | null;
  /** Host-tier cases during which the method was called, in first-seen order. */
  surfaces: string[];
  /** Params of the first observed call, with machine-local paths redacted. */
  sampleParams: unknown;
  paramsShape: unknown;
}

export interface IpcInventory {
  description: string;
  protocolVersion: number;
  methods: InventoryEntry[];
}

/** Surface label for requests logged before the harness wrote any marker. */
export const BEFORE_FIRST_CASE = "activation (before the first case)";

export function parseLog(text: string): IpcLogEntry[] {
  return text
    .split("\n")
    .filter((line) => line.trim().length > 0)
    .map((line) => JSON.parse(line) as IpcLogEntry);
}

/** Map of method name to the result type the generated client declares. */
export function resultTypesFromClient(source: string): Map<string, string> {
  const types = new Map<string, string>();
  const callPattern = /this\.call<([\s\S]*?)>\(\s*['"]([\w.]+)['"]/g;
  for (const match of source.matchAll(callPattern)) {
    const type = match[1]
      .replace(/import\("[^"]+"\)\./g, "")
      .replace(/\s+/g, " ")
      .trim();
    if (!types.has(match[2])) types.set(match[2], type);
  }
  return types;
}

export function protocolVersionFromClient(source: string): number {
  const match = /export const IPC_PROTOCOL_VERSION = (\d+);/.exec(source);
  if (!match) {
    throw new Error("IPC_PROTOCOL_VERSION not found in the generated client");
  }
  return Number(match[1]);
}

/** Params whose name marks a credential; their values never reach the inventory. */
const SECRET_KEY = /token|secret|password|key$/i;

/**
 * Replace each `from` substring with its label, recursively through strings,
 * and blank the value of every credential-named key.
 */
export function redact(value: unknown, replacements: ReadonlyArray<[string, string]>): unknown {
  if (typeof value === "string") {
    let out = value;
    for (const [from, to] of replacements) {
      if (from) out = out.split(from).join(to);
    }
    return out;
  }
  if (Array.isArray(value)) return value.map((item) => redact(item, replacements));
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value).map(([key, item]) => [
        key,
        SECRET_KEY.test(key) && item ? "<redacted>" : redact(item, replacements),
      ])
    );
  }
  return value;
}

export function buildInventory(
  entries: readonly IpcLogEntry[],
  clientSource: string,
  manualClientSource: string,
  replacements: ReadonlyArray<[string, string]> = []
): IpcInventory {
  const generated = resultTypesFromClient(clientSource);
  const manual = resultTypesFromClient(manualClientSource);
  const byMethod = new Map<string, InventoryEntry>();
  let surface = BEFORE_FIRST_CASE;

  for (const entry of entries) {
    if (entry.marker !== undefined) {
      surface = entry.marker;
      continue;
    }
    if (!entry.method) continue;
    let item = byMethod.get(entry.method);
    if (!item) {
      item = {
        method: entry.method,
        resultType: generated.get(entry.method) ?? manual.get(entry.method) ?? null,
        client: generated.has(entry.method)
          ? "generated"
          : manual.has(entry.method)
            ? "manual"
            : null,
        surfaces: [],
        sampleParams: redact(entry.params ?? null, replacements),
        paramsShape: entry.paramsShape ?? null,
      };
      byMethod.set(entry.method, item);
    }
    if (!item.surfaces.includes(surface)) item.surfaces.push(surface);
  }

  return {
    description:
      "IPC methods the VS Code extension calls at activation, while resolving each tree " +
      "view and while opening each dashboard tab, recorded against demo/ipc-stub.cjs by " +
      "the vscode-host tier. Regenerate: npm run -w nightgauge-vscode demo:inventory",
    protocolVersion: protocolVersionFromClient(clientSource),
    methods: [...byMethod.values()].sort((a, b) => a.method.localeCompare(b.method)),
  };
}
