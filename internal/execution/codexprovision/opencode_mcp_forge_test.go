package codexprovision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// The test doubles of the forge an OpenCode stage's MCP servers are read
// from, and the one call every MCP test makes, ProvisionOpenCode with an
// McpSource.

// fixtureRepo is the repository the pipeline records for a fixture's run.
const fixtureRepo = "fixture-owner/fixture-repo"

// gitForge serves the default branch of a bare repository the test controls,
// origin, as a forge would: the branch origin's HEAD names, its head commit,
// and each file at that commit, read from origin itself and never from the
// repository a stage runs in. It records every repository it is asked for.
type gitForge struct {
	origin string
	mu     sync.Mutex
	asked  []string
}

func (f *gitForge) DefaultBranchFiles(ctx context.Context, owner, name string, paths []string) (*forgetypes.DefaultBranchFiles, error) {
	f.mu.Lock()
	f.asked = append(f.asked, owner+"/"+name)
	f.mu.Unlock()
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", f.origin}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	head, err := git("symbolic-ref", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("the fixture forge has no HEAD: %w", err)
	}
	branch := strings.TrimPrefix(head, "refs/heads/")
	commit, err := git("rev-parse", head)
	if err != nil {
		return nil, fmt.Errorf("the fixture forge's %s has no commit: %w", branch, err)
	}
	out := &forgetypes.DefaultBranchFiles{Branch: branch, Commit: commit, Files: map[string]forgetypes.RepoFile{}}
	for _, p := range paths {
		entry, err := git("ls-tree", commit, "--", p)
		if err != nil {
			return nil, err
		}
		if entry == "" {
			continue
		}
		if mode := strings.Fields(entry)[0]; mode != "100644" && mode != "100755" {
			out.Files[p] = forgetypes.RepoFile{}
			continue
		}
		content, err := exec.CommandContext(ctx, "git", "-C", f.origin, "cat-file", "blob", commit+":"+p).Output()
		if err != nil {
			return nil, err
		}
		out.Files[p] = forgetypes.RepoFile{Regular: true, Content: content}
	}
	return out, nil
}

func (f *gitForge) askedFor() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// stalledForge never answers until release is closed, whatever its ctx says,
// as a forge whose connection hangs.
type stalledForge struct{ release chan struct{} }

func (f stalledForge) DefaultBranchFiles(context.Context, string, string, []string) (*forgetypes.DefaultBranchFiles, error) {
	<-f.release
	return nil, errors.New("released")
}

// failingForge answers every read with err.
type failingForge struct{ err error }

func (f failingForge) DefaultBranchFiles(context.Context, string, string, []string) (*forgetypes.DefaultBranchFiles, error) {
	return nil, f.err
}

// fixedForge answers every read with files.
type fixedForge struct {
	files *forgetypes.DefaultBranchFiles
}

func (f fixedForge) DefaultBranchFiles(context.Context, string, string, []string) (*forgetypes.DefaultBranchFiles, error) {
	return f.files, nil
}

// forgeOf is the forge serving origin for fixtureRepo.
func forgeOf(origin string) *gitForge { return &gitForge{origin: origin} }

// provisionFrom runs ProvisionOpenCode on wt in the test's environment, its
// MCP servers read from forge for fixtureRepo.
func provisionFrom(wt string, forge interface {
	DefaultBranchFiles(context.Context, string, string, []string) (*forgetypes.DefaultBranchFiles, error)
}) (OpenCodeProvision, error) {
	return provisionSource(wt, McpSource{Repo: fixtureRepo, Forge: forge})
}

// provisionSource runs ProvisionOpenCode on wt in the test's environment with
// src.
func provisionSource(wt string, src McpSource) (OpenCodeProvision, error) {
	return ProvisionOpenCode(context.Background(), wt, src, os.LookupEnv)
}

// provision runs ProvisionOpenCode on wt in the test's environment, for a run
// that records no repository.
func provision(wt string) (OpenCodeProvision, error) {
	return provisionSource(wt, McpSource{})
}

// readForge is ReadForgeMcpServers for src.
func readForge(src McpSource) (map[string]PipelineMcpServer, string, error) {
	return ReadForgeMcpServers(context.Background(), src)
}
