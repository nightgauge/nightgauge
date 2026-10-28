# Changelog

All notable changes to this skill are documented here.

## [1.0.0]

### Added

- Initial skill: detects new posts on allowlisted https sources with
  `nightgauge blog-watch detect`, screens each through the block-mode
  sanitization firewall, matches its concepts against our docs and open
  issues, and records exactly one of five verdicts per post in an append-only
  decision log. Files an issue only for `Adopted`, with a closed label set
  (#185).
