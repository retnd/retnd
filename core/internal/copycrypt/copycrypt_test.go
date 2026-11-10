package copycrypt_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/retnd/retnd/core/internal/copycrypt"
)

// These are the byte-path tests for EPIC N's N1.3 (#991). Every fixture is
// built from a seeded generator so a failure reproduces, none is all zero
// (ADR 0027: a keystream bug hides in zeros), and the sizes that matter
// are the ones around the age 64 KiB chunk boundary.
//
// chunk is spelled here because the library owns the value and this
// repository defines no constant for it; a test has to know where the
// boundaries are to land bytes on them.
const (
	chunk       = 64 << 10
	chunkSealed = chunk + 16 // one full chunk plus its Poly1305 tag
	nonceLen    = 16         // the per-file payload nonce that follows the header
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func fixture(seed uint64, n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for i := range b {
		b[i] = byte(r.UintN(255)) + 1
	}
	return b
}

func newKey(t *testing.T) (identity []byte, recipient string) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	return []byte(id.String()), id.Recipient().String()
}

func staged(t *testing.T, plain []byte, recipient string) (dir string, s copycrypt.Staged) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "staging")
	s, err := copycrypt.EncryptToStaging(context.Background(), dir, "artifact.bin", bytes.NewReader(plain), recipient, sum(plain))
	if err != nil {
		t.Fatalf("EncryptToStaging(%d bytes): %v", len(plain), err)
	}
	return dir, s
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// headerEnd is the offset of the first payload byte's 16-byte nonce: the
// age header ends with the line that starts "--- " (the MAC line).
func headerEnd(t *testing.T, ct []byte) int {
	t.Helper()
	i := bytes.Index(ct, []byte("\n--- "))
	if i < 0 {
		t.Fatalf("no MAC line in what should be an age header: %q", ct[:min(len(ct), 80)])
	}
	nl := bytes.IndexByte(ct[i+1:], '\n')
	if nl < 0 {
		t.Fatal("unterminated MAC line")
	}
	return i + 1 + nl + 1
}

func TestEveryBoundarySizeRoundTripsAndTheDigestsStayApart(t *testing.T) {
	t.Parallel()
	identity, recipient := newKey(t)

	for _, n := range []int{0, 1, chunk - 1, chunk, chunk + 1, 2 * chunk, 2*chunk + 1, 3*chunk + 17} {
		plain := fixture(uint64(n)+1, n)
		dir, s := staged(t, plain, recipient)
		ct := read(t, s.Path)

		if s.PlaintextHash != sum(plain) || s.PlaintextSize != int64(n) {
			t.Fatalf("%d bytes: plaintext digest/size = %s/%d, want %s/%d", n, s.PlaintextHash, s.PlaintextSize, sum(plain), n)
		}
		if s.StoredHash != sum(ct) || s.StoredSize != int64(len(ct)) {
			t.Fatalf("%d bytes: stored digest/size = %s/%d, but the file at %s is %s/%d", n, s.StoredHash, s.StoredSize, s.Path, sum(ct), len(ct))
		}
		if s.StoredHash == s.PlaintextHash {
			t.Fatalf("%d bytes: the two digests are the same number", n)
		}
		if s.StoredSize <= s.PlaintextSize {
			t.Fatalf("%d bytes: stored size %d does not exceed plaintext size %d (a 64 KiB boundary must not make them coincide)", n, s.StoredSize, s.PlaintextSize)
		}
		if n >= 64 && bytes.Contains(ct, plain[:64]) {
			t.Fatalf("%d bytes: the plaintext appears in the stored object", n)
		}
		if !bytes.HasPrefix(ct, []byte("age-encryption.org/v1\n")) {
			t.Fatalf("%d bytes: the stored object does not start with the age v1 header", n)
		}
		if got := names(t, dir); len(got) != 1 || got[0] != "artifact.bin" {
			t.Fatalf("%d bytes: staging holds %v, want exactly artifact.bin", n, got)
		}

		got, err := copycrypt.DecryptVerify(context.Background(), bytes.NewReader(ct), identity, sum(plain))
		if err != nil {
			t.Fatalf("%d bytes: DecryptVerify: %v", n, err)
		}
		if got != int64(n) {
			t.Fatalf("%d bytes: DecryptVerify reports %d plaintext bytes", n, got)
		}
	}
}

