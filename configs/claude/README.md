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

Run these from the repository:

```bash
claude plugin marketplace add https://github.com/nightgauge/nightgauge.git#stable --scope project
claude plugin install nightgauge@nightgauge-plugins --scope project
```

They record the plugin in `.claude/settings.json`, but that file alone was not
shown to install it for anyone, so each teammate runs the same two commands
after cloning.
