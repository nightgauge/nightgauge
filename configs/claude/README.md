# Claude Code Team Configuration

Settings for configuring Claude Code across your organization.

## For Team Administrators

There is no settings file to distribute. Declaring the marketplace and plugin in
settings was not shown to install the plugin: a fresh config did not materialize
it from the CLI, `claude -p` or a logged-out startup, and an authenticated startup
has not been checked. Have each member run the commands below.

## For Individual Developers

Install the `nightgauge` binary (see [docs/GO_BINARY.md](../../docs/GO_BINARY.md)),
confirm it with `nightgauge doctor`, then run:

```bash
claude plugin marketplace add https://github.com/nightgauge/nightgauge.git#stable
claude plugin install nightgauge@nightgauge-plugins
```

## For Repository-Level Configuration

Run these from the repository and commit the `.claude/settings.json` they write:

```bash
claude plugin marketplace add https://github.com/nightgauge/nightgauge.git#stable --scope project
claude plugin install nightgauge@nightgauge-plugins --scope project
```

Each teammate runs the same two commands after cloning. The committed settings
alone were not shown to install the plugin for them.
