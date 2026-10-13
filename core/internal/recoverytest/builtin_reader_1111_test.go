package recoverytest

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// #1111: the built-in structure check's reader, driven over generated
// fixtures that live on disk (see builtin_fixtures_gen_test.go).

func fixture(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(fixtureDir, name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return p
}

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func all(r BuiltinReport) string {
	return strings.Join(append([]string{r.Outcome, r.Recognised, r.Magic, r.Mismatch, r.ChecksumNote, r.Detail}, r.Layers...), "|")
}

func TestIssue1111_TheReaderOverEveryFixture(t *testing.T) {
	for _, tc := range []struct {
		fixture, name string
		outcome       string
		layers        string
		members       int64
		entries       int64
		decoded       int64
		detail        string // substring of Detail (or of the whole report for passes)
		mismatch      bool
	}{
		{"gz-single.gz", "a.gz", BuiltinPass, "gzip", 1, 0, 150000, "", false},
		{"gz-multi.gz", "a.gz", BuiltinPass, "gzip", 3, 0, 150000, "", false},
		{"gz-truncated.gz", "a.gz", BuiltinFail, "gzip", 0, 0, -1, "unexpected EOF", false},
		{"gz-flipped.gz", "a.gz", BuiltinFail, "gzip", 0, 0, -1, "", false},
		{"gz-trailing-garbage.gz", "a.gz", BuiltinFail, "gzip", 0, 0, -1, "", false},
		{"zst-checksum.zst", "a.zst", BuiltinPass, "zstd", 1, 0, 300000, "verified", false},
		{"zst-nochecksum.zst", "a.zst", BuiltinPass, "zstd", 1, 0, 300000, "no content checksum", false},
		{"zst-skippable.zst", "a.zst", BuiltinPass, "zstd", 1, 0, 60000, "", false},
		{"zst-truncated.zst", "a.zst", BuiltinFail, "zstd", 0, 0, -1, "", false},
		{"zst-flipped.zst", "a.zst", BuiltinFail, "zstd", 0, 0, -1, "", false},
		{"zst-window2g.zst", "a.zst", BuiltinStopped, "zstd", 0, 0, -1, "window", false},
		{"zst-dict.zst", "a.zst", BuiltinStopped, "zstd", 0, 0, -1, "dictionary", false},
		{"tar-plain.tar", "a.tar", BuiltinPass, "tar", 0, 3, -1, "", false},
		{"tar-pax.tar", "a.tar", BuiltinPass, "tar", 0, 1, -1, "", false},
		{"tar-gnu-longname.tar", "a.tar", BuiltinPass, "tar", 0, 1, -1, "", false},
		{"tar-truncated-mid-entry.tar", "a.tar", BuiltinFail, "tar", 0, 0, -1, "", false},
		{"tar-no-terminator.tar", "a.tar", BuiltinPass, "tar", 0, 3, -1, "", false},
		{"tar-dotdot-abs.tar", "a.tar", BuiltinPass, "tar", 0, 2, -1, "", false},
		{"targz-good.tar.gz", "a.tar.gz", BuiltinPass, "gzip+tar", 1, 3, -1, "", false},
		{"targz-good.tar.gz", "a.tgz", BuiltinPass, "gzip+tar", 1, 3, -1, "", false},
		{"targz-truncated-outer.tar.gz", "a.tar.gz", BuiltinFail, "gzip+tar", 0, 0, -1, "", false},
		{"targz-truncated-inner.tar.gz", "a.tar.gz", BuiltinFail, "gzip+tar", 0, 0, -1, "", false},
		{"tarzst-good.tar.zst", "a.tar.zst", BuiltinPass, "zstd+tar", 1, 3, -1, "", false},
		{"tarzst-good.tar.zst", "a.tzst", BuiltinPass, "zstd+tar", 1, 3, -1, "", false},
		// The frame is one block, so nothing decodes before it fails: the layer inside cannot be told.
		{"tarzst-truncated-outer.tar.zst", "a.tar.zst", BuiltinFail, "zstd", 0, 0, -1, "", false},
		{"tarzst-truncated-inner.tar.zst", "a.tar.zst", BuiltinFail, "zstd+tar", 0, 0, -1, "", false},
		{"gz-holding-zstd.gz", "a.gz", BuiltinPass, "zstd", 1, 0, 300000, "", true},
		{"targz-good.tar.gz", "a.tar", BuiltinPass, "gzip+tar", 1, 3, -1, "", true},
		{"gz-random.gz", "a.gz", BuiltinFail, "", 0, 0, -1, "not", false},
	} {
		t.Run(tc.fixture+" as "+tc.name, func(t *testing.T) {
			rep := StructureCheck{}.Check(context.Background(), BuiltinInput{File: fixture(t, tc.fixture), Name: tc.name})
			if rep.Outcome != tc.outcome {
				t.Fatalf("outcome = %q, want %q (%s)", rep.Outcome, tc.outcome, all(rep))
			}
			if got := strings.Join(rep.Layers, "+"); got != tc.layers {
				t.Errorf("layers = %q, want %q", got, tc.layers)
			}
			if tc.outcome == BuiltinPass {
				if rep.Members != tc.members {
					t.Errorf("members = %d, want %d", rep.Members, tc.members)
				}
				if rep.Entries != tc.entries {
					t.Errorf("entries = %d, want %d", rep.Entries, tc.entries)
				}
				if tc.decoded >= 0 && rep.Decoded != tc.decoded {
					t.Errorf("decoded = %d, want %d", rep.Decoded, tc.decoded)
				}
			}
			if tc.detail != "" && !strings.Contains(strings.ToLower(all(rep)), strings.ToLower(tc.detail)) {
				t.Errorf("report %q does not mention %q", all(rep), tc.detail)
			}
			if tc.mismatch != (rep.Mismatch != "") {
				t.Errorf("mismatch = %q, want a note: %v", rep.Mismatch, tc.mismatch)
			}
			if tc.outcome != BuiltinPass && rep.Detail == "" {
				t.Errorf("a %s report with no detail", rep.Outcome)
			}
		})
	}
}

// Nothing the archive names is ever printed, and nothing is extracted.
func TestIssue1111_NoMemberNameIsEverReportedAndNothingIsWritten(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "evil.tar")
	if err := os.WriteFile(src, fixtureBytes(t, "tar-dotdot-abs.tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := StructureCheck{}.Check(context.Background(), BuiltinInput{File: src, Name: "evil.tar"})
	if rep.Outcome != BuiltinPass {
		t.Fatalf("%s", all(rep))
	}
	for _, name := range []string{"etc", "cron", "evil", "olute", "passwd"} {
		if strings.Contains(all(rep), name) {
			t.Errorf("the report names %q: %s", name, all(rep))
		}
	}
	es, _ := os.ReadDir(dir)
	if len(es) != 1 {
		t.Errorf("the directory holds %d entries after the check; nothing may be extracted or written", len(es))
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "..", "etc", "cron.d", "evil")); err == nil {
		t.Error("a member with a ../ name was extracted")
	}
}

func TestIssue1111_WhichNamesTheCheckHasAReaderFor(t *testing.T) {
	for _, n := range []string{"a.gz", "a.tgz", "a.tar.gz", "a.zst", "a.tzst", "a.tar.zst", "a.tar", "BACKUP.TAR.GZ", "pg_dump-2026.sql.gz"} {
		if _, ok := (StructureCheck{}).Applies(n); !ok {
			t.Errorf("Applies(%q) = false", n)
		}
	}
	for _, n := range []string{"a.dump", "a.sql", "tar", "a.zip", "a.gz.bak", "a.bz2", "a.xz", "gz", ""} {
		if typ, ok := (StructureCheck{}).Applies(n); ok {
			t.Errorf("Applies(%q) = true (%s): the check has no reader for it", n, typ)
		}
	}
}

// --- bounds: each named constant is driven over its line (the FR-74 shape) ---

func TestIssue1111_TheBoundsAreNamedConstantsWithTheirDefaults(t *testing.T) {
	if DefaultBuiltinCheckBudget != 2*time.Hour {
		t.Errorf("budget = %v", DefaultBuiltinCheckBudget)
	}
	if DefaultBuiltinMaxDecodedBytes != 4<<40 {
		t.Errorf("ceiling = %d", DefaultBuiltinMaxDecodedBytes)
	}
	if BuiltinZstdMaxWindow != 128<<20 {
		t.Errorf("window = %d", BuiltinZstdMaxWindow)
	}
	if BuiltinReadBufferBytes != 4<<20 {
		t.Errorf("buffer = %d", BuiltinReadBufferBytes)
	}
}

// rleZeros writes a zstd stream of n 128 KiB RLE blocks: 4 bytes of file for
// each 128 KiB of zeros, so a huge decoded size costs almost nothing to store.
func rleZeros(t *testing.T, blocks int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "zeros.zst")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	w.Write([]byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 7 << 3}) // no checksum, 128 KiB window
	const size = 128 << 10
	for i := 0; i < blocks; i++ {
		h := uint32(size<<3) | 1<<1
		if i == blocks-1 {
			h |= 1
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], h)
		w.Write(b[:3])
		w.WriteByte(0)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

