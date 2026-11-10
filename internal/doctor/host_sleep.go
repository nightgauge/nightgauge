package doctor

import (
	"bufio"
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// System sleep enabled on AC power (#3136).
//
// A macOS host that idle-sleeps keeps the autonomous scheduler dispatching in
// DarkWake until the host force-sleeps it mid-stage. A wall-clock limit then
// expires while it sleeps. The daemon holds a caffeinate assertion while the
// scheduler runs, and the orchestrator books a run lost to sleep as
// host_slept, but neither helps a stage started from a terminal or a scheduler
// that is not running. So doctor names the setting. It is a warning, never a
// failure: a host that sleeps still runs every stage it is awake for.

const (
	hostSleepCheck = "host_sleep"
	hostSleepCode  = "NGD113"
)

// hostSleepPmsetTimeout bounds `pmset -g custom` so a wedged pmset cannot hang
// doctor.
const hostSleepPmsetTimeout = 5 * time.Second

// hostSleepGOOS and hostSleepPmset are the check's reach into the machine.
// Variables so a test can supply them.
var (
	hostSleepGOOS  = runtime.GOOS
	hostSleepPmset = func(ctx context.Context) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, hostSleepPmsetTimeout)
		defer cancel()
		return exec.CommandContext(ctx, "pmset", "-g", "custom").Output()
	}
)

func init() {
	builtinChecks = append(builtinChecks, Check{
		ID: hostSleepCheck, Title: "Host sleep on AC power", Group: "hygiene", Code: hostSleepCode,
		Run: func(ctx context.Context, env *Env) []Finding {
			fs, detail := hostSleepFindings(ctx)
			env.SetDetail(hostSleepCheck, detail)
			return fs
		},
	})
}

func hostSleepFindings(ctx context.Context) ([]Finding, string) {
	if hostSleepGOOS != "darwin" {
		return nil, "skipped: not macOS"
	}
	out, err := hostSleepPmset(ctx)
	if err != nil {
		return nil, "skipped: `pmset -g custom` did not run: " + err.Error()
	}
	value, ok := acSleepSetting(string(out))
	if !ok {
		return nil, "no AC power profile reported"
	}
	if value == "0" {
		return nil, "system sleep on AC power is disabled"
	}
	f := newFinding(hostSleepCheck, hostSleepCode, SeverityWarning,
		"System sleep is enabled on AC power (sleep "+value+")",
		"`pmset -g custom` reports `sleep "+value+"` under AC Power, so the host idle-sleeps after "+
			value+" minute(s). A stage running while it sleeps can outlive its time limit; the run is "+
			"then booked host_slept and retried. The daemon holds a `caffeinate -i -s` assertion only "+
			"while the autonomous scheduler runs.",
		map[string]string{"ac_sleep_minutes": value}, []string{"ac", value},
		manualRemedy("disable-ac-sleep", "Disable system sleep on AC power", hostSleepCheck,
			"Run `sudo pmset -c sleep 0`",
			"Or keep the autonomous scheduler running, which holds the daemon's caffeinate assertion"))
	return []Finding{f}, "system sleep on AC power: " + value + " minute(s)"
}

// acSleepSetting returns the `sleep` value of the "AC Power:" section of
// `pmset -g custom` output. ok is false when there is no AC section or it has
// no sleep line.
func acSleepSetting(out string) (value string, ok bool) {
	inAC := false
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Section headers start in column 0 and end with a colon:
		// "AC Power:", "Battery Power:", "UPS Power:".
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && strings.HasSuffix(trimmed, ":") {
			inAC = trimmed == "AC Power:"
			continue
		}
		if !inAC {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && fields[0] == "sleep" {
			return fields[1], true
		}
	}
	return "", false
}
