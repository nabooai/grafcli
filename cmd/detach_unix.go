//go:build !windows

package cmd

import (
	"os/exec"
	"syscall"
)

// detach puts the child in its own session so it outlives this process and
// never receives the terminal's signals.
func detach(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
