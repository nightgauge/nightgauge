package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge/okf"
)

// freshnessFiles are the entries whose provenance and mtime decide which of
// several same-numbered issue directories is current.
var freshnessFiles = []string{"PRD.md", "decisions.md"}

// resolveIssueDir returns the directory under catDir that holds issueNumber's
// knowledge, or "" when none matches. A directory matches when its name starts
// with "<N>-" and filter (nil accepts all) returns true for its path.
//
// Several directories can share a number when an issue was re-scaffolded under
// a new title slug, leaving a stale one behind (#2898). The current one wins
// by, in order: newest frontmatter generated.at across freshnessFiles (missing
// or unparseable frontmatter counts as zero, so a legacy directory loses to a
// stamped one), newest file modification time, lexically greatest name.
func resolveIssueDir(catDir string, issueNumber int, filter func(dir string) bool) (string, error) {
	entries, err := os.ReadDir(catDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", catDir, err)
	}
	prefix := fmt.Sprintf("%d-", issueNumber)
	var best string
	var bestGen, bestMod time.Time
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		dir := filepath.Join(catDir, e.Name())
		if filter != nil && !filter(dir) {
			continue
		}
		gen, mod := dirFreshness(dir)
		// ReadDir sorts by name, so a later candidate that ties on both
		// timestamps is the lexically greater one and replaces best.
		if best == "" || gen.After(bestGen) ||
			(gen.Equal(bestGen) && !mod.Before(bestMod)) {
			best, bestGen, bestMod = dir, gen, mod
		}
	}
	return best, nil
}

// dirFreshness returns the newest generated.at and the newest modification
// time across dir's freshnessFiles. Absent or unreadable data is zero time.
func dirFreshness(dir string) (generated, modified time.Time) {
	for _, name := range freshnessFiles {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().After(modified) {
			modified = info.ModTime()
		}
		block, err := okf.ParseFrontmatterFile(path)
		if err != nil || block == nil || block.Generated == nil {
			continue
		}
		at, err := time.Parse(time.RFC3339, block.Generated.At)
		if err == nil && at.After(generated) {
			generated = at
		}
	}
	return generated, modified
}
