package build

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/detect"
)

// BuildResult is the structured output of a build run.
// The `commands` field maps to `commands_run` in dev-{N}.json via the skill.
type BuildResult struct {
	Ran       bool     `json:"ran"`
	Status    string   `json:"status"` // "passed" | "failed" | "skipped"
	Commands  []string `json:"commands"`
	Output    string   `json:"output"`
	Timestamp string   `json:"timestamp"`
}

// staleSDKMarkers are output strings that indicate a recoverable stale-SDK-dist error.
var staleSDKMarkers = []string{
	"RECOVERABLE: stale_sdk_dist",
	"SDK dist/index.js not found",
	"SDK dist is stale",
}

// RunBuild detects the build system in workdir and executes the build.
// Framework detection is shared with internal/ci (internal/detect) so the
// two layers cannot disagree about the same project (#195). Detection
// order: pubspec.yaml → go.mod → package.json with "build" script → skipped.
func RunBuild(ctx context.Context, workdir string) (BuildResult, error) {
	result := BuildResult{
		Ran:    false,
		Status: "skipped",
	}

	switch detect.Framework(workdir) {
	case detect.FrameworkFlutter:
		return runFlutterBuild(ctx, workdir)
	case detect.FrameworkGo:
		return runGoBuild(ctx, workdir)
	case detect.FrameworkNode:
		if hasBuildScript(filepath.Join(workdir, "package.json")) {
			return runNPMBuild(ctx, workdir)
		}
	}

	result.Timestamp = time.Now().UTC().Format(time.RFC3339)
	return result, nil
}

// RunBuildJSON is a convenience wrapper that returns JSON bytes.
func RunBuildJSON(ctx context.Context, workdir string) ([]byte, error) {
	result, err := RunBuild(ctx, workdir)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// runFlutterBuild runs `flutter analyze` as the fast compile-correctness
// gate: Dart has no separate build-only step, and this matches what
// internal/ci already assumes for the framework (#195).
func runFlutterBuild(ctx context.Context, workdir string) (BuildResult, error) {
	out, err := runCmd(ctx, workdir, "flutter", "analyze")
	ts := time.Now().UTC().Format(time.RFC3339)
	status := "passed"
	if err != nil {
		status = "failed"
	}
	return BuildResult{
		Ran:       true,
		Status:    status,
		Commands:  []string{"flutter analyze"},
		Output:    out,
		Timestamp: ts,
	}, nil
}

func runGoBuild(ctx context.Context, workdir string) (BuildResult, error) {
	out, err := runCmd(ctx, workdir, "go", "build", "./...")
	ts := time.Now().UTC().Format(time.RFC3339)
	status := "passed"
	if err != nil {
		status = "failed"
	}
	return BuildResult{
		Ran:       true,
		Status:    status,
		Commands:  []string{"go build ./..."},
		Output:    out,
		Timestamp: ts,
	}, nil
}

func runNPMBuild(ctx context.Context, workdir string) (BuildResult, error) {
	out, err := runCmd(ctx, workdir, "npm", "run", "build")
	ts := time.Now().UTC().Format(time.RFC3339)

	if err == nil {
		return BuildResult{
			Ran:       true,
			Status:    "passed",
			Commands:  []string{"npm run build"},
			Output:    out,
			Timestamp: ts,
		}, nil
	}

	// Check for recoverable stale SDK dist.
	if isStaleSDK(out) {
		healOut, healErr := runCmd(ctx, workdir, "npm", "run", "-w", "@nightgauge/sdk", "build")
		if healErr == nil {
			fmt.Printf("=== SDK rebuilt — retrying extension build ===\n%s\n", healOut)
			retryOut, retryErr := runCmd(ctx, workdir, "npm", "run", "build")
			ts2 := time.Now().UTC().Format(time.RFC3339)
			status := "failed"
			if retryErr == nil {
				status = "passed"
			}
			return BuildResult{
				Ran:       true,
				Status:    status,
				Commands:  []string{"npm run -w @nightgauge/sdk build", "npm run build"},
				Output:    retryOut,
				Timestamp: ts2,
			}, nil
		}
	}

	return BuildResult{
		Ran:       true,
		Status:    "failed",
		Commands:  []string{"npm run build"},
		Output:    out,
		Timestamp: ts,
	}, nil
}

func isStaleSDK(output string) bool {
	for _, marker := range staleSDKMarkers {
		if strings.Contains(output, marker) {
			return true
		}
	}
	return false
}

func hasBuildScript(pkgJSONPath string) bool {
	data, err := os.ReadFile(pkgJSONPath)
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return strings.Contains(string(data), `"build"`)
	}
	_, ok := pkg.Scripts["build"]
	return ok
}

func runCmd(ctx context.Context, workdir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workdir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
