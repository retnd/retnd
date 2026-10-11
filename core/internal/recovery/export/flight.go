package export

import (
	"context"
	"errors"
	"sync"
)

// Flight makes concurrent requests for the same thing one execution.
//
// Two operators asking for the same artifact from the same copy at the same
// time is not two jobs, it is one read of a copy that may cost money to
// read. So the first request does the work and the rest wait for it and are
// each handed their own handle on the result (Staged.Discard gives a handle
// up, and the file goes with the last one), their own audit row and their
// own target. A request that arrives after the first has finished starts a
// new execution: nothing is cached, because a copy verified a minute ago
// says nothing about the copy now.
//
// The recovery-test runner keys on the same thing for the same reason.
type Flight struct {
	mu    sync.Mutex
	calls map[string]*call
}

type call struct {
	done    chan struct{}
	waiters int
	staged  *Staged
	err     error
}

// Do runs fn for key, or joins the run already in flight for it.
//
// If the execution fails because its OWNER was cancelled while a waiter's own
// context is still live, the waiter does not inherit somebody else's
// cancellation: it runs the work itself.
func (f *Flight) Do(ctx context.Context, key string, fn func(context.Context) (*Staged, error)) (*Staged, error) {
	for {
		s, err, ownerCancelled := f.do(ctx, key, fn)
		if err != nil && ownerCancelled && ctx.Err() == nil {
			continue
		}
		return s, err
	}
}

func (f *Flight) do(ctx context.Context, key string, fn func(context.Context) (*Staged, error)) (s *Staged, err error, ownerCancelled bool) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]*call{}
	}
	if c, ok := f.calls[key]; ok {
		c.waiters++
		f.mu.Unlock()
		select {
		case <-c.done:
		case <-ctx.Done():
			// This caller has gone. Its handle was counted when it joined,
			// so give it back once the execution ends, or the file would
			// outlive every real holder.
			go func() {
				<-c.done
				if c.staged != nil {
					_ = c.staged.Discard()
				}
			}()
			return nil, ctx.Err(), false
		}
		if c.err != nil {
			return nil, c.err, errors.Is(c.err, context.Canceled)
		}
		return c.staged, nil, false
	}
	c := &call{done: make(chan struct{})}
	f.calls[key] = c
	f.mu.Unlock()

	staged, runErr := fn(ctx)

	f.mu.Lock()
	delete(f.calls, key)
	if runErr == nil && staged != nil {
		// Every waiter was counted before this point, under the lock, so
		// their handles exist before anyone can finish with the owner's.
		staged.addRefs(c.waiters)
	}
	c.staged, c.err = staged, runErr
	close(c.done)
	f.mu.Unlock()
	return staged, runErr, false
}
