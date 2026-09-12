package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// TestCommitAll_NeverPublishesGeneratedSteering_1675 reproduces the leak at the
// reset path's rescue commit: `git add -A` swept the live Codex steering block
// into the checkpoint. The staged AGENTS.md must carry the user's edit only.
func TestCommitAll_NeverPublishesGeneratedSteering_1675(t *testing.T) {
	svc, dir := setupTestRepo(t)
	const block = "<!-- BEGIN NIGHTGAUGE MANAGED STEERING -->\ngenerated\n<!-- END NIGHTGAUGE MANAGED STEERING -->\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Rules\n\nUser rule.\n\n"+block), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.commitAll("chore: checkpoint"); err != nil {
		t.Fatalf("commitAll: %v", err)
	}
	head := gittest.Run(t, dir, "show", "HEAD:AGENTS.md")
	if strings.Contains(head, "MANAGED STEERING") || !strings.Contains(head, "User rule.") {
		t.Fatalf("checkpoint commit published generated steering or lost user content:\n%s", head)
	}
}
