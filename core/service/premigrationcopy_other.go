//go:build !unix

package service

// freeBytesUnder reports "unknown" off unix: the copy is then attempted and a
// failed write is a warning like any other.
func freeBytesUnder(string) (uint64, bool) { return 0, false }
