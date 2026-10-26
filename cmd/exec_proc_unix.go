// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build unix

package cmd

import (
	"os/exec"
	"syscall"
)

// killExecProcessGroup starts cmd in its own process group and makes its
// cancellation kill the whole group, so a deadline also stops the
// children the shell started (`make test`, `sleep`), not only the shell.
func killExecProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
