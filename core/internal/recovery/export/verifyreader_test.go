package export

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

// memSource is a copy in memory that counts what is read from it and can be
// told to damage bytes, so a test can see exactly how far a stream got.
type memSource struct {
	data    []byte
	opens   []int64
	read    int64
	flip    int // offset to bit-flip on read, -1 for none
	short   int // when > 0, serve this many bytes fewer than there are
	trailer []byte
}

func (m *memSource) OpenAt(_ context.Context, off int64) (io.ReadCloser, error) {
	m.opens = append(m.opens, off)
	d := append([]byte(nil), m.data...)
	if m.flip >= 0 && m.flip < len(d) {
		d[m.flip] ^= 1
	}
	if m.short > 0 {
		d = d[:len(d)-m.short]
	}
	d = append(d, m.trailer...)
	return io.NopCloser(&rangeCountingReader{r: bytes.NewReader(d[off:]), n: &m.read}), nil
}

type rangeCountingReader struct {
	r io.Reader
	n *int64
}

func (c *rangeCountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i/251)
	}
	return b
}

func hexSum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func idxExpect(data []byte) Expect {
	return expectFor(data, OriginLocal)
}

const testChunk = 4 << 10

func buildIdx(t *testing.T, data []byte) *ChunkIndex {
	t.Helper()
	ix, err := BuildIndex(context.Background(), bytes.NewReader(data), idxExpect(data), testChunk)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestBuildIndexRecordsOneDigestPerChunkAndTheWholeDigest(t *testing.T) {
	data := payload(testChunk*3 + 100)
	ix := buildIdx(t, data)
	if ix.Count() != 4 || ix.Size != int64(len(data)) || ix.SHA256 != hexSum(data) {
		t.Fatalf("index = %d chunks, size %d, %s", ix.Count(), ix.Size, ix.SHA256)
	}
	if ix.ChunkLen(3) != 100 || ix.ChunkLen(0) != testChunk {
		t.Fatalf("chunk lengths wrong: %d, %d", ix.ChunkLen(0), ix.ChunkLen(3))
	}
	if ix.Chunks[1] != sha256.Sum256(data[testChunk:2*testChunk]) {
		t.Fatal("chunk 1 digest is not the digest of chunk 1")
	}
}

func TestBuildIndexRefusesBytesThatAreNotTheRecordedOnes(t *testing.T) {
	data := payload(testChunk*2 + 7)
	flipped := append([]byte(nil), data...)
	flipped[testChunk+3] ^= 1
	_, err := BuildIndex(context.Background(), bytes.NewReader(flipped), idxExpect(data), testChunk)
	var ie *IntegrityError
	if !errors.As(err, &ie) || ie.Kind != KindDigest {
		t.Fatalf("want a digest mismatch, got %v", err)
	}
	_, err = BuildIndex(context.Background(), bytes.NewReader(data[:len(data)-1]), idxExpect(data), testChunk)
	if !errors.As(err, &ie) || ie.Kind != KindSize {
		t.Fatalf("want a size mismatch for a short copy, got %v", err)
	}
	_, err = BuildIndex(context.Background(), bytes.NewReader(append(append([]byte(nil), data...), 1)), idxExpect(data), testChunk)
	if !errors.As(err, &ie) || ie.Kind != KindSize {
		t.Fatalf("want a size mismatch for a long copy, got %v", err)
	}
}

func TestAnIndexRoundTripsAndEveryDamagedFormIsRefused(t *testing.T) {
	ix := buildIdx(t, payload(testChunk*2+1))
	raw, _ := ix.MarshalBinary()
	back, err := UnmarshalChunkIndex(raw)
	if err != nil || back.Size != ix.Size || back.SHA256 != ix.SHA256 || len(back.Chunks) != 3 {
		t.Fatalf("round trip: %v %+v", err, back)
	}
	for i := range raw {
		bad := append([]byte(nil), raw...)
		bad[i] ^= 0x40
		if _, err := UnmarshalChunkIndex(bad); err == nil {
			t.Fatalf("a flip at byte %d of the stored index was accepted", i)
		}
	}
	if _, err := UnmarshalChunkIndex(raw[:len(raw)-5]); err == nil {
		t.Fatal("a truncated index was accepted")
	}
}

func TestTheETagIsAStrongValidatorFromTheArtifactAndTheDigestAlone(t *testing.T) {
	d := hexSum([]byte("x"))
	a := ETag("p/s/a", d)
	if a != ETag("p/s/a", strings.ToUpper(d)) {
		t.Fatal("the ETag depends on the digest's letter case")
	}
	if a == ETag("p/s/b", d) || a == ETag("p/s/a", hexSum([]byte("y"))) {
		t.Fatal("the ETag does not change with the artifact or the digest")
	}
	if !strings.HasPrefix(a, `"`) || strings.HasPrefix(a, `W/`) || strings.Contains(a, d) {
		t.Fatalf("ETag %s must be a quoted strong validator that does not carry the digest", a)
	}
}

func newReader(src RangeSource, ix *ChunkIndex) *VerifiedReader {
	return NewVerifiedReader(context.Background(), src, ix, OriginLocal, "local")
}

func TestAWholeReadReturnsTheBytesAndVerifiesTheWholeDigest(t *testing.T) {
	data := payload(testChunk*5 + 11)
	r := newReader(&memSource{data: data, flip: -1}, buildIdx(t, data))
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read: %v, equal=%v", err, bytes.Equal(got, data))
	}
	if !r.WholeVerified() {
		t.Fatal("a read of every chunk in order did not verify the whole digest")
	}
}

func TestMemoryIsOneChunkNotTheFile(t *testing.T) {
	data := payload(testChunk * 40)
	r := newReader(&memSource{data: data, flip: -1}, buildIdx(t, data))
	if int64(len(r.buf)) != testChunk {
		t.Fatalf("buffer is %d bytes for a %d byte copy", len(r.buf), len(data))
	}
}