func TestAnEmptyObjectIsOneAuthenticatedFinalChunk(t *testing.T) {
	t.Parallel()
	identity, recipient := newKey(t)
	_, s := staged(t, nil, recipient)
	ct := read(t, s.Path)

	if want := int64(headerEnd(t, ct) + nonceLen + 16); s.StoredSize != want {
		t.Fatalf("empty object stored as %d bytes, want header + nonce + one 16-byte tag = %d", s.StoredSize, want)
	}
	// Dropping that one final chunk must not read as a clean empty file.
	cut := ct[:headerEnd(t, ct)+nonceLen]
	_, err := copycrypt.DecryptVerify(context.Background(), bytes.NewReader(cut), identity, sum(nil))
	if !errors.Is(err, copycrypt.ErrCorrupt) {
		t.Fatalf("an empty object with its final chunk removed: err = %v, want ErrCorrupt", err)
	}
}

func TestEveryNegativeFailsClosedAndWritesNoPartialPlaintext(t *testing.T) {
	t.Parallel()
	identity, recipient := newKey(t)
	otherIdentity, _ := newKey(t)

	// Three full chunks and a short final one, so every mutation below has
	// a chunk on each side of it.
	plain := fixture(7, 3*chunk+100)
	_, s := staged(t, plain, recipient)
	good := read(t, s.Path)
	hdr := headerEnd(t, good)
	payload := hdr + nonceLen

	_, foreign := staged(t, plain, recipient) // same recipient, its own file key and nonce
	foreignCT := read(t, foreign.Path)
	foreignHdr := headerEnd(t, foreignCT)

	mutate := func(f func(b []byte) []byte) []byte { return f(bytes.Clone(good)) }

	cases := []struct {
		name     string
		ct       []byte
		identity []byte
		want     error
	}{
		{"single bit flip in the second chunk", mutate(func(b []byte) []byte { b[payload+chunkSealed+1000] ^= 0x01; return b }), identity, copycrypt.ErrCorrupt},
		{"single bit flip in the final tag", mutate(func(b []byte) []byte { b[len(b)-1] ^= 0x80; return b }), identity, copycrypt.ErrCorrupt},
		{"single bit flip in the header MAC line", mutate(func(b []byte) []byte { b[hdr-3] ^= 0x01; return b }), identity, copycrypt.ErrCorrupt},
		{"truncated by one byte", good[:len(good)-1], identity, copycrypt.ErrCorrupt},
		{"truncated mid chunk", good[:payload+chunkSealed+5], identity, copycrypt.ErrCorrupt},
		{"truncated exactly on a chunk boundary", good[:payload+3*chunkSealed], identity, copycrypt.ErrCorrupt},
		{"truncated inside the header", good[:hdr-10], identity, copycrypt.ErrCorrupt},
		{"trailing bytes after the final chunk", append(bytes.Clone(good), 0x00), identity, copycrypt.ErrCorrupt},
		{"two chunks swapped", mutate(func(b []byte) []byte {
			a := b[payload : payload+chunkSealed]
			c := b[payload+chunkSealed : payload+2*chunkSealed]
			tmp := bytes.Clone(a)
			copy(a, c)
			copy(c, tmp)
			return b
		}), identity, copycrypt.ErrCorrupt},
		{"header swapped for another encryption's", append(bytes.Clone(foreignCT[:foreignHdr]), good[hdr:]...), identity, copycrypt.ErrCorrupt},
		{"wrong key", good, otherIdentity, copycrypt.ErrWrongKey},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			// The digest-only verifier.
			n, err := copycrypt.DecryptVerify(context.Background(), bytes.NewReader(c.ct), c.identity, sum(plain))
			if !errors.Is(err, c.want) {
				t.Fatalf("DecryptVerify err = %v, want %v", err, c.want)
			}
			if n != 0 {
				t.Fatalf("DecryptVerify returned %d plaintext bytes alongside an error", n)
			}

			// The export path: nothing at the final name, and no temporary file either.
			dir := filepath.Join(t.TempDir(), "out")
			path, err := copycrypt.DecryptToFile(context.Background(), bytes.NewReader(c.ct), c.identity, sum(plain), dir, "recovered.bin")
			if !errors.Is(err, c.want) {
				t.Fatalf("DecryptToFile err = %v, want %v", err, c.want)
			}
			if path != "" {
				t.Fatalf("DecryptToFile returned path %q alongside an error", path)
			}
			if left := names(t, dir); len(left) != 0 {
				t.Fatalf("a failed decrypt left %v behind; no partial plaintext may be written", left)
			}
		})
	}
}

