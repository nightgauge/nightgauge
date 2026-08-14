/**
 * Parse Grok Build headless `--output-format streaming-json` (and the
 * terminal `json` object) into text, token buckets, optional cost, and
 * quota/auth signals.
 *
 * @see Issue #526
 */

export interface GrokUsageBuckets {
  input_tokens: number;
  output_tokens: number;
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
  reasoning_tokens: number;
}

export interface GrokStreamSummary {
  displayText: string;
  sessionId?: string;
  usage: GrokUsageBuckets;
  /** Present only when the vendor stamped a complete cost. */
  totalCostUsd?: number;
  costIsPartial: boolean;
  usageIsIncomplete: boolean;
  hasExplicitFailure: boolean;
  failureReason?: string;
  isQuotaExhausted: boolean;
  isAuthFailure: boolean;
  servedModel?: string;
}

const EMPTY_USAGE: GrokUsageBuckets = {
  input_tokens: 0,
  output_tokens: 0,
  cache_read_input_tokens: 0,
  cache_creation_input_tokens: 0,
  reasoning_tokens: 0,
};

const QUOTA_RE =
  /\b(weekly (usage )?pool|usage pool exhausted|usage (limit|paused)|quota (exhausted|exceeded)|rate limit)\b/i;
const AUTH_RE =
  /\b(not authenticated|authentication failed|unauthori[sz]ed|invalid api key|please (run )?grok login)\b/i;

export function isGrokQuotaMessage(text: string): boolean {
  return QUOTA_RE.test(text);
}

export function isGrokAuthMessage(text: string): boolean {
  return AUTH_RE.test(text);
}

function asNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function readUsage(raw: unknown): GrokUsageBuckets | undefined {
  if (!raw || typeof raw !== "object") return undefined;
  const u = raw as Record<string, unknown>;
  return {
    input_tokens: asNumber(u.input_tokens),
    output_tokens: asNumber(u.output_tokens),
    cache_read_input_tokens: asNumber(u.cache_read_input_tokens),
    cache_creation_input_tokens: asNumber(u.cache_creation_input_tokens),
    reasoning_tokens: asNumber(u.reasoning_tokens),
  };
}

function applyUsage(into: GrokUsageBuckets, next: GrokUsageBuckets): void {
  into.input_tokens = Math.max(into.input_tokens, next.input_tokens);
  into.output_tokens = Math.max(into.output_tokens, next.output_tokens);
  into.cache_read_input_tokens = Math.max(
    into.cache_read_input_tokens,
    next.cache_read_input_tokens
  );
  into.cache_creation_input_tokens = Math.max(
    into.cache_creation_input_tokens,
    next.cache_creation_input_tokens
  );
  into.reasoning_tokens = Math.max(into.reasoning_tokens, next.reasoning_tokens);
}

/**
 * Summarize a Grok headless stdout capture (NDJSON streaming-json, or a
 * single terminal json object).
 */
export function summarizeGrokStream(stdout: string): GrokStreamSummary {
  const summary: GrokStreamSummary = {
    displayText: "",
    usage: { ...EMPTY_USAGE },
    costIsPartial: false,
    usageIsIncomplete: false,
    hasExplicitFailure: false,
    isQuotaExhausted: false,
    isAuthFailure: false,
  };

  const chunks: string[] = [];
  const trimmed = stdout.trim();
  if (!trimmed) return summary;

  const lines = trimmed.startsWith("{") && !trimmed.includes("\n") ? [trimmed] : trimmed.split("\n");

  for (const rawLine of lines) {
    const line = rawLine.trim();
    if (!line || line[0] !== "{") continue;
    let ev: Record<string, unknown>;
    try {
      ev = JSON.parse(line) as Record<string, unknown>;
    } catch {
      continue;
    }

    const type = typeof ev.type === "string" ? ev.type : "";
    if (type === "text" && typeof ev.data === "string") {
      chunks.push(ev.data);
    } else if (type === "error") {
      const message = typeof ev.message === "string" ? ev.message : "grok error";
      summary.hasExplicitFailure = true;
      summary.failureReason = message;
      if (isGrokQuotaMessage(message)) summary.isQuotaExhausted = true;
      if (isGrokAuthMessage(message)) summary.isAuthFailure = true;
    } else if (type === "end" || type === "") {
      if (typeof ev.text === "string" && ev.text.trim()) {
        summary.displayText = ev.text.trim();
      }
    }

    const session = ev.sessionId ?? ev.session_id;
    if (typeof session === "string" && session) summary.sessionId = session;

    const usage = readUsage(ev.usage);
    if (usage) applyUsage(summary.usage, usage);

    if (ev.cost_is_partial === true) summary.costIsPartial = true;
    if (ev.usage_is_incomplete === true) summary.usageIsIncomplete = true;

    if (typeof ev.total_cost_usd === "number" && ev.cost_is_partial !== true) {
      summary.totalCostUsd = ev.total_cost_usd;
    }

    const modelUsage = ev.modelUsage;
    if (modelUsage && typeof modelUsage === "object") {
      const ids = Object.keys(modelUsage as object);
      if (ids.length > 0) summary.servedModel = ids[ids.length - 1];
    }
    if (typeof ev.model === "string" && ev.model) summary.servedModel = ev.model;
  }

  if (!summary.displayText && chunks.length > 0) {
    summary.displayText = chunks.join("");
  }

  const blob = `${summary.displayText}\n${summary.failureReason ?? ""}`;
  if (isGrokQuotaMessage(blob)) summary.isQuotaExhausted = true;
  if (isGrokAuthMessage(blob)) summary.isAuthFailure = true;

  return summary;
}
