package export

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// TargetKind is where recovered bytes are allowed to go. There are three in
// v1 and the set is closed on purpose: a fourth is a new egress path and
// needs its own review.
type TargetKind string

const (
	// TargetDownload streams to the HTTP client that redeems the grant.
	TargetDownload TargetKind = "download"

	// TargetStaging places one file under the recovery staging root.
	TargetStaging TargetKind = "staging"

	// TargetStorage uploads to another configured storage destination.
	TargetStorage TargetKind = "storage"

	// TargetSource uploads back onto the backup set's own source, over the
	// set's own SFTP settings (issue #1191). Path is the file's path under
	// the set's remote path, always inside a new recovered folder.
	TargetSource TargetKind = "source"
)

// Target is the one place a recovery writes. A grant is minted for exactly
// one, and the redeemer cannot choose another.
type Target struct {
	Kind TargetKind

	// Path is the name under the staging root, for TargetStaging.
	Path string

	// Medium and Key address a TargetStorage destination.
	Medium string
	Key    string

	// Destination is, for TargetSource, the id of the destination the
	// operator acknowledged (issue #1193): the host, port, account, remote
	// path, folder and host-key file it resolved to at mint. The upload
	// works it out again from the configuration as it is then and refuses
	// a mismatch, so a reload between mint and write cannot redirect it.
	Destination string
}

// Session is the identity a grant is bound to. The platform's
// AuthContext carries no session id and no role (capabilities.go:85-90),
// so the binding is made out of the two things it does carry: who, and
// when that authentication lapses. A zero Expires means the platform
// reports no lapse (a trusted-proxy deployment), in which case the session
// is exactly as long as the proxy keeps vouching for the actor on each
// request.
type Session struct {
	Actor   string
	Expires time.Time
}

func (s Session) key() string {
	return s.Actor + "\x00" + s.Expires.UTC().Format(time.RFC3339Nano)
}

func (s Session) endedAt(now time.Time) bool {
	return !s.Expires.IsZero() && !now.Before(s.Expires)
}

// GrantSpec is what a mint is asked for. The copy is already resolved: a
// grant names a copy, not a selector, so what is redeemed is what was
// authorised.
type GrantSpec struct {
	Session  Session
	Artifact string
	Copy     string
	Target   Target

	// TTL, when positive and shorter than the store's, is this grant's own
	// lifetime. A source-host grant (issue #1193) lives 60 seconds, so it
	// cannot outlive the gate state it was minted under by much.
	TTL time.Duration
}

// Grant is the bound, short-lived permission to read one copy once.
type Grant struct {
	Actor     string
	Artifact  string
	Copy      string
	Target    Target
	ExpiresAt time.Time
}

// GrantOptions configures a Grants store.
type GrantOptions struct {
	// TTL is how long a grant lives. It never outlives its session.
	TTL time.Duration

	// Now is the clock; nil means time.Now.
	Now func() time.Time

	// MaxOutstanding bounds live grants, so an authenticated caller cannot
	// grow the table without limit. Zero means a conservative default.
	MaxOutstanding int
}

// DefaultGrantTTL is deliberately short: long enough to start a download,
// not long enough to be forwarded around.
const DefaultGrantTTL = 2 * time.Minute

const defaultMaxOutstanding = 64

// Redemption is a successful Redeem.
type Redemption struct {
	Grant Grant

	// First is true for the redemption that started the recovery and false
	// for a continuation (a further range of the same download).
	First bool

	// Attached is whatever the caller stored with Attach, typically the
	// staged file a download is served from.
	Attached any
}

var (
	ErrGrantUnknown      = errors.New("recovery: unknown grant")
	ErrGrantUsed         = errors.New("recovery: this grant has already been used")
	ErrGrantExpired      = errors.New("recovery: this grant has expired")
	ErrGrantSessionEnded = errors.New("recovery: the session this grant was minted in has ended")
	ErrTooManyGrants     = errors.New("recovery: too many recovery grants are outstanding")
)

type entry struct {
	grant    Grant
	session  Session
	redeemed bool
	revoked  bool

	// open counts requests Redeem admitted and Done has not yet ended. A
	// grant with a request open is being used: it does not lapse, and the
	// reaper leaves its attachment alone, however long the one request
	// takes. Without this a slow download of a large copy outlives its
	// two-minute grant and has the staged file taken from under it.
	open     int
	closedAt time.Time // non-zero once spent; the entry stays as a tombstone
	attached any

	// served is the byte ranges requests have delivered whole, kept sorted
	// and merged, so a download is complete when it is the single span
	// [0, size). Order and overlap do not matter: a client that asks for
	// the tail first, or runs several connections, still finishes.
	served []span
}

// span is the half-open byte range [from, to).
type span struct{ from, to int64 }

func (e *entry) closed() bool { return !e.closedAt.IsZero() }

