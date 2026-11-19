//go:build unix

package export

import "golang.org/x/sys/unix"

// freeBytes is the space available to an unprivileged writer on the
// filesystem holding dir.
func freeBytes(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:gosec // block counts fit in int64
}
