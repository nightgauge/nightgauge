# Claude Code Team Configuration

Settings for configuring Claude Code across your organization.

## For Team Administrators

Copy `settings.json` to your organization's Claude Code configuration to enable plugins for all team members automatically.

## For Individual Developers

Add to your `~/.claude/settings.json`:

```json
{
  "plugins": ["https://github.com/nightgauge/nightgauge/tree/main/claude-plugins/nightgauge"]
}
```

## For Repository-Level Configuration

Add `.claude/settings.json` to any repository:

```json
{
  "plugins": ["nightgauge"]
}
```

All team members who clone the repository will have the plugin available.
