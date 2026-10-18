package conversation

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/conversation/tools"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// turnTools is one turn's tool server and the toolset behind it (#1570).
type turnTools struct {
	set    *tools.Toolset
	server *tools.Server
	// config is the MCP config file naming the server and its token.
	config string
	once   sync.Once
}

// mcpConfigName is the turn's MCP config file, in the turn's own directory.
const mcpConfigName = "mcp.json"

// startTools starts the turn's tool server and returns the plan with its
// argv rebuilt to reach it: the planned options, plus the config file naming
// the server. The file holds the server's token and is written, readable by
// its owner only, into the turn's directory, and close removes it, so it is
// gone before the turn's terminal report is sent.
// The toolset is bound to this turn alone: its poster, the repositories they
// may read, and their own message, the only text that can ask for a
// mutation.
func (h *Host) startTools(t *turn, lease platform.TurnLease, p plan, dir string, in turnInput) (plan, *turnTools, error) {
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
	runs := h.cfg.Runs
	if runs == nil {
		runs = tools.LocalRuns{}
	}
	set := tools.NewToolset(tools.Env{
		Root:      h.cfg.WorkspaceRoot,
		RepoRoots: config.WorkspaceRepoPaths(h.cfg.WorkspaceRoot),
		Forge:     forge,
		Runs:      runs,
		Attention: turnAttention{platform: h.cfg.Platform, lease: lease},
		Dispatch:  turnDispatch{platform: h.cfg.Platform, lease: lease},
		Executor:  h.cfg.Executor,
		Proposals: h.cfg.Proposals,
	}, tools.Turn{
		ConversationID:   t.cmd.ConversationID,
		TurnID:           t.cmd.TurnID,
		PosterAccountID:  t.cmd.PosterAccountID,
		PosterLogin:      login,
		PosterIsOperator: t.cmd.PosterIsOperator,
		ReadableRepos:    readable,
		Message:          in.gate,
		IssueProposed:    in.proposed,
		Proposal:         in.proposal,
	})
	server, err := tools.Start(set)
	if err != nil {
		set.Close()
		return p, nil, err
	}
	path := filepath.Join(dir, mcpConfigName)
	tt := &turnTools{set: set, server: server, config: path}
	if err := server.WriteConfig(path); err != nil {
		tt.close()
		return p, nil, err
	}
	opts := p.opts
	opts.MCPConfig = path
	p.cmd, p.args, p.env = p.builder.BuildConversationTurn(opts)
	return p, tt, nil
}

// turnAttention is the hosted service's turn-scoped attention routes bound
// to one turn's lease (#1587): the only way a turn reaches the cards. The
// lease never leaves the daemon; the tool server holds no credential.
type turnAttention struct {
	platform Platform
	lease    platform.TurnLease
}

func (a turnAttention) ListCards(ctx context.Context, q platform.TurnCardQuery) (platform.TurnCardList, error) {
	return a.platform.ListTurnCards(ctx, a.lease, q)
}

func (a turnAttention) ActOnCard(ctx context.Context, cardID string, act platform.TurnCardAct) (platform.TurnCardActResult, error) {
	return a.platform.ActOnTurnCard(ctx, a.lease, cardID, act)
}

// turnDispatch is the hosted service's turn-bound run dispatch bound to one
// turn's lease (#1894): the only way a turn starts work. The lease never
// leaves the daemon.
type turnDispatch struct {
	platform Platform
	lease    platform.TurnLease
}

func (d turnDispatch) DispatchRun(ctx context.Context, run platform.TurnRunRequest) (platform.TurnRunHandle, error) {
	return d.platform.DispatchTurnRun(ctx, d.lease, run)
}

// close ends the calls in flight, waits for the server to stop, releases
// the workspace and removes the config file. Only once it returned is the
// record of calls final. Every terminal report reads that record first, so
// the file holding the server's token never outlives the turn's report.
func (tt *turnTools) close() {
	tt.once.Do(func() {
		tt.server.Close()
		tt.set.Close()
		if err := os.Remove(tt.config); err != nil && !os.IsNotExist(err) {
			log.Printf("conversation: the tool server's config %s was not removed: %v", tt.config, err)
		}
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

// turnCalls is the record the retention class reads (#1893): the tool
// server's own record of every call it served, which no model output can
// change, and every call the stream shows that the server has no record of
// (one the adapter denied, one cut off before it reached the server, or a
// tool outside the boundary), matched by name. A mutating call either saw
// forces the mutation floor. The server is closed first, so a call still
// running is in its record.
func turnCalls(stream []ToolCall, tt *turnTools) []ToolCall {
	var out []ToolCall
	served := map[string]int{}
	if tt != nil {
		tt.close()
		for _, inv := range tt.invocations() {
			name := tools.QualifiedName(inv.Name)
			out = append(out, ToolCall{Name: name, Mutating: inv.Mutating})
			served[name]++
		}
	}
	for _, c := range stream {
		if served[c.Name] > 0 {
			served[c.Name]--
			continue
		}
		out = append(out, c)
	}
	return out
}