// tombstoneLife is how long a spent token keeps answering "already used"
// rather than "unknown". After that it is unknown, which is also a refusal.
const tombstoneLife = 30 * time.Minute

// maxTombstones bounds the spent entries kept, oldest evicted first.
const maxTombstones = 4096

// Grants is the in-memory table of live grants.
//
// In memory because a grant must not survive the process: there is no
// resume across a restart, and a table on disk would be a list of live
// bearer credentials at rest. It keeps a SHA-256 of each token, never the
// token, so a heap dump of an idle process does not hand out downloads.
//
// A grant is single-use in this sense: the first redeem starts the
// recovery, further redeems are continuations of that same download (the
// ranges a browser or curl asks for) and are allowed only while it is
// open and the client keeps coming back inside the TTL, and once it is
// closed, by completion, by the client going away or by the session
// ending, the token is spent.
type Grants struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time
	max  int
	live map[[32]byte]*entry
}

// NewGrants returns an empty table.
func NewGrants(o GrantOptions) *Grants {
	if o.TTL <= 0 {
		o.TTL = DefaultGrantTTL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxOutstanding <= 0 {
		o.MaxOutstanding = defaultMaxOutstanding
	}
	return &Grants{ttl: o.TTL, now: o.Now, max: o.MaxOutstanding, live: map[[32]byte]*entry{}}
}

func digest(token string) [32]byte { return sha256.Sum256([]byte(token)) }

// Mint creates a grant bound to spec.Session and returns the bearer token
// once. The token is not recoverable afterwards.
func (g *Grants) Mint(spec GrantSpec) (string, Grant, error) {
	now := g.now()
	if spec.Session.endedAt(now) {
		return "", Grant{}, ErrGrantSessionEnded
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", Grant{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])

	grant := Grant{
		Actor:     spec.Session.Actor,
		Artifact:  spec.Artifact,
		Copy:      spec.Copy,
		Target:    spec.Target,
		ExpiresAt: g.expiryFor(spec.Session, now),
	}
	if spec.TTL > 0 && now.Add(spec.TTL).Before(grant.ExpiresAt) {
		grant.ExpiresAt = now.Add(spec.TTL)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.reclaimLocked(now)
	if g.outstandingLocked() >= g.max {
		return "", Grant{}, ErrTooManyGrants
	}
	g.live[digest(token)] = &entry{grant: grant, session: spec.Session}
	return token, grant, nil
}

// expiryFor is now plus the TTL, clipped to the end of the session.
func (g *Grants) expiryFor(s Session, now time.Time) time.Time {
	exp := now.Add(g.ttl)
	if !s.Expires.IsZero() && s.Expires.Before(exp) {
		exp = s.Expires
	}
	return exp
}

func (g *Grants) outstandingLocked() int {
	n := 0
	now := g.now()
	for _, e := range g.live {
		if !e.closed() && now.Before(e.grant.ExpiresAt) {
			n++
		}
	}
	return n
}

// reclaimLocked drops what can never matter again, so a slot frees itself
// without a background goroutine: lapsed grants nobody redeemed and
// tombstones past their life, with the oldest tombstones evicted past the
// bound.
func (g *Grants) reclaimLocked(now time.Time) {
	var tombs []([32]byte)
	for k, e := range g.live {
		switch {
		case e.closed() && now.Sub(e.closedAt) > tombstoneLife:
			delete(g.live, k)
		case e.closed():
			tombs = append(tombs, k)
		case !e.redeemed && now.Sub(e.grant.ExpiresAt) > tombstoneLife:
			// A lapsed grant keeps answering "expired" for a while, so the
			// refusal is the accurate one, then becomes unknown.
			delete(g.live, k)
		}
	}
	for len(tombs) > maxTombstones {
		oldest := 0
		for i, k := range tombs {
			if g.live[k].closedAt.Before(g.live[tombs[oldest]].closedAt) {
				oldest = i
			}
		}
		delete(g.live, tombs[oldest])
		tombs = append(tombs[:oldest], tombs[oldest+1:]...)
	}
}

// TargetKindOf says which kind of target a live grant writes to, without
// redeeming it, so a caller can decide whether redeeming it needs a gate
// (issue #1193: a source-host grant does). Unknown, spent or expired grants
// answer false; Redeem gives the reason.
func (g *Grants) TargetKindOf(token string) (TargetKind, bool) {
	if token == "" || len(token) > 256 {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.live[digest(token)]
	if !ok || e.closed() {
		return "", false
	}
	return e.grant.Target.Kind, true
}

// Redeem presents a token from the session now making the request.
func (g *Grants) Redeem(token string, caller Session) (Redemption, error) {
	if token == "" || len(token) > 256 {
		return Redemption{}, ErrGrantUnknown
	}
	k := digest(token)
	now := g.now()

	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.live[k]
	if !ok {
		return Redemption{}, ErrGrantUnknown
	}
	if e.closed() {
		return Redemption{}, ErrGrantUsed
	}
	if e.revoked || e.session.endedAt(now) || caller.endedAt(now) ||
		subtle.ConstantTimeCompare([]byte(e.session.key()), []byte(caller.key())) != 1 {
		return Redemption{}, ErrGrantSessionEnded
	}
	if e.open == 0 && !now.Before(e.grant.ExpiresAt) {
		return Redemption{}, ErrGrantExpired
	}
	first := !e.redeemed
	e.redeemed = true
	e.open++
	// A started download is kept alive by its own traffic: each range has
	// the TTL again, but never past the end of the session.
	e.grant.ExpiresAt = g.expiryFor(e.session, now)
	return Redemption{Grant: e.grant, First: first, Attached: e.attached}, nil
}

// Done ends a request that Redeem admitted. The idle clock restarts from
// here, so a client coming back for the next range has the full lifetime
// again no matter how long the request that just ended took.
func (g *Grants) Done(token string) {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.live[digest(token)]
	if !ok || e.closed() {
		return
	}
	if e.open > 0 {
		e.open--
	}
	e.grant.ExpiresAt = g.expiryFor(e.session, now)
}

// Revert undoes a Redeem that turned out not to be usable yet, so the refusal
// that followed it (a budget full, say) does not spend the grant: the holder
// can come back for it. Only meaningful for the first redemption, before
// anything has been attached.
func (g *Grants) Revert(token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.live[digest(token)]
	if !ok || e.closed() {
		return
	}
	if e.open > 0 {
		e.open--
	}
	if e.open == 0 && e.attached == nil {
		e.redeemed = false
	}
}

// Served records that the inclusive byte range [start, end] of a copy of
// size bytes was delivered in full, and reports whether the ranges delivered
// so far cover the whole copy. Only then is a download finished: a tail
// range, or any other part, leaves the grant open for the rest.
//
// A zero-length copy is complete as soon as it is served at all.
func (g *Grants) Served(token string, start, end, size int64) (complete bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.live[digest(token)]
	if !ok || e.closed() {
		return false
	}
	if size <= 0 {
		return true
	}
	if start < 0 {
		start = 0
	}
	if end >= size {
		end = size - 1
	}
	if end < start {
		return false
	}
	e.served = mergeSpan(e.served, span{start, end + 1})
	return len(e.served) == 1 && e.served[0].from == 0 && e.served[0].to >= size
}

// mergeSpan inserts s into the sorted, disjoint list, joining anything it
// overlaps or touches.
func mergeSpan(list []span, s span) []span {
	out := make([]span, 0, len(list)+1)
	placed := false
	for _, x := range list {
		switch {
		case x.to < s.from:
			out = append(out, x)
		case s.to < x.from:
			if !placed {
				out = append(out, s)
				placed = true
			}
			out = append(out, x)
		default:
			if x.from < s.from {
				s.from = x.from
			}
			if x.to > s.to {
				s.to = x.to
			}
		}
	}
	if !placed {
		out = append(out, s)
	}
	return out
}

// Active is the number of started downloads still open: redeemed, not spent
// and not revoked. It is what a reaper or a logout brings to zero, and what a
// test reads to tell that a download was really let go.
func (g *Grants) Active() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, e := range g.live {
		if e.redeemed && !e.closed() && !e.revoked {
			n++
		}
	}
	return n
}

// Attach stores a value with a redeemed grant.
func (g *Grants) Attach(token string, v any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e, ok := g.live[digest(token)]; ok && !e.closed() {
		e.attached = v
	}
}

// Close spends the token for good and returns what was attached so the
// caller can release it. A closed token reads as used, not unknown, so the
// second attempt gets the accurate refusal.
func (g *Grants) Close(token string) any {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.live[digest(token)]
	if !ok {
		return nil
	}
	v := e.attached
	e.attached = nil
	if !e.closed() {
		e.closedAt = g.now()
	}
	return v
}

// EndSession revokes every grant minted in s, as a logout does. It returns
// what was attached to each so the caller can release the staged files.
func (g *Grants) EndSession(s Session) []any {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := s.key()
	var released []any
	for _, e := range g.live {
		if e.closed() || e.session.key() != k {
			continue
		}
		e.revoked = true
		if e.attached != nil {
			released = append(released, e.attached)
			e.attached = nil
		}
	}
	return released
}

// Reap closes every started download whose grant lapsed (the client walked
// away between ranges) and returns what was attached to each.
func (g *Grants) Reap() []any {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	var released []any
	for _, e := range g.live {
		if e.closed() {
			continue
		}
		if e.redeemed && (e.session.endedAt(now) || (e.open == 0 && !now.Before(e.grant.ExpiresAt))) {
			if e.attached != nil {
				released = append(released, e.attached)
				e.attached = nil
			}
			e.closedAt = now
		}
	}
	g.reclaimLocked(now)
	return released
}
