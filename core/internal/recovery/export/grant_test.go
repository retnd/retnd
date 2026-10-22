package export

import (
	"errors"
	"reflect"
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
	if path := findInMemory(reflect.ValueOf(g), tok, map[uintptr]bool{}); path != "" {
		t.Fatalf("the store keeps the bearer token itself (found at %s); it should keep only a digest", path)
	}
}

// findInMemory walks v and returns where tok appears as a string, a byte
// slice or a byte array, or "". reflect can read unexported fields through
// Kind-based accessors, which is all this needs, so the check does not
// depend on how the store happens to name or shape its table.
func findInMemory(v reflect.Value, tok string, seen map[uintptr]bool) string {
	switch v.Kind() {
	case reflect.String:
		if strings.Contains(v.String(), tok) {
			return "a string"
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 && strings.Contains(string(v.Bytes()), tok) {
			return "a byte slice"
		}
		for i := 0; i < v.Len(); i++ {
			if p := findInMemory(v.Index(i), tok, seen); p != "" {
				return p
			}
		}
	case reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			b := make([]byte, v.Len())
			for i := range b {
				b[i] = byte(v.Index(i).Uint())
			}
			if strings.Contains(string(b), tok) {
				return "a byte array"
			}
			return ""
		}
		for i := 0; i < v.Len(); i++ {
			if p := findInMemory(v.Index(i), tok, seen); p != "" {
				return p
			}
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			if p := findInMemory(k, tok, seen); p != "" {
				return "a map key: " + p
			}
			if p := findInMemory(v.MapIndex(k), tok, seen); p != "" {
				return p
			}
		}
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return ""
		}
		if v.Kind() == reflect.Pointer {
			if seen[v.Pointer()] {
				return ""
			}
			seen[v.Pointer()] = true
		}
		return findInMemory(v.Elem(), tok, seen)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if p := findInMemory(v.Field(i), tok, seen); p != "" {
				return p
			}
		}
	}
	return ""
}

func TestFindInMemoryActuallyFindsATokenWhereverItIsKept(t *testing.T) {
	// The positive control for the check above: a walker that finds
	// nothing would pass whatever the store did.
	type holder struct {
		m map[string]int
		s []string
		b []byte
	}
	const tok = "secrettoken-abcdefghijklmnopqrstuvwxyz"
	for name, h := range map[string]*holder{
		"map key":    {m: map[string]int{tok: 1}},
		"slice":      {s: []string{"x", "y-" + tok}},
		"byte slice": {b: []byte("a" + tok)},
	} {
		if findInMemory(reflect.ValueOf(h), tok, map[uintptr]bool{}) == "" {
			t.Errorf("%s: the walker missed a token it was handed", name)
		}
	}
	if findInMemory(reflect.ValueOf(&holder{m: map[string]int{"other": 1}}), tok, map[uintptr]bool{}) != "" {
		t.Error("the walker found a token that is not there")
	}
}

func TestAnOpenRequestKeepsItsGrantAliveAcrossTheTTLAndReapSparesIt(t *testing.T) {
	c := newClock()
	g := NewGrants(GrantOptions{TTL: time.Minute, Now: c.Now, MaxOutstanding: 4})
	sess := sessionFor(c, "alice", 24*time.Hour)
	tok, _, _ := g.Mint(spec(sess))
	if _, err := g.Redeem(tok, sess); err != nil {
		t.Fatal(err)
	}
	g.Attach(tok, "staged")

	// One long download: ten minutes in a single request, far past the
	// grant's own lifetime.
	c.Advance(10 * time.Minute)
	if released := g.Reap(); len(released) != 0 {
		t.Fatalf("Reap released %v from under a request that is still streaming it", released)
	}
	if _, err := g.Redeem(tok, sess); err != nil {
		t.Fatalf("a client reconnecting while its first request is still open was refused: %v", err)
	}
	g.Done(tok)
	g.Done(tok)

	// Once nothing has the grant open, the idle clock runs from the moment
	// the last request ended, not from when the download began.
	c.Advance(30 * time.Second)
	if _, err := g.Redeem(tok, sess); err != nil {
		t.Fatalf("the idle clock should restart when a request ends: %v", err)
	}
	g.Done(tok)
	c.Advance(2 * time.Minute)
	if released := g.Reap(); len(released) != 1 || released[0] != "staged" {
		t.Fatalf("an abandoned download should be released, got %v", released)
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

func TestARevertedRedemptionLeavesTheGrantFirstUseAgain(t *testing.T) {
	c := newClock()
	g := newGrants(c)
	sess := sessionFor(c, "alice", time.Hour)
	tok, _, _ := g.Mint(spec(sess))
	if r, err := g.Redeem(tok, sess); err != nil || !r.First {
		t.Fatalf("%+v, %v", r, err)
	}
	g.Revert(tok)
	r, err := g.Redeem(tok, sess)
	if err != nil || !r.First {
		t.Fatalf("a refusal after the redeem (a budget full) spent the grant: %+v, %v", r, err)
	}
	// And a reverted grant that was attached to is not reverted: there is a
	// started download behind it.
	g.Attach(tok, "staged")
	g.Revert(tok)
	if r, _ := g.Redeem(tok, sess); r.First {
		t.Fatal("a grant with a started download behind it was made first-use again")
	}
}
