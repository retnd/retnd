//go:build unix

package service

import "syscall"

// freeBytesUnder is the space an unprivileged writer has left on the
// filesystem holding dir.
func freeBytesUnder(dir string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true //nolint:unconvert // field widths differ by platform
}
