// Package procgroup runs a command as the leader of its own process group and
// kills that group the moment the leader exits, on every exit path: a zero
// exit, a non-zero exit, a timeout and a cancel (#3303).
//
// Every OpenCode 2.x process Nightgauge starts with --standalone spawns a
// private `serve --stdio --port 0` holding a loopback listener, and spike
// #3197 saw it still alive just after its client exited. A kill only on
// cancel leaves it running after every normal exit.
//
// The kill fires between the leader's exit and its reaping. waitid with
// WNOWAIT leaves the leader a zombie, which pins its pid and so the group id
// it leads: a kill sent after cmd.Wait has reaped the leader can reach an
// unrelated group that was handed the recycled id. It also fires before the
// output readers finish, so a survivor holding a stdout or stderr pipe cannot
// keep cmd.Wait blocked until WaitDelay or a stage timeout.
package procgroup

import (
	"errors"
	"os/exec"
	"syscall"
)

// Prepare makes cmd lead its own process group, killed whole with SIGKILL
// when cmd's context is done. It is for callers with no Cancel of their own.
func Prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// Group is a started command whose process group is SIGKILLed the moment its
// leader exits, before the leader is reaped.
type Group struct {
	cmd    *exec.Cmd
	killed chan struct{}
}

// errNoProcessGroup is a command that does not lead its own process group,
// whose group kill would reach the caller's group.
var errNoProcessGroup = errors.New("procgroup: command does not set SysProcAttr.Setpgid")

// Start starts cmd, which must set SysProcAttr.Setpgid, and watches its
// leader: once the leader exits, the group is killed. A failed watch skips
// the kill and never blocks Wait.
func Start(cmd *exec.Cmd) (*Group, error) {
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		return nil, errNoProcessGroup
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	g := &Group{cmd: cmd, killed: make(chan struct{})}
	go func(pid int) {
		defer close(g.killed)
		if waitLeaderExitNoReap(pid) == nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL) // ESRCH: the group is already empty
		}
	}(cmd.Process.Pid)
	return g, nil
}

// Wait waits for the group kill, then for cmd, and returns cmd.Wait's error.
func (g *Group) Wait() error {
	<-g.killed
	return g.cmd.Wait()
}

// Run starts cmd with Start and waits for it with Wait.
func Run(cmd *exec.Cmd) error {
	g, err := Start(cmd)
	if err != nil {
		return err
	}
	return g.Wait()
}
