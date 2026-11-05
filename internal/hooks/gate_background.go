package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/contractrollout"
)

// EvaluateGateBackground is the PreToolUse:Bash check that refuses to start
// the full local gate in a way that cannot finish (#2953).
//
// A foreground Bash call is killed at twice its timeout (1200 s by default),
// and the core gate takes 1,100-1,800 s, so a foreground gate is discarded
// after twenty minutes. A `&` inside a foreground call does not help: the
// process is orphaned and nothing waits for it. The only form that finishes
// is the Bash tool's own `run_in_background: true` with a `timeout` at least
// the gate's limit (hooks.pre_push.timeout_seconds, else its default).
//
// It is not stage-scoped: an interactive session hits the same kill. It fails
// open on any parse error so it can never wedge a session.
func EvaluateGateBackground(inputJSON []byte) GateDecision {
	var input GateInput
	if err := json.Unmarshal(inputJSON, &input); err != nil {
		return Allow()
	}
	workDir := input.Cwd
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	return evaluateGateBackground(input, gateLimitSeconds(workDir), declaredGateSteps(workDir))
}

// gateLimitSeconds resolves the gate's limit, falling back to the default.
func gateLimitSeconds(workDir string) int {
	cfg, err := config.Load(workDir)
	if err != nil {
		return config.DefaultPrePushTimeoutSeconds
	}
	return config.ResolvePrePushTimeoutSeconds(cfg)
}

// declaredGateSteps returns the repository's declared gate when it is a
// single step, the only form a single command can start. A multi-step gate is
// started through `nightgauge ci gate-run`, which is matched by name.
func declaredGateSteps(workDir string) []string {
	gate, err := contractrollout.ResolveGate(workDir)
	if err != nil || len(gate.Steps) != 1 {
		return nil
	}
	return gate.Steps[0]
}

func evaluateGateBackground(input GateInput, limitSeconds int, declared []string) GateDecision {
	if input.ToolName != "Bash" {
		return Allow()
	}
	var ti BashToolInput
	if err := json.Unmarshal(input.ToolInput, &ti); err != nil || ti.Command == "" {
		return Allow()
	}
	gate := ""
	for _, seg := range ExpandWrappers(SplitSegments(ti.Command)) {
		if name := gateStartName(seg.CommandArgv(), declared); name != "" {
			gate = name
			break
		}
	}
	if gate == "" {
		return Allow()
	}

	requiredMs := limitSeconds * 1000
	if limitSeconds <= 0 {
		requiredMs = maxBackgroundTimeoutMs
	}
	if requiredMs > maxBackgroundTimeoutMs {
		requiredMs = maxBackgroundTimeoutMs
	}
	var problem string
	switch {
	case hasBackgroundAmpersand(ti.Command):
		problem = "a `&` inside the call orphans the gate; nothing waits for it"
	case !ti.RunInBackground:
		problem = "it is a foreground call, which is killed at twice its timeout (1200 s by default)"
	case ti.Timeout < requiredMs:
		problem = fmt.Sprintf("its timeout is %d ms, below the gate's limit of %d ms", ti.Timeout, requiredMs)
	default:
		return Allow()
	}
	return Block(fmt.Sprintf(
		"the full local gate (%s) cannot finish as started: %s. Start it with the Bash tool's "+
			"`run_in_background: true` and `timeout: %d` (ms, the gate's limit from "+
			"hooks.pre_push.timeout_seconds), with no `&`, and wait for its completion "+
			"notification. Never run `gate_command` directly: run `nightgauge ci gate-run` (#2953).",
		gate, problem, requiredMs))
}

// maxBackgroundTimeoutMs is the Bash tool's ceiling for a background timeout.
const maxBackgroundTimeoutMs = 7_200_000

// shellNames are interpreters whose first non-flag argument is the script run.
var shellNames = map[string]bool{"bash": true, "sh": true, "zsh": true}

// launcherNames run the next word as the command.
var launcherNames = map[string]bool{"nohup": true, "exec": true, "time": true, "nice": true, "setsid": true}

// gateStartName names the gate the argv starts, or "" when it starts none.
// Only command position counts: `cat scripts/ci-local.sh` is not a start.
func gateStartName(argv, declared []string) string {
	if len(argv) == 0 {
		return ""
	}
	if len(declared) > 0 && hasArgvPrefix(argv, declared) {
		return strings.Join(declared, " ")
	}
	for len(argv) > 1 && launcherNames[baseName(argv[0])] {
		argv = argv[1:]
	}
	run := argv
	if shellNames[baseName(run[0])] {
		run = run[1:]
		for len(run) > 0 && strings.HasPrefix(run[0], "-") && run[0] != "-c" {
			run = run[1:]
		}
	}
	if len(run) > 0 && isGateScript(run[0]) {
		return contractrollout.DefaultGateScript
	}
	if len(argv) >= 3 && isNightgaugeBinary(argv[0]) {
		switch argv[1] + " " + argv[2] {
		case "ci gate-run", "pre-push validate":
			return "nightgauge " + argv[1] + " " + argv[2]
		}
	}
	return ""
}

func isGateScript(word string) bool {
	return filepath.Clean(word) == contractrollout.DefaultGateScript
}

// isNightgaugeBinary accepts the binary by name or path, and the variables
// the skills resolve it into ($BINARY, $NIGHTGAUGE_BIN).
func isNightgaugeBinary(word string) bool {
	if baseName(word) == "nightgauge" {
		return true
	}
	switch strings.Trim(word, "{}$") {
	case "BINARY", "NIGHTGAUGE_BIN":
		return true
	}
	return false
}

func hasArgvPrefix(argv, prefix []string) bool {
	if len(argv) < len(prefix) {
		return false
	}
	for i, w := range prefix {
		if argv[i] != w {
			return false
		}
	}
	return true
}

// hasBackgroundAmpersand reports a top-level `&` that backgrounds a process.
// `&&`, `>&`, `&>` and `2>&1` are not backgrounding; quoted text and
// here-document bodies never count.
func hasBackgroundAmpersand(cmd string) bool {
	s := stripHeredocs(cmd)
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' {
				i++
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '\\':
			i++
		case '&':
			if i+1 < len(s) && (s[i+1] == '&' || s[i+1] == '>') {
				i++
				continue
			}
			if i > 0 && (s[i-1] == '>' || s[i-1] == '<') {
				continue
			}
			return true
		}
	}
	return false
}
