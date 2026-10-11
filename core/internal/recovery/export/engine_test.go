package export

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/retnd/retnd/core/internal/recovery/budget"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

type bytesSource struct {
	data []byte
	// flipAt, when >= 0, flips one bit of that offset as it is read, which
	// keeps the size identical and changes the digest.
	flipAt int
	opens  atomic.Int32
}

func (b *bytesSource) Open(context.Context) (io.ReadCloser, error) {
	b.opens.Add(1)
	d := append([]byte(nil), b.data...)
	if b.flipAt >= 0 {
		d[b.flipAt] ^= 0x01
	}
	return io.NopCloser(bytes.NewReader(d)), nil
}

func goodSource(data []byte) *bytesSource { return &bytesSource{data: data, flipAt: -1} }

func digestOf(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func expectFor(data []byte, o Origin) Expect {
	n := int64(len(data))
	return Expect{Size: &n, HashAlg: "sha256", Hash: digestOf(data), Origin: o, Medium: "m"}
}

func newEngine(t *testing.T) (*Engine, *Staging) {
	st := newStaging(t, newClock())
	return &Engine{Staging: st}, st
}

func sample(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + 7)
	}
	return b
}

func TestStageAGoodCopyVerifiesAndLeavesOneStagedFile(t *testing.T) {
	e, st := newEngine(t)
	data := sample(1 << 20)
	s, err := e.Stage(context.Background(), goodSource(data), expectFor(data, OriginLocal))
	if err != nil {
		t.Fatal(err)
	}
	if s.Size != int64(len(data)) || s.Digest != digestOf(data) {
		t.Fatalf("staged = %+v", s)
	}
	f, err := s.Open()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("staged bytes differ from the source")
	}
	if err := s.Discard(); err != nil {
		t.Fatal(err)
	}
	if e, _ := st.Entries(); len(e) != 0 {
		t.Fatalf("discard left %v", e)
	}
}

func TestStageBytesFlippedMidFileWithSizeAndClassUnchangedFailsAndLeavesNothing(t *testing.T) {
	e, st := newEngine(t)
	data := sample(1 << 20)
	src := &bytesSource{data: data, flipAt: len(data) / 2}
	s, err := e.Stage(context.Background(), src, expectFor(data, OriginRemote))
	if !errors.Is(err, ErrIntegrity) || s != nil {
		t.Fatalf("want ErrIntegrity and no staged file, got %v, %v", s, err)
	}
	var ie *IntegrityError
	if !errors.As(err, &ie) || ie.Origin != OriginRemote || ie.Medium != "m" {
		t.Fatalf("the failure must say which kind of copy was corrupt: %v", err)
	}
	if entries, _ := st.Entries(); len(entries) != 0 {
		t.Fatalf("a corrupt copy left staging entries: %v", entries)
	}
}

func TestStageLocalAndRemoteCorruptionAreDistinguishable(t *testing.T) {
	data := sample(4096)
	e, _ := newEngine(t)
	var kinds []Origin
	for _, o := range []Origin{OriginLocal, OriginRemote} {
		_, err := e.Stage(context.Background(), &bytesSource{data: data, flipAt: 10}, expectFor(data, o))
		var ie *IntegrityError
		if !errors.As(err, &ie) {
			t.Fatalf("%s: %v", o, err)
		}
		kinds = append(kinds, ie.Origin)
	}
	if kinds[0] == kinds[1] {
		t.Fatalf("local and remote failures read the same: %v", kinds)
	}
}

func TestStageShortAndLongCopiesFail(t *testing.T) {
	e, st := newEngine(t)
	data := sample(10000)
	exp := expectFor(data, OriginLocal)

	// The kind matters, not just the failure: a short copy would also fail
	// the digest, so only the kind shows the length itself was checked.
	for name, src := range map[string]*bytesSource{
		"short copy": goodSource(data[:9000]),
		"long copy":  goodSource(append(append([]byte(nil), data...), 1)),
	} {
		_, err := e.Stage(context.Background(), src, exp)
		var ie *IntegrityError
		if !errors.As(err, &ie) || ie.Kind != KindSize {
			t.Fatalf("%s: want a size mismatch, got %v", name, err)
		}
	}
	if entries, _ := st.Entries(); len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}

func TestStageWithoutARecordedSha256DigestRefusesRatherThanTrusting(t *testing.T) {
	e, _ := newEngine(t)
	data := sample(100)
	for _, exp := range []Expect{
		{HashAlg: "", Hash: "", Origin: OriginLocal},
		{HashAlg: "md5", Hash: "abc", Origin: OriginLocal},
		{HashAlg: "sha256", Hash: "", Origin: OriginLocal},
	} {
		src := goodSource(data)
		if _, err := e.Stage(context.Background(), src, exp); !errors.Is(err, ErrNoDigest) {
			t.Fatalf("%+v: want ErrNoDigest, got %v", exp, err)
		}
		if src.opens.Load() != 0 {
			t.Fatal("the copy must not even be opened when nothing can verify it")
		}
	}
}

