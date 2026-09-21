//go:build unix

package main

import (
	"errors"
	"os/exec"
	"syscall"
)

// isolateProcess pone al hijo en su propio grupo de procesos para poder matar de una vez a imapsync
// y a lo que lance (el filtro antivirus y su shell).
func isolateProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	hardenProcess(cmd.SysProcAttr)
	cmd.Cancel = func() error { return killGroup(cmd) }
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
