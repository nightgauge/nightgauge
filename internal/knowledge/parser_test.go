package knowledge

import (
	"errors"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantNil     bool
		wantRepos   []string
		wantTags    []string
		wantRelated []string
		wantStatus  string
		wantSuperBy string
		wantErr     bool
	}{
		{
			name:      "valid scoped entry",
			input:     "---\nrepos:\n  - nightgauge\n  - acme-platform\n---\n# Content",
			wantRepos: []string{"nightgauge", "acme-platform"},
		},
		{
			name:      "valid workspace-wide entry (no repos field)",
			input:     "---\ntitle: workspace knowledge\n---\n# Content",
			wantRepos: nil,
		},
		{
			name:      "empty frontmatter block",
			input:     "---\n---\n# Content",
			wantRepos: nil,
		},
		{
			name:    "no frontmatter",
			input:   "# Just a heading\nSome content",
			wantNil: true,
		},
		{
			name:    "content starting with newline then heading (no frontmatter)",
			input:   "\n# Heading",
			wantNil: true,
		},
		{
			name:    "malformed YAML",
			input:   "---\nrepos: [unclosed\n---\n# Content",
			wantErr: true,
		},
		{
			name:    "missing closing sentinel",
			input:   "---\nrepos:\n  - nightgauge\n# Content",
			wantErr: true,
		},
		{
			name:    "repos wrong type (string instead of list)",
			input:   "---\nrepos: nightgauge\n---\n# Content",
			wantErr: true,
		},
		{
			name:      "extra whitespace around content",
			input:     "---\nrepos:\n  - nightgauge\n---\n\nContent here",
			wantRepos: []string{"nightgauge"},
		},
		{
			name:      "frontmatter with extra fields preserved in Raw",
			input:     "---\nrepos:\n  - nightgauge\ntags:\n  - security\n---\n# Doc",
			wantRepos: []string{"nightgauge"},
		},
		{
			name:      "empty repos list",
			input:     "---\nrepos: []\n---\n# Doc",
			wantRepos: []string{},
		},
		{
			name:     "tags field parsed",
			input:    "---\ntags:\n  - auth\n  - pipeline\n---\n# Doc",
			wantTags: []string{"auth", "pipeline"},
		},
		{
			name:        "related field parsed",
			input:       "---\nrelated:\n  - '#2090'\n  - '#2091'\n---\n# Doc",
			wantRelated: []string{"#2090", "#2091"},
		},
		{
			name:       "status field parsed (stable)",
			input:      "---\nstatus: stable\n---\n# Doc",
			wantStatus: "stable",
		},
		{
			name:       "status field parsed (draft)",
			input:      "---\nstatus: draft\n---\n# Doc",
			wantStatus: "draft",
		},
		{
			name:        "superseded_by field parsed",
			input:       "---\nstatus: superseded\nsuperseded_by: '#2100'\n---\n# Doc",
			wantStatus:  "superseded",
			wantSuperBy: "#2100",
		},
		{
			name:       "unknown status value accepted (forward compat)",
			input:      "---\nstatus: archived\n---\n# Doc",
			wantStatus: "archived",
		},
		{
			name:        "all new fields together",
			input:       "---\ntags:\n  - kb\nrelated:\n  - '#1999'\nstatus: stable\n---\n# Doc",
			wantTags:    []string{"kb"},
			wantRelated: []string{"#1999"},
			wantStatus:  "stable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseFrontmatter(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Errorf("expected nil result, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if len(got.Repos) != len(tc.wantRepos) {
				t.Errorf("repos len = %d, want %d; got %v", len(got.Repos), len(tc.wantRepos), got.Repos)
				return
			}
			for i, r := range got.Repos {
				if r != tc.wantRepos[i] {
					t.Errorf("repos[%d] = %q, want %q", i, r, tc.wantRepos[i])
				}
			}

			// Check new fields when test case specifies them.
			if tc.wantTags != nil {
				if len(got.Tags) != len(tc.wantTags) {
					t.Errorf("tags len = %d, want %d; got %v", len(got.Tags), len(tc.wantTags), got.Tags)
				} else {
					for i, tag := range got.Tags {
						if tag != tc.wantTags[i] {
							t.Errorf("tags[%d] = %q, want %q", i, tag, tc.wantTags[i])
						}
					}
				}
			}
			if tc.wantRelated != nil {
				if len(got.Related) != len(tc.wantRelated) {
					t.Errorf("related len = %d, want %d; got %v", len(got.Related), len(tc.wantRelated), got.Related)
				} else {
					for i, r := range got.Related {
						if r != tc.wantRelated[i] {
							t.Errorf("related[%d] = %q, want %q", i, r, tc.wantRelated[i])
						}
					}
				}
			}
			if tc.wantStatus != "" && got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if tc.wantSuperBy != "" && got.SupersededBy != tc.wantSuperBy {
				t.Errorf("superseded_by = %q, want %q", got.SupersededBy, tc.wantSuperBy)
			}
		})
	}
}

func TestValidateRepos(t *testing.T) {
	ws := &WorkspaceConfig{
		Repositories: []WorkspaceRepository{
			{Name: "nightgauge"},
			{Name: "acme-platform"},
		},
	}

	tests := []struct {
		name        string
		repos       []string
		config      *WorkspaceConfig
		wantErr     bool
		wantUnknown []string
	}{
		{
			name:   "all valid repos",
			repos:  []string{"nightgauge", "acme-platform"},
			config: ws,
		},
		{
			name:        "unknown repo name",
			repos:       []string{"nightgauge", "nonexistent-repo"},
			config:      ws,
			wantErr:     true,
			wantUnknown: []string{"nonexistent-repo"},
		},
		{
			name:        "multiple unknown repos",
			repos:       []string{"foo", "bar"},
			config:      ws,
			wantErr:     true,
			wantUnknown: []string{"foo", "bar"},
		},
		{
			name:   "empty repos list (workspace-wide)",
			repos:  nil,
			config: ws,
		},
		{
			name:    "nil workspace config with repos",
			repos:   []string{"nightgauge"},
			config:  nil,
			wantErr: true,
		},
		{
			name:   "nil workspace config with no repos",
			repos:  nil,
			config: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRepos(tc.repos, tc.config)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if len(tc.wantUnknown) > 0 {
					var ve *ValidationError
					if !errors.As(err, &ve) {
						t.Fatalf("expected *ValidationError, got %T: %v", err, err)
					}
					if len(ve.UnknownRepos) != len(tc.wantUnknown) {
						t.Errorf("unknown repos = %v, want %v", ve.UnknownRepos, tc.wantUnknown)
					}
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
