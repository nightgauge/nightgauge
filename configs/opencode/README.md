# OpenCode operator templates

Starting points for running OpenCode by hand against a local model server:

| File                      | Server    | Endpoint id | `baseURL`                   |
| ------------------------- | --------- | ----------- | --------------------------- |
| `opencode.lmstudio.jsonc` | LM Studio | `lmstudio`  | `http://127.0.0.1:1234/v1`  |
| `opencode.ollama.jsonc`   | Ollama    | `ollama`    | `http://localhost:11434/v1` |

Copy one to `~/.config/opencode/opencode.jsonc`, or to a project's
`opencode.jsonc`, and change the model id to one the server has loaded.

Each template sets what the stock examples leave out:

- **`limit.context` and `limit.output`.** Without them OpenCode reports
  `limit.context: 0` and never compacts. Keep `limit.context` at or below the
  context the model is _loaded_ with in LM Studio, or its `num_ctx` in Ollama.
- **`headerTimeout` and `chunkTimeout`**, because a local model can take
  minutes to load and to produce its first token.
- **`compaction`, `tool_output`, `share: "disabled"` and `autoupdate: false`.**

The templates hold no credentials. If a server needs a key, reference it as
`{env:VAR}`, never as a literal.

Pipeline runs do not read these files; the adapter builds a per-run config.
Both templates are validated against the pinned OpenCode config schema by
`internal/execution/adapters/templates_contract_test.go`.

To install the Nightgauge skills and `/nightgauge-*` commands into OpenCode,
run `./scripts/install-agent-skills.sh --opencode-only`. Add `--with-plugin`
for the Nightgauge OpenCode plugin; OpenCode then installs
`@opencode-ai/plugin` from npm into its config directory on its next start.
A plugin installed by an earlier `--with-plugin` run is not pruned by a later
run without the flag, so that npm install keeps firing until you delete
`plugins/nightgauge.js` and `plugins/nightgauge/` yourself.

The templates carry no `permission` block: they do not restrict OpenCode's
`bash`, `edit` or `task` tools. Add one yourself if you want those gated.
