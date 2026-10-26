// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !unix

package cmd

import "os/exec"

// killExecProcessGroup keeps the default cancellation on platforms
// without POSIX process groups: the shell process is killed, and
// WaitDelay bounds how long its children may hold the output open.
func killExecProcessGroup(*exec.Cmd) {}
