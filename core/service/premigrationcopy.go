package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/retnd/retnd/core/internal/state"
)

// This file keeps a copy of the journal as it was just before a migration, on
// disk, once that migration has succeeded (#1584).
//
// snapshot.go argues that the engine's own copy is in memory because the
// moment it is used is a moment something has already failed, and a second
// read off disk on that path is a second chance to fail. That argument is
// about the failure path, and this is not on it. This runs only after the
// migration has committed, from the bytes snapshotSQLite already holds, so it
// does no read of the journal at all. The in-memory copy is still what undoes
// a failed migration, and nothing here changes when restore can fire: the
// engine never reads a retained copy back. It is there for a person, with the
// steps in docs/install.md ("Rolling back a release that carried a
// migration"), because only `install --mode upgrade` takes an archive and
// anyone who upgrades by pulling an image gets nothing.
//
// What makes it safe to add:
//
//   - It cannot cost a migration its success. Every failure here (no room, a
//     directory that will not be made, a write that fails) is logged at
//     warning level and the start carries on, because the copy is a courtesy
//     the engine did not promise. A half-written copy is removed, never left
//     under a name an operator would trust.
//   - It lives in the state directory, so it is on the same filesystem as the
//     journal by construction and cannot be a copy that quietly landed on a
//     different volume or a tmpfs. The write is checked against the free space
//     there first, with headroom, so it does not push the journal's own
//     filesystem into the state where SQLite cannot write.
//   - It is private. The directory is 0700 and every file 0600, set
//     explicitly rather than trusted to the umask.
//   - It is written under a dot-name and renamed into place last, and the
//     MANIFEST is the last file written, so a directory that carries the real
//     name and a MANIFEST is a complete copy.
//
// The cap and the pruning order are fixed, not configurable:
//
//   - At most retainedCopyCap copies are kept, the new one included. Past that
//     the oldest go first (by the time in the name, then by sequence), and
//     each removal is logged with its path. The installer refuses past five
//     archives instead, but an archive holds private keys and this holds a
//     journal of lifecycle rows, and refusing here would mean the migration
//     that just ran, the one whose way back matters most, is the one with no
//     copy.
//   - The copy just written is never removed, whatever its name sorts like.
//   - Only a directory that matches the name this file writes AND holds a
//     MANIFEST is ever counted or removed. Anything else in the state
//     directory, including a directory an operator made with a similar name,
//     is left alone and not counted.
//   - A copy whose schema is above the newest migration this binary carries
//     was made by a newer build (the operator rolled back, which is what these
//     copies are for). This build cannot tell what that copy is the way back
//     from, so it is never pruned and does not count toward the cap.

const (
	retainedCopyCap       = 5
	retainedCopyPrefix    = "pre-migration-"
	retainedManifestName  = "MANIFEST"
	retainedTimeLayout    = "20060102T150405Z"
	retainedHeadroomBytes = 1 << 20
	retainedMaxSequence   = 99
)

var retainedCopyPattern = regexp.MustCompile(`^pre-migration-(\d{8}T\d{6}Z)-schema(\d+)(?:-(\d+))?$`)

// premigrationFreeBytes reports how many bytes a process without special
// privileges can still write under dir, and whether that is known at all.
// It is a variable so the tests can stand in for a full disk.
var premigrationFreeBytes = freeBytesUnder

// retainPreMigrationCopy writes snap's captured files to a new directory next
// to dbPath, then prunes past the cap. It never returns an error: see the
// comment at the top of this file for why every failure is a warning.
func retainPreMigrationCopy(ctx context.Context, dbPath string, schemaBefore int, snap *sqliteSnapshot) {
	if !snap.existed[dbPath] {
		return
	}
	dir := filepath.Dir(dbPath)
	sweepStalePartialCopies(ctx, dir)

	var total int64
	for _, p := range snap.paths {
		if snap.existed[p] && !strings.HasSuffix(p, shmSuffix) {
			total += int64(len(snap.content[p]))
		}
	}
	if free, known := premigrationFreeBytes(dir); known && uint64(total)+retainedHeadroomBytes > free {
		slog.WarnContext(ctx, "pre-migration copy of the journal skipped: not enough free space beside it",
			"state_dir", dir, "needed_bytes", total+retainedHeadroomBytes, "free_bytes", free)
		return
	}

	final, err := writeRetainedCopy(dir, dbPath, schemaBefore, snap, time.Now().UTC())
	if err != nil {
		slog.WarnContext(ctx, "pre-migration copy of the journal could not be written, the migration itself succeeded", "error", err)
		return
	}
	slog.InfoContext(ctx, "kept a copy of the journal as it was before the migration",
		"path", final, "schema_before", schemaBefore, "bytes", total)

	pruneRetainedCopies(ctx, dir, filepath.Base(final))
}

var partialCopyPattern = regexp.MustCompile(`^\.pre-migration-\d+$`)

