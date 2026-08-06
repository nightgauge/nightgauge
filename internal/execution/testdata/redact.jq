# Redaction applied to the raw `claude --output-format stream-json --verbose`
# capture before it was committed as a test fixture. Shape-preserving: only
# values are rewritten, no keys are added and no event ordering is changed.
def ph($s): $s;

# Drop local hook telemetry lines entirely (personal ~/.claude hook output).
select(.type != "system" or (.subtype | startswith("hook_") | not))

# system/init carries the whole local environment (skills, plugins, agents,
# slash_commands, memory_paths, cwd). Keep only what a stream consumer reads.
| if .subtype == "init" then
    {type, subtype, cwd: "/tmp/nightgauge-fixture", session_id, model, permissionMode, uuid}
  else . end

# Stable placeholder identifiers.
| walk(
    if type == "object" then
      (if has("session_id") then .session_id = "00000000-0000-4000-8000-000000000000" else . end)
      | (if has("uuid") then .uuid = "00000000-0000-4000-8000-000000000001" else . end)
      | (if has("request_id") then .request_id = "req_REDACTED" else . end)
      | (if has("signature") then .signature = "REDACTED" else . end)
    else . end
  )
