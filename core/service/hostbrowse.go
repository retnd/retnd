package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

// Issue #1179: the folder browser behind "Staging path on the host".
//
// A recovery to the host takes a NAME under the staging root, and until this
// the only way to choose one was to type it blind. This lists the folders
// below that root so a page (and `retnd system staging`) can offer them.
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
//   - It never leaves the root, and never lists through a symbolic link. A
//     link inside the root can point anywhere, and following one is the usual
//     way out of a directory that was meant to be a boundary. Every part of a
//     name is looked at with Lstat, which does not follow, and a part that is
//     a link is refused; links are never listed. The walk and the open both
//     go through os.Root, so the kernel holds the boundary too: os.Root does
//     follow a link that stays inside the root, so a link swapped in between
//     the walk and the open could still land on another folder under it, and
//     the folder opened is therefore compared with the one the walk found
//     (os.SameFile) and refused when they differ.
//   - What goes back to the caller is names relative to the root. A refusal
//     names no path of the host's.
//
// Files are not listed: the page chooses a folder, and the name of the file
// that lands in it is the recovery's own.
//
// # What one listing may cost
//
// It is an authenticated GET that anyone with a session can repeat, on hosts
// where a staging root can be a slow network mount. So the limit bounds the
// work and not only the answer: the folder is read in batches, files are
// dropped as they are read, at most a couple of limits' worth of names is
// held at once, the read stops after stagingScanBudget entries, and the
// context is looked at between batches. The HTTP layer puts a deadline on it.

// StagingDirectoryLimit is the most folders one listing returns. A folder holding
// more is cut and the answer says so, because a list that silently stops reads
// as a complete one.
const StagingDirectoryLimit = 1000

// StagingBrowseNameLimit bounds the relative name accepted, well above any real
// path under a staging root and well below anything worth walking. It is the
// one copy of the rule: the HTTP layer passes the name on as it came.
const StagingBrowseNameLimit = 4096

// stagingReadBatch is how many entries one ReadDir call asks for.
const stagingReadBatch = 512

// stagingScanBudget is the most entries, files included, one listing reads
// before it stops and says the folder was cut. A variable so a test can use a
// small one.
var stagingScanBudget = 100_000

// stagingBrowseBeforeOpen, when set, runs between the walk and the open. A
// test hook for a folder swapped for a link in that gap.
var stagingBrowseBeforeOpen func()

var (
	// ErrStagingBrowseInvalid is a name that is not a folder name under the root:
	// absolute, climbing, malformed, or through a symbolic link.
	ErrStagingBrowseInvalid = errors.New("that is not a folder name under the browsable root")
	// ErrStagingBrowseNotFound is a well-formed name that is not a folder there.
	ErrStagingBrowseNotFound = errors.New("there is no such folder under the browsable root")
	// ErrStagingBrowseUnavailable is a deployment with no staging root it would
	// use: no state directory, or a root that fails the custody check.
	ErrStagingBrowseUnavailable = errors.New("this deployment has no folder to browse")
	// ErrStagingBrowseNotReadable is a folder the host may not read (or could
	// not, an I/O error): a real folder, and not a bad name.
	ErrStagingBrowseNotReadable = errors.New("the host cannot read that folder")
)

// StagingDirectoryEntry is one sub-folder of a listed folder.
type StagingDirectoryEntry struct {
	// Name is the folder's own name.
	Name string
	// Path is its name relative to the root, the form a staging target takes.
	Path string
}

// StagingDirectory is one folder under the browsable root.
type StagingDirectory struct {
	// Root is the staging root, as the host spells it.
	Root string
	// Path is the listed folder relative to Root, "" for Root itself.
	Path string
	// Parent is the folder above Path, "" for Root, and nil at Root.
	Parent *string
	// Entries are the sub-folders, by name.
	Entries []StagingDirectoryEntry
	// Truncated is true when the folder holds more than StagingDirectoryLimit.
	Truncated bool
}

// ListStagingDirectory lists the sub-folders of rel under the staging root.
func (b *BackupService) ListStagingDirectory(ctx context.Context, rel string) (StagingDirectory, error) {
	if err := ctx.Err(); err != nil {
		return StagingDirectory{}, err
	}
	// The same root and the same custody check a recovery uses: a folder
	// recovery would refuse to write into is not one to offer.
	h := b.recoveryHost()
	if h.unavailable || h.staging == nil {
		return StagingDirectory{}, ErrStagingBrowseUnavailable
	}
	// recoveryHost checked custody once, when this process first needed it.
	// The directory can change after that, and the check is one Lstat, so a
	// listing asks again rather than offering a root that has since become
	// writable by another account.
	if err := ValidateStagingRoot(h.staging.Root); err != nil {
		b.logger.Error(ctx, "staging_browse_root", fmt.Errorf("assurance.staging_root: %w", err))
		return StagingDirectory{}, ErrStagingBrowseUnavailable
	}
	return listStagingDirectory(ctx, h.staging.Root, rel)
}

// stagingBrowseSegments validates rel and splits it. "" is the root.
func stagingBrowseSegments(rel string) ([]string, error) {
	if rel == "" {
		return nil, nil
	}
	if len(rel) > StagingBrowseNameLimit || !utf8.ValidString(rel) {
		return nil, ErrStagingBrowseInvalid
	}
	for _, r := range rel {
		// A control character, or a backslash (a separator on another host,
		// and never part of a name worth choosing). U+FFFD is a character a
		// real folder name can hold, so only bytes that are not UTF-8 at all
		// are refused, above.
		if r < 0x20 || r == 0x7f || r == '\\' {
			return nil, ErrStagingBrowseInvalid
		}
	}
	if strings.HasPrefix(rel, "/") {
		return nil, ErrStagingBrowseInvalid
	}
	segs := strings.Split(rel, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return nil, ErrStagingBrowseInvalid
		}
	}
	return segs, nil
}