func TestADecryptWhoseDigestDisagreesWithTheJournalFails(t *testing.T) {
	t.Parallel()
	identity, recipient := newKey(t)
	plain := fixture(11, chunk+5)
	_, s := staged(t, plain, recipient)

	altered := sum(append(bytes.Clone(plain), 'x')) // the recorded artifact digest is wrong
	_, err := copycrypt.DecryptVerify(context.Background(), bytes.NewReader(read(t, s.Path)), identity, altered)
	if !errors.Is(err, copycrypt.ErrDigestMismatch) {
		t.Fatalf("a recorded digest that is not the plaintext's: err = %v, want ErrDigestMismatch", err)
	}

	dir := filepath.Join(t.TempDir(), "out")
	if _, err := copycrypt.DecryptToFile(context.Background(), bytes.NewReader(read(t, s.Path)), identity, altered, dir, "recovered.bin"); !errors.Is(err, copycrypt.ErrDigestMismatch) {
		t.Fatalf("DecryptToFile with a wrong recorded digest: err = %v, want ErrDigestMismatch", err)
	}
	if left := names(t, dir); len(left) != 0 {
		t.Fatalf("a digest mismatch left %v behind", left)
	}

	// Case is not part of the digest.
	if _, err := copycrypt.DecryptVerify(context.Background(), bytes.NewReader(read(t, s.Path)), identity, strings.ToUpper(sum(plain))); err != nil {
		t.Fatalf("an upper-case recorded digest: %v", err)
	}
}

func TestEncryptingASourceThatIsNotTheRecordedArtifactIsRefusedAndLeavesNothing(t *testing.T) {
	t.Parallel()
	_, recipient := newKey(t)
	plain := fixture(13, 2*chunk)
	dir := filepath.Join(t.TempDir(), "staging")

	_, err := copycrypt.EncryptToStaging(context.Background(), dir, "a.bin", bytes.NewReader(plain), recipient, sum([]byte("something else")))
	if !errors.Is(err, copycrypt.ErrDigestMismatch) {
		t.Fatalf("err = %v, want ErrDigestMismatch", err)
	}
	if left := names(t, dir); len(left) != 0 {
		t.Fatalf("a refused encrypt left %v in the staging area", left)
	}
}

