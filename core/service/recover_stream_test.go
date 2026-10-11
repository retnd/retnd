package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/recovery/export"
)

// A browser download is streamed from the retained copy, not staged (EPIC Q,
// owner decisions 3 and 4). These tests hold what that promises: nothing is
// copied to the staging disk, memory is a chunk, a refusal comes before the
// first byte, a copy that rots mid-stream stops at the bad chunk, a resumed
// range is the same bytes, and the end of a download records whether the
// whole thing verified.

const streamTestChunk = 64 << 10

func smallChunks(t *testing.T) {
	t.Helper()
	old := recoveryChunkSize
	recoveryChunkSize = streamTestChunk
	t.Cleanup(func() { recoveryChunkSize = old })
}

type etagged interface{ ETag() string }

func mintDownload(t *testing.T, f *recoveryFixture, ack bool) RecoveryGrant {
	t.Helper()
	g, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{
		ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}, AcknowledgeCost: ack,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return g
}

func TestADownloadIsStreamedAndStagesNothing(t *testing.T) {
	smallChunks(t)
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	_, dl := redeemDownload(t, f)
	defer dl.Close()
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("a download copied the artifact to the staging disk before sending it: %v", e)
	}
	got, err := io.ReadAll(dl.Reader())
	if err != nil || !bytes.Equal(got, f.data) {
		t.Fatalf("read: %v, equal=%v", err, bytes.Equal(got, f.data))
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("staging holds %v after the stream", e)
	}
}

func TestADownloadReadsTheSourceForTheRangeItIsAskedFor(t *testing.T) {
	smallChunks(t)
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	_, dl := func() (string, RecoveryStream) {
		g := mintDownload(t, f, true)
		d, _, err := f.b.RedeemRecovery(context.Background(), f.session(), g.Token)
		if err != nil {
			t.Fatalf("redeem: %v", err)
		}
		return g.Token, d
	}()
	defer dl.Close()
	before := f.store.rangeBytes.Load()
	size := dl.Length()
	if _, err := dl.Reader().Seek(size-1000, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 1000)
	if _, err := io.ReadFull(dl.Reader(), got); err != nil || !bytes.Equal(got, f.data[size-1000:]) {
		t.Fatalf("tail read: %v", err)
	}
	read := f.store.rangeBytes.Load() - before
	if read < 1000 {
		t.Fatalf("the tail was served without reading it from the copy (%d bytes read)", read)
	}
	if read > 2*streamTestChunk {
		t.Fatalf("a 1000 byte range read %d bytes of the copy; a chunk is %d", read, streamTestChunk)
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("staging holds %v", e)
	}
}

func TestTheETagIsTheSameForEveryGrantAndComesFromTheArtifactAndItsDigest(t *testing.T) {
	smallChunks(t)
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	_, a := redeemDownload(t, f)
	ea, ok := a.(etagged)
	if !ok {
		t.Fatal("a download offers no ETag, so a client cannot resume it safely")
	}
	a.Close()
	_, b := redeemDownload(t, f)
	defer b.Close()
	if ea.ETag() == "" || ea.ETag() != b.(etagged).ETag() {
		t.Fatalf("ETag differs between grants: %q vs %q", ea.ETag(), b.(etagged).ETag())
	}
	if want := export.ETag(f.artifact, sha(f.data)); ea.ETag() != want {
		t.Fatalf("ETag %s is not derived from the artifact and its recorded digest (%s)", ea.ETag(), want)
	}
}

func TestThePreparationReadIsPaidOncePerArtifact(t *testing.T) {
	smallChunks(t)
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	g := mintDownload(t, f, true)
	d1, _, err := f.b.RedeemRecovery(context.Background(), f.session(), g.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, d1.Reader()); err != nil {
		t.Fatal(err)
	}
	d1.Close()
	d1.Served(0, d1.Length()-1, d1.Length(), nil)

	reads := f.store.opens.Load() + f.store.rangeOpens.Load()
	g2 := mintDownload(t, f, true)
	d2, _, err := f.b.RedeemRecovery(context.Background(), f.session(), g2.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if after := f.store.opens.Load() + f.store.rangeOpens.Load(); after != reads {
		t.Fatalf("the second download of the same artifact read the copy again before sending a byte (%d opens became %d)", reads, after)
	}
}

func TestPreflightRefusesBeforeAnyByteWhenTheCopyCannotCompleteTheDownload(t *testing.T) {
	smallChunks(t)
	cases := []struct {
		name   string
		opts   fixtureOpts
		ack    bool
		break_ func(t *testing.T, f *recoveryFixture)
		code   string
	}{
		{"the local file is gone", fixtureOpts{localCopy: true}, false, func(t *testing.T, f *recoveryFixture) {
			if err := os.Remove(filepath.Join(f.localDir, "dump.zst")); err != nil {
				t.Fatal(err)
			}
		}, RecoveryNotReadable},
		{"the local file lost its tail", fixtureOpts{localCopy: true}, false, func(t *testing.T, f *recoveryFixture) {
			if err := os.Truncate(filepath.Join(f.localDir, "dump.zst"), int64(len(f.data))-100); err != nil {
				t.Fatal(err)
			}
		}, RecoveryLocalCopyCorrupt},
		{"the remote object is gone", fixtureOpts{remoteClass: "warm"}, true, func(t *testing.T, f *recoveryFixture) {
			delete(f.store.objects, "prefix/dump.zst")
		}, RecoveryNotReadable},
		{"the remote object is the wrong size", fixtureOpts{remoteClass: "warm"}, true, func(t *testing.T, f *recoveryFixture) {
			f.store.objects["prefix/dump.zst"] = f.data[:len(f.data)-100]
		}, RecoveryRemoteCopyCorrupt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRecoveryFixture(t, c.opts)
			g := mintDownload(t, f, c.ack)
			c.break_(t, f)
			dl, _, err := f.b.RedeemRecovery(context.Background(), f.session(), g.Token)
			if err == nil {
				dl.Close()
				t.Fatal("a download that cannot complete was started")
			}
			if got := codeOf(t, err); got != c.code {
				t.Fatalf("code %s, want %s (%v)", got, c.code, err)
			}
			if dl != nil {
				t.Fatal("a refused redeem returned a stream")
			}
			if n := f.store.opens.Load() + f.store.rangeOpens.Load(); n != 0 {
				t.Fatalf("the refusal came after the copy was opened %d times, so bandwidth was spent finding out what a metadata call knew", n)
			}
			if e := f.stagingEntries(t); len(e) != 0 {
				t.Fatalf("staging holds %v", e)
			}
		})
	}
}

