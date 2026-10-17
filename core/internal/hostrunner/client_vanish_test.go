package hostrunner

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A runner that stops answering is the failure these tests are about (#931).
//
// "Vanishes" covers two different things and the suite keeps them apart:
//
//   - a runner that is GONE (process dead, socket closed). The kernel tells
//     the client at once and nothing here was ever at risk.
//   - a runner that is STILL THERE and says nothing (SIGSTOP, a wedged
//     docker daemon under it, a host so loaded it does not schedule it).
//     The connection stays open, no EOF ever arrives, and the only thing
//     that can end the wait is the client's own deadline.
//
// The second kind is the one a set's script_timeout has to cover, and it
// has to cover every blocking read the client does, not only the one that
// happens while the step is "running": a hello that is never answered is a
// step that never got its bound applied.
//
// The stand-in below is a raw listener rather than a Server, because a
// real Server cannot be made to go quiet at a chosen point.

type quietMode int

const (
	// quietBeforeWelcome accepts the connection, reads the hello, and
	// never answers it. What a stopped runner looks like: the kernel
	// completes the connect from the listen backlog, the write succeeds
	// into the socket buffer, and nothing ever reads it.
	quietBeforeWelcome quietMode = iota

	// quietMidStep welcomes, reads the request, sends one chunk of
	// output, and then goes silent with the connection held open.
	quietMidStep

	// vanishMidStep does the same and then closes the connection, which
	// is a runner that died.
	vanishMidStep
)

type quietRunner struct {
	path string

	mu     sync.Mutex
	closed []time.Time // when the stand-in saw a connection end
	ended  chan struct{}
}

func startQuietRunner(t *testing.T, mode quietMode) *quietRunner {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "bdq")
	if err != nil {
		t.Fatalf("preparing a short temporary root: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	q := &quietRunner{path: filepath.Join(dir, "r.sock"), ended: make(chan struct{}, 8)}
	ln, err := net.Listen("unix", q.path)
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go q.serve(conn, mode)
		}
	}()

	return q
}

func (q *quietRunner) serve(conn net.Conn, mode quietMode) {
	defer conn.Close()
	defer func() { q.ended <- struct{}{} }()

	if _, err := ReadMessage(conn); err != nil { // the hello
		return
	}
	if mode == quietBeforeWelcome {
		q.drain(conn)
		return
	}

	_ = WriteMessage(conn, Message{Kind: KindWelcome, Welcome: &Welcome{Protocol: Protocol, Version: "test"}})
	if _, err := ReadMessage(conn); err != nil { // the request
		return
	}
	_ = WriteMessage(conn, Message{Kind: KindChunk, Chunk: &Chunk{Stream: StreamStdout, Seq: 1, Data: []byte("quiescing the database\n")}})
	if mode == vanishMidStep {
		return
	}
	q.drain(conn)
}

// drain holds the connection open until the client ends it, which is how
// the stand-in notices a lease expiring.
func (q *quietRunner) drain(conn net.Conn) {
	buf := make([]byte, 64)
	for {
		if _, err := conn.Read(buf); err != nil {
			return
		}
	}
}

func (q *quietRunner) client() Client {
	return Client{SocketPath: q.path, Version: "test", Token: testToken}
}

func (q *quietRunner) waitEnded(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-q.ended:
	case <-time.After(within):
		t.Fatalf("the connection to the runner was still open %s after the client gave up: the runner would never read the lease as expired and the hook's process group would run on", within)
	}
}

type outcome struct {
	elapsed time.Duration
	err     error
}

// within runs fn and fails the test if it has not returned by limit. The
// goroutine is left behind on failure; the stand-in's cleanup closes its
// socket.
func within(t *testing.T, limit time.Duration, fn func() error) outcome {
	t.Helper()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return outcome{elapsed: time.Since(start), err: err}
	case <-time.After(limit):
		t.Fatalf("still blocked after %s: the call ignores its context and the step's script_timeout never fires", limit)
		return outcome{}
	}
}

const (
	stepBound = 400 * time.Millisecond
	// slack is generous on purpose: MARS runs these under load.
	slack = 5 * time.Second
)

