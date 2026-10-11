package export

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// IndexStore keeps built chunk indexes on disk so the verifying read that
// makes one is paid once per artifact and not once per download.
//
// # It is a cache
//
// Losing a file, or the whole directory, costs one more verifying read and
// nothing else, which is why a file that fails its own checks, or that is
// about different bytes than the journal now records, is treated as absent
// and not as an error. It holds digests, never content, so it is small
// (32 bytes per chunk: 8 MiB for a terabyte at the default chunk size).
//
// # Bounded
//
// Sweep removes indexes not used for MaxAge and, past MaxFiles, the least
// recently used. A use refreshes the file's modification time.
type IndexStore struct {
	Dir      string
	MaxFiles int
	MaxAge   time.Duration
	Now      func() time.Time
}

// Defaults for the bounds a zero value gets.
const (
	DefaultIndexMaxFiles = 1024
	DefaultIndexMaxAge   = 30 * 24 * time.Hour

	indexSuffix = ".rcvidx"
	indexTmp    = ".tmp-"
)

func (s *IndexStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// IndexKey names the index of an artifact's bytes. The chunk size is part of
// it so a change of chunk size misses instead of being read wrongly.
func IndexKey(artifactID, sha256Hex string, chunkSize int64) string {
	h := sha256.Sum256([]byte(artifactID + "\x00" + strings.ToLower(sha256Hex) + "\x00" + strconv.FormatInt(ClampChunkSize(chunkSize), 10)))
	return hex.EncodeToString(h[:])
}

func (s *IndexStore) path(key string) string { return filepath.Join(s.Dir, key+indexSuffix) }

// Load returns the stored index for key if there is one, it passes its own
// checks and it is about the bytes exp describes.
func (s *IndexStore) Load(key string, exp Expect) (*ChunkIndex, bool) {
	p := s.path(key)
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	ix, err := UnmarshalChunkIndex(raw)
	if err != nil || !ix.Matches(exp) {
		return nil, false
	}
	t := s.now()
	_ = os.Chtimes(p, t, t)
	return ix, true
}

// Save stores ix under key. The file appears whole or not at all.
func (s *IndexStore) Save(key string, ix *ChunkIndex) error {
	raw, err := ix.MarshalBinary()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Dir, indexTmp+"*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(raw)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	if err := os.Rename(tmp, s.path(key)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Remove drops one index, for a copy found to disagree with it.
func (s *IndexStore) Remove(key string) { _ = os.Remove(s.path(key)) }

// Sweep applies the bounds and removes torn temporary files. It reports how
// many files it removed; a file it cannot remove is counted in the error.
func (s *IndexStore) Sweep() (removed int, err error) {
	ents, rerr := os.ReadDir(s.Dir)
	if errors.Is(rerr, fs.ErrNotExist) {
		return 0, nil
	}
	if rerr != nil {
		return 0, rerr
	}
	maxFiles, maxAge := s.MaxFiles, s.MaxAge
	if maxFiles <= 0 {
		maxFiles = DefaultIndexMaxFiles
	}
	if maxAge <= 0 {
		maxAge = DefaultIndexMaxAge
	}
	type f struct {
		path string
		mod  time.Time
	}
	var keep []f
	now := s.now()
	drop := func(p string) {
		if e := os.Remove(p); e != nil && !errors.Is(e, fs.ErrNotExist) {
			err = errors.Join(err, e)
			return
		}
		removed++
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		p := filepath.Join(s.Dir, e.Name())
		switch {
		case strings.HasPrefix(e.Name(), indexTmp):
			if now.Sub(info.ModTime()) > time.Hour {
				drop(p)
			}
		case strings.HasSuffix(e.Name(), indexSuffix):
			if now.Sub(info.ModTime()) > maxAge {
				drop(p)
			} else {
				keep = append(keep, f{p, info.ModTime()})
			}
		}
	}
	if len(keep) > maxFiles {
		sort.Slice(keep, func(i, j int) bool { return keep[i].mod.After(keep[j].mod) })
		for _, k := range keep[maxFiles:] {
			drop(k.path)
		}
	}
	return removed, err
}
