## Problem

OpenCode 1.18.30 treats `~/.opencode` (and an inherited `OPENCODE_CONFIG_DIR`) as a config directory, and on load it installs `@opencode-ai/plugin` into every config directory it reads. Nightgauge's per-run OpenCode isolation (#1625, #1635) sets a per-run config root, but it does not change `HOME`. So on a machine where `~/.opencode` exists, or where `inherit_user_config` is set, a Nightgauge-dispatched run still triggers OpenCode's own install into that operator-owned directory. Offline, the run waits on that install.

#1635 deliberately leaves operator-owned directories alone: Nightgauge never writes into them, and the offline wait is bounded by the stage context and fails with a classified error. That makes the wait safe, but it doesn't remove it.

## Proposal

Give OpenCode stages a per-run `HOME` (or an equivalent OpenCode setting, if one exists), so `~/.opencode` stops being a config directory for Nightgauge runs unless `inherit_user_config` is set. Also check which operator state such a `HOME` would hide: auth files, the provider cache, `opencode session list`.

## Acceptance criteria

- [ ] With `~/.opencode` present and `inherit_user_config` unset, an offline OpenCode stage never waits on an install into `~/.opencode`.
- [ ] Operator credentials that OpenCode needs still resolve, or the stage fails with a classified error that names the missing credential.
- [ ] The operator's `opencode session list` stays untouched.
- [ ] ADR-022 records the choice.

## Verification

- An offline stub-provider stage with a populated `~/.opencode` completes, and nothing under `~/.opencode` changes (compare mtimes before and after).

Follow-up from #1635 (A11).
