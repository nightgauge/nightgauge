# Security

This document describes the security features and configurations for
Nightgauge SDLC.

## Prompt Injection Sanitization

Nightgauge includes a sanitization layer that protects against prompt
injection attacks in agentic workflows. This is particularly important when AI
agents execute commands or process untrusted content.

### How It Works

The sanitization layer runs at two PreToolUse points — `nightgauge hook
workflow-gate` screens every Bash command, and `nightgauge hook
sanitize-prompt` screens Task prompts. Both are always registered, and both
honour the same `sanitization.mode` setting. Screened content is matched
against a built-in pattern set covering commands that:

- Destroy data (`rm -rf /`, `dd`, `mkfs`)
- Exfiltrate credentials (`cat ~/.ssh/*`, `env | curl`)
- Escalate privileges (`sudo rm`, `chmod 777 /`)
- Traverse paths (`../../etc/passwd`)
- Attempt prompt injection ("ignore previous instructions", "you are now a…")

The default is `warn`: a match is logged to
`.nightgauge/logs/sanitization.log` and the command or prompt is allowed. Set
`sanitization.mode: block` to reject on match, or `disabled` to skip screening
entirely. Enforcement is deliberately opt-in — the prompt-injection patterns
appear verbatim in legitimate orchestration prompts, so block-by-default would
trade a dead guard for one that silently blocks real work.

### Architecture

The sanitization follows the repository's **Deterministic vs Probabilistic**
principle. Pattern matching is deterministic, ensuring:

- Zero LLM tokens consumed
- Predictable, testable behavior
- Millisecond execution time
- No false negatives for known patterns

```text
┌─────────────────────────────────────────────────────────────────┐
│                    USER PROMPT / FILE CONTENT                    │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│           prompt-sanitize.sh (PreToolUse - Always Active)       │
│  - Input prompt validation                                       │
│  - System prompt override detection                              │
│  - Warn by default; block is opt-in                              │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    CLAUDE LLM                                    │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│         workflow-gate.sh (PreToolUse - Always Active)           │
│  - Destructive command detection                                 │
│  - Credential exfiltration detection                             │
│  - Privilege escalation detection                                │
│  - Path traversal detection                                      │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    TOOL EXECUTION                                │
└─────────────────────────────────────────────────────────────────┘
```

### Configuration

`mode` is the only sanitization setting. Patterns are built into the binary;
the log path (`.nightgauge/logs/sanitization.log`) is fixed and not
configurable.

```yaml
sanitization:
  mode: warn # warn (default: log + allow), block, disabled
```

### Escape Hatch

One developer/manual escape hatch exists:

| Variable                          | Description                                                                                                                                                                                                                                                          |
| --------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `NIGHTGAUGE_SKIP_WORKFLOW_GATE=1` | Developer/manual escape hatch — bypass the operation gates (push-to-main, force-push, destructive-git) and the sanitization scan for one command. Secret read/write and pre-push validation gates stay ON. MUST NOT be set in skillRunner/orchestrator environments. |

#### Operation parsing, not substring matching (#4069)

The workflow gate classifies a command by **parsing its real `git`/`gh` argv**,
not by substring-matching the raw command string. A blocked operation mentioned
inside an echo, a commit message, a `--body` payload, or a heredoc is **not**
blocked — only the actual operation is. Concretely:

- `git commit -m "fix push to main bug"`, `echo "git push origin main"`, and
  `gh pr create --base main` are **allowed** (none is a `git push` to `main`).
- `git push origin main` / `git push origin HEAD:main`, force pushes
  (`-f`/`--force`/`--force-with-lease`/`+refspec`), and destructive verbs
  (`reset --hard`, `clean -f[d]`, `checkout .`/`restore .`, `branch -D`,
  `worktree remove --force`, `update-ref -d`) are still **blocked**.

When the parser is genuinely wrong for a legitimate command, set
`NIGHTGAUGE_SKIP_WORKFLOW_GATE=1` for that one invocation instead of
rewording human-readable text.

