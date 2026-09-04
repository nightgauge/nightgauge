package config

// Applying the `github.api_ledger.enabled` setting (#1347).
//
// The direction of this dependency is forced: internal/config already imports
// internal/github, so the ledger cannot read config itself. It exposes a
// setter instead, and this is the one place that calls it — a single
// application point, so "which config won?" has one answer rather than one per
// entry point.

import "github.com/nightgauge/nightgauge/internal/github"

// ApplyAPILedgerSetting pushes `github.api_ledger.enabled` into the GitHub
// transport's ledger.
//
// Call it immediately after loading config and BEFORE the first GitHub
// request. A nil config, a nil block, or a nil Enabled all mean "leave the
// default in place" — the ledger is on unless something explicitly says
// otherwise, and an absent setting is not a statement.
func ApplyAPILedgerSetting(cfg *Config) {
	if cfg == nil || cfg.GitHubAPILedger == nil || cfg.GitHubAPILedger.Enabled == nil {
		return
	}
	github.SetAPILedgerEnabled(*cfg.GitHubAPILedger.Enabled)
}
