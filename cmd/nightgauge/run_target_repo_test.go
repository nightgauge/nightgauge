package main

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/spf13/cobra"
)

// newRunTargetCmd builds the minimum cobra command resolveRunTargetRepo reads:
// it only consults the `owner` flag's Changed bit.
func newRunTargetCmd(ownerChanged bool) *cobra.Command {
	cmd := &cobra.Command{Use: "run"}
	var owner string
	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	if ownerChanged {
		if err := cmd.Flags().Set("owner", "nightgauge"); err != nil {
			panic(err)
		}
	}
	return cmd
}

func TestResolveRunTargetRepo(t *testing.T) {
	tests := []struct {
		name         string
		cfg          *config.Config
		ownerFlag    string
		ownerChanged bool
		repoFlag     string
		want         string
		wantErr      string
	}{
		{
			// The #1553 regression: run from a non-core checkout with no
			// --repo must target THAT repo, not <owner>/nightgauge.
			name:      "config repo wins over the old hardcoded core repo",
			cfg:       &config.Config{Owner: "nightgauge", DefaultRepo: "nightgauge-platform"},
			ownerFlag: "nightgauge",
			want:      "nightgauge/nightgauge-platform",
		},
		{
			name:      "core checkout still resolves to core",
			cfg:       &config.Config{Owner: "nightgauge", DefaultRepo: "nightgauge"},
			ownerFlag: "nightgauge",
			want:      "nightgauge/nightgauge",
		},
		{
			name:      "explicit --repo overrides the checkout config",
			cfg:       &config.Config{Owner: "nightgauge", DefaultRepo: "nightgauge-platform"},
			ownerFlag: "nightgauge",
			repoFlag:  "nightgauge/nightgauge-flutter",
			want:      "nightgauge/nightgauge-flutter",
		},
		{
			name:      "bare --repo resolves against the owner",
			cfg:       &config.Config{Owner: "nightgauge", DefaultRepo: "nightgauge-platform"},
			ownerFlag: "nightgauge",
			repoFlag:  "nightgauge-dashboard",
			want:      "nightgauge/nightgauge-dashboard",
		},
		{
			name:      "owner-qualified --repo keeps its own owner",
			cfg:       &config.Config{Owner: "nightgauge", DefaultRepo: "nightgauge"},
			ownerFlag: "nightgauge",
			repoFlag:  "someone-else/fork",
			want:      "someone-else/fork",
		},
		{
			// --owner carries a non-empty default, so an untouched flag must
			// not beat the checkout's configured owner.
			name:      "unchanged --owner defers to the config owner",
			cfg:       &config.Config{Owner: "edibu", DefaultRepo: "widget"},
			ownerFlag: "nightgauge",
			want:      "edibu/widget",
		},
		{
			name:         "an explicitly passed --owner wins",
			cfg:          &config.Config{Owner: "edibu", DefaultRepo: "widget"},
			ownerFlag:    "nightgauge",
			ownerChanged: true,
			want:         "nightgauge/widget",
		},
		{
			// Refusing is the point: guessing the repo is what #1553 was.
			name:      "no config and no --repo refuses rather than guessing",
			cfg:       nil,
			ownerFlag: "nightgauge",
			wantErr:   "cannot tell which repository",
		},
		{
			name:      "config without a repo refuses",
			cfg:       &config.Config{Owner: "nightgauge"},
			ownerFlag: "nightgauge",
			wantErr:   "cannot tell which repository",
		},
		{
			name:      "malformed --repo is rejected",
			cfg:       &config.Config{Owner: "nightgauge", DefaultRepo: "nightgauge"},
			ownerFlag: "nightgauge",
			repoFlag:  "nightgauge/",
			wantErr:   "invalid --repo",
		},
		{
			name:      "over-qualified --repo is rejected",
			cfg:       &config.Config{Owner: "nightgauge", DefaultRepo: "nightgauge"},
			ownerFlag: "nightgauge",
			repoFlag:  "a/b/c",
			wantErr:   "invalid --repo",
		},
		{
			name:      "no owner anywhere refuses",
			cfg:       &config.Config{DefaultRepo: "widget"},
			ownerFlag: "",
			wantErr:   "cannot tell which owner",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveRunTargetRepo(newRunTargetCmd(tt.ownerChanged), tt.cfg, tt.ownerFlag, tt.repoFlag)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got repo %q and nil error", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %q", tt.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("want %q, got %q", tt.want, got)
			}
		})
	}
}
