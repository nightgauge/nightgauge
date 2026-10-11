package conversation

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// turnProcess is one turn's child process, spawned as a process-group leader
// so a stop reaches everything it started (the stage spawn discipline,
// execution.Manager, #1253).
type turnProcess struct {
	cmd    *exec.Cmd
	pgid   int
	stdout io.ReadCloser
}

// withheldEnvPrefixes are host variables a turn's process never inherits: the
// forge tokens and every Nightgauge credential or setting. A turn's argv
// carries everything it needs, and the posture allows no tool that could read
// the environment, but a credential the child never holds cannot leak.
var withheldEnvPrefixes = []string{"NIGHTGAUGE_"}

var withheldEnv = map[string]bool{
	"GITHUB_TOKEN": true, "GH_TOKEN": true, "GH_ENTERPRISE_TOKEN": true,
	"GITHUB_ENTERPRISE_TOKEN": true, "GITLAB_TOKEN": true,
}

// turnEnv is the host environment less the withheld variables, plus the
// builder's own.
func turnEnv(base []string, extra map[string]string) []string {
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if withheldEnv[name] || hasAnyPrefix(name, withheldEnvPrefixes) {
			continue
		}
		if _, overridden := extra[name]; overridden {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// startTurnProcess spawns the turn's command in dir with stdin as its whole
// input. The message never touches argv.
func startTurnProcess(p plan, dir, stdin string) (*turnProcess, error) {
	cmd := exec.Command(p.cmd, p.args...)
	cmd.Dir = dir
	cmd.Env = turnEnv(os.Environ(), p.env)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Wait waits for the stdin copy too; a child that never reads its stdin
	// must not hold Wait past its own exit.
	cmd.WaitDelay = stopGrace
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &turnProcess{cmd: cmd, pgid: cmd.Process.Pid, stdout: stdout}, nil
}

// signal delivers sig to the turn's whole process group.
func (p *turnProcess) signal(sig syscall.Signal) {
	if p == nil || p.pgid <= 0 {
		return
	}
	if err := syscall.Kill(-p.pgid, sig); err != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(sig)
	}
}

// groupAlive reports whether any process of the group still exists:
// kill(-pgid, 0) succeeds, or is refused for permission, which also means a
// member exists.
func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// verifyGone kills whatever is left of the group and polls until kill -0 on
// it fails, or until deadline. Only a true return lets a turn be reported
// cancelled. The leader must already have been reaped (cmd.Wait), or it
// lingers as a zombie in its own group.
func (p *turnProcess) verifyGone(deadline time.Time) bool {
	for {
		if !groupAlive(p.pgid) {
			return true
		}
		p.signal(syscall.SIGKILL)
		if time.Now().After(deadline) {
			return !groupAlive(p.pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
