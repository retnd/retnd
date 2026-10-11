package export

import (
	"errors"
	"time"
)

type TargetKind string

const (
	TargetDownload TargetKind = "download"
	TargetStaging  TargetKind = "staging"
	TargetStorage  TargetKind = "storage"
)

type Target struct {
	Kind   TargetKind
	Path   string
	Medium string
	Key    string
}

type Session struct {
	Actor   string
	Expires time.Time
}

type GrantSpec struct {
	Session  Session
	Artifact string
	Copy     string
	Target   Target
}

type Grant struct {
	Actor     string
	Artifact  string
	Copy      string
	Target    Target
	ExpiresAt time.Time
}

type GrantOptions struct {
	TTL            time.Duration
	Now            func() time.Time
	MaxOutstanding int
}

type Redemption struct {
	Grant    Grant
	First    bool
	Attached any
}

var (
	ErrGrantUnknown      = errors.New("recovery: unknown grant")
	ErrGrantUsed         = errors.New("recovery: this grant has already been used")
	ErrGrantExpired      = errors.New("recovery: this grant has expired")
	ErrGrantSessionEnded = errors.New("recovery: the session this grant was minted in has ended")
	ErrTooManyGrants     = errors.New("recovery: too many recovery grants are outstanding")
)

type Grants struct{}

func NewGrants(GrantOptions) *Grants { return &Grants{} }

func (g *Grants) Mint(GrantSpec) (string, Grant, error)      { return "", Grant{}, nil }
func (g *Grants) Redeem(string, Session) (Redemption, error) { return Redemption{}, nil }
func (g *Grants) Attach(string, any)                         {}
func (g *Grants) Close(string) any                           { return nil }
func (g *Grants) EndSession(Session)                         {}
func (g *Grants) holdsPlaintext(string) bool                 { return false }
