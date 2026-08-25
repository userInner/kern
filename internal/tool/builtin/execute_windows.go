//go:build windows

package builtin

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// configureProcessTree creates a process group and asks taskkill to terminate
// the complete descendant tree on cancellation.
func configureProcessTree(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(command.Process.Pid)).Run()
		if err == nil {
			return nil
		}
		killErr := command.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return errors.Join(err, killErr)
	}
}