const rleBlocks = 2 << 20 // 256 GiB decoded, far more than any bound below lets through

func TestIssue1111_ABudgetEndsAsStoppedNeverFailedAndSaysHowFarItGot(t *testing.T) {
	start := time.Now()
	rep := StructureCheck{Budget: time.Second}.Check(context.Background(), BuiltinInput{File: rleZeros(t, rleBlocks), Name: "z.zst"})
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("a 1s budget took %v: the deadline is not being enforced", took)
	}
	if rep.Outcome != BuiltinStopped {
		t.Fatalf("outcome = %q (%s), want stopped: a bound is never a failure", rep.Outcome, all(rep))
	}
	if rep.Decoded <= 0 || rep.Decoded >= int64(rleBlocks)*(128<<10) {
		t.Errorf("decoded = %d, want the bytes read so far", rep.Decoded)
	}
	if !strings.Contains(rep.Detail, "budget") {
		t.Errorf("detail = %q", rep.Detail)
	}
}

func TestIssue1111_ADecodedBytesCeilingEndsAsStopped(t *testing.T) {
	rep := StructureCheck{MaxDecoded: 1 << 20}.Check(context.Background(), BuiltinInput{File: rleZeros(t, rleBlocks), Name: "z.zst"})
	if rep.Outcome != BuiltinStopped {
		t.Fatalf("outcome = %q (%s)", rep.Outcome, all(rep))
	}
	if rep.Decoded != 1<<20 {
		t.Errorf("decoded = %d, want exactly the ceiling", rep.Decoded)
	}
	if !strings.Contains(rep.Detail, "ceiling") {
		t.Errorf("detail = %q", rep.Detail)
	}
}

