//go:build !unix

package service

import "os"

// fileUID is a stub for non-unix platforms. The data-directory ownership check
// is only reachable on Linux (Install returns earlier elsewhere).
func fileUID(info os.FileInfo) int {
	return -1
}
