package suitetest_test

import (
	"errors"
	"io"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/copycrypt/suitetest"
)

// TestEveryRegisteredSuitePassesTheVectors runs the shared vectors over
// the whole registry, so a suite added to the table is covered without a
// line here (FR-90).
func TestEveryRegisteredSuitePassesTheVectors(t *testing.T) {
	all := copycrypt.Suites()
	if len(all) < 2 {
		t.Fatalf("the registry has %d suite(s); phase 1 ships age/v1 and age-pq/v1, and group 3 needs both", len(all))
	}
	for _, suite := range all {
		var others []copycrypt.Suite
		for _, o := range all {
			if o.ID() != suite.ID() {
				others = append(others, o)
			}
		}
		t.Run(suite.ID(), func(t *testing.T) { suitetest.Run(t, suite, others) })
	}
}

// acceptsTruncation is the planted broken adapter: age/v1 whose reader
// turns any failure into a clean end of stream, which is exactly how a
// truncated object gets accepted.
type acceptsTruncation struct{ copycrypt.AgeX25519 }

func (a acceptsTruncation) Open(secret []byte, ciphertext io.Reader) (io.Reader, error) {
	r, err := a.AgeX25519.Open(secret, ciphertext)
	if err != nil {
		return nil, err
	}
	return swallow{r}, nil
}

type swallow struct{ r io.Reader }

func (s swallow) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, err
}

// TestAnAdapterThatAcceptsTruncationIsCaught is the test of the test: the
// negatives group, pointed at the planted adapter, has to object, and it
// has to object about the truncation by name. A vector that passed this
// adapter would pass anything.
func TestAnAdapterThatAcceptsTruncationIsCaught(t *testing.T) {
	problems := suitetest.Negatives(acceptsTruncation{})
	found := false
	for _, p := range problems {
		if p == "a tail cut on a chunk boundary: the adapter opened it and read to a clean end" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the negatives group did not catch an adapter that accepts a truncated tail; it said:\n%v", problems)
	}
	// The control: the real adapter draws no complaint.
	if problems := suitetest.Negatives(copycrypt.AgeX25519{}); len(problems) != 0 {
		t.Fatalf("the real age/v1 adapter drew complaints, so the planted one proves nothing: %v", problems)
	}
}
