package transport

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Issue #1191: the source host's write slot is what keeps a recovery upload
// and a backup cycle from stacking connections on one machine.

func slotSource(host string, port int) Source {
	return Source{ID: "s", Type: "sftp", Host: host, Port: port, Root: "/r"}
}

func TestAcquireSourceHost_OneHostIsOneAtATime(t *testing.T) {
	release, err := AcquireSourceHost(context.Background(), slotSource("db1.example", 22))
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		// The same machine spelled with another case and the default port
		// left out is still the same machine.
		r2, err := AcquireSourceHost(context.Background(), slotSource("DB1.example", 0))
		if err == nil {
			r2()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("a second holder got the slot of a host that was already held")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting holder never got the slot after it was given back")
	}
}

func TestAcquireSourceHost_DifferentHostsDoNotWait(t *testing.T) {
	r1, err := AcquireSourceHost(context.Background(), slotSource("a.example", 22))
	if err != nil {
		t.Fatal(err)
	}
	defer r1()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, src := range []Source{slotSource("b.example", 22), slotSource("a.example", 2222)} {
		r, err := AcquireSourceHost(ctx, src)
		if err != nil {
			t.Fatalf("%s:%d waited behind another host: %v", src.Host, src.Port, err)
		}
		r()
	}
}

func TestAcquireSourceHost_ACancelledWaitReturnsTheContextsError(t *testing.T) {
	r1, err := AcquireSourceHost(context.Background(), slotSource("c.example", 22))
	if err != nil {
		t.Fatal(err)
	}
	defer r1()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r, err := AcquireSourceHost(ctx, slotSource("c.example", 22))
	if !errors.Is(err, context.DeadlineExceeded) || r != nil {
		t.Fatalf("a wait that ran out = (%v, release %v), want the context's error and no release", err, r != nil)
	}
}

func TestAcquireSourceHost_ALocalSourceIsNeverBlocked(t *testing.T) {
	src := Source{ID: "l", Type: "local", Root: "/srv"}
	r1, err := AcquireSourceHost(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	defer r1()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r2, err := AcquireSourceHost(ctx, src)
	if err != nil {
		t.Fatalf("a local source waited for a slot: %v", err)
	}
	r2()
}

func TestAcquireSourceHost_ReleasingTwiceGivesBackOneSlot(t *testing.T) {
	src := slotSource("d.example", 22)
	r1, err := AcquireSourceHost(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	r1()
	r1() // must not free a slot somebody else now holds
	r2, err := AcquireSourceHost(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	defer r2()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if r3, err := AcquireSourceHost(ctx, src); err == nil {
		r3()
		t.Fatal("a double release let two holders in at once")
	}
}
