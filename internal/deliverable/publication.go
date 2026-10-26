package deliverable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Publication-boundary classification of planned paths (#2562).
//
// Spike #1684 planned docs/spikes/1684-*.md, a path the PRIVATE deny rule
// docs/spikes/** rejects, and nothing looked until feature-validate's ci-local
// step 17 minutes and $3.48 later. These helpers let planning ask the same
// question first. They do not port the matcher: the repository's own
// scripts/publication-boundary-check.py --classify is the single rule
// implementation, so a verdict here can never drift from the one CI enforces.

const (
	publicationManifest = ".github/publication-boundary.yaml"
	publicationChecker  = "scripts/publication-boundary-check.py"
	publicationTimeout  = 30 * time.Second
)

// PathVerdict is one --classify verdict: "allow", "deny" or "unclassified".
type PathVerdict struct {
	Path    string `json:"path"`
	Verdict string `json:"verdict"`
	Rule    string `json:"rule,omitempty"`
	Class   string `json:"class,omitempty"`
}

// PublicationResult is the outcome of CheckPublication.
type PublicationResult struct {
	// Applicable is false when the workspace has no boundary manifest or no
	// checker, so the repository has no publication boundary to enforce.
	Applicable bool          `json:"applicable"`
	Verdicts   []PathVerdict `json:"verdicts"`
}

// Rejected returns every verdict that is not "allow".
func (r PublicationResult) Rejected() []PathVerdict {
	var out []PathVerdict
	for _, v := range r.Verdicts {
		if v.Verdict != "allow" {
			out = append(out, v)
		}
	}
	return out
}

// CheckPublication classifies paths against the workspace's publication
// boundary. A checker that cannot run is an error, never an allow.
func CheckPublication(workspace string, paths []string) (PublicationResult, error) {
	for _, rel := range []string{publicationManifest, publicationChecker} {
		if _, err := os.Stat(filepath.Join(workspace, rel)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return PublicationResult{Applicable: false}, nil
			}
			return PublicationResult{}, fmt.Errorf("stat %s: %w", rel, err)
		}
	}
	var clean []string
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" {
			clean = append(clean, p)
		}
	}
	if len(clean) == 0 {
		return PublicationResult{Applicable: true}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), publicationTimeout)
	defer cancel()
	args := append([]string{publicationChecker, "--classify", "--"}, clean...)
	cmd := exec.CommandContext(ctx, "python3", args...)
	cmd.Dir = workspace
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	// Exit 1 is a verdict (some path rejected); anything else non-zero means
	// the checker could not run.
	if err != nil && !(errors.As(err, &exitErr) && exitErr.ExitCode() == 1) {
		return PublicationResult{}, fmt.Errorf("publication checker failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var verdicts []PathVerdict
	if jerr := json.Unmarshal(stdout.Bytes(), &verdicts); jerr != nil {
		return PublicationResult{}, fmt.Errorf("publication checker output is not a verdict list: %w", jerr)
	}
	if len(verdicts) != len(clean) {
		return PublicationResult{}, fmt.Errorf("publication checker returned %d verdicts for %d paths", len(verdicts), len(clean))
	}
	if err == nil && len(PublicationResult{Verdicts: verdicts}.Rejected()) > 0 {
		return PublicationResult{}, fmt.Errorf("publication checker exited 0 but rejected a path")
	}
	return PublicationResult{Applicable: true, Verdicts: verdicts}, nil
}
