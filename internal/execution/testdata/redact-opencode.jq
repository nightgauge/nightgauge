# Redaction applied to a raw `opencode run --format json` capture, and to its
# stderr, before either is committed as a test fixture. Run by
# scripts/capture-opencode-fixture.sh, which then refuses to write anything
# that still matches a credential shape.
#
#   jq -c -s --arg mode stream --argjson roots '["<sandbox>"]' -f redact-opencode.jq raw.jsonl
#   jq -j -R -s --arg mode stderr --argjson roots '["<sandbox>"]' -f redact-opencode.jq raw.stderr
#
# Shape-preserving in the same sense as redact.jq and redact-grok.jq: only
# string values are rewritten. No key is added, dropped or reordered, no event
# is reordered, and no token count or cost is touched.

def fixture_root: "/tmp/nightgauge-fixture";

# What may come right before a credential, as credentialLeft in
# opencode_usage.go: the start, a character no credential holds, a JSON escape
# ending in a letter or digit, or a terminal escape sequence (the stderr keeps
# its escape codes).
def left: "(?<l>^|[^A-Za-z0-9_]|\\\\[bfnrt]|\\\\u[0-9A-Fa-f]{4}|(?:\\x1b|\\\\u001[bB])\\[[0-9;?]*[A-Za-z])";

# The credential shapes RedactCredentials (opencode_usage.go) removes from a
# live stage's output, so a fixture never holds what a stage's log would not.
def redact_credentials:
  gsub(left + "(?:sk-[A-Za-z0-9_-]{20,}|xai-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{35}|(?:AKIA|ASIA)[0-9A-Z]{16}|gsk_[A-Za-z0-9]{20,}|hf_[A-Za-z0-9]{30,})"; "\(.l)[REDACTED:api-key]")
  | gsub(left + "(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,}|glpat-[A-Za-z0-9_-]{20,})"; "\(.l)[REDACTED:forge-token]")
  | gsub(left + "(?<p>bearer\\s+)[A-Za-z0-9._~+/-]{16,}=*"; "\(.l)\(.p)[REDACTED:bearer-token]"; "i")
  | gsub(left + "(?<p>authorization\\s*[:=]\\s*(?:basic|token)\\s+)[A-Za-z0-9._~+/-]{8,}=*"; "\(.l)\(.p)[REDACTED:authorization]"; "i")
  | gsub(left + "(?<p>[A-Za-z][A-Za-z0-9+.-]*://)[^\\s/@:\"'\\\\]+:[^\\s/@\"'\\\\]+@"; "\(.l)\(.p)[REDACTED:userinfo]@")
  | gsub("(?<p>[?&](?:api[_-]?key|apikey|key|access[_-]?token|auth[_-]?token|token|client[_-]?secret|secret|password|passwd|pwd|sig|signature|x-amz-signature|x-amz-credential|x-amz-security-token|x-goog-signature|x-goog-credential)=)(?!\\[REDACTED)[^&#\\s\"'\\\\]+"; "\(.p)[REDACTED:query-credential]"; "i");

# The capture's throwaway directories, as given and as the OS resolved them
# (macOS resolves /tmp to /private/tmp), longest first so a root that is a
# suffix of another cannot leave a prefix behind; then any home directory
# left over.
def redact_paths:
  reduce ($roots | map(select(length > 0)) | sort_by(-length) | .[]) as $r (.; split($r) | join(fixture_root))
  | gsub("/Users/[^/\\s\"']+"; "/Users/fixture")
  | gsub("/home/[^/\\s\"']+"; "/home/fixture");

# Every session id gets a stable placeholder, numbered in order of first
# appearance, wherever it appears (envelope, part, or inside a string).
def session_placeholders:
  [.. | strings | scan("ses_[0-9A-Za-z]{8,}")]
  | reduce .[] as $id ({order: [], seen: {}};
      if .seen[$id] then . else .order += [$id] | .seen[$id] = true end)
  | .order
  | to_entries
  | map({key: .value, value: ("ses_fixture" + (("0000000000000000000" + ((.key + 1) | tostring)) | .[-19:]))})
  | from_entries;

def replace_sessions($m):
  reduce ($m | to_entries[]) as $e (.; split($e.key) | join($e.value));

if $mode == "stderr" then
  split("\n") | map(redact_paths | redact_credentials) | join("\n")
else
  session_placeholders as $sessions
  | map(walk(
      if type == "string" then redact_paths | redact_credentials | replace_sessions($sessions)
      # An error event names the failed request's full URL (ADR-022,
      # § Endpoints); the parser never reads it and a fixture never keeps it.
      elif type == "object" and ((.url? | type) == "string") then .url = "[REDACTED:url]"
      else . end))
  | .[]
end
