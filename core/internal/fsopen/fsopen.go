// Package fsopen opens a stored copy the way a reader that must not be
// wedged by what is at the path has to.
//
// A scrub, a recovery test and a recovery source all read a path the journal
// recorded, and a path is not a promise about what is behind it. Anyone who
// can write the backup root can put a FIFO there, and a plain os.Open on a
// FIFO blocks until a writer shows up, in a call no context can interrupt.
// The caller then holds its lease, a global read slot and, because the
// scrub and recovery-test ticks run one after the other, the whole schedule.
//
// OpenRegular is the one place that rule is written. Reuse it for any reader
// of a recorded copy path rather than writing the check again.
package fsopen

import (
	"errors"
	"fmt"
	"os"
)

// ErrNotRegular is returned when the path names something other than a
// regular file: a FIFO, a socket, a device, a directory or a symlink.
var ErrNotRegular = errors.New("fsopen: not a regular file")

// OpenRegular opens path for reading and returns it only if it is a regular
// file.
//
// On unix the open itself cannot block (O_NONBLOCK) and does not follow a
// final symlink (O_NOFOLLOW), and the kind is then checked on the open
// descriptor with fstat, so there is no window between the check and the use
// in which the path can be swapped for something else. Elsewhere the kind is
// checked with Lstat first, which is the best that platform allows.
func OpenRegular(path string) (*os.File, error) {
	f, err := openNoBlock(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%w: %s is %s", ErrNotRegular, path, fi.Mode().Type())
	}
	return f, nil
}