// sweepStalePartialCopies removes the dot-directories writeRetainedCopy leaves
// when a process dies between making one and renaming it into place. Each holds
// a full journal and nothing else would ever clean it. We hold the exclusive
// journal lock and the startup lock, so no other start is writing one, which is
// what makes every match stale. Only a real directory (never a symlink) with the
// exact name MkdirTemp gives is touched, and each removal is logged.
func sweepStalePartialCopies(ctx context.Context, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !partialCopyPattern.MatchString(e.Name()) || !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(path); err != nil {
			slog.WarnContext(ctx, "a stale half-written pre-migration copy could not be removed", "path", path, "error", err)
			continue
		}
		slog.WarnContext(ctx, "removed a half-written pre-migration copy left by an interrupted start", "path", path)
	}
}

func writeRetainedCopy(dir, dbPath string, schemaBefore int, snap *sqliteSnapshot, now time.Time) (string, error) {
	partial, err := os.MkdirTemp(dir, ".pre-migration-*")
	if err != nil {
		return "", fmt.Errorf("creating the copy directory in %s: %w", dir, err)
	}
	done := false
	defer func() {
		if !done {
			_ = os.RemoveAll(partial)
		}
	}()
	if err := os.Chmod(partial, 0o700); err != nil {
		return "", err
	}

	for _, p := range snap.paths {
		if !snap.existed[p] || strings.HasSuffix(p, shmSuffix) {
			continue
		}
		if err := writeSyncedFile(filepath.Join(partial, filepath.Base(p)), snap.content[p]); err != nil {
			return "", fmt.Errorf("writing %s: %w", filepath.Base(p), err)
		}
	}
	manifest := fmt.Sprintf(`This is a copy of the journal as it was before a migration ran, kept by the engine.

taken:          %s
schema_before:  %d
files:          %s

The engine never reads this directory. To go back to the build that held this
schema, stop the stack and follow "Rolling back a release that carried a
migration" in docs/install.md, using these files in place of an archive's
state.db and state.db-wal. Leave state.db-shm out; SQLite rebuilds it.
`, now.Format(time.RFC3339), schemaBefore, strings.Join(retainedFileNames(snap), ", "))
	if err := writeSyncedFile(filepath.Join(partial, retainedManifestName), []byte(manifest)); err != nil {
		return "", fmt.Errorf("writing the manifest: %w", err)
	}
	if err := fsyncDir(partial); err != nil {
		return "", err
	}

	base := fmt.Sprintf("%s%s-schema%d", retainedCopyPrefix, now.Format(retainedTimeLayout), schemaBefore)
	for seq := 1; seq <= retainedMaxSequence; seq++ {
		name := base
		if seq > 1 {
			name = base + "-" + strconv.Itoa(seq)
		}
		final := filepath.Join(dir, name)
		if _, err := os.Lstat(final); err == nil {
			continue
		}
		if err := os.Rename(partial, final); err != nil {
			return "", err
		}
		done = true
		_ = fsyncDir(dir)
		return final, nil
	}
	return "", errors.New("no free name for the copy directory")
}

func retainedFileNames(snap *sqliteSnapshot) []string {
	var names []string
	for _, p := range snap.paths {
		if snap.existed[p] && !strings.HasSuffix(p, shmSuffix) {
			names = append(names, filepath.Base(p))
		}
	}
	return names
}

// writeSyncedFile creates path exclusively with mode 0600, writes content,
// fsyncs it, and checks what landed against what was meant to.
func writeSyncedFile(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() != int64(len(content)) {
		return fmt.Errorf("%s is %d bytes after the write, want %d", path, fi.Size(), len(content))
	}
	return nil
}

type retainedCopy struct {
	name   string
	taken  string
	seq    int
	schema int
}

// pruneRetainedCopies removes the oldest engine-made copies beyond the cap.
// keep is the copy just written and is never removed.
func pruneRetainedCopies(ctx context.Context, dir, keep string) {
	latest, err := state.LatestSchemaVersion()
	if err != nil {
		slog.WarnContext(ctx, "pre-migration copies were not pruned", "error", err)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.WarnContext(ctx, "pre-migration copies were not pruned", "error", err)
		return
	}
	var ours []retainedCopy
	for _, e := range entries {
		m := retainedCopyPattern.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(dir, e.Name(), retainedManifestName)); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		schema, _ := strconv.Atoi(m[2])
		if schema > latest {
			continue
		}
		seq := 1
		if m[3] != "" {
			seq, _ = strconv.Atoi(m[3])
		}
		ours = append(ours, retainedCopy{name: e.Name(), taken: m[1], seq: seq, schema: schema})
	}
	sort.Slice(ours, func(i, j int) bool {
		if ours[i].taken != ours[j].taken {
			return ours[i].taken < ours[j].taken
		}
		return ours[i].seq < ours[j].seq
	})
	for excess := len(ours) - retainedCopyCap; excess > 0 && len(ours) > 0; {
		oldest := ours[0]
		ours = ours[1:]
		if oldest.name == keep {
			continue
		}
		path := filepath.Join(dir, oldest.name)
		if err := os.RemoveAll(path); err != nil {
			slog.WarnContext(ctx, "an old pre-migration copy could not be removed", "path", path, "error", err)
			return
		}
		slog.WarnContext(ctx, "removed the oldest pre-migration copy of the journal, past the cap",
			"path", path, "cap", retainedCopyCap)
		excess--
	}
}
