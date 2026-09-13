# Redaction applied to real `exec`-style CLI stream captures (codex today;
# gemini and copilot follow the same shape family when captured) before they
# are committed as test fixtures (#1620). Shape-preserving in the same sense
# as redact.jq / redact-grok.jq: only values are rewritten, no key is added,
# no event is reordered, and no token count is touched.

# Absolute local paths can appear inside any string value — a command's
# argv, its aggregated output, a cwd field — not only in known top-level
# keys, so this walks every string rather than keying off field names.
walk(
    if type == "string" then
      gsub("/Users/[^ \",]*"; "/tmp/nightgauge-fixture")
      | gsub("/private/tmp/[^ \",]*"; "/tmp/nightgauge-fixture")
      | gsub("/private/var/[^ \",]*"; "/tmp/nightgauge-fixture")
      | gsub("/home/[^ \",]*"; "/tmp/nightgauge-fixture")
      | gsub("[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}"; "user@example.com")
    else . end
  )

# Stable placeholder identifiers. Each CLI in this family spells its
# session/request id differently — codex's `thread.started` event carries
# `thread_id`; gemini and copilot use `session_id`/`sessionId` and
# `request_id`/`requestId` — so all four keys are covered here even though
# today's capture only exercises `thread_id`.
| walk(
    if type == "object" then
      (if has("thread_id") then .thread_id = "00000000-0000-4000-8000-000000000000" else . end)
      | (if has("session_id") then .session_id = "00000000-0000-4000-8000-000000000000" else . end)
      | (if has("sessionId") then .sessionId = "00000000-0000-4000-8000-000000000000" else . end)
      | (if has("request_id") then .request_id = "req_REDACTED" else . end)
      | (if has("requestId") then .requestId = "req_REDACTED" else . end)
    else . end
  )
