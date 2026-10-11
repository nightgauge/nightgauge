# Redaction applied to the raw `claude ... --output-format stream-json
# --include-partial-messages` captures before they were committed as test
# fixtures. Shape-preserving: only values are rewritten and whole lines
# dropped; no key is added and no event is reordered.

# Drop local hook telemetry and the account's rate-limit window lines.
select(.type != "rate_limit_event")
| select(.type != "system" or (.subtype | startswith("hook_") | not))

# system/init carries the whole local environment (skills, plugins, agents,
# slash_commands, cwd). Keep only what a stream consumer reads.
| if .type == "system" and .subtype == "init" then
    {type, subtype, cwd: "/tmp/nightgauge-fixture", session_id, tools, mcp_servers, model,
     permissionMode, per_turn_effort_active, claude_code_version, uuid}
  else . end

# Absolute local paths can appear in any string value.
| walk(
    if type == "string" then
      gsub("/Users/[^ \",]*"; "/tmp/nightgauge-fixture")
      | gsub("/private/tmp/[^ \",]*"; "/tmp/nightgauge-fixture")
    else . end
  )

# Stable placeholder identifiers; a thinking signature is opaque. The
# per-line api_message_id is replaced too (a secret scanner reads the key name
# as an API key); the message id a parser keys on is message.id, kept.
| walk(
    if type == "object" then
      (if has("session_id") then .session_id = "00000000-0000-4000-8000-000000000000" else . end)
      | (if has("uuid") then .uuid = "00000000-0000-4000-8000-000000000001" else . end)
      | (if has("request_id") then .request_id = "req_REDACTED" else . end)
      | (if has("signature") then .signature = "REDACTED" else . end)
      | (if has("api_message_id") then .api_message_id = "msg_REDACTED" else . end)
    else . end
  )
