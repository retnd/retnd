package export

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ownedFile is an entry the walk found, with the time it was last written.
type ownedFile struct {
	Path    string
	ModTime time.Time
}

// walkOwned lists the partials and outputs under root. It does not follow
// symlinks (WalkDir never does) and it skips a root that does not exist
// yet, which is a clean state rather than an error.
func walkOwned(root string) (partials, outputs []ownedFile, err error) {
	if _, serr := os.Lstat(root); serr != nil {
		if os.IsNotExist(serr) {
			return nil, nil, nil
		}
		return nil, nil, serr
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		switch {
		case isPartial(d.Name()):
			partials = append(partials, ownedFile{p, info.ModTime()})
		case isOutput(d.Name()):
			outputs = append(outputs, ownedFile{p, info.ModTime()})
		}
		return nil
	})
	return partials, outputs, err
}
