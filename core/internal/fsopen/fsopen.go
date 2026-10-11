package fsopen

import "os"

// OpenRegular opens path for reading.
func OpenRegular(path string) (*os.File, error) { return os.Open(path) }
