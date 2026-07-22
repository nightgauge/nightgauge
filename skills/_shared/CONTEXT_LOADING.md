### Context Loading and Stage Signaling

Each stage follows a consistent pattern:

1. **Extract issue number** from the current branch name:

   ```bash
   ISSUE_NUMBER=$(git branch --show-current | grep -oE '[0-9]+' | head -1)
   ```

2. **Load the predecessor's context file** from
   `.nightgauge/pipeline/<prefix>-{N}.json`

3. **Parse required fields** using `jq` or equivalent JSON parsing

4. **Fail fast** if the context file is missing:
   ```
   ERROR: Missing context file: .nightgauge/pipeline/<file>.json
   Created by: /nightgauge-<previous-stage>
   Please run the pipeline in order.
   ```

### Signaling Stage Status

Use the Go binary to signal stage transitions:

```bash
# Go binary: project move-status
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
if [ -n "$BINARY" ]; then
  "$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true
fi
```

Valid statuses: `running`, `complete`, `failed`

### Writing Context Files

After completing stage work, write the output context file:

```bash
cat > .nightgauge/pipeline/<output>.json << EOF
{ ... }
EOF

# Validate JSON
python3 -m json.tool .nightgauge/pipeline/<output>.json > /dev/null && \
  echo "Context written successfully"
```

**Full schema reference**: See `docs/CONTEXT_ARCHITECTURE.md`.
