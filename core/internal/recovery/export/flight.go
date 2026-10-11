package export

import (
	"context"
	"errors"
)

// Flight is a stub until flight_test.go is red.
type Flight struct{}

func (f *Flight) Do(ctx context.Context, key string, fn func(context.Context) (*Staged, error)) (*Staged, error) {
	return fn(ctx)
}

func (s *Staged) PublishTo(rel string) (string, error) { return "", errors.New("not implemented") }
