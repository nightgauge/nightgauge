// Package dockercompose owns docker-compose teardown for the per-issue
// pipeline stacks (project name "issue-NNN"). It is shared by the Go
// worktree cleanup, the `nightgauge cleanup` CLI, the doctor orphan
// check, and the scheduler startup reconciler. See docs/PIPELINE_EXECUTION.md
// for the project-naming contract.
package dockercompose

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// dockerTimeout is the per-invocation timeout for any docker subprocess.
// Compose teardown can be slow when stopping many containers; 30s gives
// docker enough headroom while keeping cleanup bounded.
const dockerTimeout = 30 * time.Second

// issueProjectRe matches compose project names spawned by the pipeline.
var issueProjectRe = regexp.MustCompile(`^issue-(\d+)$`)

// Project describes a single docker-compose project as observed via
// `docker compose ls --format json`.
type Project struct {
	Name        string
	IssueNumber int
	// Status is the raw status reported by `docker compose ls` (e.g.
	// "running(2)", "exited(1)"). Not used for control flow — purely
	// informational for human / JSON output.
	Status string
}

// TeardownOptions controls TeardownProject behaviour.
type TeardownOptions struct {
	DryRun       bool
	RemoveImages bool
	Logger       Logger
}

// TeardownResult records what was actually removed (or would have been
// removed under DryRun).
type TeardownResult struct {
	Project       string   `json:"project"`
	IssueNumber   int      `json:"issue_number"`
	ComposeRan    bool     `json:"compose_ran"`
	ImagesRemoved []string `json:"images_removed"`
	DryRun        bool     `json:"dry_run"`
	Skipped       bool     `json:"skipped"`
	SkipReason    string   `json:"skip_reason,omitempty"`
}

// Logger is the minimal logging interface used by the helper. nil is
// equivalent to the default stderr logger.
type Logger interface {
	Warnf(format string, args ...any)
	Infof(format string, args ...any)
}

// IsAvailable returns true iff `docker version` succeeds, which implies the
// docker CLI exists on PATH AND the daemon is reachable. Used as a soft gate
// so callers can skip teardown entirely on hosts without docker.
func IsAvailable(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "docker", "version", "--format", "{{.Server.Version}}")
	return cmd.Run() == nil
}

// composeLsEntry mirrors the subset of fields we read from `docker compose
// ls --format json`. Newer docker versions emit a JSON array; older versions
// emit one object per line. We tolerate both.
type composeLsEntry struct {
	Name   string `json:"Name"`
	Status string `json:"Status"`
}

// ListIssueProjects returns every compose project whose name matches
// "issue-<digits>". Returns nil and a nil error when docker isn't available.
func ListIssueProjects(ctx context.Context) ([]Project, error) {
	if !IsAvailable(ctx) {
		return nil, nil
	}
	cctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "docker", "compose", "ls", "--all", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker compose ls: %w", err)
	}
	entries, err := parseComposeLs(out)
	if err != nil {
		return nil, err
	}
	projects := make([]Project, 0, len(entries))
	for _, e := range entries {
		match := issueProjectRe.FindStringSubmatch(e.Name)
		if match == nil {
			continue
		}
		num := 0
		fmt.Sscanf(match[1], "%d", &num)
		projects = append(projects, Project{
			Name:        e.Name,
			IssueNumber: num,
			Status:      e.Status,
		})
	}
	return projects, nil
}