func TestAnUnresolvableKeyIsKeyUnavailableNotACorruptionOrAWrongKey(t *testing.T) {
	t.Parallel()
	_, recipient := newKey(t)
	plain := fixture(17, 100)
	_, s := staged(t, plain, recipient)
	ct := read(t, s.Path)

	for _, bad := range []string{"", "   ", "not a key", "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"} {
		_, err := copycrypt.DecryptVerify(context.Background(), bytes.NewReader(ct), []byte(bad), sum(plain))
		if !errors.Is(err, copycrypt.ErrKeyUnavailable) {
			t.Fatalf("identity %q: err = %v, want ErrKeyUnavailable", bad, err)
		}
		if errors.Is(err, copycrypt.ErrWrongKey) || errors.Is(err, copycrypt.ErrCorrupt) {
			t.Fatalf("identity %q: an unresolvable key was reported as a different failure: %v", bad, err)
		}
		if strings.TrimSpace(bad) != "" && strings.Contains(err.Error(), strings.TrimSpace(bad)) {
			t.Fatalf("the error repeats the identity string: %v", err)
		}
	}

	dir := filepath.Join(t.TempDir(), "staging")
	for _, bad := range []string{"", "age1notarecipient"} {
		_, err := copycrypt.EncryptToStaging(context.Background(), dir, "a.bin", bytes.NewReader(plain), bad, sum(plain))
		if !errors.Is(err, copycrypt.ErrKeyUnavailable) {
			t.Fatalf("recipient %q: err = %v, want ErrKeyUnavailable", bad, err)
		}
	}
	if left := names(t, dir); len(left) != 0 {
		t.Fatalf("an unresolvable recipient left %v in the staging area", left)
	}
}

func TestSuccessiveEncryptionsOfOneArtifactShareNoNonceOrStreamState(t *testing.T) {
	t.Parallel()
	_, recipient := newKey(t)
	plain := fixture(19, chunk+1)

	nonces := map[string]bool{}
	headers := map[string]bool{}
	bodies := map[string]bool{}
	for range 24 {
		_, s := staged(t, plain, recipient)
		ct := read(t, s.Path)
		h := headerEnd(t, ct)
		nonce := string(ct[h : h+nonceLen])
		if nonces[nonce] {
			t.Fatalf("a payload nonce was emitted twice across attempts at one artifact")
		}
		nonces[nonce] = true
		if headers[string(ct[:h])] {
			t.Fatalf("a header (ephemeral share and wrapped file key) was emitted twice")
		}
		headers[string(ct[:h])] = true
		if bodies[sum(ct[h+nonceLen:])] {
			t.Fatalf("two attempts produced the same ciphertext body")
		}
		bodies[sum(ct[h+nonceLen:])] = true
	}
}

func TestTwoCopiesToOneNameLeaveOneObjectAndTheDigestOfThatObject(t *testing.T) {
	t.Parallel()
	_, recipient := newKey(t)
	plain := fixture(23, 2*chunk+3)
	dir := filepath.Join(t.TempDir(), "staging")

	first, err := copycrypt.EncryptToStaging(context.Background(), dir, "a.bin", bytes.NewReader(plain), recipient, sum(plain))
	if err != nil {
		t.Fatal(err)
	}
	second, err := copycrypt.EncryptToStaging(context.Background(), dir, "a.bin", bytes.NewReader(plain), recipient, sum(plain))
	if err != nil {
		t.Fatal(err)
	}
	if first.StoredHash == second.StoredHash {
		t.Fatal("two encryptions of one artifact hash the same; ciphertext is not deterministic, so a recorded digest would be reusable by mistake")
	}
	if got := names(t, dir); len(got) != 1 {
		t.Fatalf("staging holds %v after two copies to one name, want one object", got)
	}
	if got := sum(read(t, second.Path)); got != second.StoredHash {
		t.Fatalf("the object at the name hashes to %s, but the second attempt recorded %s", got, second.StoredHash)
	}
	if first.PlaintextHash != second.PlaintextHash {
		t.Fatal("the plaintext digest changed between attempts")
	}
}

type interrupting struct {
	r      io.Reader
	after  int
	read   int
	action func()
}

func (i *interrupting) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	i.read += n
	if i.read >= i.after && i.action != nil {
		i.action()
		i.action = nil
	}
	return n, err
}

