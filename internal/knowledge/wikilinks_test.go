package knowledge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractWikiLinks(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []WikiLink
	}{
		{
			name:    "single link",
			content: "See [[architecture/ADR-001]] for details.",
			want: []WikiLink{
				{Raw: "architecture/ADR-001", Match: "[[architecture/ADR-001]]", Index: 4},
			},
		},
		{
			name:    "multiple links",
			content: "[[foo]] and [[bar/baz]]",
			want: []WikiLink{
				{Raw: "foo", Match: "[[foo]]", Index: 0},
				{Raw: "bar/baz", Match: "[[bar/baz]]", Index: 12},
			},
		},
		{
			name:    "no links",
			content: "No wiki links here.",
			want:    []WikiLink{},
		},
		{
			name:    "issue-ref link",
			content: "See [[#2090]] for context.",
			want: []WikiLink{
				{Raw: "#2090", Match: "[[#2090]]", Index: 4},
			},
		},
		{
			name:    "topic-ref link",
			content: "Refer to [[topic:auth]] for details.",
			want: []WikiLink{
				{Raw: "topic:auth", Match: "[[topic:auth]]", Index: 9},
			},
		},
		{
			name:    "trimmed whitespace in raw",
			content: "[[ spaced link ]]",
			want: []WikiLink{
				{Raw: "spaced link", Match: "[[ spaced link ]]", Index: 0},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractWikiLinks(tc.content)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d; got %v", len(got), len(tc.want), got)
			}
			for i, link := range got {
				if link.Raw != tc.want[i].Raw {
					t.Errorf("[%d] Raw = %q, want %q", i, link.Raw, tc.want[i].Raw)
				}
				if link.Match != tc.want[i].Match {
					t.Errorf("[%d] Match = %q, want %q", i, link.Match, tc.want[i].Match)
				}
				if link.Index != tc.want[i].Index {
					t.Errorf("[%d] Index = %d, want %d", i, link.Index, tc.want[i].Index)
				}
			}
		})
	}
}

func TestResolveIssueRefGo(t *testing.T) {
	// Set up a temporary workspace with knowledge/features/2090-my-feature/ directory.
	workspace := t.TempDir()
	featuresDir := filepath.Join(workspace, ".nightgauge", "knowledge", "features")
	if err := os.MkdirAll(filepath.Join(featuresDir, "2090-my-feature"), 0o755); err != nil {
		t.Fatal(err)
	}
	epicsDir := filepath.Join(workspace, ".nightgauge", "knowledge", "epics")
	if err := os.MkdirAll(filepath.Join(epicsDir, "1000-my-epic"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		inner       string
		wantExists  bool
		wantAnchor  string
		wantWarning bool
	}{
		{
			name:       "resolves existing feature issue",
			inner:      "#2090",
			wantExists: true,
		},
		{
			name:       "resolves existing feature issue with anchor",
			inner:      "#2090#decisions",
			wantExists: true,
			wantAnchor: "decisions",
		},
		{
			name:        "non-existent issue returns exists=false with warning",
			inner:       "#9999",
			wantExists:  false,
			wantWarning: true,
		},
		{
			name:       "resolves epic issue",
			inner:      "#1000",
			wantExists: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolvedPath, display, exists, warning := resolveIssueRefGo(tc.inner, workspace)
			if exists != tc.wantExists {
				t.Errorf("exists = %v, want %v; resolvedPath=%q display=%q warning=%q", exists, tc.wantExists, resolvedPath, display, warning)
			}
			if tc.wantAnchor != "" && !contains(resolvedPath, "#"+tc.wantAnchor) {
				t.Errorf("resolvedPath = %q, want it to contain #%s", resolvedPath, tc.wantAnchor)
			}
			if tc.wantWarning && warning == "" {
				t.Errorf("expected a warning but got none")
			}
			if !tc.wantWarning && warning != "" {
				t.Errorf("expected no warning but got: %q", warning)
			}
		})
	}
}

