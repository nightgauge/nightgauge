package e2e

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// errE2ETimeout is returned by runCmd when the subprocess is killed for
// exceeding its timeout, so RunE2E can distinguish a timeout from a genuine
// non-zero exit and map it to Status: "timeout" instead of "failed".
var errE2ETimeout = errors.New("e2e: command timed out")

// reapGrace bounds the post-kill reap on the timeout path. A descendant that
// escaped the killed process group (e.g. via setsid) can keep a captured
// pipe open indefinitely; without a bound, waiting for it to reap would
// reinstate the exact hang the timeout exists to prevent. Short enough not
// to meaningfully extend the caller-visible timeout, long enough that the
// normal kill-and-EOF path never brushes it.
const reapGrace = 2 * time.Second

// E2EDetectResult is the structured output of e2e framework detection.
type E2EDetectResult struct {
	Detected    bool     `json:"detected"`
	Frameworks  []string `json:"frameworks"`
	ConfigFiles []string `json:"config_files"`
	TestDirs    []string `json:"test_dirs"`
	Timestamp   string   `json:"timestamp"`
}

// E2ERunResult is the structured output of an e2e test run.
type E2ERunResult struct {
	Ran       bool     `json:"ran"`
	Status    string   `json:"status"` // "passed" | "failed" | "skipped" | "timeout"
	Framework string   `json:"framework"`
	Commands  []string `json:"commands"`
	Output    string   `json:"output"`
	Timestamp string   `json:"timestamp"`
}

// DefaultE2ETimeout bounds an e2e subprocess run when the caller does not
// supply an explicit timeout, so unattended pipeline runs never hang forever
// on a stuck framework.
const DefaultE2ETimeout = 10 * time.Minute

// DetectE2E scans workdir for E2E test frameworks.
// Detection order: Playwright > Cypress > Vitest > Jest > Go test.
func DetectE2E(_ context.Context, workdir string) (E2EDetectResult, error) {
	ts := time.Now().UTC().Format(time.RFC3339)
	result := E2EDetectResult{
		Detected:    false,
		Frameworks:  []string{},
		ConfigFiles: []string{},
		TestDirs:    []string{},
		Timestamp:   ts,
	}

	// Collect test directories.
	for _, dir := range []string{"e2e", "tests/e2e", "test/e2e"} {
		if fileExists(filepath.Join(workdir, dir)) {
			result.TestDirs = append(result.TestDirs, dir)
		}
	}

	// Playwright.
	if cfgs := playwrightConfigs(workdir); len(cfgs) > 0 {
		result.Frameworks = append(result.Frameworks, "playwright")
		result.ConfigFiles = append(result.ConfigFiles, cfgs...)
	}

	// Cypress.
	if cfgs := cypressConfigs(workdir); len(cfgs) > 0 {
		result.Frameworks = append(result.Frameworks, "cypress")
		result.ConfigFiles = append(result.ConfigFiles, cfgs...)
	}

	// Vitest.
	if cfgs := vitestConfigs(workdir); len(cfgs) > 0 {
		result.Frameworks = append(result.Frameworks, "vitest")
		result.ConfigFiles = append(result.ConfigFiles, cfgs...)
	}

	// Jest.
	if cfgs := jestConfigs(workdir); len(cfgs) > 0 {
		result.Frameworks = append(result.Frameworks, "jest")
		result.ConfigFiles = append(result.ConfigFiles, cfgs...)
	}

	// Go test.
	if hasGoTest(workdir) {
		result.Frameworks = append(result.Frameworks, "go")
	}

	result.Detected = len(result.Frameworks) > 0
	return result, nil
}

// RunE2E executes the E2E test suite in workdir, bounded by timeout (a
// non-positive timeout falls back to DefaultE2ETimeout).
// If framework is non-empty it skips detection and uses the specified framework.
// Detection order mirrors DetectE2E: first detected framework wins when auto-detecting.
func RunE2E(ctx context.Context, workdir, framework string, timeout time.Duration) (E2ERunResult, error) {
	if timeout <= 0 {
		timeout = DefaultE2ETimeout
	}
	ts := time.Now().UTC().Format(time.RFC3339)

	if framework == "" {
		detected, err := DetectE2E(ctx, workdir)
		if err != nil {
			return E2ERunResult{Ran: false, Status: "skipped", Timestamp: ts}, err
		}
		if len(detected.Frameworks) == 0 {
			return E2ERunResult{Ran: false, Status: "skipped", Timestamp: ts}, nil
		}
		framework = detected.Frameworks[0]
	}

	cmd, args := frameworkCommand(framework)
	if cmd == "" {
		return E2ERunResult{
			Ran:       false,
			Status:    "skipped",
			Framework: framework,
			Timestamp: ts,
		}, nil
	}

	commandStr := cmd
	for _, a := range args {
		commandStr += " " + a
	}

	out, err := runCmd(ctx, workdir, timeout, cmd, args...)
	ts = time.Now().UTC().Format(time.RFC3339)
	status := "passed"
	if err != nil {
		status = "failed"
		if errors.Is(err, errE2ETimeout) {
			status = "timeout"
		}
	}

	return E2ERunResult{
		Ran:       true,
		Status:    status,
		Framework: framework,
		Commands:  []string{commandStr},
		Output:    out,
		Timestamp: ts,
	}, nil
}

