package conversation

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/conversation/tools"
)

// turnTools is one turn's tool server and the toolset behind it (#1570).
type turnTools struct {
	set    *tools.Toolset
	server *tools.Server
	once   sync.Once
}

// mcpConfigName is the turn's MCP config file, in the turn's own directory.
const mcpConfigName = "mcp.json"

// startTools starts the turn's tool server and returns the plan with its
// argv rebuilt to reach it: the planned options, plus the config file naming
// the server. The file holds the server's token and is written, readable by
// its owner only, into the turn's directory, which is removed with the turn.
// The toolset is bound to this turn alone: its poster, the repositories they
// may read, and their own message, the only text that can ask for a
// mutation.
func (h *Host) startTools(t *turn, p plan, dir string, in turnInput) (plan, *turnTools, error) {
	forge := h.cfg.Forge
	if forge == nil {
		cfg, err := config.Load(h.cfg.WorkspaceRoot)
		if err != nil {
			cfg = nil
		}
		forge = tools.NewGitHubForge(cfg)
	}
	login := ""
	if t.cmd.PosterGitHubLogin != nil {
		login = *t.cmd.PosterGitHubLogin
	}
	readable := make([]tools.Repo, 0, len(t.cmd.ReadableRepos))
	for _, r := range t.cmd.ReadableRepos {
		readable = append(readable, tools.Repo{Owner: r.Owner, Repo: r.Repo})
	}
	set := tools.NewToolset(tools.Env{
		Root:      h.cfg.WorkspaceRoot,
		RepoRoots: config.WorkspaceRepoPaths(h.cfg.WorkspaceRoot),
		Forge:     forge,
	}, tools.Turn{
		ConversationID:   t.cmd.ConversationID,
		TurnID:           t.cmd.TurnID,
		PosterAccountID:  t.cmd.PosterAccountID,
		PosterLogin:      login,
		PosterIsOperator: t.cmd.PosterIsOperator,
		ReadableRepos:    readable,
		Message:          in.current,
	})
	server, err := tools.Start(set)
	if err != nil {
		set.Close()
		return p, nil, err
	}
	tt := &turnTools{set: set, server: server}
	path := filepath.Join(dir, mcpConfigName)
	if err := server.WriteConfig(path); err != nil {
		tt.close()
		return p, nil, err
	}
	opts := p.opts
	opts.MCPConfig = path
	p.cmd, p.args, p.env = p.builder.BuildConversationTurn(opts)
	return p, tt, nil
}

// close ends the calls in flight, waits for the server to stop, and
// releases the workspace. Only once it returned is the record of calls
// final.
func (tt *turnTools) close() {
	tt.once.Do(func() {
		tt.server.Close()
		tt.set.Close()
	})
}

// invocations are the tool calls the turn made, as its server recorded them.
func (tt *turnTools) invocations() []tools.Invocation { return tt.set.Invocations() }

// writeReceipts closes the tool server, so no call is still running, then
// appends one line to the reply for every mutating call the turn made, from
// the server's record rather than the model's account: what was written, or
// the typed error that stopped it. A refused write is shown in the transcript
// whatever the model said about it.
func (tt *turnTools) writeReceipts(out *poster) {
	tt.close()
	for _, inv := range tt.invocations() {
		if !inv.Mutating {
			continue
		}
		switch inv.Code {
		case "":
			out.write(fmt.Sprintf("\n\n[nightgauge] %s: %s", inv.Name, inv.Detail))
		case tools.CodeForgeError:
			out.write(fmt.Sprintf("\n\n[nightgauge] %s failed (%s): %s", inv.Name, inv.Code, inv.Detail))
		default:
			out.write(fmt.Sprintf("\n\n[nightgauge] %s refused (%s): %s", inv.Name, inv.Code, inv.Detail))
		}
	}
}