func TestIssue1111_AFileJustUnderTheCeilingPasses(t *testing.T) {
	rep := StructureCheck{MaxDecoded: 150000}.Check(context.Background(), BuiltinInput{File: fixture(t, "gz-single.gz"), Name: "a.gz"})
	if rep.Outcome != BuiltinPass {
		t.Errorf("a file exactly at the ceiling: %s", all(rep))
	}
	rep = StructureCheck{MaxDecoded: 149999}.Check(context.Background(), BuiltinInput{File: fixture(t, "gz-single.gz"), Name: "a.gz"})
	if rep.Outcome != BuiltinStopped {
		t.Errorf("a file one byte over the ceiling: %s", all(rep))
	}
}

func TestIssue1111_ACancelledContextIsStoppedNotFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep := StructureCheck{}.Check(ctx, BuiltinInput{File: fixture(t, "gz-single.gz"), Name: "a.gz"})
	if rep.Outcome != BuiltinStopped {
		t.Errorf("%s", all(rep))
	}
}

// --- the decoder options, through the seam ---

func TestIssue1111_TheZstdDecoderIsBuiltWithTheDocumentedOptions(t *testing.T) {
	var got []ZstdDecoderConfig
	c := StructureCheck{NewZstd: func(r io.Reader, cfg ZstdDecoderConfig) (*zstd.Decoder, error) {
		got = append(got, cfg)
		return zstd.NewReader(r)
	}}
	rep := c.Check(context.Background(), BuiltinInput{File: fixture(t, "zst-checksum.zst"), Name: "a.zst"})
	if rep.Outcome != BuiltinPass {
		t.Fatalf("%s", all(rep))
	}
	if len(got) != 1 {
		t.Fatalf("the seam was called %d times", len(got))
	}
	want := ZstdDecoderConfig{Concurrency: 1, Lowmem: true, MaxWindow: 128 << 20, IgnoreChecksum: false}
	if got[0] != want {
		t.Errorf("decoder config = %+v, want %+v", got[0], want)
	}
	if BuiltinZstdConfig() != want {
		t.Errorf("BuiltinZstdConfig() = %+v", BuiltinZstdConfig())
	}
}

// A byte flipped inside a stored block can only be noticed by the content
// checksum, so this is the test that fails if the checksum is switched off.
func TestIssue1111_AFlippedByteInAStoredBlockIsCaughtOnlyByTheChecksum(t *testing.T) {
	good := StructureCheck{}.Check(context.Background(), BuiltinInput{File: fixture(t, "zst-flipped.zst"), Name: "a.zst"})
	if good.Outcome != BuiltinFail {
		t.Fatalf("the checksum did not catch the flip: %s", all(good))
	}
	// Control: with the checksum ignored the same bytes decode, which proves
	// the fixture really is checksum-only.
	d, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(128<<20), zstd.IgnoreChecksum(true))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DecodeAll(fixtureBytes(t, "zst-flipped.zst"), nil); err != nil {
		t.Fatalf("control: the fixture is not checksum-only: %v", err)
	}
}

func TestIssue1111_ADecoderPanicIsStoppedAndTheProcessLives(t *testing.T) {
	c := StructureCheck{NewZstd: func(io.Reader, ZstdDecoderConfig) (*zstd.Decoder, error) { panic("decoder blew up") }}
	rep := c.Check(context.Background(), BuiltinInput{File: fixture(t, "zst-checksum.zst"), Name: "a.zst"})
	if rep.Outcome != BuiltinStopped {
		t.Fatalf("outcome = %q (%s)", rep.Outcome, all(rep))
	}
	if !strings.Contains(rep.Detail, "panic") {
		t.Errorf("detail = %q", rep.Detail)
	}
}

func TestIssue1111_TheStreamMustEndOnAMemberBoundary(t *testing.T) {
	// A gzip stream cut exactly after its first member is a complete stream;
	// one cut one byte into the second is not.
	one := fixtureBytes(t, "gz-single.gz")
	two := append(append([]byte(nil), one...), one[:1]...)
	p := filepath.Join(t.TempDir(), "x.gz")
	if err := os.WriteFile(p, two, 0o600); err != nil {
		t.Fatal(err)
	}
	if rep := (StructureCheck{}).Check(context.Background(), BuiltinInput{File: p, Name: "x.gz"}); rep.Outcome != BuiltinFail {
		t.Errorf("a stream that ends inside a member passed: %s", all(rep))
	}
}