func TestStageDigestComparisonIgnoresCase(t *testing.T) {
	e, _ := newEngine(t)
	data := sample(100)
	exp := expectFor(data, OriginLocal)
	exp.Hash = strings.ToUpper(exp.Hash)
	if _, err := e.Stage(context.Background(), goodSource(data), exp); err != nil {
		t.Fatal(err)
	}
}

type cancelAtHalf struct {
	data   []byte
	cancel context.CancelFunc
}

func (c *cancelAtHalf) Open(context.Context) (io.ReadCloser, error) {
	return &halfReader{r: bytes.NewReader(c.data), half: len(c.data) / 2, cancel: c.cancel}, nil
}

type halfReader struct {
	r      *bytes.Reader
	half   int
	read   int
	cancel context.CancelFunc
}

func (h *halfReader) Read(p []byte) (int, error) {
	if len(p) > 4096 {
		p = p[:4096]
	}
	n, err := h.r.Read(p)
	h.read += n
	if h.read >= h.half {
		h.cancel()
	}
	return n, err
}
func (h *halfReader) Close() error { return nil }

func TestStageCancelledHalfwayLeavesNoStagingEntry(t *testing.T) {
	e, st := newEngine(t)
	data := sample(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := e.Stage(ctx, &cancelAtHalf{data: data, cancel: cancel}, expectFor(data, OriginRemote))
	if !errors.Is(err, context.Canceled) || s != nil {
		t.Fatalf("want context.Canceled, got %v, %v", s, err)
	}
	if entries, _ := st.Entries(); len(entries) != 0 {
		t.Fatalf("a cancelled recovery left %v", entries)
	}
}

type failingFile struct {
	File
	budget int
}

func (f *failingFile) Write(p []byte) (int, error) {
	if f.budget <= 0 {
		return 0, &os.PathError{Op: "write", Path: f.Name(), Err: syscall.ENOSPC}
	}
	if len(p) > f.budget {
		n, _ := f.File.Write(p[:f.budget])
		f.budget = 0
		return n, &os.PathError{Op: "write", Path: f.Name(), Err: syscall.ENOSPC}
	}
	f.budget -= len(p)
	return f.File.Write(p)
}

func TestStageDiskFullRefusesAndLeavesNothingThatLooksComplete(t *testing.T) {
	e, st := newEngine(t)
	st.OpenFile = func(path string) (File, error) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		return &failingFile{File: f, budget: 100000}, nil
	}
	data := sample(1 << 20)
	s, err := e.Stage(context.Background(), goodSource(data), expectFor(data, OriginLocal))
	if !errors.Is(err, ErrNoSpace) || s != nil {
		t.Fatalf("want ErrNoSpace, got %v, %v", s, err)
	}
	if entries, _ := st.Entries(); len(entries) != 0 {
		t.Fatalf("disk-full left %v behind", entries)
	}
	all, _ := os.ReadDir(st.Root)
	if len(all) != 0 {
		t.Fatalf("root not empty: %v", all)
	}
}

func TestStageBelowReserveRefusesBeforeOpeningTheCopy(t *testing.T) {
	e, st := newEngine(t)
	st.Reserve = 1 << 30
	st.FreeBytes = func(string) (int64, error) { return 1 << 20, nil }
	data := sample(4096)
	src := goodSource(data)
	if _, err := e.Stage(context.Background(), src, expectFor(data, OriginRemote)); !errors.Is(err, ErrBelowReserve) {
		t.Fatalf("want ErrBelowReserve, got %v", err)
	}
	if src.opens.Load() != 0 {
		t.Fatal("a refused recovery must not have read (and been billed for) the copy")
	}
}

func TestStageCleanupFailureIsDistinctFromTheRecoveryFailure(t *testing.T) {
	e, st := newEngine(t)
	st.Remove = func(string) error { return errors.New("read-only filesystem") }
	data := sample(4096)
	_, err := e.Stage(context.Background(), &bytesSource{data: data, flipAt: 5}, expectFor(data, OriginLocal))
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("the recovery failure must still be reported: %v", err)
	}
	var ce *CleanupError
	if !errors.As(err, &ce) || len(ce.Failures) != 1 {
		t.Fatalf("the cleanup failure must be separately visible: %v", err)
	}
}

type countingReader struct {
	remaining int64
	seed      byte
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	for i := range p {
		p[i] = c.seed + byte(i)
	}
	c.remaining -= int64(len(p))
	return len(p), nil
}

