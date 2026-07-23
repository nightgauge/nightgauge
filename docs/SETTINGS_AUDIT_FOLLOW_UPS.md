# Settings Audit Follow-ups

Issue #48 establishes the deterministic schema-to-surface inventory and its
contract test. During the Codex run, the feature-dev phase markers for issue
#48 were emitted and the existing Pipeline tree stage/phase suites passed. A
visual assertion against the installed RC extension is deliberately not
claimed as automated evidence.

The following behavior checks remain separate because they require a running
VS Code extension host rather than static configuration validation. They are
candidate issue titles, ready to file after maintainer triage:

- Verify a live Codex stage transition updates the Pipeline tree without a
  window reload.
- Exercise repository switching with multiple linked GitHub Projects and
  confirm each repository retains one explicit default assignment.
- Enter, reload, replace, and clear each Settings credential field and confirm
  the value remains in VS Code SecretStorage and never appears in a YAML tier.
- Exercise every adapter-specific conditional Settings control in the packaged
  RC extension.

These are not assertions of missing behavior in the current implementation.
Per the public-core boundary, this feature-dev stage does not create follow-up
issues autonomously; a maintainer must triage and authorize each one.
