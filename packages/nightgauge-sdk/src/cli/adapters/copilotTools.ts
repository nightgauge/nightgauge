/**
 * Copilot tool policy — translate a stage's Claude-style allowed tools into
 * Copilot CLI permission flags (#1). Mirrors the Go copilotToolPolicy in
 * internal/execution/adapters/copilot_tools.go; both are pinned by the shared
 * fixture internal/execution/testdata/copilot-tool-policy.json.
 *
 * An empty list keeps `--allow-all-tools`. A non-empty list never emits it:
 * each entry maps to a narrower flag, and an entry with no Copilot equivalent
 * is denied and returned in `dropped`, so access is never broadened.
 *
 * Syntax source: https://docs.github.com/en/copilot/concepts/agents/about-copilot-cli
 * ("Allowed tools"), read 2026-10-28; not verified against a live CLI.
 */

const WRITE_TOOLS = new Set(["Write", "Edit", "MultiEdit", "NotebookEdit"]);
/** Read-only tools: Copilot has no tool spec for reading inside the cwd. */
const UNGATED_TOOLS = new Set(["Read", "Grep", "Glob", "LS"]);

export interface CopilotToolPolicy {
  args: string[];
  dropped: string[];
}

function baseName(entry: string): string {
  const i = entry.indexOf("(");
  return (i === -1 ? entry : entry.slice(0, i)).trim();
}

function scopeOf(entry: string): string {
  const open = entry.indexOf("(");
  if (open === -1 || !entry.endsWith(")")) return "";
  let s = entry.slice(open + 1, -1).trim();
  if (s.endsWith(":*")) s = s.slice(0, -2);
  else if (s.endsWith(" *")) s = s.slice(0, -2);
  return s === "*" ? "" : s.trim();
}

export function copilotToolPolicy(allowed?: readonly string[]): CopilotToolPolicy {
  const specs: string[] = [];
  const urls: string[] = [];
  const dropped: string[] = [];
  let allUrls = false;
  let nonEmpty = false;
  const addUnique = (list: string[], v: string) => {
    if (!list.includes(v)) list.push(v);
  };
  for (const raw of allowed ?? []) {
    const entry = raw.trim();
    if (entry === "") continue;
    nonEmpty = true;
    const name = baseName(entry);
    const scope = scopeOf(entry);
    if (name === "Bash") {
      addUnique(specs, scope === "" ? "shell" : `shell(${scope})`);
    } else if (WRITE_TOOLS.has(name)) {
      addUnique(specs, "write");
    } else if (UNGATED_TOOLS.has(name)) {
      // no flag needed
    } else if (name === "WebFetch" || name === "WebSearch") {
      const domain = scope.startsWith("domain:") ? scope.slice("domain:".length) : "";
      if (domain !== "") addUnique(urls, domain);
      else allUrls = true;
    } else if (name.startsWith("mcp__")) {
      const rest = name.slice("mcp__".length);
      const sep = rest.indexOf("__");
      const server = sep === -1 ? rest : rest.slice(0, sep);
      const tool = sep === -1 ? "" : rest.slice(sep + 2);
      if (server !== "" && sep === -1) addUnique(specs, server);
      else if (server !== "" && tool !== "") addUnique(specs, `${server}(${tool})`);
      else dropped.push(entry);
    } else {
      dropped.push(entry);
    }
  }
  if (!nonEmpty) return { args: ["--allow-all-tools"], dropped: [] };
  const args = specs.flatMap((s) => ["--allow-tool", s]);
  if (allUrls) args.push("--allow-all-urls");
  else for (const u of urls) args.push("--allow-url", u);
  return { args, dropped };
}

/**
 * A copilot argv with the stage's tool policy applied. Without an allow list,
 * or when args already name `--allow-tool` (an operator's
 * NIGHTGAUGE_COPILOT_CLI_ARGS), the args are kept as given. Otherwise
 * `--allow-all-tools` is removed and the policy flags are appended; dropped
 * entries are warned about.
 */
export function withCopilotToolPolicy(
  args: readonly string[],
  allowedTools?: readonly string[]
): string[] {
  const named = args.some((a) => a === "--allow-tool" || a.startsWith("--allow-tool="));
  if (!allowedTools || allowedTools.length === 0 || named) return [...args];
  const policy = copilotToolPolicy(allowedTools);
  if (policy.dropped.length > 0) {
    console.warn(
      `copilot: allowed-tools entries have no Copilot equivalent and are denied: ${policy.dropped.join(", ")}`
    );
  }
  const rest = args.filter((a) => a !== "--allow-all-tools");
  const policyArgs = policy.args.filter((a) => a !== "--allow-all-tools");
  return [...policyArgs, ...rest];
}
