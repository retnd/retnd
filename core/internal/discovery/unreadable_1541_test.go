// Issue #1541: a source file that is complete by its strategy but cannot be
// opened yet waits as Pending. It is not journaled, so it cannot be FAILED,
// and it is admitted on the first pass that can read it.
//
// The cicd-pipeline/forge shape: the gitea dump's run directory was made
// group-readable before the dump inside it, retnd proved the dump complete,
// journaled it, opened it nineteen seconds later, got permission_denied and
// wrote FAILED. The set read FAILING for two weeks with a good backup every
// morning.
//
// Most of these use fakeTransport, because a real backend will not refuse an
// open on request. The last one replays the forge shape against the real
// rclone adapter over a directory tree with real modes, and skips under
// root, which reads through any mode bits.
package discovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
	"github.com/retnd/retnd/core/internal/transport/rclone"
)

const forgeDump = "gitea-dump-20261103T033855Z.tar.gz"

func forgeArtifactID(t *testing.T, set config.BackupSet) model.ArtifactID {
	t.Helper()
	id, err := model.NewArtifactID(set.ID, forgeDump)
	if err != nil {
		t.Fatalf("NewArtifactID: %v", err)
	}
	return id
}

// assertNotJournaled is the half of every test here that matters most: a
// row would be a row the transfer step can fail.
func assertNotJournaled(t *testing.T, j lifecycle.Journal, id model.ArtifactID) {
	t.Helper()
	rec, err := j.Get(context.Background(), id)
	if err == nil {
		t.Fatalf("%s was journaled as %s on a pass that could not read it; the transfer that follows writes FAILED (issue #1541)", id, rec.State)
	}
	if !errors.Is(err, state.ErrArtifactNotFound) {
		t.Fatalf("journal.Get(%s): %v, want ErrArtifactNotFound", id, err)
	}
}

// unreadableFlag reads PendingCandidate.Unreadable by name, so this file
// compiles on the tree before the fix and goes red there on the behaviour
// rather than on a build error.
func unreadableFlag(t *testing.T, p PendingCandidate) bool {
	t.Helper()
	f := reflect.ValueOf(p).FieldByName("Unreadable")
	if !f.IsValid() || f.Kind() != reflect.Bool {
		t.Fatalf("PendingCandidate has no Unreadable flag: health has no way to count a complete but unreadable file apart from one still being written")
	}
	return f.Bool()
}

// assertOneUnreadablePending checks res holds exactly the one candidate as
// Pending, flagged unreadable, with a reason naming want.
func assertOneUnreadablePending(t *testing.T, res Result, want string) {
	t.Helper()
	if len(res.Discovered) != 0 || len(res.AlreadyKnown) != 0 {
		t.Fatalf("a complete but unreadable file was admitted: Discovered=%+v AlreadyKnown=%+v", res.Discovered, res.AlreadyKnown)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("a complete but unreadable file was reported as an error rather than waiting: %+v", res.Errors)
	}
	if len(res.Pending) != 1 {
		t.Fatalf("Pending = %+v, want exactly the one unreadable file", res.Pending)
	}
	p := res.Pending[0]
	if !strings.HasSuffix(p.RemotePath, forgeDump) {
		t.Errorf("Pending path = %q, want the dump", p.RemotePath)
	}
	if !unreadableFlag(t, p) {
		t.Errorf("Pending[0].Unreadable = false, want true: health counts these apart from files still being written (reason %q)", p.Reason)
	}
	if !strings.Contains(p.Reason, "unreadable at the source") || !strings.Contains(p.Reason, want) {
		t.Errorf("Pending reason = %q, want it to say the file is unreadable at the source (%s)", p.Reason, want)
	}
}