func TestResolveTopicRefGo(t *testing.T) {
	workspace := t.TempDir()
	glossaryDir := filepath.Join(workspace, ".nightgauge", "knowledge", "glossary")
	if err := os.MkdirAll(glossaryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(glossaryDir, "auth.md"), []byte("# Auth"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		inner       string
		wantExists  bool
		wantDisplay string
		wantWarning bool
	}{
		{
			name:        "existing glossary term",
			inner:       "topic:auth",
			wantExists:  true,
			wantDisplay: "auth",
		},
		{
			name:        "missing glossary term degrades gracefully",
			inner:       "topic:unknown-term",
			wantExists:  false,
			wantDisplay: "unknown-term",
			wantWarning: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, display, exists, warning := resolveTopicRefGo(tc.inner, workspace)
			if exists != tc.wantExists {
				t.Errorf("exists = %v, want %v", exists, tc.wantExists)
			}
			if display != tc.wantDisplay {
				t.Errorf("display = %q, want %q", display, tc.wantDisplay)
			}
			if tc.wantWarning && warning == "" {
				t.Errorf("expected a warning but got none")
			}
		})
	}
}

func TestResolveWikiLinks_EndToEnd(t *testing.T) {
	workspace := t.TempDir()

	// Set up knowledge/features/2090-my-feature/
	if err := os.MkdirAll(filepath.Join(workspace, ".nightgauge", "knowledge", "features", "2090-my-feature"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Set up knowledge/glossary/auth.md
	glossaryDir := filepath.Join(workspace, ".nightgauge", "knowledge", "glossary")
	if err := os.MkdirAll(glossaryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(glossaryDir, "auth.md"), []byte("# Auth"), 0o644); err != nil {
		t.Fatal(err)
	}

	fromFile := filepath.Join(workspace, ".nightgauge", "knowledge", "features", "2959-test", "decisions.md")

	tests := []struct {
		name         string
		content      string
		wantRendered string
		wantWarnings int
	}{
		{
			name:         "issue-ref resolved to markdown link",
			content:      "See [[#2090]] for context.",
			wantRendered: "See [#2090](",
			wantWarnings: 0,
		},
		{
			name:         "topic-ref resolved to markdown link",
			content:      "Read [[topic:auth]] for more.",
			wantRendered: "[auth](",
			wantWarnings: 0,
		},
		{
			name:         "broken link preserved with warning",
			content:      "See [[#9999]] for more.",
			wantRendered: "[[#9999]]",
			wantWarnings: 1,
		},
		{
			name:         "multiple links mixed",
			content:      "[[#2090]] and [[#9999]]",
			wantWarnings: 1,
		},
		{
			name:         "no links unchanged",
			content:      "No links here.",
			wantRendered: "No links here.",
			wantWarnings: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rendered, warnings, err := ResolveWikiLinks(tc.content, fromFile, workspace)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(warnings) != tc.wantWarnings {
				t.Errorf("warnings count = %d, want %d; warnings=%v", len(warnings), tc.wantWarnings, warnings)
			}
			if tc.wantRendered != "" && !contains(rendered, tc.wantRendered) {
				t.Errorf("rendered = %q, expected to contain %q", rendered, tc.wantRendered)
			}
		})
	}
}

func TestResolveRelativePathGo(t *testing.T) {
	workspace := t.TempDir()
	knowledgeRoot := filepath.Join(workspace, ".nightgauge", "knowledge")
	fromDir := filepath.Join(knowledgeRoot, "features", "42-my-feature")
	if err := os.MkdirAll(fromDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a file relative to knowledge root.
	if err := os.MkdirAll(filepath.Join(knowledgeRoot, "architecture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knowledgeRoot, "architecture", "ADR-001.md"), []byte("# ADR"), 0o644); err != nil {
		t.Fatal(err)
	}

	fromFile := filepath.Join(fromDir, "decisions.md")

	_, _, exists, _ := resolveRelativePathGo("architecture/ADR-001", fromFile, workspace)
	if !exists {
		t.Errorf("expected exists=true for knowledge-root relative path")
	}

	_, _, exists, warning := resolveRelativePathGo("nonexistent/path", fromFile, workspace)
	if exists {
		t.Errorf("expected exists=false for nonexistent path")
	}
	if warning == "" {
		t.Errorf("expected warning for broken relative link")
	}
}

func TestResolveWorkspaceNamespaceGo(t *testing.T) {
	workspace := t.TempDir()
	kbRoot := filepath.Join(workspace, ".nightgauge", "knowledge")

	for _, sub := range []string{"product", "cross-repo", "architecture"} {
		if err := os.MkdirAll(filepath.Join(kbRoot, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Existing seeds:
	if err := os.WriteFile(filepath.Join(kbRoot, "product", "self-hosted-first.md"), []byte("# Self-Hosted First"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kbRoot, "cross-repo", "platform-api-contract.md"), []byte("# Platform API"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kbRoot, "architecture", "ecosystem-topology.md"), []byte("# Topology"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		inner       string
		wantExists  bool
		wantDisplay string
		wantWarning bool
		wantRelSub  string // workspace-relative path fragment expected in resolvedPath
	}{
		{
			name:        "product namespace existing entry",
			inner:       "product:self-hosted-first",
			wantExists:  true,
			wantDisplay: "self-hosted-first",
			wantRelSub:  filepath.Join(".nightgauge", "knowledge", "product", "self-hosted-first.md"),
		},
		{
			name:        "cross-repo namespace existing entry",
			inner:       "cross-repo:platform-api-contract",
			wantExists:  true,
			wantDisplay: "platform-api-contract",
			wantRelSub:  filepath.Join(".nightgauge", "knowledge", "cross-repo", "platform-api-contract.md"),
		},
		{
			name:        "architecture namespace existing entry",
			inner:       "architecture:ecosystem-topology",
			wantExists:  true,
			wantDisplay: "ecosystem-topology",
			wantRelSub:  filepath.Join(".nightgauge", "knowledge", "architecture", "ecosystem-topology.md"),
		},
		{
			name:        "product namespace missing entry warns",
			inner:       "product:missing-topic",
			wantExists:  false,
			wantDisplay: "missing-topic",
			wantWarning: true,
			wantRelSub:  filepath.Join(".nightgauge", "knowledge", "product", "missing-topic.md"),
		},
		{
			name:        "explicit .md suffix still resolves",
			inner:       "architecture:ecosystem-topology.md",
			wantExists:  true,
			wantDisplay: "ecosystem-topology",
			wantRelSub:  filepath.Join(".nightgauge", "knowledge", "architecture", "ecosystem-topology.md"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolved, display, exists, warning := resolveWikiLinkGo(tc.inner, "", workspace)
			if exists != tc.wantExists {
				t.Errorf("exists = %v, want %v", exists, tc.wantExists)
			}
			if display != tc.wantDisplay {
				t.Errorf("display = %q, want %q", display, tc.wantDisplay)
			}
			if !contains(resolved, tc.wantRelSub) {
				t.Errorf("resolvedPath %q does not contain %q", resolved, tc.wantRelSub)
			}
			if tc.wantWarning && warning == "" {
				t.Error("expected warning, got none")
			}
			if !tc.wantWarning && warning != "" {
				t.Errorf("unexpected warning: %q", warning)
			}
		})
	}
}

func TestResolveWorkspaceNamespace_EndToEnd(t *testing.T) {
	workspace := t.TempDir()
	productDir := filepath.Join(workspace, ".nightgauge", "knowledge", "product")
	if err := os.MkdirAll(productDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(productDir, "positioning.md"), []byte("# Positioning"), 0o644); err != nil {
		t.Fatal(err)
	}

	content := "See [[product:positioning]] and [[cross-repo:missing]] for details."
	rendered, warnings, err := ResolveWikiLinks(content, filepath.Join(workspace, "some", "file.md"), workspace)
	if err != nil {
		t.Fatalf("ResolveWikiLinks: %v", err)
	}
	if !contains(rendered, "[positioning](") {
		t.Errorf("rendered should contain markdown link for product:positioning, got: %s", rendered)
	}
	if !contains(rendered, "[[cross-repo:missing]]") {
		t.Errorf("rendered should preserve broken cross-repo link, got: %s", rendered)
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning for broken link, got %d: %v", len(warnings), warnings)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
