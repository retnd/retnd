// Package suitetest is the one set of conformance vectors every encryption
// suite in copycrypt's registry has to pass (EPIC T, FR-90, ADR 0033 D5).
// A new suite is one adapter, one table row, and these vectors going green
// for it, red first.
//
// Each group is a function that returns the problems it found rather than
// failing a test itself, so the vectors can be pointed at a deliberately
// broken adapter and be seen to object (the anti-vacuity control in
// suitetest_test.go). Run turns the problems into test failures.
//
// Nothing here designs cryptography either: the vectors only drive a
// suite's Seal and Open and copycrypt's staging and decrypt functions, and
// look at lengths and offsets the age specification fixes.
package suitetest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/procseam"
)

// Chunk is the age STREAM chunk size. The library owns the value; the
// vectors spell it to land bytes on chunk boundaries.
const (
	Chunk    = 64 << 10
	tagLen   = 16
	sealedCh = Chunk + tagLen
)

// Fixture is n non-zero bytes from a seeded generator, so a failure
// reproduces and a keystream bug cannot hide in zeros.
func Fixture(seed uint64, n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for i := range b {
		b[i] = byte(r.UintN(255)) + 1
	}
	return b
}

// sizes are the round-trip fixtures: empty, one byte, and the lengths
// around the chunk boundary, ending in a short final chunk.
var sizes = []int{0, 1, Chunk - 1, Chunk, Chunk + 1, 2*Chunk + 17}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Seal encrypts plain to recipient under suite into memory.
func Seal(suite copycrypt.Suite, recipient string, plain []byte) ([]byte, error) {
	var out bytes.Buffer
	w, err := suite.Seal(&out, recipient)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// payloadLen is the sealed payload length of an n-byte plaintext: one tag
// per chunk, and one empty final chunk for the empty plaintext.
func payloadLen(n int) int {
	if n == 0 {
		return tagLen
	}
	chunks := (n + Chunk - 1) / Chunk
	return n + chunks*tagLen
}

// openAll reads everything suite.Open yields. A conformant adapter returns
// an error, from Open or from a read, for anything that does not
// authenticate.
func openAll(suite copycrypt.Suite, secret, ct []byte) ([]byte, error) {
	r, err := suite.Open(secret, bytes.NewReader(ct))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// RoundTrip is group 1: every fixture comes back byte for byte, through
// the adapter and through copycrypt's staging and decrypt-verify.
func RoundTrip(suite copycrypt.Suite) []string {
	var problems []string
	secret, recipient, err := suite.Generate()
	if err != nil {
		return []string{fmt.Sprintf("Generate: %v", err)}
	}
	for i, n := range sizes {
		plain := Fixture(uint64(100+i), n)
		ct, err := Seal(suite, recipient, plain)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%d bytes: Seal: %v", n, err))
			continue
		}
		got, err := openAll(suite, secret, ct)
		if err != nil || !bytes.Equal(got, plain) {
			problems = append(problems, fmt.Sprintf("%d bytes: Open gave %d bytes, err %v; want the fixture back", n, len(got), err))
		}
		dir, err := os.MkdirTemp("", "suitetest-")
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		s, err := copycrypt.EncryptToStaging(context.Background(), dir, "a.bin", bytes.NewReader(plain), suite, recipient, sum(plain))
		if err == nil {
			f, ferr := os.Open(s.Path)
			if ferr != nil {
				err = ferr
			} else {
				var got int64
				got, err = copycrypt.DecryptVerify(context.Background(), f, suite, secret, sum(plain))
				f.Close()
				if err == nil && got != int64(n) {
					err = fmt.Errorf("decrypt-verified %d bytes", got)
				}
			}
			if s.SuiteID != suite.ID() || s.Version != suite.Version() {
				problems = append(problems, fmt.Sprintf("%d bytes: Staged names %s/%d, want %s/%d", n, s.SuiteID, s.Version, suite.ID(), suite.Version()))
			}
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%d bytes: staging and decrypt-verify: %v", n, err))
		}
		os.RemoveAll(dir)
	}
	return problems
}

// Negatives is group 2: a flipped bit, a tail cut on a chunk boundary, two
// chunks swapped, another encryption's header and a wrong key each fail
// closed. At the adapter, Open or a read returns an error (a clean EOF is
// the failure); at copycrypt, DecryptVerify reports no length and
// DecryptToFile leaves nothing behind.
func Negatives(suite copycrypt.Suite) []string {
	var problems []string
	secret, recipient, err := suite.Generate()
	if err != nil {
		return []string{fmt.Sprintf("Generate: %v", err)}
	}
	plain := Fixture(7, 2*Chunk+17)
	ct, err := Seal(suite, recipient, plain)
	if err != nil {
		return []string{fmt.Sprintf("Seal: %v", err)}
	}
	ct2, err := Seal(suite, recipient, plain)
	if err != nil {
		return []string{fmt.Sprintf("Seal: %v", err)}
	}
	start := len(ct) - payloadLen(len(plain))
	if start <= 0 || len(ct2) != len(ct) {
		return []string{fmt.Sprintf("the stored object is %d bytes for a %d-byte payload; the vectors cannot find the header", len(ct), payloadLen(len(plain)))}
	}
	wrongSecret, _, err := suite.Generate()
	if err != nil {
		return []string{fmt.Sprintf("Generate: %v", err)}
	}

	flipped := append([]byte(nil), ct...)
	flipped[start+Chunk/2] ^= 0x01
	cut := append([]byte(nil), ct[:start+2*sealedCh]...)
	swapped := append([]byte(nil), ct...)
	copy(swapped[start:start+sealedCh], ct[start+sealedCh:start+2*sealedCh])
	copy(swapped[start+sealedCh:start+2*sealedCh], ct[start:start+sealedCh])
	header := append(append([]byte(nil), ct2[:start]...), ct[start:]...)

	cases := []struct {
		name   string
		ct     []byte
		secret []byte
		want   error
	}{
		{"a flipped bit", flipped, secret, copycrypt.ErrCorrupt},
		{"a tail cut on a chunk boundary", cut, secret, copycrypt.ErrCorrupt},
		{"two chunks swapped", swapped, secret, copycrypt.ErrCorrupt},
		{"another encryption's header", header, secret, copycrypt.ErrCorrupt},
		{"a wrong key", ct, wrongSecret, copycrypt.ErrWrongKey},
	}
	for _, c := range cases {
		if _, err := openAll(suite, c.secret, c.ct); err == nil {
			problems = append(problems, fmt.Sprintf("%s: the adapter opened it and read to a clean end", c.name))
		}
		n, err := copycrypt.DecryptVerify(context.Background(), bytes.NewReader(c.ct), suite, c.secret, sum(plain))
		if !errors.Is(err, c.want) || n != 0 {
			problems = append(problems, fmt.Sprintf("%s: DecryptVerify = %d, %v; want 0 and %v", c.name, n, err, c.want))
		}
		dir, err := os.MkdirTemp("", "suitetest-")
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if path, err := copycrypt.DecryptToFile(context.Background(), bytes.NewReader(c.ct), suite, c.secret, sum(plain), dir, "out.bin"); err == nil || path != "" {
			problems = append(problems, fmt.Sprintf("%s: DecryptToFile = %q, %v; want no file and an error", c.name, path, err))
		}
		if es, _ := os.ReadDir(dir); len(es) != 0 {
			problems = append(problems, fmt.Sprintf("%s: DecryptToFile left %d file(s) behind; no partial plaintext may be written", c.name, len(es)))
		}
		os.RemoveAll(dir)
	}
	return problems
}

// CrossSuite is group 3: a key of every other suite is refused by Recipient
// and Open, and its recipient is refused by Seal.
func CrossSuite(suite copycrypt.Suite, others []copycrypt.Suite) []string {
	var problems []string
	_, recipient, err := suite.Generate()
	if err != nil {
		return []string{fmt.Sprintf("Generate: %v", err)}
	}
	ct, err := Seal(suite, recipient, Fixture(9, 1000))
	if err != nil {
		return []string{fmt.Sprintf("Seal: %v", err)}
	}
	for _, other := range others {
		otherSecret, otherRecipient, err := other.Generate()
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s.Generate: %v", other.ID(), err))
			continue
		}
		if _, err := suite.Recipient(otherSecret); err == nil {
			problems = append(problems, fmt.Sprintf("Recipient accepted a %s key", other.ID()))
		}
		if _, err := openAll(suite, otherSecret, ct); err == nil {
			problems = append(problems, fmt.Sprintf("Open accepted a %s key", other.ID()))
		}
		if _, err := suite.Seal(io.Discard, otherRecipient); err == nil {
			problems = append(problems, fmt.Sprintf("Seal accepted a %s recipient", other.ID()))
		}
	}
	return problems
}