func TestDiscover_ACompleteFileTheSourceWillNotOpenWaitsAsPending_1541(t *testing.T) {
	fake := &fakeTransport{
		artifacts: []transport.RemoteArtifact{{Path: "run-20261103/" + forgeDump, Size: 4096, ModTime: epoch.Unix()}},
		openErr: map[string]error{
			"run-20261103/" + forgeDump: transport.NewError(transport.PermissionDenied, "open_source_stream", os.ErrPermission),
		},
	}
	source := transport.Source{ID: "forge", Type: "sftp", Root: "/srv/backups/gitea"}
	set := backupSet(t, config.Completion{Strategy: "rename"}, []string{"*.tar.gz"})
	journal := openJournal(t)
	deps := Deps{Transport: fake, Journal: journal, Now: fixedNow(epoch)}

	res, err := Discover(context.Background(), deps, source, set)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	assertOneUnreadablePending(t, res, "permission denied")
	assertNotJournaled(t, journal, forgeArtifactID(t, set))

	// The producer publishes. The next pass admits it, exactly once.
	delete(fake.openErr, "run-20261103/"+forgeDump)
	res, err = Discover(context.Background(), deps, source, set)
	if err != nil {
		t.Fatalf("Discover (after publish): %v", err)
	}
	if len(res.Discovered) != 1 || len(res.Pending) != 0 {
		t.Fatalf("after the producer published: Discovered=%+v Pending=%+v, want the dump DISCOVERED", res.Discovered, res.Pending)
	}
	rec, err := journal.Get(context.Background(), forgeArtifactID(t, set))
	if err != nil {
		t.Fatalf("journal.Get after publish: %v", err)
	}
	if rec.State != string(lifecycle.Discovered) {
		t.Errorf("journal state after publish = %s, want %s", rec.State, lifecycle.Discovered)
	}
}

// Listed and then gone by the open is the other half of a publish race (a
// producer that writes, then moves into place), and waits the same way.
func TestDiscover_AFileListedThenGoneAtTheOpenWaitsAsPending_1541(t *testing.T) {
	fake := &fakeTransport{
		artifacts: []transport.RemoteArtifact{{Path: forgeDump, Size: 4096, ModTime: epoch.Unix()}},
		openErr: map[string]error{
			forgeDump: transport.NewError(transport.NotFound, "open_source_stream", os.ErrNotExist),
		},
	}
	set := backupSet(t, config.Completion{Strategy: "rename"}, nil)
	journal := openJournal(t)
	deps := Deps{Transport: fake, Journal: journal, Now: fixedNow(epoch)}

	res, err := Discover(context.Background(), deps, transport.Source{ID: "forge", Type: "sftp", Root: "/x"}, set)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	assertOneUnreadablePending(t, res, "not found")
	assertNotJournaled(t, journal, forgeArtifactID(t, set))
}

// Anything else at the open is not evidence about publishing. It is an
// Errors entry, the same as a Stat that fails, and it is not journaled
// either: a broken connection says nothing about the file.
func TestDiscover_AnOpenThatFailsForAnyOtherReasonIsAnErrorNotPending_1541(t *testing.T) {
	fake := &fakeTransport{
		artifacts: []transport.RemoteArtifact{{Path: forgeDump, Size: 4096, ModTime: epoch.Unix()}},
		openErr: map[string]error{
			forgeDump: transport.NewError(transport.Transient, "open_source_stream", errors.New("connection reset by peer")),
		},
	}
	set := backupSet(t, config.Completion{Strategy: "rename"}, nil)
	journal := openJournal(t)
	deps := Deps{Transport: fake, Journal: journal, Now: fixedNow(epoch)}

	res, err := Discover(context.Background(), deps, transport.Source{ID: "forge", Type: "sftp", Root: "/x"}, set)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(res.Pending) != 0 {
		t.Errorf("a transient open failure was reported as Pending: %+v", res.Pending)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), "connection reset") {
		t.Fatalf("Errors = %+v, want the one transient open failure", res.Errors)
	}
	assertNotJournaled(t, journal, forgeArtifactID(t, set))
}

