//go:build windows

package evalrunner

import (
	"os"
	"os/exec"
)

func configureProcessTree(command *exec.Cmd) {
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return command.Process.Kill()
	}
}