// FreshHeaders is group 4: two encryptions of one plaintext to one
// recipient differ in their headers (no reused file key or nonce).
func FreshHeaders(suite copycrypt.Suite) []string {
	_, recipient, err := suite.Generate()
	if err != nil {
		return []string{fmt.Sprintf("Generate: %v", err)}
	}
	plain := Fixture(11, Chunk+5)
	a, err := Seal(suite, recipient, plain)
	if err != nil {
		return []string{fmt.Sprintf("Seal: %v", err)}
	}
	b, err := Seal(suite, recipient, plain)
	if err != nil {
		return []string{fmt.Sprintf("Seal: %v", err)}
	}
	start := len(a) - payloadLen(len(plain))
	if start <= 0 || bytes.Equal(a[:start], b[:start]) || bytes.Equal(a, b) {
		return []string{"two encryptions of one plaintext to one recipient share a header"}
	}
	return nil
}

// Overhead is group 5: the stored object is strictly larger than the
// plaintext for every fixture, the empty one included, which is what
// state's stored-size refusal relies on.
func Overhead(suite copycrypt.Suite) []string {
	var problems []string
	_, recipient, err := suite.Generate()
	if err != nil {
		return []string{fmt.Sprintf("Generate: %v", err)}
	}
	for i, n := range sizes {
		ct, err := Seal(suite, recipient, Fixture(uint64(200+i), n))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%d bytes: Seal: %v", n, err))
			continue
		}
		if len(ct) <= n {
			problems = append(problems, fmt.Sprintf("%d bytes sealed to %d; the stored size must exceed the plaintext", n, len(ct)))
		}
	}
	return problems
}