// frameworkCommand returns the command and arguments for the given framework.
func frameworkCommand(framework string) (string, []string) {
	switch framework {
	case "playwright":
		return "npx", []string{"playwright", "test"}
	case "cypress":
		return "npx", []string{"cypress", "run"}
	case "vitest":
		return "npx", []string{"vitest", "run", "--run"}
	case "jest":
		return "npx", []string{"jest", "e2e"}
	case "go":
		return "go", []string{"test", "-run", "E2E", "./..."}
	}
	return "", nil
}

// hasPlaywrightConfig returns true if any playwright config file exists in workdir.
func hasPlaywrightConfig(workdir string) bool {
	return len(playwrightConfigs(workdir)) > 0
}

// hasCypressConfig returns true if any cypress config file exists in workdir.
func hasCypressConfig(workdir string) bool {
	return len(cypressConfigs(workdir)) > 0
}

// hasVitestConfig returns true if any vitest config file exists in workdir.
func hasVitestConfig(workdir string) bool {
	return len(vitestConfigs(workdir)) > 0
}

// hasJestConfig returns true if any jest config file exists in workdir.
func hasJestConfig(workdir string) bool {
	return len(jestConfigs(workdir)) > 0
}

// hasGoTest returns true if workdir contains a go.mod and at least one _test.go file.
func hasGoTest(workdir string) bool {
	if !fileExists(filepath.Join(workdir, "go.mod")) {
		return false
	}
	found := false
	_ = filepath.WalkDir(workdir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if !d.IsDir() && len(d.Name()) > 8 && d.Name()[len(d.Name())-8:] == "_test.go" {
			found = true
		}
		return nil
	})
	return found
}

func playwrightConfigs(workdir string) []string {
	return existingFiles(workdir, []string{
		"playwright.config.ts",
		"playwright.config.js",
		"playwright.config.mts",
		"playwright.config.mjs",
	})
}

func cypressConfigs(workdir string) []string {
	return existingFiles(workdir, []string{
		"cypress.config.ts",
		"cypress.config.js",
		"cypress.config.json",
		"cypress.json",
	})
}

func vitestConfigs(workdir string) []string {
	return existingFiles(workdir, []string{
		"vitest.config.ts",
		"vitest.config.js",
		"vitest.config.mts",
		"vitest.config.mjs",
	})
}

func jestConfigs(workdir string) []string {
	return existingFiles(workdir, []string{
		"jest.config.ts",
		"jest.config.js",
		"jest.config.json",
		"jest.config.mjs",
	})
}

func existingFiles(workdir string, names []string) []string {
	var found []string
	for _, name := range names {
		p := filepath.Join(workdir, name)
		if fileExists(p) {
			found = append(found, p)
		}
	}
	return found
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// runCmd runs name/args in workdir, bounded by timeout. Output is streamed
// incrementally from stdout/stderr so a killed process still yields whatever
// was captured before the kill (AC4), rather than the all-or-nothing
// bytes.Buffer capture this replaced. On timeout the whole process group is
// killed (AC3) — a plain cmd.Process.Kill() would only kill the immediate
// npx/go process and leave framework child processes (e.g. a spawned
// browser) orphaned.
func runCmd(ctx context.Context, workdir string, timeout time.Duration, name string, args ...string) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command(name, args...)
	cmd.Dir = workdir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	if err := cmd.Start(); err != nil {
		return "", err
	}

	var mu sync.Mutex
	var buf []byte
	var wg sync.WaitGroup
	// Copy raw bytes rather than scanning lines. bufio.Scanner caps a single
	// token at 64KB and reports the overflow only via Err(); a longer line
	// would end the read loop, leave the pipe undrained, block the child on
	// write, and surface as a timeout on a suite that actually passed (#238).
	// Playwright emits such lines routinely (reporter JSON, long stack traces).
	stream := func(r io.Reader) {
		defer wg.Done()
		b := make([]byte, 32*1024)
		for {
			n, readErr := r.Read(b)
			if n > 0 {
				mu.Lock()
				buf = append(buf, b[:n]...)
				mu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}

	wg.Add(2)
	go stream(stdout)
	go stream(stderr)

	waitDone := make(chan error, 1)
	go func() {
		wg.Wait()
		waitDone <- cmd.Wait()
	}()

	select {
	case waitErr := <-waitDone:
		mu.Lock()
		out := string(buf)
		mu.Unlock()
		return out, waitErr
	case <-execCtx.Done():
		if pgid, pidErr := syscall.Getpgid(cmd.Process.Pid); pidErr == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = cmd.Process.Kill()
		}
		select {
		case <-waitDone:
		case <-time.After(reapGrace):
			// A descendant escaped the killed process group and is still
			// holding a captured pipe open; give up waiting rather than
			// hang past the timeout this path exists to enforce.
		}
		mu.Lock()
		out := string(buf)
		mu.Unlock()
		return out, errE2ETimeout
	}
}
