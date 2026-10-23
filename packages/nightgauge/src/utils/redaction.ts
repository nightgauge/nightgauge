/**
 * Redact common secret shapes from a free-form string.
 *
 * Value-based (not key-name based): safe to run over any text that may have
 * captured a credential — webhook payloads, error strings, and on-disk session
 * logs. Extracted from `services/notifications/transport.ts` in #170 so both the
 * notifier transport and the log-file writer share one redactor without a
 * util→service dependency. `transport.ts` re-exports this symbol, so existing
 * importers are unaffected.
 *
 * Defense-in-depth: callers should not embed secrets in the first place, but we
 * cannot trust every code path (stage stdout, tool_result output) that flows
 * into a log line or a webhook.
 *
 * Patterns covered:
 *   - PEM blocks (PRIVATE KEY, RSA PRIVATE KEY, EC PRIVATE KEY, etc.)
 *   - Token prefixes: ghp_, gho_, ghu_, ghs_, ghr_, github_pat_, sk-ant-, sk-,
 *     sk_live_, sk_test_, xox[bpars]-, AKIA…, ASIA…, glpat-… (GitLab PAT)
 *   - JWTs (three base64url segments separated by dots, length-bounded)
 *   - Bearer credentials, Gemini API keys, and webhook URLs
 *   - "...KEY=…", "...TOKEN=…", "...SECRET=…", "...PASSWORD=…" assignments
 *
 * @see Issue #170 - Harden session-log writer with redaction
 */
const PEM_BEGIN = "-----BEGIN ";
const PEM_END = "-----END ";
const PEM_ARMOUR = "-----";

/**
 * Replace every `-----BEGIN …----- … -----END …-----` block with a placeholder.
 *
 * Scanned with `indexOf` rather than matched with a regex. The natural pattern
 * (`/-----BEGIN [A-Z0-9 ]+-----[\s\S]*?-----END [A-Z0-9 ]+-----/`) re-scans the
 * remainder of the string from every `-----BEGIN ` that has no matching footer,
 * which is quadratic on input containing many unterminated headers — CodeQL
 * `js/polynomial-redos`, and reachable here because the redactor runs over
 * untrusted stage stdout and webhook payloads.
 *
 * Matching is deliberately looser than the old label class: any header text is
 * accepted, so a novel PEM label still redacts. Both real newlines and literal
 * `\n` escapes are covered, because nothing constrains the block body.
 */
function redactPemBlocks(input: string): string {
  if (!input.includes(PEM_BEGIN)) return input;

  let out = "";
  let cursor = 0;
  for (;;) {
    const begin = input.indexOf(PEM_BEGIN, cursor);
    if (begin === -1) break;
    const headerEnd = input.indexOf(PEM_ARMOUR, begin + PEM_BEGIN.length);
    if (headerEnd === -1) break;
    const footer = input.indexOf(PEM_END, headerEnd + PEM_ARMOUR.length);
    if (footer === -1) break;
    const footerEnd = input.indexOf(PEM_ARMOUR, footer + PEM_END.length);
    if (footerEnd === -1) break;

    out += input.slice(cursor, begin) + "[REDACTED:PEM_BLOCK]";
    cursor = footerEnd + PEM_ARMOUR.length;
  }
  return out + input.slice(cursor);
}

export function redactSecrets(input: string): string {
  if (!input) return input;
  let s = input;

  // PEM blocks — match across newlines (real and literal "\n")
  s = redactPemBlocks(s);
  // Token prefixes — capture up to a non-token boundary
  // `ghu` (user-to-server) was missing from this list until #1335 — the same
  // sweep that found a github_pat_ value reaching an evidence artifact.
  s = s.replace(/\b(ghp|gho|ghu|ghs|ghr|github_pat)_[A-Za-z0-9_]{16,}/g, "[REDACTED:GH_TOKEN]");
  s = s.replace(/\bglpat-[A-Za-z0-9_-]{16,}/g, "[REDACTED:GITLAB_TOKEN]");
  s = s.replace(/\bsk-ant-[A-Za-z0-9-_]{16,}/g, "[REDACTED:ANTHROPIC_KEY]");
  s = s.replace(/\bsk-[A-Za-z0-9-_]{16,}/g, "[REDACTED:OPENAI_KEY]");
  s = s.replace(/\bsk_(?:live|test)_[A-Za-z0-9]{16,}/g, "[REDACTED:STRIPE_KEY]");
  s = s.replace(/\bxox[bpars]-[A-Za-z0-9-]{10,}/g, "[REDACTED:SLACK_TOKEN]");
  s = s.replace(/\b(?:AKIA|ASIA)[A-Z0-9]{16}\b/g, "[REDACTED:AWS_ACCESS_KEY]");
  s = s.replace(/\bAIza[A-Za-z0-9_-]{30,}\b/g, "[REDACTED:GEMINI_KEY]");

  // Authorization headers and webhook URLs often do not use a distinctive
  // token prefix. Keep the scheme visible while removing the credential.
  s = s.replace(/\bBearer\s+[A-Za-z0-9._~+/=-]{12,}/gi, "Bearer [REDACTED]");
  s = s.replace(
    /https:\/\/(?:discord(?:app)?\.com\/api\/webhooks|hooks\.slack\.com\/services|[^\s/]+\/hooks)\/[^\s"'<>]+/gi,
    "[REDACTED:WEBHOOK_URL]"
  );

  // JWTs — header.payload.signature, base64url
  s = s.replace(
    /\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b/g,
    "[REDACTED:JWT]"
  );

  // KEY=value / TOKEN=value / SECRET=value / PASSWORD=value assignments
  s = s.replace(
    /\b([A-Z0-9_]*(?:KEY|TOKEN|SECRET|PASSWORD|PASSWD|API_KEY))\s*[:=]\s*['"]?([A-Za-z0-9+/_=\-.]{12,})['"]?/g,
    (_m, k) => `${k}=[REDACTED]`
  );

  return s;
}
