package forgecmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestHelpText_GitLabReferenceOrMarker walks the entire forge command
// tree and asserts every leaf command's Long text either explicitly
// references a GitLab equivalent or carries the no-equivalent marker.
// This prevents adding a verb without documenting the cross-forge
// difference (or its absence).
func TestHelpText_GitLabReferenceOrMarker(t *testing.T) {
	root := Cmd()
	for _, leaf := range walkLeaves(root) {
		long := strings.TrimSpace(leaf.Long)
		if long == "" {
			t.Errorf("%s: Long is empty — every leaf must document its semantics", leaf.CommandPath())
			continue
		}
		hasReference := strings.Contains(long, "GitLab equivalent:")
		hasNoEquivalent := strings.Contains(long, "(no GitLab equivalent — GitHub only)")
		if !hasReference && !hasNoEquivalent {
			t.Errorf("%s: Long must contain 'GitLab equivalent:' or '(no GitLab equivalent — GitHub only)'", leaf.CommandPath())
		}
	}
}

// TestHelpText_LeafCount sanity-checks that we have leaves under each
// of the five subcommand groups. This guards against accidental
// removals.
func TestHelpText_LeafCount(t *testing.T) {
	root := Cmd()
	groups := map[string]int{
		"issue":   0,
		"pr":      0,
		"project": 0,
		"label":   0,
		"auth":    0,
	}
	for _, leaf := range walkLeaves(root) {
		if leaf.Parent() == nil {
			continue
		}
		if _, ok := groups[leaf.Parent().Name()]; ok {
			groups[leaf.Parent().Name()]++
		}
	}
	for g, n := range groups {
		if n == 0 {
			t.Errorf("group %q has no leaves", g)
		}
	}
}

func walkLeaves(c *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	if !c.HasSubCommands() {
		out = append(out, c)
		return out
	}
	for _, child := range c.Commands() {
		out = append(out, walkLeaves(child)...)
	}
	return out
}
