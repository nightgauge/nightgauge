package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestKnowledgeIndexHelpNamesIndexMd(t *testing.T) {
	out := helpOutput(t, knowledgeIndexCmd())
	if !strings.Contains(out, "index.md") {
		t.Errorf("knowledge index --help does not name index.md:\n%s", out)
	}
	if strings.Contains(out, "README.md") {
		t.Errorf("knowledge index --help still names README.md:\n%s", out)
	}
}

func TestKnowledgeNewHelpNamesIndexMdForNewCategories(t *testing.T) {
	out := helpOutput(t, knowledgeNewCmd())
	if !strings.Contains(out, "index.md") {
		t.Errorf("knowledge new --help does not name index.md:\n%s", out)
	}
	if strings.Contains(out, "README.md") {
		t.Errorf("knowledge new --help still names README.md:\n%s", out)
	}
}

func helpOutput(t *testing.T, cmd *cobra.Command) string {
	t.Helper()
	var buf strings.Builder
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help: %v", err)
	}
	return buf.String()
}
