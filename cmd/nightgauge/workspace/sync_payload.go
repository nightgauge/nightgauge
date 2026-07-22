package workspacecmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	workspace "github.com/nightgauge/nightgauge/internal/knowledge/workspace"
	"github.com/spf13/cobra"
)

// SyncPayload is the JSON output of `workspace sync-payload`.
type SyncPayload struct {
	Workspace *WorkspaceRegisterMeta `json:"workspace,omitempty"`
	Repos     []RepoRef              `json:"repos"`
}

// WorkspaceRegisterMeta carries the workspace identity fields sent to the
// platform register endpoint.
type WorkspaceRegisterMeta struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

// RepoRef is the minimal owner/repo pair included in the register payload.
type RepoRef struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

// syncWorkspaceYAML is the minimal subset of .vscode/nightgauge-workspace.yaml
// needed for payload assembly.
type syncWorkspaceYAML struct {
	Name         string          // workspace.name
	Repositories []syncRepoEntry // repositories[]
}

type syncRepoEntry struct {
	Name string
	Path string
}

func syncPayloadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync-payload",
		Short: "Assemble the workspace register payload and print it as JSON",
		Long: `sync-payload reads .vscode/nightgauge-workspace.yaml (walking up from CWD),
derives the workspace slug and display_name, and reads each linked repository's
.nightgauge/config.yaml to assemble the repos array.

Outputs the JSON payload fragment used by the CLI agent register flow:
  { "workspace": { "slug": "...", "display_name": "..." }, "repos": [...] }

When no workspace YAML is found, outputs { "repos": [] }.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncPayload(cmd)
		},
	}
	return cmd
}

func runSyncPayload(cmd *cobra.Command) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("workspace sync-payload: getwd: %w", err)
	}

	wsRoot, err := workspace.DetectWorkspaceRoot(wd)
	if err != nil {
		// No workspace found — output empty payload (single-repo mode).
		return encodePayload(cmd, SyncPayload{Repos: []RepoRef{}})
	}

	yamlPath := filepath.Join(wsRoot, ".vscode", "nightgauge-workspace.yaml")
	wsYAML, err := parseSyncWorkspaceYAML(yamlPath)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: workspace sync-payload: parse workspace YAML: %v\n", err)
		return encodePayload(cmd, SyncPayload{Repos: []RepoRef{}})
	}

	var wsMeta *WorkspaceRegisterMeta
	if wsYAML.Name != "" {
		slug := workspace.GenerateSlug(wsYAML.Name)
		if slug != "" {
			wsMeta = &WorkspaceRegisterMeta{
				Slug:        slug,
				DisplayName: wsYAML.Name,
			}
		}
	}

	repos := assembleRepos(wsRoot, wsYAML.Repositories)

	return encodePayload(cmd, SyncPayload{
		Workspace: wsMeta,
		Repos:     repos,
	})
}

// assembleRepos builds the repos array by loading each workspace repository's
// .nightgauge/config.yaml and extracting owner/repo.
func assembleRepos(wsRoot string, entries []syncRepoEntry) []RepoRef {
	var repos []RepoRef
	for _, entry := range entries {
		repoAbs := filepath.Join(wsRoot, entry.Path)
		cfg, err := config.Load(repoAbs)
		if err != nil || cfg == nil {
			continue
		}
		owner := cfg.Owner
		repo := cfg.DefaultRepo
		if owner == "" || repo == "" {
			continue
		}
		repos = append(repos, RepoRef{Owner: owner, Repo: repo})
	}
	if repos == nil {
		repos = []RepoRef{}
	}
	return repos
}

func encodePayload(cmd *cobra.Command, payload SyncPayload) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("workspace sync-payload: encode json: %w", err)
	}
	return nil
}

// parseSyncWorkspaceYAML reads workspace name and repositories from the YAML file.
// Uses a simple line-by-line scanner to avoid importing a YAML library, matching
// the pattern established in internal/knowledge/knowledge.go.
func parseSyncWorkspaceYAML(path string) (syncWorkspaceYAML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return syncWorkspaceYAML{}, fmt.Errorf("read %s: %w", path, err)
	}

	var result syncWorkspaceYAML
	lines := strings.Split(string(data), "\n")

	inWorkspace := false
	inRepos := false
	var currentRepo *syncRepoEntry

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		isTopLevel := !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t")

		if isTopLevel {
			// Flush pending repo when leaving the repositories section.
			if inRepos && currentRepo != nil {
				result.Repositories = append(result.Repositories, *currentRepo)
				currentRepo = nil
			}

			inWorkspace = (trimmed == "workspace:")
			inRepos = (trimmed == "repositories:")
			continue
		}

		if inWorkspace {
			if kv, ok := splitKV(trimmed); ok && kv[0] == "name" {
				result.Name = kv[1]
			}
			continue
		}

		if !inRepos {
			continue
		}

		if strings.HasPrefix(trimmed, "- ") {
			if currentRepo != nil {
				result.Repositories = append(result.Repositories, *currentRepo)
			}
			currentRepo = &syncRepoEntry{}
			trimmed = strings.TrimPrefix(trimmed, "- ")
		}

		if currentRepo == nil {
			continue
		}

		if kv, ok := splitKV(trimmed); ok {
			switch kv[0] {
			case "name":
				currentRepo.Name = kv[1]
			case "path":
				currentRepo.Path = kv[1]
			}
		}
	}

	// Flush last pending repo.
	if inRepos && currentRepo != nil {
		result.Repositories = append(result.Repositories, *currentRepo)
	}

	return result, nil
}

// splitKV splits "key: value" into [key, value]. Returns ok=false when the
// line does not contain ": " or has no value.
func splitKV(line string) ([2]string, bool) {
	idx := strings.Index(line, ": ")
	if idx < 0 {
		// Handle "key:" with no value (empty string value)
		if strings.HasSuffix(line, ":") {
			return [2]string{strings.TrimSuffix(line, ":"), ""}, true
		}
		return [2]string{}, false
	}
	key := strings.TrimSpace(line[:idx])
	val := stripQuotes(strings.TrimSpace(line[idx+2:]))
	if key == "" {
		return [2]string{}, false
	}
	return [2]string{key, val}, true
}

// stripQuotes removes a single matching pair of surrounding quotes ("…" or
// '…') from a scalar value. This bespoke line-scanner does not import a YAML
// library, so quoted values like `name: "Acmesvc Product"` would otherwise
// leak the quote characters into display_name (#3859). Interior characters —
// including unmatched or embedded quotes — are left untouched.
func stripQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	first := s[0]
	last := s[len(s)-1]
	if (first == '"' || first == '\'') && last == first {
		return s[1 : len(s)-1]
	}
	return s
}
