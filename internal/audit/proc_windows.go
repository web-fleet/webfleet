//go:build windows

package audit

import (
	"os"
	"os/exec"
)

func commandContextCmd(cmd *exec.Cmd) {}

func killTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