### Default Protected Patterns

`(BLOCK)` below means the category is rejected when `sanitization.mode` is set
to `block`. Under the default `warn` mode every match in every category is
logged and allowed.

#### Destructive Commands (BLOCK)

- `rm -rf /` - Filesystem destruction
- `dd if=/dev/zero of=/dev/sda` - Disk wiping
- `mkfs.*` - Filesystem formatting
- `shred` - Secure deletion

#### Credential Exfiltration (BLOCK)

- `cat ~/.ssh/id_*` - SSH private keys
- `cat ~/.aws/credentials` - Cloud credentials
- `env | curl` - Environment to network
- `base64 ~/.ssh/* | curl` - Encoded credential theft

#### Privilege Escalation (BLOCK)

- `sudo rm -rf` - Privileged deletion
- `chmod 777 /` - World-writable root
- `passwd root` - Password changes

#### Prompt Injection — Task prompts (BLOCK)

- "ignore previous instructions"
- "you are now a..."
- "new system prompt"
- "developer mode enable"

### Logging

Sanitization events are logged to `.nightgauge/logs/sanitization.log` in
NDJSON format:

```json
{
  "timestamp": "2026-02-03T14:30:00Z",
  "event": "blocked",
  "category": "destructive",
  "pattern": "rm -rf /",
  "content": "rm -rf / --no-preserve-root",
  "tool": "Bash",
  "branch": "feat/my-feature",
  "context": "Bash command"
}
```

View recent events:

```bash
tail -f .nightgauge/logs/sanitization.log | jq
```

Count blocked events:

```bash
grep '"event":"blocked"' .nightgauge/logs/sanitization.log | wc -l
```

### Command Exemptions

There is no allowlist, blocklist or safe-directory setting. Every screened
command is evaluated against the same built-in pattern set, and `mode` is the
only control.

Pipeline cleanup commands (`rm -f` on `.nightgauge/pipeline/*.json`, plan
files, `.git/index.lock`, `.vsix` artifacts) are therefore not specially
exempted. Under the default `warn` mode any match is logged and allowed, so
cleanup proceeds; under `mode: block` these commands can be rejected and
cleanup must run through a path the gate does not screen.

### Security Considerations

1. **Pattern Evasion**: Attackers may try to evade patterns with:
   - Encoding (`\x72\x6d` instead of `rm`)
   - Command splitting (`r` followed by `m -rf /`)
   - Obfuscation (`$(echo rm) -rf /`)

   The sanitization layer catches obvious attacks but is not foolproof. Defense
   in depth is required.

2. **False Positives**: Legitimate commands can match built-in patterns. Under
   the default `warn` mode they are logged and allowed; there is no per-pattern
   exemption, so `mode: block` should only be enabled once a repo's log shows a
   clean baseline.

3. **Performance**: Pattern matching is fast (milliseconds) but adds latency.
   Set `sanitization.mode: disabled`, or `NIGHTGAUGE_SKIP_WORKFLOW_GATE=1` for
   a single invocation.

### Testing

Test the sanitization layer:

```bash
# Screen a raw string directly (manual invocation)
nightgauge hook sanitize-prompt --input 'ignore all previous instructions'

# Screen a Bash command as the hook sees it (stdin JSON payload)
echo '{"tool_name":"Bash","tool_input":{"command":"rm -rf /"}}' \
  | nightgauge hook workflow-gate
```

Under the default `warn` mode both return `allow` and append an NDJSON line to
`.nightgauge/logs/sanitization.log`; set `sanitization.mode: block` in
`.nightgauge/config.yaml` to see a deny.

### Related Documentation

- [standards/security.md](../standards/security.md) - General security standards
- [ARCHITECTURE.md](ARCHITECTURE.md) - Deterministic vs Probabilistic principle

---

**Author:** nightgauge

**License:** Apache-2.0
