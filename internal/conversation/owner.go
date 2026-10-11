package conversation

import (
	"context"
	"fmt"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// defaultWorkspaceSlug is the slug of a team's Default workspace, which the
// repositories of a daemon that declares no named workspace are linked to.
const defaultWorkspaceSlug = "default"

// ServedWorkspaceOwner is the Owner of a daemon that serves one workspace.
// read lists the workspaces the hosted service says the agent covers, with
// their ids; servedSlug is the slug of the workspace this daemon declares, ""
// when it declares no named workspace. A turn is this daemon's when its target
// workspace is one the agent covers and has that slug. Every daemon on a
// machine shares the machine's agent, so the agent covers each of their
// workspaces; the slug tells them apart.
func ServedWorkspaceOwner(
	read func(ctx context.Context, agentID string) ([]platform.AgentWorkspaceThrottle, error),
	servedSlug func() (string, error),
) Owner {
	return func(ctx context.Context, agentID, target string) (bool, error) {
		if target == "" {
			return false, fmt.Errorf("the turn names no target workspace")
		}
		slug, err := servedSlug()
		if err != nil {
			return false, err
		}
		if slug == "" {
			slug = defaultWorkspaceSlug
		}
		covered, err := read(ctx, agentID)
		if err != nil {
			return false, err
		}
		for _, w := range covered {
			if w.WorkspaceID == target {
				return w.Slug == slug, nil
			}
		}
		return false, nil
	}
}
