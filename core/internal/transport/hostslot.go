package transport

import (
	"context"
	"strconv"
	"strings"
	"sync"
)

// The source host's write slot (issue #1191).
//
// Both production hosts this manager pulls from reset a third simultaneous
// connection from one address (see the rclone adapter's shutdownFs). A
// backup cycle's pass over a set already holds up to two (a listing pool and
// a transfer), so a recovery uploading onto the same host at the same time
// would be the third. The slot is one per host: a cycle's pass over an SFTP
// set and a source upload session both take it, so the two never overlap on
// one machine, and two recoveries onto one host queue behind each other.
//
// It is process-wide on purpose. The cycle (internal/app) and the recovery
// service (core/service) share one transport and nothing else that could
// carry a lock between them, and two processes on one host (the CLI next to
// a daemon) are already serialised by the journal's own lock for the cases
// that matter.

var hostSlots = struct {
	sync.Mutex
	m map[string]chan struct{}
}{m: map[string]chan struct{}{}}

// sourceHostKey is what makes two sources the same machine: the backend,
// host and port. A local source has no host to protect and gets no slot.
func sourceHostKey(src Source) (string, bool) {
	if src.Type != "sftp" || src.Host == "" {
		return "", false
	}
	port := src.Port
	if port == 0 {
		port = 22
	}
	return src.Type + "://" + strings.ToLower(src.Host) + ":" + strconv.Itoa(port), true
}

// AcquireSourceHost waits for src's host write slot, or for ctx to end. The
// release function is safe to call more than once. A source with no host
// (a local folder) is never blocked.
func AcquireSourceHost(ctx context.Context, src Source) (release func(), err error) {
	key, ok := sourceHostKey(src)
	if !ok {
		return func() {}, nil
	}
	hostSlots.Lock()
	ch := hostSlots.m[key]
	if ch == nil {
		ch = make(chan struct{}, 1)
		hostSlots.m[key] = ch
	}
	hostSlots.Unlock()
	select {
	case ch <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() { once.Do(func() { <-ch }) }, nil
}
