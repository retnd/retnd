//go:build !unix

package fsopen

import (
	"fmt"
	"os"
)

func openNoBlock(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is %s", ErrNotRegular, path, fi.Mode().Type())
	}
	return os.Open(path)
}