// unreadable says whether err is the host refusing to read, or failing to.
func unreadable(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EIO)
}

func listStagingDirectory(ctx context.Context, rootPath, rel string) (StagingDirectory, error) {
	segs, err := stagingBrowseSegments(rel)
	if err != nil {
		return StagingDirectory{}, err
	}
	out := StagingDirectory{Root: rootPath, Path: rel, Entries: []StagingDirectoryEntry{}}
	if rel != "" {
		parent := ""
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			parent = rel[:i]
		}
		out.Parent = &parent
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
			// A staging root nobody has used yet. Nothing to list, and
			// not ours to create.
			if rel == "" {
				return out, nil
			}
			return StagingDirectory{}, ErrStagingBrowseNotFound
		case unreadable(err):
			return StagingDirectory{}, ErrStagingBrowseNotReadable
		}
		return StagingDirectory{}, fmt.Errorf("opening the browsable root: %w", err)
	}
	defer root.Close()

	// Every component must be a real folder. Lstat does not follow, so a
	// link is seen as a link and refused, and a missing one is not found.
	// walked is what the last component was, to hold the open to it.
	walked, err := root.Lstat(".")
	if err != nil {
		if unreadable(err) {
			return StagingDirectory{}, ErrStagingBrowseNotReadable
		}
		return StagingDirectory{}, fmt.Errorf("examining the browsable root: %w", err)
	}
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
			return StagingDirectory{}, ErrStagingBrowseNotFound
		case unreadable(err):
			// A folder on the way that the host may not look inside.
			return StagingDirectory{}, ErrStagingBrowseNotReadable
		default:
			// os.Root's own refusal of a name that escapes, or a link it
			// would not follow, is a refusal of the name.
			return StagingDirectory{}, ErrStagingBrowseInvalid
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return StagingDirectory{}, ErrStagingBrowseInvalid
		}
		if !fi.IsDir() {
			return StagingDirectory{}, ErrStagingBrowseNotFound
		}
		walked = fi
	}

	if stagingBrowseBeforeOpen != nil {
		stagingBrowseBeforeOpen()
	}
	open := "."
	if rel != "" {
		open = rel
	}
	f, err := root.Open(open)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return StagingDirectory{}, ErrStagingBrowseNotFound
		case unreadable(err):
			return StagingDirectory{}, ErrStagingBrowseNotReadable
		}
		// os.Root refuses a link that leads out of the root. Its error is not
		// one this package can name, so the name is looked at again: a part
		// that is a link now is a refusal of the name, as it was in the walk.
		if fi, lerr := root.Lstat(open); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return StagingDirectory{}, ErrStagingBrowseInvalid
		}
		return StagingDirectory{}, fmt.Errorf("opening a folder to list it: %w", err)
	}
	defer f.Close()
	// The folder opened must be the one the walk found. os.Root follows a
	// link that stays inside the root, so a folder swapped for one after the
	// walk would otherwise be listed in its place.
	opened, err := f.Stat()
	if err != nil {
		return StagingDirectory{}, fmt.Errorf("examining a folder to list it: %w", err)
	}
	if !os.SameFile(walked, opened) {
		return StagingDirectory{}, ErrStagingBrowseInvalid
	}

	names, cut, err := readFolderNames(ctx, f)
	if err != nil {
		return StagingDirectory{}, err
	}
	out.Truncated = cut
	for _, n := range names {
		p := n
		if rel != "" {
			p = rel + "/" + n
		}
		out.Entries = append(out.Entries, StagingDirectoryEntry{Name: n, Path: p})
	}
	return out, nil
}

// readFolderNames reads the real sub-folders of f in batches and returns the
// first StagingDirectoryLimit of them by name, and whether there were more (or
// the read stopped at the scan budget before the end). It never holds more
// than twice the limit: once the kept names reach that, they are sorted and
// the second half dropped, which keeps the first names by name whatever order
// the disk hands them back in.
func readFolderNames(ctx context.Context, f *os.File) (names []string, cut bool, err error) {
	scanned := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		ents, rerr := f.ReadDir(min(stagingReadBatch, stagingScanBudget-scanned))
		for _, e := range ents {
			scanned++
			// A real folder only: Type() is the entry's own type and does
			// not follow a link, so a link to a folder is not a folder here.
			if !e.Type().IsDir() {
				continue
			}
			names = append(names, e.Name())
			if len(names) >= 2*StagingDirectoryLimit {
				sort.Strings(names)
				names = names[:StagingDirectoryLimit]
				cut = true
			}
		}
		if errors.Is(rerr, io.EOF) || (rerr == nil && len(ents) == 0) {
			break
		}
		if rerr != nil {
			if unreadable(rerr) {
				return nil, false, ErrStagingBrowseNotReadable
			}
			return nil, false, fmt.Errorf("listing a folder: %w", rerr)
		}
		if scanned >= stagingScanBudget {
			// Reading on is what the budget is there to stop. One more
			// entry says whether the folder really holds more, so a folder
			// that ends exactly at the budget is not called cut.
			if more, _ := f.ReadDir(1); len(more) > 0 {
				cut = true
			}
			break
		}
	}
	sort.Strings(names)
	if len(names) > StagingDirectoryLimit {
		names = names[:StagingDirectoryLimit]
		cut = true
	}
	return names, cut, nil
}
