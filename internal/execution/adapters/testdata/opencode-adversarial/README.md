# OpenCode adversarial config-merge and plugin-loading fixtures (ADR-022 § 8)

A target repository can supply `opencode.json` and `.opencode/**`, and so can a
stage that writes into it. Each directory here is the fixture for one question
about what OpenCode then does, and `../../opencode_merge_contract_test.go`
(build tag `opencode_integration`) runs the real, pinned binary against it and
asserts the answer recorded below. An OpenCode upgrade that changes one of these
answers turns the suite red; the answers are also recorded in ADR-022 § 8.

```bash
go test -tags opencode_integration ./internal/execution/adapters/ -count=1 -v \
  -run 'TestOpenCode(PurePluginLoading|InlineDenyBeatsProjectAllow|ArrayMerge|ProviderBaseURLPrecedence|ConfigDiscoveryAboveWorktree|DisableProjectConfig|MergeHarnessReapsEveryProcess)$'
```

The tests skip when `opencode` is not on `PATH`, except on CI (`CI=true`), where
they fail. A binary other than 1.18.30 fails them.

## How a run is kept safe

- **Isolation.** Every process gets a throwaway `HOME`, `TMPDIR` and per-run
  root under `t.TempDir()`, the variables `OpenCodeIsolationEnv` sets for a
  pipeline run (#1616), and an environment built from nothing, so neither the
  operator's config nor an inherited `OPENCODE_*` variable takes part.
  `OPENCODE_DISABLE_MODELS_FETCH`, `_AUTOUPDATE` and `_SHARE` are always `1`.
- **No model endpoint.** A run names `nosuchprovider/x`, which OpenCode does not
  know, and exits 1 on the model lookup. Where a test must see what OpenCode
  would send to a model, the provider's `baseURL` is an `httptest` server on
  `127.0.0.1` that the test owns and that refuses every request with a 400.
- **No registry.** OpenCode installs packages with npm on every run, and to
  load an npm plugin. Every process has `npm_config_registry` pointed at a
  second loopback stub, which serves `q1-pure-plugins/npm-plugin/` and answers
  404 for everything else. The one non-loopback address in the fixtures,
  `192.0.2.1` (RFC 5737), is never contacted.
- **Processes.** Each `opencode` process runs in its own process group under a
  60-second cap that kills the whole group. Its PID is recorded when it starts,
  and cleanup kills every recorded group and fails the test if a PID or group
  still answers `kill -0`. `TestOpenCodeMergeHarnessReapsEveryProcess` checks
  that path against `opencode debug wait`, which never exits by itself.
- **Plugins.** The fixture plugins append their own name to the file
  `ADVERSARIAL_FIXTURE_SENTINEL` names, always inside `t.TempDir()`, and do
  nothing else.

Two conventions keep the tree inert in this repository: a file named
`<name>.fixture.md` is written as `<name>.md` when the tree is copied, so no
tool reading the repository takes `AGENTS.fixture.md` or `SKILL.fixture.md` for
its own, and `@PROJECT_DIR@` in a JSON file becomes the copy's absolute path.

## Observed answers (opencode 1.18.30)

Observed on 2026-09-13 on macOS 27.0 (arm64), and on Linux (arm64, Debian
bookworm, Go 1.26.6) in a container started with `--network none`.

| #   | Question                                                                                       | Test                                       | Answer                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| --- | ---------------------------------------------------------------------------------------------- | ------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Does `--pure` skip a project `.opencode/plugins/*.ts` file and a project `plugin[]` npm entry? | `TestOpenCodePurePluginLoading`            | Yes, both: neither loads, and the npm plugin is never requested from the registry. Without `--pure` both load (the positive control). Not a plugin load, and not stopped by `--pure`: for every config directory it loads, the run's own XDG config directory always among them, OpenCode starts a background npm install of `@opencode-ai/plugin` (read from the bundled source), which a run that loads a plugin waits for. A run with no plugin at all makes that registry request once it lives a few seconds; one that ends within about a second may exit first. By hand, not pinned: with the registry refusing connections, a run that loads a plugin waited past 60 s and the plugin never loaded. |
| 2   | Does an inline `permission.bash` deny beat a project allow?                                    | `TestOpenCodeInlineDenyBeatsProjectAllow`  | Yes for a scalar (`bash: deny` over `bash: allow`: bash leaves the agent's tools) and for a pattern the project does not name (`{"rm -rf *": "deny"}` over `{"*": "allow"}`: `rm -rf x` is refused). **No when the project names the same patterns first (KNOWN hole):** a merged pattern map keeps the key order of the lowest layer that has the key, the last matching rule wins, and a project that lists `rm -rf *` before `*` makes `rm -rf x` run.                                                                                                                                                                                                                                                   |
| 3   | Do `plugin`, `instructions` and `mcp` concatenate with the inline values or get replaced?      | `TestOpenCodeArrayMerge`                   | `instructions` and `plugin` concatenate, the project's first. `mcp` merges by server name, the inline entry winning for a name both set (its `command` replaces, not appends). An empty inline list removes nothing (KNOWN): `debug config` then reports `plugin: []`, but `plugin_origins` still lists the project's plugin and it loads.                                                                                                                                                                                                                                                                                                                                                                  |
| 4   | Does a project `provider.<key>.options.baseURL` override the injected one?                     | `TestOpenCodeProviderBaseURLPrecedence`    | No: the inline `baseURL` wins, in `debug config` and on the wire. A key the inline block does not set, such as a header, still comes from the project and is sent.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| 5   | Does config and rules discovery walk above the worktree root?                                  | `TestOpenCodeConfigDiscoveryAboveWorktree` | No. From a git worktree nested at `<checkout>/.nightgauge/worktrees/<repo>-issue-<N>`, neither the checkout's `opencode.json`, `.opencode/` or `AGENTS.md` nor the directory above it loads; discovery stops at the git root of the directory it starts in. Outside any git repository it walks up.                                                                                                                                                                                                                                                                                                                                                                                                         |
| 6   | What does `OPENCODE_DISABLE_PROJECT_CONFIG` disable?                                           | `TestOpenCodeDisableProjectConfig`         | Every project key the fixture sets (`agent`, `permission`, `instructions`, `plugin`, `mcp`, `provider`), all of `.opencode/` (its `opencode.json`, agents, commands, skills and plugins), the project's `AGENTS.md`, and the loading of those plugins. An inline `instructions` entry with an absolute path into the project still loads.                                                                                                                                                                                                                                                                                                                                                                   |

A KNOWN row is OpenCode's own behaviour, pinned as observed. It is a hole only
while a run loads project config; Nightgauge's control for it is to stop loading
project config and gate the repository's config (#1638), not a change to this
suite, which drives the binary directly.

## Negative controls

Each of these was run, and turned the named test red, before the suite was
committed:

- Delete `q1-pure-plugins/project/.opencode/plugins/sentinel.ts`: the positive
  control of `TestOpenCodePurePluginLoading` fails, and the test says the
  harness is invalid.
- Flip `q2-permission-precedence/scalar/inline.json` to `"bash": "allow"`: the
  `scalar` case of `TestOpenCodeInlineDenyBeatsProjectAllow` fails, because the
  resolved rule is `allow` and the tool runs.
- Expect replacement instead of concatenation in `TestOpenCodeArrayMerge`:
  its `instructions` and `plugin` assertions fail.
- Remove `baseURL` from `q4-provider-baseurl/inline.json`: the project's
  address wins and `TestOpenCodeProviderBaseURLPrecedence` fails.
- Make the worktree a plain directory inside the checkout instead of a git
  worktree: discovery walks up to the checkout, and
  `TestOpenCodeConfigDiscoveryAboveWorktree` fails on its config and its
  `AGENTS.md`.
- Leave `OPENCODE_DISABLE_PROJECT_CONFIG` unset: every key, skill, rule and
  plugin assertion of `TestOpenCodeDisableProjectConfig` fails.
- Make the harness's `kill -0` check blind, or stop cleanup from killing
  anything and leave `opencode debug wait` running:
  `TestOpenCodeMergeHarnessReapsEveryProcess` fails, the second time from the
  cleanup check naming the surviving PID.