func TestACopyThatRotsAfterPreparationStopsAtTheBadChunk(t *testing.T) {
	smallChunks(t)
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	token, dl := redeemDownload(t, f)
	defer dl.Close()

	rot := 3*streamTestChunk + 5
	p := filepath.Join(f.localDir, "dump.zst")
	b, _ := os.ReadFile(p)
	b[rot] ^= 1
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := io.ReadAll(dl.Reader())
	var ie *export.IntegrityError
	if !errors.As(err, &ie) || ie.Kind != export.KindDigest {
		t.Fatalf("a rotted chunk was not detected: %v", err)
	}
	if len(got) != 3*streamTestChunk {
		t.Fatalf("%d bytes went out; exactly the 3 chunks before the bad one were owed", len(got))
	}
	if !bytes.Equal(got, f.data[:3*streamTestChunk]) {
		t.Fatal("the bytes sent before the bad chunk were not the recorded ones")
	}
	dl.Close()
	dl.Served(0, dl.Length()-1, int64(len(got)), err)
	rows := f.rows(t)
	last := rows[len(rows)-1]
	if last["outcome"] != "failed" || last["error_code"] != RecoveryLocalCopyCorrupt {
		t.Fatalf("the audit row for the failed download is %v", last)
	}
	requireGrantSpent(t, f, token, "after a chunk failed verification")
}

func TestAnInterruptedDownloadResumesWithTheSameBytesUnderANewGrant(t *testing.T) {
	smallChunks(t)
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	_, first := redeemDownload(t, f)
	buf := make([]byte, 100_000)
	n, _ := io.ReadFull(first.Reader(), buf)
	first.Close()
	first.Served(0, int64(n)-1, int64(n), errors.New("connection reset")) // a crash or a cut link

	_, second := redeemDownload(t, f)
	defer second.Close()
	if _, err := second.Reader().Seek(int64(n), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(second.Reader())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(buf[:n:n], rest...), f.data) {
		t.Fatal("the resumed download is not the artifact")
	}
	if first.(etagged).ETag() != second.(etagged).ETag() {
		t.Fatal("the validator changed between the attempt and its resumption, so If-Range would refuse")
	}
}

func TestACompleteDownloadRecordsWhetherTheWholeThingVerified(t *testing.T) {
	smallChunks(t)
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	_, dl := redeemDownload(t, f)
	if _, err := io.Copy(io.Discard, dl.Reader()); err != nil {
		t.Fatal(err)
	}
	dl.Close()
	dl.Served(0, dl.Length()-1, dl.Length(), nil)

	var verified map[string]any
	for _, r := range f.rows(t) {
		if r["operation"] == "recovery verified" {
			verified = r
		}
	}
	if verified == nil {
		t.Fatalf("a finished download left no verification row; rows: %v", f.rows(t))
	}
	if verified["outcome"] != "ok" || !strings.Contains(verified["detail"].(string), sha(f.data)) {
		t.Fatalf("the verification row does not state the digest it matched: %v", verified)
	}
}

func TestAPartialDownloadClaimsNoVerification(t *testing.T) {
	smallChunks(t)
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	_, dl := redeemDownload(t, f)
	defer dl.Close()
	buf := make([]byte, 1000)
	_, _ = io.ReadFull(dl.Reader(), buf)
	dl.Served(0, 999, 1000, nil)
	for _, r := range f.rows(t) {
		if r["operation"] == "recovery verified" {
			t.Fatalf("1000 of %d bytes were served and the trail says the download verified: %v", dl.Length(), r)
		}
	}
}
