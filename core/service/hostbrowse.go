package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"syscall"
)

// Issue #1179: the folder browser behind "Staging path on the host".
//
// A recovery to the host takes a NAME under the staging root, and until this
// the only way to choose one was to type it blind. This lists the folders
// below that root so a page (and `retnd system browse`) can offer them.
//
// # What makes a read of the host's file system safe to offer
//
//   - It only reads. Nothing here creates, opens for writing, chmods or
//     removes anything, and a root that does not exist yet is listed as empty
//     and is not created.
//   - It only reaches the staging root and what is below it. The root is the
//     only one the deployment allows: it is the one place a recovery may write,
//     so it is the one place a person has a reason to choose from. A name is
//     relative to it and a name that is absolute, or climbs with "..", or
//     carries a separator the host does not mean, is refused before the file
//     system is touched.
//   - A symbolic link is never followed and never listed. A link inside the
//     root can point anywhere, and following one is the usual way out of a
//     directory that was meant to be a boundary. Folders are therefore listed
//     only when they are real folders, and a name that goes through a link is
//     refused. The walk itself is done through os.Root, so the kernel enforces
//     the boundary as well and a link swapped in after the check still cannot
//     lead out.
//   - What goes back to the caller is names relative to the root. A refusal
//     names no path of the host's.
//
// Files are not listed: the page chooses a folder, and the name of the file
// that lands in it is the recovery's own.

// HostDirectoryLimit is the most folders one listing returns. A folder holding
// more is cut and the answer says so, because a list that silently stops reads
// as a complete one.
const HostDirectoryLimit = 1000

// hostBrowseNameLimit bounds the relative name accepted, well above any real
// path under a staging root and well below anything worth walking.
const hostBrowseNameLimit = 4096

var (
	// ErrHostBrowseInvalid is a name that is not a folder name under the root:
	// absolute, climbing, malformed, or through a symbolic link.
	ErrHostBrowseInvalid = errors.New("that is not a folder name under the browsable root")
	// ErrHostBrowseNotFound is a well-formed name that is not a folder there.
	ErrHostBrowseNotFound = errors.New("there is no such folder under the browsable root")
	// ErrHostBrowseUnavailable is a deployment with no staging root it would
	// use: no state directory, or a root that fails the custody check.
	ErrHostBrowseUnavailable = errors.New("this deployment has no folder to browse")
)

// HostDirectoryEntry is one sub-folder of a listed folder.
type HostDirectoryEntry struct {
	// Name is the folder's own name.
	Name string
	// Path is its name relative to the root, the form a staging target takes.
	Path string
}

// HostDirectory is one folder under the browsable root.
type HostDirectory struct {
	// Root is the staging root, as the host spells it.
	Root string
	// Path is the listed folder relative to Root, "" for Root itself.
	Path string
	// Parent is the folder above Path, "" for Root, and nil at Root.
	Parent *string
	// Entries are the sub-folders, by name.
	Entries []HostDirectoryEntry
	// Truncated is true when the folder holds more than HostDirectoryLimit.
	Truncated bool
}

// BrowseHostDirectory lists the sub-folders of rel under the staging root.
func (b *BackupService) BrowseHostDirectory(ctx context.Context, rel string) (HostDirectory, error) {
	if err := ctx.Err(); err != nil {
		return HostDirectory{}, err
	}
	// The same root and the same custody check a recovery uses: a folder
	// recovery would refuse to write into is not one to offer.
	h := b.recoveryHost()
	if h.unavailable || h.staging == nil {
		return HostDirectory{}, ErrHostBrowseUnavailable
	}
	return listHostDirectory(h.staging.Root, rel)
}

// hostBrowseSegments validates rel and splits it. "" is the root.
func hostBrowseSegments(rel string) ([]string, error) {
	if rel == "" {
		return nil, nil
	}
	if len(rel) > hostBrowseNameLimit {
		return nil, ErrHostBrowseInvalid
	}
	for _, r := range rel {
		// A control character, a backslash (a separator on another host,
		// and never part of a name worth choosing) or a byte that is not
		// text at all.
		if r < 0x20 || r == 0x7f || r == '\\' || r == 0xFFFD {
			return nil, ErrHostBrowseInvalid
		}
	}
	if strings.HasPrefix(rel, "/") {
		return nil, ErrHostBrowseInvalid
	}
	segs := strings.Split(rel, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return nil, ErrHostBrowseInvalid
		}
	}
	return segs, nil
}

func listHostDirectory(rootPath, rel string) (HostDirectory, error) {
	segs, err := hostBrowseSegments(rel)
	if err != nil {
		return HostDirectory{}, err
	}
	out := HostDirectory{Root: rootPath, Path: rel, Entries: []HostDirectoryEntry{}}
	if rel != "" {
		parent := ""
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			parent = rel[:i]
		}
		out.Parent = &parent
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			// A staging root nobody has used yet. Nothing to list, and
			// not ours to create.
			if rel == "" {
				return out, nil
			}
			return HostDirectory{}, ErrHostBrowseNotFound
		}
		return HostDirectory{}, fmt.Errorf("opening the browsable root: %w", err)
	}
	defer root.Close()

	// Every component must be a real folder. Lstat does not follow, so a
	// link is seen as a link and refused, and a missing one is not found.
	at := ""
	for _, s := range segs {
		if at != "" {
			at += "/"
		}
		at += s
		fi, err := root.Lstat(at)
		switch {
		case err == nil:
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
			return HostDirectory{}, ErrHostBrowseNotFound
		default:
			// os.Root's own refusal of a name that escapes, or a link it
			// would not follow, is a refusal of the name.
			return HostDirectory{}, ErrHostBrowseInvalid
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return HostDirectory{}, ErrHostBrowseInvalid
		}
		if !fi.IsDir() {
			return HostDirectory{}, ErrHostBrowseNotFound
		}
	}

	open := "."
	if rel != "" {
		open = rel
	}
	f, err := root.Open(open)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return HostDirectory{}, ErrHostBrowseNotFound
		}
		return HostDirectory{}, fmt.Errorf("opening a folder to list it: %w", err)
	}
	defer f.Close()
	ents, err := f.ReadDir(-1)
	if err != nil {
		return HostDirectory{}, fmt.Errorf("listing a folder: %w", err)
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for _, e := range ents {
		// A real folder only: Type() is the entry's own type and does not
		// follow a link, so a link to a folder is not a folder here.
		if !e.Type().IsDir() {
			continue
		}
		if len(out.Entries) >= HostDirectoryLimit {
			out.Truncated = true
			break
		}
		p := e.Name()
		if rel != "" {
			p = rel + "/" + e.Name()
		}
		out.Entries = append(out.Entries, HostDirectoryEntry{Name: e.Name(), Path: p})
	}
	return out, nil
}
