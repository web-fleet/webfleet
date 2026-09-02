//go:build unix

package service

import (
	"os"
	"syscall"
)

// fileUID returns the numeric owner UID of a file. Only meaningful on unix
// systems where os.FileInfo.Sys() exposes a syscall.Stat_t; the data-directory
// ownership check only runs on Linux (Install refuses earlier elsewhere).
func fileUID(info os.FileInfo) int {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}
