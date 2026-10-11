package export

import (
	"context"
	"errors"
	"io"
	"os"
)

type Origin string

const (
	OriginLocal  Origin = "local"
	OriginRemote Origin = "remote"
)

var (
	ErrIntegrity = errors.New("recovery: the copy's bytes do not match what was recorded")
	ErrNoSpace   = errors.New("recovery: no space to stage the copy")
	ErrNoDigest  = errors.New("recovery: the copy has no usable recorded digest")
)

type IntegrityKind string

type IntegrityError struct {
	Origin Origin
	Medium string
	Kind   IntegrityKind
}

func (e *IntegrityError) Error() string   { return "recovery: integrity" }
func (e *IntegrityError) Is(t error) bool { return t == ErrIntegrity }

type Source interface {
	Open(ctx context.Context) (io.ReadCloser, error)
}

type Expect struct {
	Size    *int64
	HashAlg string
	Hash    string
	Origin  Origin
	Medium  string
}

type Staged struct {
	Path   string
	Size   int64
	Digest string
}

func (s *Staged) Open() (*os.File, error) { return nil, errors.New("not implemented") }
func (s *Staged) Discard() error          { return nil }

type Engine struct {
	Staging *Staging
	Buffer  int
}

func (e *Engine) Stage(ctx context.Context, src Source, exp Expect) (*Staged, error) {
	return nil, errors.New("not implemented")
}
