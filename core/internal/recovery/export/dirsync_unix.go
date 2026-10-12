//go:build unix

package export

import "os"

// syncDir flushes a directory entry. Best effort: a filesystem that cannot
// fsync a directory has already given its answer about durability.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
