package export

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func sessionFor(c *fakeClock, actor string, lifetime time.Duration) Session {
	return Session{Actor: actor, Expires: c.Now().Add(lifetime)}
}

func newGrants(c *fakeClock) *Grants {
	return NewGrants(GrantOptions{TTL: time.Minute, Now: c.Now, MaxOutstanding: 4})
}

func spec(s Session) GrantSpec {
	return GrantSpec{Session: s, Artifact: "src/set/a.tar", Copy: "local", Target: Target{Kind: TargetDownload}}
}

func TestGrantRedeemsOnceThenIsUsed(t *testing.T) {
	c := newClock()
	g := newGrants(c)
	sess := sessionFor(c, "alice", time.Hour)
	tok, _, err := g.Mint(spec(sess))
	if err != nil {
		t.Fatal(err)
	}
	r, err := g.Redeem(tok, sess)
	if err != nil || !r.First {
		t.Fatalf("first redeem: %+v, %v", r, err)
	}
	if r.Grant.Artifact != "src/set/a.tar" || r.Grant.Actor != "alice" || r.Grant.Copy != "local" {
		t.Fatalf("grant lost its binding: %+v", r.Grant)
	}
	g.Close(tok)
	if _, err := g.Redeem(tok, sess); !errors.Is(err, ErrGrantUsed) {
		t.Fatalf("second use after close: want ErrGrantUsed, got %v", err)
	}
}

func TestGrantContinuationIsAllowedUntilClosedAndIsNotFirst(t *testing.T) {
	c := newClock()
	g := newGrants(c)
	sess := sessionFor(c, "alice", time.Hour)
	tok, _, _ := g.Mint(spec(sess))
	if _, err := g.Redeem(tok, sess); err != nil {
		t.Fatal(err)
	}
	r, err := g.Redeem(tok, sess)
	if err != nil || r.First {
		t.Fatalf("a range continuation is not a first use: %+v, %v", r, err)
	}
}

func TestGrantAttachmentTravelsWithTheGrantAndCloseReturnsIt(t *testing.T) {
	c := newClock()
	g := newGrants(c)
	sess := sessionFor(c, "alice", time.Hour)
	tok, _, _ := g.Mint(spec(sess))
	if _, err := g.Redeem(tok, sess); err != nil {
		t.Fatal(err)
	}
	g.Attach(tok, "staged-file")
	r, _ := g.Redeem(tok, sess)
	if r.Attached != "staged-file" {
		t.Fatalf("attachment = %v", r.Attached)
	}
	if got := g.Close(tok); got != "staged-file" {
		t.Fatalf("Close should hand the attachment back for cleanup, got %v", got)
	}
}

func TestGrantExpires(t *testing.T) {
	c := newClock()
	g := newGrants(c)
	sess := sessionFor(c, "alice", time.Hour)
	tok, _, _ := g.Mint(spec(sess))
	c.Advance(time.Minute + time.Second)
	if _, err := g.Redeem(tok, sess); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("want ErrGrantExpired, got %v", err)
	}
}

func TestGrantDiesWithItsSession(t *testing.T) {
	c := newClock()
	g := NewGrants(GrantOptions{TTL: time.Hour, Now: c.Now, MaxOutstanding: 4})
	sess := sessionFor(c, "alice", 30*time.Second)
	tok, _, _ := g.Mint(spec(sess))
	c.Advance(31 * time.Second)
	if _, err := g.Redeem(tok, sess); !errors.Is(err, ErrGrantSessionEnded) {
		t.Fatalf("grant outlived its session: %v", err)
	}
}

func TestGrantRefusesAnotherSessionAndAnotherActor(t *testing.T) {
	c := newClock()
	g := newGrants(c)
	mine := sessionFor(c, "alice", time.Hour)
	tok, _, _ := g.Mint(spec(mine))

	other := Session{Actor: "alice", Expires: mine.Expires.Add(time.Hour)}
	if _, err := g.Redeem(tok, other); !errors.Is(err, ErrGrantSessionEnded) {
		t.Fatalf("a different session of the same actor: %v", err)
	}
	bob := Session{Actor: "bob", Expires: mine.Expires}
	if _, err := g.Redeem(tok, bob); !errors.Is(err, ErrGrantSessionEnded) {
		t.Fatalf("a different actor: %v", err)
	}
}

