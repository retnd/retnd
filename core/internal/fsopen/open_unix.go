//go:build unix

package fsopen

import (
	"fmt"
	"os"
	"syscall"
)

func openNoBlock(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if err == syscall.ELOOP || err == syscall.EMLINK {
			return nil, fmt.Errorf("%w: %s is a symbolic link", ErrNotRegular, path)
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