// Identity is group 7's per-suite half: a stable id, a version of at least
// 1, a status from the vocabulary and a sentence to print. The table pin
// itself is copycrypt's golden-list test.
func Identity(suite copycrypt.Suite) []string {
	var problems []string
	if suite.ID() == "" || strings.TrimSpace(suite.ID()) != suite.ID() {
		problems = append(problems, fmt.Sprintf("id %q is not a stable string", suite.ID()))
	}
	if suite.Version() < 1 {
		problems = append(problems, fmt.Sprintf("version %d is below 1", suite.Version()))
	}
	switch suite.Status() {
	case copycrypt.StatusSupported, copycrypt.StatusDeprecated, copycrypt.StatusRetired:
	default:
		problems = append(problems, fmt.Sprintf("status %q is not in the vocabulary", suite.Status()))
	}
	if strings.TrimSpace(string(suite.Describe())) == "" {
		problems = append(problems, "no Describe() sentence")
	}
	return problems
}

// allocBudget is the most live heap a multi-GiB pass may reach: a few
// chunks and the collector's headroom, never the object.
const allocBudget = 256 << 20

// generated yields non-zero bytes without holding them.
type generated struct {
	remaining int64
	buf       []byte
}

func (g *generated) Read(p []byte) (int, error) {
	if g.remaining <= 0 {
		return 0, io.EOF
	}
	if g.buf == nil {
		g.buf = Fixture(43, 1<<20)
	}
	n := min(int64(len(p)), g.remaining, int64(len(g.buf)))
	copy(p, g.buf[:n])
	g.remaining -= n
	return int(n), nil
}

// Budget is group 6: a size-byte object through EncryptToStaging and
// DecryptVerify with the live heap sampled, in the shape of
// core/internal/backupengine/source/adapter_test.go. The problem names the
// defect.
func Budget(suite copycrypt.Suite, size int64) []string {
	secret, recipient, err := suite.Generate()
	if err != nil {
		return []string{fmt.Sprintf("Generate: %v", err)}
	}
	h := sha256.New()
	if _, err := io.Copy(h, &generated{remaining: size}); err != nil {
		return []string{err.Error()}
	}
	want := hex.EncodeToString(h.Sum(nil))
	dir, err := os.MkdirTemp("", "suitetest-huge-")
	if err != nil {
		return []string{err.Error()}
	}
	defer os.RemoveAll(dir)

	runtime.GC()
	var peak atomic.Uint64
	stop, done := make(chan struct{}), make(chan struct{})
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
	s, err := copycrypt.EncryptToStaging(context.Background(), dir, "huge.bin", &generated{remaining: size}, suite, recipient, want)
	var n int64
	if err == nil {
		var f *os.File
		if f, err = os.Open(s.Path); err == nil {
			n, err = copycrypt.DecryptVerify(context.Background(), f, suite, secret, want)
			f.Close()
		}
	}
	close(stop)
	<-done
	switch {
	case err != nil:
		return []string{fmt.Sprintf("the %d-byte pass failed: %v", size, err)}
	case n != size:
		return []string{fmt.Sprintf("decrypt-verified %d bytes of a %d-byte object", n, size)}
	case peak.Load() > allocBudget:
		return []string{fmt.Sprintf("the live heap peaked at %d bytes against a budget of %d: the object is being buffered rather than streamed", peak.Load(), allocBudget)}
	}
	return nil
}

