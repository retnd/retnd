package hostrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"
)

// The engine's half of the socket.
//
// It lives in this package rather than beside the engine for one reason:
// the two halves are one program cut by a socket, so a client written
// somewhere else is a second implementation of a protocol whose whole
// safety argument is that both ends agree about what may cross it. The
// version refusal in particular only means anything if the client cannot
// be an older copy of itself.
//
// Nothing here retries. A hook is not idempotent -- it quiesces a
// database, it rotates a file, it sends a notification -- so a client
// that transparently re-sent an execute after a timeout would run
// somebody's script twice and there is no way for it to know that was
// wrong. Every failure is returned to the caller, who is in a position to
// know.

// DialTimeout is how long a connect to the socket may take.
//
// Generous for a local socket, and the generosity is the point: a NAS
// under a full backup is a machine where a connect can take a second, and
// a runner that reported itself unreachable there would fail a backup for
// load rather than for a fault.
const DialTimeout = 10 * time.Second

// HandshakeTimeout is how long the hello and its welcome may take once the
// connect has succeeded.
//
// The connect succeeding proves very little about a runner: the kernel
// completes it from the listen backlog, so a runner that has been stopped
// (SIGSTOP, a host too loaded to schedule it, a wedged daemon under it)
// still accepts the connection and the hello into the socket buffer, and
// then never answers. Without a bound of its own the welcome read waited
// forever, and because the caller's context was only wired to the
// connection after the handshake, a step's script_timeout could not end
// that wait either (#931). The caller's context still wins when it is
// shorter; this is the floor for a caller that passed none.
const HandshakeTimeout = DialTimeout

// Client reaches one installation's runner.
type Client struct {
	// SocketPath is the runner's Unix socket. There is no address field
	// and no TCP mode; see Server.
	SocketPath string

	// Version is this engine's product version, which must equal the
	// runner's.
	Version string

	// Token is the installation-scoped credential, read from the
	// secrets area.
	Token string
}

// session is one authenticated connection carrying one operation.
type session struct {
	conn    net.Conn
	welcome Welcome
}

// endWith closes the connection when ctx ends and returns the function
// that stops watching. Every operation does this, not only Execute: a
// runner that answers the hello and then goes quiet must not be able to
// hold a status check or a cancel past its caller's deadline either.
func (s *session) endWith(ctx context.Context) (stop func()) {
	cancel := context.AfterFunc(ctx, func() { s.conn.Close() })

	return func() { cancel() }
}

func (c Client) open(ctx context.Context) (*session, error) {
	if c.SocketPath == "" {
		return nil, fmt.Errorf("%w: no workflow runner socket was configured", ErrServer)
	}
	var dialer net.Dialer
	dialCtx, cancel := context.WithTimeout(ctx, DialTimeout)
	defer cancel()

	conn, err := dialer.DialContext(dialCtx, "unix", c.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("hostrunner: the workflow runner at %s cannot be reached: %w. A deployment with local hook scripts needs it running; one without local hooks does not", c.SocketPath, err)
	}

	// The handshake is bounded twice over: by the caller's context, which
	// closes the connection so the blocked read returns, and by a
	// deadline of its own for a caller with no deadline. Both are lifted
	// once the welcome arrives, because from then on the operation
	// itself owns the connection's lifetime.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(HandshakeTimeout))

	fail := func(err error) (*session, error) {
		stop()
		conn.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("hostrunner: the workflow runner at %s did not complete its handshake before the caller's deadline (%w): %v", c.SocketPath, ctxErr, err)
		}
		return nil, err
	}

	if err := WriteMessage(conn, Message{
		Kind:  KindHello,
		Hello: &Hello{Protocol: Protocol, Version: c.Version, Token: c.Token},
	}); err != nil {
		return fail(err)
	}

	msg, err := ReadMessage(conn)
	if err != nil {
		return fail(err)
	}
	if !stop() {
		// The context ended while the welcome was arriving and the
		// connection is already closed under us.
		return fail(ctx.Err())
	}
	_ = conn.SetDeadline(time.Time{})
	switch {
	case msg.Kind == KindFailure && msg.Failure != nil:
		conn.Close()
		return nil, msg.Failure
	case msg.Kind == KindWelcome && msg.Welcome != nil:
		return &session{conn: conn, welcome: *msg.Welcome}, nil
	default:
		conn.Close()
		return nil, fmt.Errorf("%w: the runner answered a hello with %q", ErrProtocol, msg.Kind)
	}
}

