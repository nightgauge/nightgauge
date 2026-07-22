# Security

This document describes the security features and configurations for
Nightgauge SDLC.

## Prompt Injection Sanitization

Nightgauge includes a sanitization layer that protects against prompt
injection attacks in agentic workflows. This is particularly important when AI
agents execute commands or process untrusted content.

### How It Works

The sanitization layer operates at two levels:

1. **Output Sanitization** (enabled by default): Before any Bash command
   executes, it's validated against a blocklist of dangerous patterns. This
   catches commands that could:
   - Destroy data (`rm -rf /`, `dd`, `mkfs`)
   - Exfiltrate credentials (`cat ~/.ssh/*`, `env | curl`)
   - Escalate privileges (`sudo rm`, `chmod 777 /`)
   - Traverse paths (`../../etc/passwd`)

2. **Input Sanitization** (disabled by default): User prompts can be checked for
   prompt injection attempts like "ignore previous instructions". Usually
   unnecessary since user prompts are trusted.

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
│           prompt-sanitize.sh (PreToolUse - Optional)            │
│  - Input prompt validation                                       │
│  - System prompt override detection                              │
│  - Disabled by default                                           │
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

Configure sanitization in `.nightgauge/config.yaml`:

```yaml
sanitization:
  # Enable/disable output sanitization (default: true)
  enabled: true

  # Enable input sanitization (default: false)
  input_enabled: false

  # Log all events (default: true)
  logging: true

  # Log file location
  log_file: ".nightgauge/logs/sanitization.log"

  # Warn-only mode for testing (default: false)
  warn_only: false

  # Custom blocklist patterns (added to defaults)
  blocklist:
    - "custom-dangerous-pattern"

  # Allowlist patterns (bypass sanitization)
  allowlist:
    - "rm -rf ./node_modules"
    - "rm -rf ./dist"
```

### Environment Variables

Override configuration via environment:

| Variable                              | Description                                                                                                                                                                                                                                                          |
| ------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `NIGHTGAUGE_SKIP_SANITIZATION=1`      | Disable output sanitization                                                                                                                                                                                                                                          |
| `NIGHTGAUGE_SANITIZE_INPUT=1`         | Enable input sanitization                                                                                                                                                                                                                                            |
| `NIGHTGAUGE_SANITIZATION_WARN_ONLY=1` | Log but don't block                                                                                                                                                                                                                                                  |
| `NIGHTGAUGE_SKIP_WORKFLOW_GATE=1`     | Developer/manual escape hatch — bypass the operation gates (push-to-main, force-push, destructive-git) and the sanitization scan for one command. Secret read/write and pre-push validation gates stay ON. MUST NOT be set in skillRunner/orchestrator environments. |

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

#### Prompt Injection (BLOCK when input_enabled)

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

### Allowlist Usage

Add patterns to the allowlist for known-safe commands that match blocklist
patterns:

```yaml
sanitization:
  allowlist:
    - "rm -rf ./node_modules" # Safe: project directory
    - "rm -rf ./dist" # Safe: build output
    - "rm -rf ./coverage" # Safe: test coverage
```

**Warning**: Allowlist patterns create security holes. Only add patterns for
commands that:

1. Are constrained to project directories
2. Cannot be manipulated by untrusted input
3. Have been reviewed for security implications

### Pipeline Cleanup Allowlist

Pipeline operations routinely clean up context files, plan files, git locks, and
build artifacts. These operations trigger the destructive command filter because
they use `rm -f` or `rm -rf` with absolute paths. The following path-scoped
patterns allowlist legitimate pipeline cleanup while keeping all other
destructive operations blocked:

```yaml
sanitization:
  allowlist:
    # Pipeline context file cleanup
    - rm -f .*\.nightgauge/pipeline/.*\.json
    # Pipeline plan file cleanup
    - rm -f .*\.nightgauge/plans/.*\.md
    # Git lock file cleanup
    - rm -f .*\.git/index\.lock
    # VSIX artifact cleanup
    - rm -f .*\.vsix
    # Temp directory cleanup
    - rm -rf /tmp/
```

**Scope constraints:**

- Pipeline patterns only match within `.nightgauge/pipeline/` and
  `.nightgauge/plans/` directories
- Git lock pattern is limited to `.git/index.lock` — not all `.git/*` files
- VSIX pattern matches any `.vsix` file (build artifacts only)
- `/tmp/` cleanup is safe since `/tmp` is transient by design
- Non-pipeline destructive operations (`rm -rf /`, `rm -rf /home`) remain
  blocked

### Security Considerations

1. **Pattern Evasion**: Attackers may try to evade patterns with:
   - Encoding (`\x72\x6d` instead of `rm`)
   - Command splitting (`r` followed by `m -rf /`)
   - Obfuscation (`$(echo rm) -rf /`)

   The sanitization layer catches obvious attacks but is not foolproof. Defense
   in depth is required.

2. **False Positives**: Some legitimate commands may match patterns. Use the
   allowlist for known-safe patterns.

3. **Performance**: Pattern matching is fast (milliseconds) but adds latency.
   Disable with `NIGHTGAUGE_SKIP_SANITIZATION=1` if needed for specific
   operations.

4. **Bypass Risk**: The allowlist creates intentional security holes. Document
   and review all allowlist entries.

### Testing

Test the sanitization layer:

```bash
# Should be blocked
echo 'rm -rf /' | ./claude-plugins/nightgauge/hooks/workflow-gate.sh

# Should pass (allowlisted)
echo 'rm -rf ./node_modules' | ./claude-plugins/nightgauge/hooks/workflow-gate.sh

# Test in warn-only mode
NIGHTGAUGE_SANITIZATION_WARN_ONLY=1 ./claude-plugins/nightgauge/hooks/workflow-gate.sh
```

### Related Documentation

- [standards/security.md](../standards/security.md) - General security standards
- [ARCHITECTURE.md](ARCHITECTURE.md) - Deterministic vs Probabilistic principle

---

**Author:** nightgauge

**License:** Apache-2.0
