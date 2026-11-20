package placement_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"sync"
	"testing"

	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/transport"
)

// streamProbeStore wraps the store an encrypt-existing run reads from, and
// for the plaintext source object hands back a reader that notes, at the
// moment the source runs dry, how many ciphertext bytes the encrypting pass
// has already written into the staging area.
//
// That number is what tells streaming from buffering without measuring
// memory. A pass that streams has encrypted and written every chunk but the
// held-back last one by the time it reads the source's last byte. An engine
// that reads the whole object into memory first reaches the end of the
// source before the encrypting pass has created its staging file at all.
// The in-memory medium double holds the object anyway, so a heap budget at
// this level would measure the double, not the engine.
type streamProbeStore struct {
	placement.MediumStore
	key     string
	staging string

	mu        sync.Mutex
	opened    int
	atEOF     []int64 // staged bytes on disk each time a source read hit EOF
	sourceLen int64
}

func (s *streamProbeStore) OpenObject(ctx context.Context, medium transport.Medium, key string) (io.ReadCloser, error) {
	rc, err := s.MediumStore.OpenObject(ctx, medium, key)
	if err != nil || key != s.key {
		return rc, err
	}
	s.mu.Lock()
	s.opened++
	s.mu.Unlock()
	return &probeReader{rc: rc, s: s}, nil
}

func (s *streamProbeStore) stagedBytes() int64 {
	var n int64
	_ = filepath.WalkDir(s.staging, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}

// probeReader deliberately has no WriteTo, so io.Copy pulls it in small reads
// the way a network body is pulled, instead of in one call that hands over
// the whole buffer.
type probeReader struct {
	rc   io.ReadCloser
	s    *streamProbeStore
	read int64
}

func (p *probeReader) Read(b []byte) (int, error) {
	n, err := p.rc.Read(b)
	p.read += int64(n)
	if errors.Is(err, io.EOF) {
		staged := p.s.stagedBytes()
		p.s.mu.Lock()
		p.s.atEOF = append(p.s.atEOF, staged)
		p.s.sourceLen = p.read
		p.s.mu.Unlock()
	}
	return n, err
}

func (p *probeReader) Close() error { return p.rc.Close() }

// NF.8: the plaintext is streamed off its medium into the encrypting pass.
// Red if the engine buffers the whole object before the pass starts (an
// io.ReadAll in openEncryptionSource, say), because then the source is read
// to its end while nothing has been staged.
func TestEncryptExistingStreamsThePlaintextIntoTheEncryptingPass_1291(t *testing.T) {
	w := newRetroWorld(t)
	probe := &streamProbeStore{MediumStore: w.engine.Store, key: w.srcKey, staging: filepath.Join(w.localDir, placement.StagingDirName)}
	w.engine.Store = probe

	report := w.encryptExisting()
	w.guard.fail()
	if report.Completed != 1 {
		t.Fatalf("the run did not complete: %+v (move error %q)", report, w.lastMove().Error)
	}

	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.opened == 0 || len(probe.atEOF) == 0 {
		t.Fatalf("control: the plaintext source %s was opened %d time(s) and never read to its end, so nothing here watched the encrypting pass read it", w.srcKey, probe.opened)
	}
	if probe.sourceLen != int64(len(w.srcBytes)) {
		t.Fatalf("control: the source read ended after %d bytes, want the whole object (%d)", probe.sourceLen, len(w.srcBytes))
	}
	var most int64
	for _, n := range probe.atEOF {
		most = max(most, n)
	}
	// Two full 64 KiB chunks is well under the four the pass has written by
	// the end of a five-chunk source when it streams, and well above the
	// nothing it has written when it buffers.
	if most < 2*64*1024 {
		t.Fatalf("the source (%d bytes) was read to its end with %d ciphertext bytes staged; the engine is buffering the object rather than streaming it into the encrypting pass", len(w.srcBytes), most)
	}
}