// A candidate the journal already holds is not opened again. Transfer owns
// what happens at its open from here (FR-22), and a read-only set lists
// every retained file on every pass, so re-probing them would cost a source
// open per retained file per pass.
func TestDiscover_ACandidateAlreadyJournaledIsNotProbedAgain_1541(t *testing.T) {
	fake := &fakeTransport{
		artifacts: []transport.RemoteArtifact{{Path: forgeDump, Size: 4096, ModTime: epoch.Unix()}},
	}
	set := backupSet(t, config.Completion{Strategy: "rename"}, nil)
	deps := Deps{Transport: fake, Journal: openJournal(t), Now: fixedNow(epoch)}
	source := transport.Source{ID: "forge", Type: "sftp", Root: "/x"}

	if res, err := Discover(context.Background(), deps, source, set); err != nil || len(res.Discovered) != 1 {
		t.Fatalf("first pass: Discovered=%+v err=%v, want the dump admitted", res.Discovered, err)
	}
	if len(fake.opened) != 1 {
		t.Fatalf("first pass opened %v, want the new candidate probed exactly once", fake.opened)
	}

	fake.openErr = map[string]error{forgeDump: transport.NewError(transport.PermissionDenied, "open_source_stream", os.ErrPermission)}
	res, err := Discover(context.Background(), deps, source, set)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(res.AlreadyKnown) != 1 || len(res.Pending) != 0 {
		t.Errorf("second pass: AlreadyKnown=%+v Pending=%+v, want the journaled dump AlreadyKnown and nothing pending", res.AlreadyKnown, res.Pending)
	}
	if len(fake.opened) != 1 {
		t.Errorf("the journaled candidate was probed again: opened %v", fake.opened)
	}
}

// The forge scenario against a real tree: the run directory group-readable,
// the dump inside it readable by nobody, then published. One DISCOVERED row
// at the end, and no FAILED row at any point.
func TestDiscover_ForgeReplay_ARootOnlyDumpInAGroupReadableRunDirectory_1541(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through any mode bits, so an unreadable file cannot be staged here")
	}
	root := t.TempDir()
	runDir := filepath.Join(root, "run-20261103T033855Z")
	dump := filepath.Join(runDir, forgeDump)
	mustWrite(t, dump, "gitea dump payload")
	if err := os.Chmod(runDir, 0o750); err != nil {
		t.Fatalf("chmod run dir: %v", err)
	}
	if err := os.Chmod(dump, 0o000); err != nil {
		t.Fatalf("chmod dump: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dump, 0o644) })

	source := transport.Source{ID: "forge", Type: "local", Root: root}
	set := backupSet(t, config.Completion{Strategy: "rename"}, []string{"*.tar.gz"})
	journal := openJournal(t)
	deps := Deps{Transport: rclone.New(), Journal: journal, Now: fixedNow(epoch)}

	res, err := Discover(context.Background(), deps, source, set)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	assertOneUnreadablePending(t, res, "permission denied")
	assertNotJournaled(t, journal, forgeArtifactID(t, set))

	if err := os.Chmod(dump, 0o640); err != nil {
		t.Fatalf("publish chmod: %v", err)
	}
	res, err = Discover(context.Background(), deps, source, set)
	if err != nil {
		t.Fatalf("Discover (after publish): %v", err)
	}
	if len(res.Discovered) != 1 {
		t.Fatalf("after publish: Discovered=%+v Pending=%+v Errors=%+v, want the dump DISCOVERED", res.Discovered, res.Pending, res.Errors)
	}

	rows, err := journal.ListByBackupSet(context.Background(), set.ID)
	if err != nil {
		t.Fatalf("ListByBackupSet: %v", err)
	}
	discovered, failed := 0, 0
	for _, r := range rows {
		switch r.State {
		case string(lifecycle.Discovered):
			discovered++
		case string(lifecycle.Failed):
			failed++
		}
	}
	if discovered != 1 || failed != 0 {
		t.Errorf("journal after the replay: %d DISCOVERED, %d FAILED, want 1 and 0 (rows %+v)", discovered, failed, rows)
	}
}
