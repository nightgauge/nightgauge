package release

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"

	"github.com/nightgauge/nightgauge/internal/notify"
)

// Alert-sink defaults. They MIRROR the release-watch issue-creation gate so the
// sink "respects the existing score thresholds + per-release cap" (#4058):
//   - DefaultAlertMinScore matches `.nightgauge/config.yaml`
//     `autonomous_discovery.score_threshold` (70).
//   - DefaultAlertMaxItems matches the skill's "max 3 issues per release" rail.
const (
	DefaultAlertMinScore = 70
	DefaultAlertMaxItems = 3
)

// CreatedIssue mirrors one entry of the release-watch creation-log
// `issues_created[]` array (schema_version 1.0). Field names are pinned to the
// JSON the release-watchdog workflow + skill write and the VSCode Discovery tab
// reads (DiscoveryActivityService.CreatedIssue).
type CreatedIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Score  int    `json:"score"`
}

// CreationLog is the subset of a `creation-log-<provider>.json` document the
// alert sink needs. Unknown fields are ignored by the decoder.
type CreationLog struct {
	SchemaVersion string         `json:"schema_version"`
	Provider      string         `json:"provider"`
	Source        string         `json:"source"`
	RunStartedAt  string         `json:"run_started_at"`
	NewVersion    string         `json:"new_version"`
	SinceVersion  string         `json:"since_version"`
	Status        string         `json:"status"`
	IssuesCreated []CreatedIssue `json:"issues_created"`
}

// NotifyOptions controls a single NotifyFindings call.
type NotifyOptions struct {
	// LogPath is the creation-log JSON file to read (required).
	LogPath string
	// WebhookURL is the full Discord webhook URL to POST to. Empty disables the
	// sink (the call is a no-op skip) so the feature is opt-in and a missing
	// secret never breaks the release-watch workflow. Tests inject a
	// httptest.Server URL.
	WebhookURL string
	// MinScore routes only findings whose score is >= MinScore. 0/negative
	// defaults to DefaultAlertMinScore.
	MinScore int
	// MaxItems caps how many findings are routed (per-release cap). 0/negative
	// defaults to DefaultAlertMaxItems.
	MaxItems int
	// DryRun builds the payload and reports what would be sent without POSTing.
	DryRun bool
	// HTTPClient is injectable for tests. When nil, a 10-second-timeout client
	// is used.
	HTTPClient *http.Client
}

// NotifyResult reports the outcome. Delivery is BEST-EFFORT: a webhook failure
// is captured here (Sent=false + Reason), NOT returned as an error, so the CLI
// can warn-and-continue. NotifyFindings returns an error only for hard input
// failures (unreadable / unparseable log).
type NotifyResult struct {
	Sent     bool   `json:"sent"`
	Skipped  bool   `json:"skipped"`
	Reason   string `json:"reason,omitempty"`
	Provider string `json:"provider,omitempty"`
	Version  string `json:"version,omitempty"`
	// Eligible is the number of findings at/above MinScore (before the cap).
	Eligible int `json:"eligible"`
	// Routed is the number actually included in the alert (after the cap).
	Routed int `json:"routed"`
}

// NotifyFindings reads the creation-log at opts.LogPath, selects the
// high-impact `issues_created` findings (score >= MinScore), caps them at
// MaxItems, and POSTs a single consolidated Discord embed to opts.WebhookURL.
//
// It is a no-op skip (no error) when the sink is disabled (empty WebhookURL) or
// no finding clears the threshold. A hard error is returned only when the log
// cannot be read or parsed.
func NotifyFindings(ctx context.Context, opts NotifyOptions) (NotifyResult, error) {
	minScore := opts.MinScore
	if minScore <= 0 {
		minScore = DefaultAlertMinScore
	}
	maxItems := opts.MaxItems
	if maxItems <= 0 {
		maxItems = DefaultAlertMaxItems
	}

	raw, err := os.ReadFile(opts.LogPath)
	if err != nil {
		return NotifyResult{}, fmt.Errorf("read creation-log: %w", err)
	}
	var log CreationLog
	if err := json.Unmarshal(raw, &log); err != nil {
		return NotifyResult{}, fmt.Errorf("parse creation-log %s: %w", opts.LogPath, err)
	}

	res := NotifyResult{Provider: log.Provider, Version: log.NewVersion}

	// Select findings at/above the threshold, highest score first, then cap.
	eligible := make([]CreatedIssue, 0, len(log.IssuesCreated))
	for _, issue := range log.IssuesCreated {
		if issue.Score >= minScore {
			eligible = append(eligible, issue)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		return eligible[i].Score > eligible[j].Score
	})
	res.Eligible = len(eligible)

	routed := eligible
	if len(routed) > maxItems {
		routed = routed[:maxItems]
	}
	res.Routed = len(routed)

	if len(routed) == 0 {
		res.Skipped = true
		res.Reason = fmt.Sprintf("no findings at/above score %d", minScore)
		return res, nil
	}
	if opts.WebhookURL == "" {
		res.Skipped = true
		res.Reason = "no webhook configured (sink disabled)"
		return res, nil
	}

	embeds := buildDiscordEmbeds(log, eligible, routed)

	if opts.DryRun {
		res.Skipped = true
		res.Reason = "dry-run (payload built, not sent)"
		return res, nil
	}

	if _, err := notify.PostEmbeds(ctx, opts.HTTPClient, opts.WebhookURL, embeds); err != nil {
		// Best-effort: capture, do not fail the command. The webhook URL carries
		// the Discord token (it IS the credential), so the reason is scrubbed of
		// it as defense-in-depth — this Reason is printed to CI logs via --json.
		res.Sent = false
		res.Reason = "webhook POST failed: " + notify.RedactURL(err.Error(), opts.WebhookURL)
		return res, nil
	}

	res.Sent = true
	return res, nil
}

// --- Discord embed payload -------------------------------------------------

// buildDiscordEmbeds renders the routed findings into a single shared
// notify.Embed (the webhook delivery lives in internal/notify).
func buildDiscordEmbeds(log CreationLog, eligible, routed []CreatedIssue) []notify.Embed {
	top := 0
	for _, f := range routed {
		if f.Score > top {
			top = f.Score
		}
	}

	fields := make([]notify.EmbedField, 0, len(routed))
	for _, f := range routed {
		fields = append(fields, notify.EmbedField{
			Name:  fmt.Sprintf("#%d · score %d", f.Number, f.Score),
			Value: fmt.Sprintf("[%s](%s)", notify.ClampField(f.Title, 200), f.URL),
		})
	}

	desc := fmt.Sprintf(
		"%d high-impact change(s) in %s (since %s).",
		len(eligible), log.Source, log.SinceVersion,
	)
	if len(eligible) > len(routed) {
		desc += fmt.Sprintf(" Showing the top %d.", len(routed))
	}

	return []notify.Embed{{
		Title:       fmt.Sprintf("🔔 Release alert: %s %s", log.Provider, log.NewVersion),
		Description: desc,
		Color:       colorForScore(top),
		Fields:      fields,
		Footer:      &notify.Footer{Text: "nightgauge release-watch"},
		Timestamp:   log.RunStartedAt, // deterministic; from the run, not time.Now()
	}}
}

// colorForScore picks an embed color band by the highest score in the alert.
func colorForScore(score int) int {
	switch {
	case score >= 85:
		return notify.ColorCritical
	case score >= 70:
		return notify.ColorHigh
	default:
		return notify.ColorNotable
	}
}
