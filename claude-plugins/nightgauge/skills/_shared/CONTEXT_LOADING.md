### Context Loading and Stage Signaling

Each stage follows a consistent pattern:

1. **Extract issue number** from the current branch name:

   ```bash
   ISSUE_NUMBER=$(git branch --show-current | grep -oE '[0-9]+' | head -1)
   ```

2. **Load the predecessor's context file** from
   `"$(nightgauge layout path pipeline <prefix>-{N}.json)"` (the clone's
   pipeline state directory)

3. **Parse required fields** using `jq` or equivalent JSON parsing

4. **Fail fast** if the context file is missing:
   ```
   ERROR: Missing context file: <file>.json (nightgauge layout path pipeline)
   Created by: /nightgauge-<previous-stage>
   Please run the pipeline in order.
   ```

### Signaling Stage Status

Use the Go binary to signal stage transitions:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
# Go binary: project move-status
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ] && [ "${NIGHTGAUGE_DEV_REPO_BIN:-}" = "1" ]; then
  for NG_ROOT in "$(git rev-parse --show-toplevel 2>/dev/null)" \
    "$(cd "$(git rev-parse --git-common-dir 2>/dev/null)/.." 2>/dev/null && pwd)"; do
    [ -x "$NG_ROOT/bin/nightgauge" ] || continue
    grep -qx 'module github.com/nightgauge/nightgauge' "$NG_ROOT/go.mod" 2>/dev/null || continue
    NG_ORIGIN="$(git -C "$NG_ROOT" config --get remote.origin.url 2>/dev/null)"
    case "${NG_ORIGIN%.git}" in
      git@github.com:nightgauge/nightgauge | https://github.com/nightgauge/nightgauge | \
        ssh://git@github.com/nightgauge/nightgauge) BINARY="$NG_ROOT/bin/nightgauge"; break ;;
    esac
  done
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
nightgauge layout write pipeline <output>.json >/dev/null << EOF
{ ... }
EOF

# Validate JSON
python3 -m json.tool "$(nightgauge layout path pipeline <output>.json)" > /dev/null && \
  echo "Context written successfully"
```

**Full schema reference**: See `docs/CONTEXT_ARCHITECTURE.md`.