func TestARangeIsReadFromTheSourceAtItsChunkAndNotFromTheStart(t *testing.T) {
	data := payload(testChunk*50 + 5)
	src := &memSource{data: data, flip: -1}
	r := newReader(src, buildIdx(t, data))
	off := int64(testChunk*30 + 17)
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 1000)
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data[off:off+1000]) {
		t.Fatal("range bytes differ")
	}
	if len(src.opens) != 1 || src.opens[0] != int64(testChunk*30) {
		t.Fatalf("opened at %v, want one open at the chunk boundary %d", src.opens, testChunk*30)
	}
	if src.read > 2*testChunk {
		t.Fatalf("read %d bytes of the source for a 1000 byte range", src.read)
	}
	if r.WholeVerified() {
		t.Fatal("a range read claimed to have verified the whole file")
	}
}

func TestAResumedRangeIsByteIdenticalToTheSameRangeOfTheFirstAttempt(t *testing.T) {
	data := payload(testChunk*9 + 1)
	ix := buildIdx(t, data)
	first, _ := io.ReadAll(newReader(&memSource{data: data, flip: -1}, ix))
	// A second reader, as after a crash and a new grant, from the middle.
	r := newReader(&memSource{data: data, flip: -1}, ix)
	mid := int64(testChunk*4 + 300)
	_, _ = r.Seek(mid, io.SeekStart)
	rest, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(rest, first[mid:]) {
		t.Fatalf("resumed bytes differ from the first attempt's: %v", err)
	}
}

func TestAStreamStopsAtTheFirstBadChunkAndNeverReturnsIt(t *testing.T) {
	data := payload(testChunk * 8)
	bad := testChunk*5 + 9
	src := &memSource{data: data, flip: bad}
	r := newReader(src, buildIdx(t, data))
	got, err := io.ReadAll(r)
	var ie *IntegrityError
	if !errors.As(err, &ie) || ie.Kind != KindDigest {
		t.Fatalf("want a digest failure, got %v", err)
	}
	if len(got) != testChunk*5 {
		t.Fatalf("returned %d bytes; exactly the %d bytes of the 5 good chunks were owed", len(got), testChunk*5)
	}
	if !bytes.Equal(got, data[:testChunk*5]) {
		t.Fatal("the bytes before the bad chunk are not the good ones")
	}
	if _, again := r.Read(make([]byte, 10)); again == nil {
		t.Fatal("a reader that failed went on to return more bytes")
	}
}

func TestAShortOrLongSourceIsASizeFailure(t *testing.T) {
	data := payload(testChunk*3 + 50)
	ix := buildIdx(t, data)
	_, err := io.ReadAll(newReader(&memSource{data: data, flip: -1, short: 10}, ix))
	var ie *IntegrityError
	if !errors.As(err, &ie) || ie.Kind != KindSize {
		t.Fatalf("a copy that lost its tail: want a size failure, got %v", err)
	}
	_, err = io.ReadAll(newReader(&memSource{data: data, flip: -1, trailer: []byte{1}}, ix))
	if !errors.As(err, &ie) || ie.Kind != KindSize {
		t.Fatalf("a copy that grew: want a size failure, got %v", err)
	}
}

func TestTheWholeDigestIsCheckedBeforeTheLastChunkIsReleased(t *testing.T) {
	// Every chunk digest is right and the recorded whole-file digest is not,
	// the shape of an index that was mended by hand. The stream must stop one
	// chunk short and not hand over the final chunk.
	data := payload(testChunk * 4)
	ix := buildIdx(t, data)
	ix.SHA256 = hexSum([]byte("something else"))
	r := newReader(&memSource{data: data, flip: -1}, ix)
	got, err := io.ReadAll(r)
	var ie *IntegrityError
	if !errors.As(err, &ie) {
		t.Fatalf("want an integrity failure, got %v", err)
	}
	if len(got) != testChunk*3 {
		t.Fatalf("returned %d bytes; the last chunk must be withheld (%d)", len(got), testChunk*3)
	}
}

func TestAFailureIsCarriedWithTheOriginOfTheCopy(t *testing.T) {
	data := payload(testChunk * 2)
	r := NewVerifiedReader(context.Background(), &memSource{data: data, flip: 1}, buildIdx(t, data), OriginRemote, "warm")
	_, err := io.ReadAll(r)
	var ie *IntegrityError
	if !errors.As(err, &ie) || ie.Origin != OriginRemote || ie.Medium != "warm" {
		t.Fatalf("got %v", err)
	}
}

func TestPrimeFailsBeforeAnythingIsReturned(t *testing.T) {
	data := payload(testChunk * 3)
	r := newReader(&memSource{data: data, flip: 2}, buildIdx(t, data))
	if err := r.Prime(0); err == nil {
		t.Fatal("priming a bad first chunk did not fail")
	}
}

func TestACancelledContextStopsTheNextChunk(t *testing.T) {
	data := payload(testChunk * 3)
	ctx, cancel := context.WithCancel(context.Background())
	r := NewVerifiedReader(ctx, &memSource{data: data, flip: -1}, buildIdx(t, data), OriginLocal, "local")
	buf := make([]byte, testChunk)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := io.ReadFull(r, buf); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestAnEmptyCopyReadsAsEmptyAndVerified(t *testing.T) {
	ix := buildIdx(t, nil)
	r := newReader(&memSource{flip: -1}, ix)
	got, err := io.ReadAll(r)
	if err != nil || len(got) != 0 || !r.WholeVerified() {
		t.Fatalf("got %d bytes, %v, whole=%v", len(got), err, r.WholeVerified())
	}
}
