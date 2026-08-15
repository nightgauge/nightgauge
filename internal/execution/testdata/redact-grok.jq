# Redaction applied to the raw `grok --output-format streaming-json` capture
# before it was committed as a test fixture. Shape-preserving in the same sense
# as redact.jq: only values are rewritten, keys are dropped but never added or
# reordered, no event is reordered, and no token count is touched.

# `available_commands` is Grok's analogue of the claude CLI's `system/init`
# line: it enumerates the whole local environment (every installed plugin's
# slash commands). Keep only the built-in tool roster a stream consumer could
# read; drop `commands` the way redact.jq drops init's `slash_commands`.
if .type == "available_commands" then {type, tools} else . end

# Absolute local paths appear inside tool_call_update payloads (`current_dir`,
# `output_file`), not only in known top-level keys.
| walk(
    if type == "string" then
      gsub("/Users/[^ \",]*"; "/tmp/nightgauge-fixture")
      | gsub("/private/tmp/[^ \",]*"; "/tmp/nightgauge-fixture")
      | gsub("/tmp/claude-[^ \",]*"; "/tmp/nightgauge-fixture")
    else . end
  )

# Stable placeholder identifiers. Grok spells these camelCase; `signature` is
# the model's opaque reasoning signature and carries no fixture value.
| walk(
    if type == "object" then
      (if has("sessionId") then .sessionId = "00000000-0000-4000-8000-000000000000" else . end)
      | (if has("requestId") then .requestId = "req_REDACTED" else . end)
      | (if has("signature") then .signature = "REDACTED" else . end)
    else . end
  )