func TestExecute_ARunnerThatNeverAnswersTheHelloIsBoundedByTheStepContext(t *testing.T) {
	q := startQuietRunner(t, quietBeforeWelcome)

	ctx, cancel := context.WithTimeout(context.Background(), stepBound)
	defer cancel()

	got := within(t, slack, func() error {
		_, err := q.client().Execute(ctx, ExecuteRequest{RunID: "r", StepID: "s", Script: []byte("true"), Timeout: stepBound}, nil)
		return err
	})
	if got.err == nil {
		t.Fatal("Execute reported success against a runner that never answered")
	}
	q.waitEnded(t, slack)
}

func TestExecute_ARunnerThatGoesSilentMidStepIsBoundedAndKeepsWhatItSaid(t *testing.T) {
	q := startQuietRunner(t, quietMidStep)

	ctx, cancel := context.WithTimeout(context.Background(), stepBound)
	defer cancel()

	var sink collector
	got := within(t, slack, func() error {
		_, err := q.client().Execute(ctx, ExecuteRequest{RunID: "r", StepID: "s", Script: []byte("true"), Timeout: stepBound}, &sink)
		return err
	})
	if got.err == nil {
		t.Fatal("Execute reported success for a step whose runner went silent before any outcome")
	}
	if got.elapsed < stepBound/2 {
		t.Errorf("returned after %s, well inside the %s bound: it gave up on something other than the bound", got.elapsed, stepBound)
	}
	if len(sink.chunks) != 1 || string(sink.chunks[0].Data) != "quiescing the database\n" {
		t.Errorf("partial output before the runner went quiet was lost or altered: %+v", sink.chunks)
	}
	q.waitEnded(t, slack)
}

func TestExecute_ARunnerThatDiesMidStepReturnsAnErrorAtOnce(t *testing.T) {
	q := startQuietRunner(t, vanishMidStep)

	var sink collector
	got := within(t, slack, func() error {
		_, err := q.client().Execute(context.Background(), ExecuteRequest{RunID: "r", StepID: "s", Script: []byte("true"), Timeout: time.Minute}, &sink)
		return err
	})
	if got.err == nil {
		t.Fatal("a runner that closed without an outcome was reported as success")
	}
	if len(sink.chunks) != 1 {
		t.Errorf("partial output was lost: %+v", sink.chunks)
	}
}

func TestOtherOperations_AreBoundedByTheirContextAgainstAQuietRunner(t *testing.T) {
	ops := map[string]func(Client, context.Context) error{
		"Status":      func(c Client, ctx context.Context) error { _, err := c.Status(ctx); return err },
		"SyntaxCheck": func(c Client, ctx context.Context) error { return c.SyntaxCheck(ctx, "r", "s", []byte("true")) },
		"Cancel":      func(c Client, ctx context.Context) error { return c.Cancel(ctx, "r", "s") },
	}
	for name, op := range ops {
		for _, mode := range []quietMode{quietBeforeWelcome, quietMidStep} {
			t.Run(name, func(t *testing.T) {
				q := startQuietRunner(t, mode)
				ctx, cancel := context.WithTimeout(context.Background(), stepBound)
				defer cancel()

				got := within(t, slack, func() error { return op(q.client(), ctx) })
				if got.err == nil {
					t.Fatalf("%s reported success against a runner that never produced an outcome", name)
				}
			})
		}
	}
}

// TestStatus_AHelloNobodyAnswersIsBoundedEvenWithNoDeadlineFromTheCaller
// covers the caller that passes context.Background(). The handshake has no
// business waiting longer than a connect may, and a bound that exists only
// when every caller remembers to set one is not a bound.
func TestStatus_AHelloNobodyAnswersIsBoundedEvenWithNoDeadlineFromTheCaller(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the handshake bound")
	}
	q := startQuietRunner(t, quietBeforeWelcome)

	got := within(t, DialTimeout+slack, func() error {
		_, err := q.client().Status(context.Background())
		return err
	})
	if got.err == nil {
		t.Fatal("Status reported success against a runner that never answered the hello")
	}
}
