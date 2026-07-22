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
 *   - Token prefixes: ghp_, gho_, ghs_, ghr_, github_pat_, sk-, sk_live_,
 *     sk_test_, xox[bpars]-, AKIA…, ASIA…, glpat-… (GitLab PAT)
 *   - JWTs (three base64url segments separated by dots, length-bounded)
 *   - "...KEY=…", "...TOKEN=…", "...SECRET=…", "...PASSWORD=…" assignments
 *
 * @see Issue #170 - Harden session-log writer with redaction
 */
export function redactSecrets(input: string): string {
  if (!input) return input;
  let s = input;

  // PEM blocks — match across newlines (real and literal "\n")
  s = s.replace(
    /-----BEGIN [A-Z0-9 ]+-----[\s\S]*?-----END [A-Z0-9 ]+-----/g,
    "[REDACTED:PEM_BLOCK]"
  );
  // Token prefixes — capture up to a non-token boundary
  s = s.replace(/\b(ghp|gho|ghs|ghr|github_pat)_[A-Za-z0-9_]{16,}/g, "[REDACTED:GH_TOKEN]");
  s = s.replace(/\bglpat-[A-Za-z0-9_-]{16,}/g, "[REDACTED:GITLAB_TOKEN]");
  s = s.replace(/\bsk-[A-Za-z0-9-_]{16,}/g, "[REDACTED:OPENAI_KEY]");
  s = s.replace(/\bsk_(?:live|test)_[A-Za-z0-9]{16,}/g, "[REDACTED:STRIPE_KEY]");
  s = s.replace(/\bxox[bpars]-[A-Za-z0-9-]{10,}/g, "[REDACTED:SLACK_TOKEN]");
  s = s.replace(/\b(?:AKIA|ASIA)[A-Z0-9]{16}\b/g, "[REDACTED:AWS_ACCESS_KEY]");

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
