//go:build !unix

package export

import "errors"

func freeBytes(string) (int64, error) {
	return 0, errors.New("free space cannot be measured on this platform")
}
