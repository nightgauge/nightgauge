package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// IssueFetcher abstracts fetching a single issue for dependency checks.
type IssueFetcher interface {
	GetIssue(ctx context.Context, owner, repo string, number int) (*types.Issue, error)
}

// IssueDepsResult is the output of the issue dependency (blockedBy) check.
type IssueDepsResult struct {
	IssueNumber         int              `json:"issue_number"`
	HasOpenDependencies bool             `json:"has_open_dependencies"`
	ShouldBlock         bool             `json:"should_block"`
	OpenDependencies    []OpenDependency `json:"open_dependencies"`
	OpenCount           int              `json:"open_count"`
}

// OpenDependency represents an open blocker issue.
type OpenDependency struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Repo   string `json:"repo"`
}

// EvaluateIssueDeps checks GitHub's blockedBy relationships for the given issue.
func EvaluateIssueDeps(ctx context.Context, fetcher IssueFetcher, owner, repo string, number int) (IssueDepsResult, error) {
	result := IssueDepsResult{IssueNumber: number}

	issue, err := fetcher.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return result, fmt.Errorf("failed to fetch issue #%d: %w", number, err)
	}

	for _, blocker := range issue.BlockedBy {
		if strings.EqualFold(blocker.State, "OPEN") {
			result.OpenDependencies = append(result.OpenDependencies, OpenDependency{
				Number: blocker.Number,
				Title:  blocker.Title,
				State:  blocker.State,
				Repo:   blocker.Repo,
			})
		}
	}

	result.OpenCount = len(result.OpenDependencies)
	result.HasOpenDependencies = result.OpenCount > 0
	result.ShouldBlock = result.HasOpenDependencies

	return result, nil
}

// DepsResult is the output of the dependency check hook.
type DepsResult struct {
	OK       bool        `json:"ok"`
	Required []DepStatus `json:"required"`
	Optional []DepStatus `json:"optional,omitempty"`
	Missing  []string    `json:"missing,omitempty"`
}

// DepStatus represents the status of a single dependency.
type DepStatus struct {
	Name       string `json:"name"`
	Available  bool   `json:"available"`
	Version    string `json:"version,omitempty"`
	MinVersion string `json:"min_version,omitempty"`
	Error      string `json:"error,omitempty"`
}

// VersionCheckResult is the output of the version consistency check.
type VersionCheckResult struct {
	OK      bool   `json:"ok"`
	Warning string `json:"warning,omitempty"`
}

// depSpec defines a dependency to check.
type depSpec struct {
	Name       string
	Binary     string
	VersionArg string // e.g., "--version"
	MinVersion string // e.g., "3.2" — compared as major.minor
	Required   bool
}

var requiredDeps = []depSpec{
	{Name: "git", Binary: "git", VersionArg: "--version", MinVersion: "2.0", Required: true},
}

var optionalDeps = []depSpec{
	{Name: "node", Binary: "node", VersionArg: "--version", Required: false},
	{Name: "npm", Binary: "npm", VersionArg: "--version", Required: false},
	{Name: "gh", Binary: "gh", VersionArg: "--version", Required: false},
	{Name: "prettier", Binary: "npx", VersionArg: "", Required: false},
	{Name: "gofmt", Binary: "gofmt", VersionArg: "", Required: false},
	{Name: "shfmt", Binary: "shfmt", VersionArg: "--version", Required: false},
	{Name: "rustfmt", Binary: "rustfmt", VersionArg: "--version", Required: false},
}

// EvaluateDeps checks all dependencies and returns their status.
func EvaluateDeps() DepsResult {
	result := DepsResult{OK: true}

	for _, dep := range requiredDeps {
		status := checkDep(dep)
		result.Required = append(result.Required, status)
		if !status.Available {
			result.OK = false
			result.Missing = append(result.Missing, dep.Name)
		}
	}

	for _, dep := range optionalDeps {
		status := checkDep(dep)
		result.Optional = append(result.Optional, status)
	}

	return result
}

// EvaluateDepsJSON returns the dependency check result as JSON bytes.
func EvaluateDepsJSON() ([]byte, error) {
	result := EvaluateDeps()
	return json.Marshal(result)
}

// checkDep checks a single dependency.
func checkDep(dep depSpec) DepStatus {
	status := DepStatus{
		Name:       dep.Name,
		MinVersion: dep.MinVersion,
	}

	path, err := exec.LookPath(dep.Binary)
	if err != nil {
		status.Available = false
		status.Error = fmt.Sprintf("%s not found in PATH", dep.Binary)
		return status
	}
	_ = path

	status.Available = true

	// Get version if possible
	if dep.VersionArg != "" {
		cmd := exec.Command(dep.Binary, dep.VersionArg)
		out, err := cmd.Output()
		if err == nil {
			status.Version = extractVersion(string(out))
		}
	}

	// Check minimum version if specified
	if dep.MinVersion != "" && status.Version != "" {
		if !meetsMinVersion(status.Version, dep.MinVersion) {
			status.Error = fmt.Sprintf("version %s < minimum %s", status.Version, dep.MinVersion)
		}
	}

	return status
}

// versionRegex extracts version numbers like "2.39.1" from version output.
var versionRegex = regexp.MustCompile(`(\d+\.\d+(?:\.\d+)?)`)

// extractVersion pulls the first version-like string from command output.
func extractVersion(output string) string {
	match := versionRegex.FindString(output)
	return match
}

// meetsMinVersion compares two version strings (major.minor).
func meetsMinVersion(version, minVersion string) bool {
	v := parseVersionParts(version)
	m := parseVersionParts(minVersion)

	if len(v) == 0 || len(m) == 0 {
		return true // can't compare, assume OK
	}

	// Compare major
	if v[0] > m[0] {
		return true
	}
	if v[0] < m[0] {
		return false
	}

	// Compare minor
	if len(v) > 1 && len(m) > 1 {
		return v[1] >= m[1]
	}

	return true
}

// parseVersionParts splits "2.39.1" into [2, 39, 1].
func parseVersionParts(version string) []int {
	parts := strings.SplitN(version, ".", 3)
	result := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		result = append(result, n)
	}
	return result
}

// EvaluateVersionCheck checks version consistency between plugin.json and SKILL.md.
// pluginVersion and skillVersion are the versions extracted from the respective files.
func EvaluateVersionCheck(pluginVersion, skillVersion string) VersionCheckResult {
	if pluginVersion == "" || skillVersion == "" {
		return VersionCheckResult{OK: true}
	}

	if pluginVersion != skillVersion {
		return VersionCheckResult{
			OK:      false,
			Warning: fmt.Sprintf("Version mismatch: plugin.json=%s, SKILL.md=%s", pluginVersion, skillVersion),
		}
	}

	return VersionCheckResult{OK: true}
}

// EvaluateVersionCheckJSON returns the version check result as JSON bytes.
func EvaluateVersionCheckJSON(pluginVersion, skillVersion string) ([]byte, error) {
	result := EvaluateVersionCheck(pluginVersion, skillVersion)
	return json.Marshal(result)
}