func TestGrantEndSessionRevokesEveryGrantItMinted(t *testing.T) {
	c := newClock()
	g := newGrants(c)
	sess := sessionFor(c, "alice", time.Hour)
	t1, _, _ := g.Mint(spec(sess))
	t2, _, _ := g.Mint(spec(sess))
	g.EndSession(sess)
	for _, tok := range []string{t1, t2} {
		if _, err := g.Redeem(tok, sess); !errors.Is(err, ErrGrantSessionEnded) {
			t.Fatalf("want ErrGrantSessionEnded after logout, got %v", err)
		}
	}
}

func TestGrantUnknownTokenAndGarbage(t *testing.T) {
	c := newClock()
	g := newGrants(c)
	sess := sessionFor(c, "alice", time.Hour)
	for _, tok := range []string{"", "nope", strings.Repeat("a", 4096)} {
		if _, err := g.Redeem(tok, sess); !errors.Is(err, ErrGrantUnknown) {
			t.Fatalf("token %.8q: want ErrGrantUnknown, got %v", tok, err)
		}
	}
}

func TestGrantTokensAreUnguessableAndDistinct(t *testing.T) {
	c := newClock()
	g := NewGrants(GrantOptions{TTL: time.Minute, Now: c.Now, MaxOutstanding: 1000})
	sess := sessionFor(c, "alice", time.Hour)
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		tok, _, err := g.Mint(spec(sess))
		if err != nil {
			t.Fatal(err)
		}
		if len(tok) < 43 || seen[tok] {
			t.Fatalf("token %q is short or repeated", tok)
		}
		seen[tok] = true
	}
}

func TestGrantTokenIsNotKeptInTheClear(t *testing.T) {
	c := newClock()
	g := newGrants(c)
	sess := sessionFor(c, "alice", time.Hour)
	tok, _, _ := g.Mint(spec(sess))
	if g.holdsPlaintext(tok) {
		t.Fatal("the store keeps the bearer token itself; it should keep only a digest")
	}
}

func TestGrantOutstandingBoundRefusesRatherThanGrowing(t *testing.T) {
	c := newClock()
	g := newGrants(c)
	sess := sessionFor(c, "alice", time.Hour)
	for i := 0; i < 4; i++ {
		if _, _, err := g.Mint(spec(sess)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := g.Mint(spec(sess)); !errors.Is(err, ErrTooManyGrants) {
		t.Fatalf("want ErrTooManyGrants, got %v", err)
	}
	c.Advance(2 * time.Minute) // all four expire and are reclaimed
	if _, _, err := g.Mint(spec(sess)); err != nil {
		t.Fatalf("expired grants should free their slot: %v", err)
	}
}

func TestGrantMintRefusesAnEndedSession(t *testing.T) {
	c := newClock()
	g := newGrants(c)
	dead := Session{Actor: "alice", Expires: c.Now().Add(-time.Second)}
	if _, _, err := g.Mint(spec(dead)); !errors.Is(err, ErrGrantSessionEnded) {
		t.Fatalf("minted into a dead session: %v", err)
	}
}

func TestGrantNeverExpiresPastItsSession(t *testing.T) {
	c := newClock()
	g := NewGrants(GrantOptions{TTL: time.Hour, Now: c.Now, MaxOutstanding: 2})
	sess := sessionFor(c, "alice", 10*time.Second)
	_, grant, _ := g.Mint(spec(sess))
	if grant.ExpiresAt.After(sess.Expires) {
		t.Fatalf("grant outlives its session: %v > %v", grant.ExpiresAt, sess.Expires)
	}
}

func TestSessionWithNoExpiryEndsOnlyWhenEnded(t *testing.T) {
	c := newClock()
	g := newGrants(c)
	sess := Session{Actor: "proxyuser"}
	tok, _, err := g.Mint(spec(sess))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Redeem(tok, sess); err != nil {
		t.Fatal(err)
	}
}