type generatedSource struct{ size int64 }

func (g generatedSource) Open(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(&countingReader{remaining: g.size, seed: 3}), nil
}

func TestStageMemoryStaysBoundedWhateverTheSize(t *testing.T) {
	const size = 192 << 20
	h := sha256.New()
	io.Copy(h, &countingReader{remaining: size, seed: 3})
	n := int64(size)
	exp := Expect{Size: &n, HashAlg: "sha256", Hash: hex.EncodeToString(h.Sum(nil)), Origin: OriginLocal, Medium: "local"}

	e, st := newEngine(t)
	var before, peak runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	done := make(chan struct{})
	stop := make(chan struct{})
	var maxHeap atomic.Uint64
	go func() {
		defer close(done)
		var ms runtime.MemStats
		for {
			select {
			case <-stop:
				return
			default:
			}
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > maxHeap.Load() {
				maxHeap.Store(ms.HeapAlloc)
			}
			runtime.Gosched()
		}
	}()
	s, err := e.Stage(context.Background(), generatedSource{size: size}, exp)
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	defer s.Discard()
	runtime.ReadMemStats(&peak)
	growth := int64(maxHeap.Load()) - int64(before.HeapAlloc)
	if growth > 24<<20 {
		t.Fatalf("streaming %d MiB grew the heap by %d MiB: that is buffering, not streaming", size>>20, growth>>20)
	}
	_ = st
}

func TestRecoveryPackageSpawnsNoSubprocess(t *testing.T) {
	matches, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, m := range matches {
		if strings.HasSuffix(m, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, m, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			switch strings.Trim(imp.Path.Value, `"`) {
			case "os/exec", "plugin":
				t.Errorf("%s imports %s; recovery executes no program", m, imp.Path.Value)
			}
		}
		src, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"os.StartProcess", "syscall.Exec", "syscall.ForkExec", "unix.Exec", "exec.Command"} {
			if bytes.Contains(src, []byte(banned)) {
				t.Errorf("%s mentions %s; recovery executes no program", m, banned)
			}
		}
	}
}

func TestStageOverTheReadBudgetIsRefusedBeforeTheCopyIsOpened(t *testing.T) {
	e, st := newEngine(t)
	e.Reads = budget.NewReads(budget.Limits{MaxConcurrentReads: 1, MaxReadsPerDestination: 1})
	hold, err := e.Reads.Acquire("m")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()

	data := sample(4096)
	src := goodSource(data)
	_, err = e.Stage(context.Background(), src, expectFor(data, OriginRemote)) // Medium "m"
	var ref *budget.Refusal
	if !errors.As(err, &ref) || ref.Budget != budget.DestinationReads || !errors.Is(err, budget.ErrOverBudget) {
		t.Fatalf("want a per-destination refusal, got %v", err)
	}
	if src.opens.Load() != 0 {
		t.Fatal("the copy was opened (and, if remote, billed) although the budget refused the read")
	}
	if entries, _ := st.Entries(); len(entries) != 0 {
		t.Fatalf("a refusal left %v", entries)
	}
}

func TestAStageGivesBackItsReadSlotWhateverHappens(t *testing.T) {
	e, _ := newEngine(t)
	e.Reads = budget.NewReads(budget.Limits{MaxConcurrentReads: 1, MaxReadsPerDestination: 1})
	data := sample(4096)

	if s, err := e.Stage(context.Background(), goodSource(data), expectFor(data, OriginRemote)); err != nil {
		t.Fatal(err)
	} else {
		_ = s.Discard()
	}
	if _, err := e.Stage(context.Background(), &bytesSource{data: data, flipAt: 9}, expectFor(data, OriginRemote)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Stage(ctx, goodSource(data), expectFor(data, OriginRemote)); err == nil {
		t.Fatal("a cancelled stage succeeded")
	}
	rel, err := e.Reads.Acquire("m")
	if err != nil {
		t.Fatalf("after a success, a failure and a cancellation the slot is still held: %v", err)
	}
	rel()
}

func TestOneDestinationBeingFullDoesNotRefuseAnother(t *testing.T) {
	e, _ := newEngine(t)
	e.Reads = budget.NewReads(budget.Limits{MaxConcurrentReads: 4, MaxReadsPerDestination: 1})
	hold, _ := e.Reads.Acquire("m")
	defer hold()
	data := sample(4096)
	exp := expectFor(data, OriginRemote)
	exp.Medium = "elsewhere"
	s, err := e.Stage(context.Background(), goodSource(data), exp)
	if err != nil {
		t.Fatalf("a read of a different destination was refused: %v", err)
	}
	_ = s.Discard()
}
