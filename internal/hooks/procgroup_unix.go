//go:build unix

package hooks

import (
	"os/exec"
	"syscall"
)

// isolateProcessGroup puts the hook in its own process group and makes
// the context cancel kill that whole group, so a child the shell forked
// cannot outlive the timeout or an interrupt and hold the output pipes
// open (see shellRunner). Negative pid addresses the group; if the group
// is already gone, fall back to the process alone.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
