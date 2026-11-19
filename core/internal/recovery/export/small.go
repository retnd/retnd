package export

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
)

// Available reports the free bytes on the filesystem holding the staging
// root, creating the root first if it is not there yet (Begin does the same
// before it stages anything). A pre-flight reads it before a byte is staged.
func (s *Staging) Available() (int64, error) {
	if s.Root == "" {
		return 0, errors.New("recovery: no staging root is configured")
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return 0, err
	}
	return s.free()
}

// WriteSmall stages data (a manifest, not a recovered copy) the same way a
// verified copy is staged: an exclusive partial in the staging root, synced
// and closed, handed back as a Staged that is published or uploaded and
// discarded like any other. It checks the reserve first and leaves nothing
// behind on a failure.
func (s *Staging) WriteSmall(data []byte) (staged *Staged, err error) {
	need := int64(len(data))
	f, err := s.Begin(need)
	if err != nil {
		return nil, err
	}
	defer s.Release(need)
	closed := false
	defer func() {
		if err == nil {
			return
		}
		if !closed {
			_ = f.Close()
		}
		if rmErr := s.remove(f.Name()); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			err = errors.Join(err, &CleanupError{Failures: []CleanupFailure{{Path: f.Name(), Err: rmErr}}})
		}
	}()
	if _, werr := f.Write(data); werr != nil {
		return nil, wrapWrite(werr)
	}
	if serr := f.Sync(); serr != nil {
		return nil, wrapWrite(serr)
	}
	closed = true
	if cerr := f.Close(); cerr != nil {
		return nil, wrapWrite(cerr)
	}
	sum := sha256.Sum256(data)
	return &Staged{Path: f.Name(), Size: need, Digest: hex.EncodeToString(sum[:]), staging: s, refs: 1}, nil
}