func TestAnInterruptedEncryptCannotLeaveAGoodLookingObject(t *testing.T) {
	t.Parallel()
	_, recipient := newKey(t)
	plain := fixture(29, 4*chunk)
	want := sum(plain)

	t.Run("cancellation mid stream", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "staging")
		ctx, cancel := context.WithCancel(context.Background())
		src := &interrupting{r: bytes.NewReader(plain), after: chunk + 1, action: cancel}
		_, err := copycrypt.EncryptToStaging(ctx, dir, "a.bin", src, recipient, want)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if left := names(t, dir); len(left) != 0 {
			t.Fatalf("a cancelled encrypt left %v in the staging area", left)
		}
	})

	t.Run("source read error mid stream", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "staging")
		boom := errors.New("disk fell off")
		src := io.MultiReader(bytes.NewReader(plain[:chunk+7]), &failing{boom})
		_, err := copycrypt.EncryptToStaging(context.Background(), dir, "a.bin", src, recipient, want)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the source's own error", err)
		}
		if errors.Is(err, copycrypt.ErrCorrupt) {
			t.Fatalf("a source read failure was reported as corruption: %v", err)
		}
		if left := names(t, dir); len(left) != 0 {
			t.Fatalf("a failed read left %v in the staging area", left)
		}
	})

	t.Run("a previous good object is not replaced by a failed retry", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "staging")
		prev, err := copycrypt.EncryptToStaging(context.Background(), dir, "a.bin", bytes.NewReader(plain), recipient, want)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		src := &interrupting{r: bytes.NewReader(plain), after: 1, action: cancel}
		if _, err := copycrypt.EncryptToStaging(ctx, dir, "a.bin", src, recipient, want); err == nil {
			t.Fatal("expected the cancelled retry to fail")
		}
		if got := sum(read(t, prev.Path)); got != prev.StoredHash {
			t.Fatalf("the failed retry disturbed the object at the name: %s vs %s", got, prev.StoredHash)
		}
		if got := names(t, dir); len(got) != 1 {
			t.Fatalf("staging holds %v, want only the earlier object", got)
		}
	})

	t.Run("an already cancelled context writes nothing", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "staging")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := copycrypt.EncryptToStaging(ctx, dir, "a.bin", bytes.NewReader(plain), recipient, want); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if left := names(t, dir); len(left) != 0 {
			t.Fatalf("left %v", left)
		}
	})
}

type failing struct{ err error }

func (f *failing) Read([]byte) (int, error) { return 0, f.err }

func TestAReadFailureWhileDecryptingIsNotCorruption(t *testing.T) {
	t.Parallel()
	identity, recipient := newKey(t)
	plain := fixture(31, 3*chunk)
	_, s := staged(t, plain, recipient)
	ct := read(t, s.Path)
	boom := errors.New("connection reset")

	src := io.MultiReader(bytes.NewReader(ct[:len(ct)/2]), &failing{boom})
	_, err := copycrypt.DecryptVerify(context.Background(), src, identity, sum(plain))
	if !errors.Is(err, boom) || errors.Is(err, copycrypt.ErrCorrupt) {
		t.Fatalf("err = %v, want the reader's own error and not ErrCorrupt", err)
	}
}

func TestDecryptToFileExportsOnlyAfterTheFinalChunkAuthenticated(t *testing.T) {
	t.Parallel()
	identity, recipient := newKey(t)
	plain := fixture(37, 2*chunk+9)
	_, s := staged(t, plain, recipient)

	dir := filepath.Join(t.TempDir(), "out")
	path, err := copycrypt.DecryptToFile(context.Background(), bytes.NewReader(read(t, s.Path)), identity, sum(plain), dir, "recovered.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read(t, path), plain) {
		t.Fatal("the exported file is not the plaintext")
	}
	if got := names(t, dir); len(got) != 1 || got[0] != "recovered.bin" {
		t.Fatalf("export directory holds %v, want only recovered.bin", got)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the recovered artifact is readable by others: %v", fi.Mode())
	}
}

