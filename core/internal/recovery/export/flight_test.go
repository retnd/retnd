package export

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gatedSource opens only when told to, and counts how many times it was
// opened: the count is the number of full reads, which is what two
// operators asking for the same thing must not double.
type gatedSource struct {
	data    []byte
	opened  atomic.Int32
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGated(data []byte) *gatedSource {
	return &gatedSource{data: data, started: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedSource) Open(ctx context.Context) (io.ReadCloser, error) {
	g.opened.Add(1)
	g.once.Do(func() { close(g.started) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return io.NopCloser(bytes.NewReader(g.data)), nil
}

func flightEngine(t *testing.T) (*Engine, *Staging, *Flight) {
	e, st := newEngine(t)
	return e, st, &Flight{}
}

func TestTwoOperatorsAskingForTheSameCopyShareOneReadAndEachGetTheirOwnHandle(t *testing.T) {
	e, st, f := flightEngine(t)
	data := sample(200_000)
	src := newGated(data)
	exp := expectFor(data, OriginRemote)
	run := func(ctx context.Context) (*Staged, error) { return e.Stage(ctx, src, exp) }

	type result struct {
		s   *Staged
		err error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	go func() { s, err := f.Do(context.Background(), "art\x00cold", run); first <- result{s, err} }()
	<-src.started
	go func() { s, err := f.Do(context.Background(), "art\x00cold", run); second <- result{s, err} }()
	time.Sleep(100 * time.Millisecond) // let the second join the first
	close(src.release)

	a, b := <-first, <-second
	if a.err != nil || b.err != nil {
		t.Fatal(a.err, b.err)
	}
	if src.opened.Load() != 1 {
		t.Fatalf("the copy was read %d times for two operators: that is a second bill for the same bytes", src.opened.Load())
	}
	if a.s != b.s {
		t.Fatal("the two operators did not share the one staged copy")
	}

	// Each one's handle is its own: one finishing does not take the file
	// from the other, and the last one out removes it.
	if err := a.s.Discard(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.s.Path); err != nil {
		t.Fatalf("the first operator finishing removed the copy the second is still using: %v", err)
	}
	if err := b.s.Discard(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := st.Entries(); len(entries) != 0 {
		t.Fatalf("the last handle did not remove the staged copy: %v", entries)
	}
}

func TestACallThatStartsAfterTheFirstHasFinishedReadsAgain(t *testing.T) {
	e, _, f := flightEngine(t)
	data := sample(5000)
	var reads atomic.Int32
	run := func(ctx context.Context) (*Staged, error) {
		reads.Add(1)
		return e.Stage(ctx, goodSource(data), expectFor(data, OriginLocal))
	}
	for i := 0; i < 2; i++ {
		s, err := f.Do(context.Background(), "k", run)
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Discard()
	}
	if reads.Load() != 2 {
		t.Fatalf("a later request reused a finished execution (%d reads): that would serve bytes that were verified before, from a copy that may have changed since", reads.Load())
	}
}

func TestAFailedSharedExecutionFailsEveryoneWhoJoinedItAndLeavesNothing(t *testing.T) {
	e, st, f := flightEngine(t)
	data := sample(100_000)
	src := &gatedFlip{gatedSource: newGated(data)}
	run := func(ctx context.Context) (*Staged, error) { return e.Stage(ctx, src, expectFor(data, OriginRemote)) }

	errs := make(chan error, 2)
	go func() { _, err := f.Do(context.Background(), "k", run); errs <- err }()
	<-src.started
	go func() { _, err := f.Do(context.Background(), "k", run); errs <- err }()
	time.Sleep(100 * time.Millisecond)
	close(src.release)
	for i := 0; i < 2; i++ {
		if err := <-errs; !errors.Is(err, ErrIntegrity) {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if entries, _ := st.Entries(); len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}

type gatedFlip struct{ *gatedSource }

func (g *gatedFlip) Open(ctx context.Context) (io.ReadCloser, error) {
	rc, err := g.gatedSource.Open(ctx)
	if err != nil {
		return nil, err
	}
	b, _ := io.ReadAll(rc)
	b[len(b)/2] ^= 1
	return io.NopCloser(bytes.NewReader(b)), nil
}

func TestAWaiterThatGivesUpLeaksNothingAndTheOwnerIsUnaffected(t *testing.T) {
	e, st, f := flightEngine(t)
	data := sample(50_000)
	src := newGated(data)
	run := func(ctx context.Context) (*Staged, error) { return e.Stage(ctx, src, expectFor(data, OriginRemote)) }

	ownerDone := make(chan *Staged, 1)
	go func() {
		s, err := f.Do(context.Background(), "k", run)
		if err != nil {
			t.Error(err)
		}
		ownerDone <- s
	}()
	<-src.started

	ctx, cancel := context.WithCancel(context.Background())
	waiterErr := make(chan error, 1)
	go func() { _, err := f.Do(ctx, "k", run); waiterErr <- err }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-waiterErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("a waiter whose caller went away should return at once with its own cancellation, got %v", err)
	}
	close(src.release)
	s := <-ownerDone
	_ = s.Discard()
	// The abandoned waiter's reference is returned in the background, so
	// poll rather than race it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if e, _ := st.Entries(); len(e) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e2, _ := st.Entries()
	t.Fatalf("an abandoned waiter's reference kept the staged copy alive: %v", e2)
}

func TestWhenTheOwnerIsCancelledALiveWaiterRunsItItself(t *testing.T) {
	e, _, f := flightEngine(t)
	data := sample(50_000)
	var reads atomic.Int32
	started := make(chan struct{}, 2)
	run := func(ctx context.Context) (*Staged, error) {
		reads.Add(1)
		started <- struct{}{}
		if reads.Load() == 1 {
			<-ctx.Done() // the first execution is the one that gets cancelled
			return nil, ctx.Err()
		}
		return e.Stage(ctx, goodSource(data), expectFor(data, OriginLocal))
	}
	ownerCtx, cancel := context.WithCancel(context.Background())
	ownerErr := make(chan error, 1)
	go func() { _, err := f.Do(ownerCtx, "k", run); ownerErr <- err }()
	<-started

	waiter := make(chan error, 1)
	var got *Staged
	go func() {
		s, err := f.Do(context.Background(), "k", run)
		got = s
		waiter <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-ownerErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("owner: %v", err)
	}
	if err := <-waiter; err != nil {
		t.Fatalf("the waiter inherited somebody else's cancellation instead of running its own: %v", err)
	}
	_ = got.Discard()
	if reads.Load() != 2 {
		t.Fatalf("reads = %d", reads.Load())
	}
}

func TestPublishingASharedCopyGivesEachOperatorTheirOwnFile(t *testing.T) {
	e, st, f := flightEngine(t)
	data := sample(30_000)
	src := newGated(data)
	run := func(ctx context.Context) (*Staged, error) { return e.Stage(ctx, src, expectFor(data, OriginLocal)) }
	type res struct {
		s   *Staged
		err error
	}
	ch := make(chan res, 2)
	go func() { s, err := f.Do(context.Background(), "k", run); ch <- res{s, err} }()
	<-src.started
	go func() { s, err := f.Do(context.Background(), "k", run); ch <- res{s, err} }()
	time.Sleep(100 * time.Millisecond)
	close(src.release)
	a, b := <-ch, <-ch
	if a.err != nil || b.err != nil {
		t.Fatal(a.err, b.err)
	}
	pa, err := a.s.PublishTo("one/out.bin")
	if err != nil {
		t.Fatal(err)
	}
	pb, err := b.s.PublishTo("two/out.bin")
	if err != nil {
		t.Fatal(err)
	}
	if pa == pb {
		t.Fatal("two operators were given one file")
	}
	for _, p := range []string{pa, pb} {
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%s: %v", p, err)
		}
	}
	// Only outputs remain; no partial.
	entries, _ := st.Entries()
	for _, e := range entries {
		if filepath.Ext(e) == ".partial" {
			t.Fatalf("a partial survived both publishes: %v", entries)
		}
	}
}

func TestPublishingASoleCopyMovesItRatherThanCopyingIt(t *testing.T) {
	e, st := newEngine(t)
	data := sample(10_000)
	s, err := e.Stage(context.Background(), goodSource(data), expectFor(data, OriginLocal))
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.PublishTo("out.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatal("the sole owner's partial is still there after publishing")
	}
	if got, _ := os.ReadFile(p); !bytes.Equal(got, data) {
		t.Fatal("content wrong")
	}
	entries, _ := st.Entries()
	if len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
}