// AgeBinary is the stock age CLI the external-reader vector uses: the
// RETND_AGE_BIN environment variable, or `age` on PATH. Empty when there is
// none, and the vector then skips rather than passes.
func AgeBinary() string {
	if p := os.Getenv("RETND_AGE_BIN"); p != "" {
		return p
	}
	p, err := exec.LookPath("age")
	if err != nil {
		return ""
	}
	return p
}

// ageAtLeast reports whether the binary's `age --version` is at least
// major.minor. An unparseable version is not assumed new enough.
func ageAtLeast(bin string, major, minor int) bool {
	out, err := procseam.Command(bin, "--version").Output()
	if err != nil {
		return false
	}
	v := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return ma > major || (ma == major && mi >= minor)
}

// ExternalReader is group 8, for age suites: a fixture the suite sealed
// decrypts with the stock age CLI and the suite's own identity. The second
// return is why it was skipped, empty when it ran.
func ExternalReader(suite copycrypt.Suite, bin string) (problems []string, skipped string) {
	if !strings.HasPrefix(suite.ID(), "age") {
		return nil, suite.ID() + " is not an age suite"
	}
	if bin == "" {
		return nil, "no age binary (set RETND_AGE_BIN or put age on PATH; the gate image has the one scripts/ci/pins.env pins)"
	}
	if suite.ID() == copycrypt.AgeMLKEM768X25519ID && !ageAtLeast(bin, 1, 3) {
		return nil, bin + " is older than age 1.3, which is the first to read post-quantum recipients"
	}
	secret, recipient, err := suite.Generate()
	if err != nil {
		return []string{fmt.Sprintf("Generate: %v", err)}, ""
	}
	plain := Fixture(13, Chunk+99)
	ct, err := Seal(suite, recipient, plain)
	if err != nil {
		return []string{fmt.Sprintf("Seal: %v", err)}, ""
	}
	dir, err := os.MkdirTemp("", "suitetest-age-")
	if err != nil {
		return []string{err.Error()}, ""
	}
	defer os.RemoveAll(dir)
	keyPath, ctPath := filepath.Join(dir, "key.txt"), filepath.Join(dir, "copy.age")
	if err := os.WriteFile(keyPath, append(append([]byte(nil), secret...), '\n'), 0o600); err != nil {
		return []string{err.Error()}, ""
	}
	if err := os.WriteFile(ctPath, ct, 0o600); err != nil {
		return []string{err.Error()}, ""
	}
	var stdout, stderr bytes.Buffer
	cmd := procseam.Command(bin, "-d", "-i", keyPath, ctPath)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return []string{fmt.Sprintf("age -d -i key.txt failed: %v: %s", err, strings.TrimSpace(stderr.String()))}, ""
	}
	if !bytes.Equal(stdout.Bytes(), plain) {
		return []string{fmt.Sprintf("age -d -i key.txt gave %d bytes that are not the fixture", stdout.Len())}, ""
	}
	return nil, ""
}

// HugeSize is group 6's object size.
const HugeSize = 2 << 30

// Run runs every group against suite. others are the rest of the registry,
// for group 3.
func Run(t *testing.T, suite copycrypt.Suite, others []copycrypt.Suite) {
	t.Helper()
	report := func(t *testing.T, problems []string) {
		t.Helper()
		for _, p := range problems {
			t.Errorf("%s: %s", suite.ID(), p)
		}
	}
	t.Run("1 round trip", func(t *testing.T) { report(t, RoundTrip(suite)) })
	t.Run("2 negatives fail closed", func(t *testing.T) { report(t, Negatives(suite)) })
	t.Run("3 other suites' keys are refused", func(t *testing.T) { report(t, CrossSuite(suite, others)) })
	t.Run("4 fresh headers", func(t *testing.T) { report(t, FreshHeaders(suite)) })
	t.Run("5 stored size exceeds plaintext", func(t *testing.T) { report(t, Overhead(suite)) })
	t.Run("6 multi-GiB inside the budget", func(t *testing.T) {
		if testing.Short() {
			t.Skip("streams a 2 GiB object twice")
		}
		report(t, Budget(suite, HugeSize))
	})
	t.Run("7 identity", func(t *testing.T) { report(t, Identity(suite)) })
	t.Run("8 the stock age CLI opens it", func(t *testing.T) {
		problems, skipped := ExternalReader(suite, AgeBinary())
		if skipped != "" {
			t.Skip(skipped)
		}
		report(t, problems)
	})
}
