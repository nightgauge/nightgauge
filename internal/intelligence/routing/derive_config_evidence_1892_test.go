package routing

import (
	"os"
	"strings"
	"testing"
)

// Issue #1892: `nightgauge issue route 1787` on the unlabelled issue answered
// `trivial path … change config, skip: feature-validate` with
// `change_rule: config-only (fast-tracked)` because its body talks about
// `~/.opencode` as a config directory. The run changed 17 Go files with
// validation skipped. Fixtures are real issue bodies.
func TestDerive_ConfigFastTrackNeedsEvidence_1892(t *testing.T) {
	body1787, err := os.ReadFile("testdata/issue-1787-body.md")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	const title1787 = "adapters: per-run HOME for OpenCode stages so ~/.opencode stops being a config directory"

	cases := []struct {
		name         string
		in           DeriveInput
		wantChange   string
		wantRule     string
		evidencePart string
	}{
		{
			name:         "#1787 unlabelled routes standard, not config-only",
			in:           DeriveInput{Title: title1787, Body: string(body1787)},
			wantChange:   "code",
			wantRule:     "",
			evidencePart: "default:code",
		},
		{
			name:         "#1787 labelled routes standard",
			in:           DeriveInput{Title: title1787, Body: string(body1787), Labels: []string{"type:bug", "component:go-binary", "size:M"}},
			wantChange:   "code",
			wantRule:     "",
			evidencePart: "label:type:bug",
		},
		{
			name: "genuine config-only issue still fast-tracks",
			in: DeriveInput{
				Title:  "ci: raise the CodeQL timeout",
				Body:   "## Proposal\n\nRaise `timeout-minutes` in `deploy/codeql.yml` and mirror it in `config/pipeline.yaml`.\n",
				Labels: []string{"type:chore", "size:XS"},
			},
			wantChange:   "config",
			wantRule:     "config-only",
			evidencePart: "targets:config-only",
		},
		{
			name:         "unlabelled issue with no signal either way",
			in:           DeriveInput{Title: "investigate the flaky thing", Body: "It sometimes fails. Look into it."},
			wantChange:   "code",
			wantRule:     "",
			evidencePart: "default:code",
		},
		{
			name: "unlabelled issue whose prose names only config files never fast-tracks",
			in: DeriveInput{
				Title: "tweak settings",
				Body:  "Change `deploy/codeql.yml` and the .yaml config.",
			},
			wantChange:   "code",
			wantRule:     "",
			evidencePart: "default:code",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Derive(tc.in)
			if got.ChangeType != tc.wantChange {
				t.Errorf("ChangeType = %q, want %q (evidence %q)", got.ChangeType, tc.wantChange, got.ChangeTypeEvidence)
			}
			if got.MatchedChangeRule != tc.wantRule {
				t.Errorf("MatchedChangeRule = %q, want %q", got.MatchedChangeRule, tc.wantRule)
			}
			if !strings.Contains(got.ChangeTypeEvidence, tc.evidencePart) {
				t.Errorf("ChangeTypeEvidence = %q, want it to contain %q", got.ChangeTypeEvidence, tc.evidencePart)
			}
			if len(tc.in.Labels) == 0 && got.MatchedChangeRule == "config-only" {
				t.Errorf("an unlabelled issue fast-tracked as config-only")
			}
		})
	}
}