// Status asks the runner what it is: the preflight answer.
//
// This is what makes #809's "fail `.local.sh` validation BEFORE the
// backup starts" possible. An engine calls it while validating a plan; a
// runner that is absent, unauthenticated, version-mismatched or without
// bash answers with a refusal here, at a point where nothing has been
// quiesced and no backup has begun.
func (c Client) Status(ctx context.Context) (Status, error) {
	s, err := c.open(ctx)
	if err != nil {
		return Status{}, err
	}
	defer s.conn.Close()
	defer s.endWith(ctx)()

	if err := WriteMessage(s.conn, Message{Kind: KindRequest, Request: &Request{Op: OpStatus}}); err != nil {
		return Status{}, err
	}
	result, err := readOutcome(s.conn, nil)
	if err != nil {
		return Status{}, err
	}
	if result.Status == nil {
		return Status{}, fmt.Errorf("%w: the runner answered a status request with no status in it", ErrProtocol)
	}
	return *result.Status, nil
}

// SyntaxCheck asks the runner to parse captured bytes and run nothing.
func (c Client) SyntaxCheck(ctx context.Context, runID, stepID string, script []byte) error {
	s, err := c.open(ctx)
	if err != nil {
		return err
	}
	defer s.conn.Close()
	defer s.endWith(ctx)()

	req := newScriptRequest(OpSyntaxCheck, runID, stepID, script)
	if err := WriteMessage(s.conn, Message{Kind: KindRequest, Request: &req}); err != nil {
		return err
	}
	_, err = readOutcome(s.conn, nil)
	return err
}

// ExecuteRequest is one step, as the engine states it.
//
// There is no path here either, and the type is what makes that visible
// at the call site: an engine holding a workflow.SpooledScript passes its
// Body, and has nothing else it could pass.
type ExecuteRequest struct {
	RunID   string
	StepID  string
	Script  []byte
	Env     EnvSet
	Timeout time.Duration
}

// Execute runs one step and delivers its output to sink as it arrives.
//
// ctx cancellation closes the connection, which the runner reads as the
// lease expiring, which terminates the hook's process group. That is the
// same mechanism an engine crash uses, exercised on purpose: a
// cancellation path that is only ever taken by a crash is a path nobody
// has tested.
func (c Client) Execute(ctx context.Context, req ExecuteRequest, sink Sink) (Result, error) {
	s, err := c.open(ctx)
	if err != nil {
		return Result{}, err
	}
	defer s.conn.Close()

	defer s.endWith(ctx)()

	wire := newScriptRequest(OpExecute, req.RunID, req.StepID, req.Script)
	wire.Env = req.Env
	wire.TimeoutMS = req.Timeout.Milliseconds()
	if err := WriteMessage(s.conn, Message{Kind: KindRequest, Request: &wire}); err != nil {
		return Result{}, err
	}
	return readOutcome(s.conn, sink)
}

// Cancel terminates a running step from a second connection.
func (c Client) Cancel(ctx context.Context, runID, stepID string) error {
	s, err := c.open(ctx)
	if err != nil {
		return err
	}
	defer s.conn.Close()
	defer s.endWith(ctx)()

	req := Request{Op: OpCancel, RunID: runID, StepID: stepID}
	if err := WriteMessage(s.conn, Message{Kind: KindRequest, Request: &req}); err != nil {
		return err
	}
	_, err = readOutcome(s.conn, nil)
	return err
}

// newScriptRequest fills in the size and hash the runner re-checks.
//
// Computed here rather than taken from the caller, deliberately: the
// engine's plan already holds a sha256 for these bytes, and passing that
// one along would make the wire check a comparison of a number against
// itself. Hashing the bytes that are actually about to be sent means the
// runner's check proves something about the transfer, and the engine's
// own check against the plan (Plan.OpenScript) proves the other half.
func newScriptRequest(op Op, runID, stepID string, script []byte) Request {
	sum := sha256.Sum256(script)
	return Request{
		Op:           op,
		RunID:        runID,
		StepID:       stepID,
		Script:       script,
		ScriptSize:   int64(len(script)),
		ScriptSHA256: hex.EncodeToString(sum[:]),
	}
}

// readOutcome consumes chunk frames until the operation ends.
func readOutcome(conn net.Conn, sink Sink) (Result, error) {
	for {
		msg, err := ReadMessage(conn)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return Result{}, fmt.Errorf("hostrunner: the connection to the workflow runner was closed before it reported an outcome: %w", err)
			}
			return Result{}, err
		}
		switch msg.Kind {
		case KindChunk:
			if msg.Chunk == nil || !msg.Chunk.Stream.Valid() {
				return Result{}, fmt.Errorf("%w: a chunk frame naming no stream", ErrProtocol)
			}
			if sink != nil {
				if err := sink.Chunk(*msg.Chunk); err != nil {
					return Result{}, err
				}
			}
		case KindResult:
			if msg.Result == nil {
				return Result{}, fmt.Errorf("%w: an empty result frame", ErrProtocol)
			}
			return *msg.Result, nil
		case KindFailure:
			if msg.Failure == nil {
				return Result{}, fmt.Errorf("%w: an empty failure frame", ErrProtocol)
			}
			return Result{}, msg.Failure
		default:
			return Result{}, fmt.Errorf("%w: the runner sent %q where an outcome belongs", ErrProtocol, msg.Kind)
		}
	}
}