// parseComposeLs accepts either a JSON array (Docker Engine 24+) or
// newline-delimited objects (older versions) and decodes both shapes.
func parseComposeLs(data []byte) ([]composeLsEntry, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []composeLsEntry
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil, fmt.Errorf("parse compose ls JSON array: %w", err)
		}
		return arr, nil
	}
	var entries []composeLsEntry
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e composeLsEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("parse compose ls JSON line: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// TeardownProject runs `docker compose -p <name> down -v --remove-orphans`
// followed by `docker rmi -f` for any locally-built image tagged with the
// project prefix. It is idempotent: running on a non-existent project is a
// no-op success. When docker is unavailable the function returns a Skipped
// result with no error so callers can treat teardown as soft-fail.
func TeardownProject(ctx context.Context, name string, opts TeardownOptions) (TeardownResult, error) {
	logger := opts.Logger
	if logger == nil {
		logger = stderrLogger{}
	}
	res := TeardownResult{
		Project: name,
		DryRun:  opts.DryRun,
	}
	if match := issueProjectRe.FindStringSubmatch(name); match != nil {
		fmt.Sscanf(match[1], "%d", &res.IssueNumber)
	}
	if !IsAvailable(ctx) {
		res.Skipped = true
		res.SkipReason = "docker not available"
		logger.Warnf("dockercompose: skipping teardown for %s — docker not available", name)
		return res, nil
	}
	if opts.DryRun {
		images := discoverProjectImages(ctx, name, logger)
		res.ImagesRemoved = images
		res.ComposeRan = false
		return res, nil
	}

	// `docker compose -p <name> down -v --remove-orphans` is idempotent;
	// it exits 0 even when the project does not exist.
	cctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	downCmd := exec.CommandContext(cctx, "docker", "compose", "-p", name, "down", "-v", "--remove-orphans")
	if out, err := downCmd.CombinedOutput(); err != nil {
		// Idempotency tolerance: some docker versions report "no such
		// project" with a non-zero exit. Treat as success.
		if isNoSuchProjectError(out) {
			res.ComposeRan = true
		} else {
			logger.Warnf("dockercompose: docker compose down failed for %s: %v: %s", name, err, strings.TrimSpace(string(out)))
			return res, nil // soft-fail
		}
	} else {
		res.ComposeRan = true
	}

	if opts.RemoveImages {
		images := discoverProjectImages(ctx, name, logger)
		for _, img := range images {
			if err := removeImage(ctx, img); err != nil {
				logger.Warnf("dockercompose: docker rmi failed for %s: %v", img, err)
				continue
			}
			res.ImagesRemoved = append(res.ImagesRemoved, img)
		}
	}
	return res, nil
}

// discoverProjectImages enumerates locally-built images whose repository
// name starts with the project prefix. We list `docker images` and filter
// by prefix rather than guessing service names so we pick up any tag the
// compose file produced.
func discoverProjectImages(ctx context.Context, project string, logger Logger) []string {
	cctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "docker", "images", "--format", "{{.Repository}}")
	out, err := cmd.Output()
	if err != nil {
		logger.Warnf("dockercompose: docker images failed: %v", err)
		return nil
	}
	prefix := project + "-"
	seen := map[string]bool{}
	var images []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		repo := strings.TrimSpace(line)
		if repo == "" || repo == "<none>" {
			continue
		}
		if !strings.HasPrefix(repo, prefix) {
			continue
		}
		if seen[repo] {
			continue
		}
		seen[repo] = true
		images = append(images, repo)
	}
	return images
}

func removeImage(ctx context.Context, image string) error {
	cctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "docker", "rmi", "-f", image)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func isNoSuchProjectError(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "no such project") || strings.Contains(s, "no resources found")
}

// IssueNumberFromProject parses the trailing digits from a compose project
// name of the form "issue-NNN". Returns (0, false) for any other shape.
func IssueNumberFromProject(name string) (int, bool) {
	match := issueProjectRe.FindStringSubmatch(name)
	if match == nil {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(match[1], "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

// stderrLogger is the default logger when callers pass nil. It emits a
// minimal `[WARN]` / `[INFO]` prefix so output is greppable.
type stderrLogger struct{}

func (stderrLogger) Warnf(format string, args ...any) {
	fmt.Fprintf(stderrWriter(), "[WARN] "+format+"\n", args...)
}

func (stderrLogger) Infof(format string, args ...any) {
	fmt.Fprintf(stderrWriter(), "[INFO] "+format+"\n", args...)
}

// stderrWriter is overridable in tests via a package-level var. Default is
// os.Stderr.
var defaultStderr io.Writer = os.Stderr

func stderrWriter() io.Writer { return defaultStderr }
