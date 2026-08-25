//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package builtin

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureProcessTree gives the command its own process group so cancellation
// terminates descendants as well as the direct child.
func configureProcessTree(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
