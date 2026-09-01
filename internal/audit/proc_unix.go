//go:build unix

package audit

import (
	"os"
	"os/exec"
	"syscall"
)

// commandContextCmd runs the browser in its own process group so the whole
// Chromium tree (main process and renderers) can be terminated together on
// timeout or cancellation instead of leaving orphaned processes behind.
func commandContextCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		return p.Kill()
	}
	return nil
}