func TestNamesThatEscapeTheDirectoryAreRefused(t *testing.T) {
	t.Parallel()
	identity, recipient := newKey(t)
	plain := fixture(41, 10)
	root := t.TempDir()
	dir := filepath.Join(root, "staging")
	_, s := staged(t, plain, recipient)

	for _, name := range []string{"", ".", "..", "a/b", "../x", `a\b`} {
		if _, err := copycrypt.EncryptToStaging(context.Background(), dir, name, bytes.NewReader(plain), recipient, sum(plain)); err == nil {
			t.Fatalf("EncryptToStaging accepted the name %q", name)
		}
		if _, err := copycrypt.DecryptToFile(context.Background(), bytes.NewReader(read(t, s.Path)), identity, sum(plain), dir, name); err == nil {
			t.Fatalf("DecryptToFile accepted the name %q", name)
		}
	}
	if got := names(t, root); len(got) != 0 && (len(got) != 1 || got[0] != "staging") {
		t.Fatalf("a refused name wrote outside the directory: %v", got)
	}
}

func TestTheFormatIsRecordedAsAgeV1(t *testing.T) {
	t.Parallel()
	if copycrypt.FormatID != "age/v1" || copycrypt.FormatVersion != 1 {
		t.Fatalf("format = %q/%d, ADR 0027 records age/v1 and 1", copycrypt.FormatID, copycrypt.FormatVersion)
	}
}

// generated yields non-zero bytes without holding them, so a 2 GiB object
// costs the test nothing but the pass over it.
type generated struct {
	remaining int64
	buf       []byte
}

func (g *generated) Read(p []byte) (int, error) {
	if g.remaining <= 0 {
		return 0, io.EOF
	}
	if g.buf == nil {
		g.buf = fixture(43, 1<<20)
	}
	n := min(int64(len(p)), g.remaining, int64(len(g.buf)))
	copy(p, g.buf[:n])
	g.remaining -= n
	return int(n), nil
}

func TestAMultiGiBObjectEncryptsAndDecryptVerifiesInsideAnAllocationBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a 2 GiB staging object")
	}
	identity, recipient := newKey(t)
	const size = 2 << 30

	// The recorded digest of the generated artifact, from a first cheap pass.
	h := sha256.New()
	if _, err := io.Copy(h, &generated{remaining: size}); err != nil {
		t.Fatal(err)
	}
	want := hex.EncodeToString(h.Sum(nil))

	dir := filepath.Join(t.TempDir(), "staging")
	runtime.GC()

	// The metric is the peak of the heap in use while the pass runs, not
	// cumulative allocation. age seals each 64 KiB chunk into a fresh buffer,
	// so TotalAlloc grows with the object (about 2 GiB here) even though
	// almost none of it is ever live at once; a bound on the total would
	// either fail an honest streamer or have to be set so high it asserts
	// nothing. What "copied rather than streamed" means is that the object
	// (or a large part of it) is held, and that shows up as live heap: a
	// whole-object buffer is at least 2 GiB, the sampled peak of a streamer
	// is a few chunks plus the collector's headroom.
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()

	s, err := copycrypt.EncryptToStaging(context.Background(), dir, "huge.bin", &generated{remaining: size}, recipient, want)
	if err != nil {
		close(stop)
		<-done
		t.Fatalf("EncryptToStaging: %v", err)
	}
	f, err := os.Open(s.Path)
	if err != nil {
		close(stop)
		<-done
		t.Fatal(err)
	}
	defer f.Close()
	n, err := copycrypt.DecryptVerify(context.Background(), f, identity, want)
	close(stop)
	<-done
	if err != nil {
		t.Fatalf("DecryptVerify: %v", err)
	}
	if n != size {
		t.Fatalf("decrypt-verified %d bytes of a %d-byte object", n, int64(size))
	}

	const peakBudget = 128 << 20
	if p := int64(peak.Load()); p > peakBudget {
		t.Fatalf("encrypting and decrypt-verifying %d bytes reached a live heap of %d; the object is being copied rather than streamed", int64(size), p)
	}
	runtime.GC()
	var end runtime.MemStats
	runtime.ReadMemStats(&end)
	if live := int64(end.HeapAlloc); live > 64<<20 {
		t.Fatalf("the heap still holds %d bytes after streaming a %d-byte object", live, int64(size))
	}
}
