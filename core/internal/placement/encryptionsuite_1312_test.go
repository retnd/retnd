package placement_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// A copy is read under the suite its record names (EPIC T, T1.2, #1312;
// ADR 0033 D7.4). A record naming a suite this build has no adapter for is
// neither plaintext nor content: it is ErrClassUnavailable, decided before
// the key or the object is touched, which the nil store and nil key custody
// below prove (either would panic if it were asked).
func TestAnUnknownSuiteOnARecordIsClassUnavailable(t *testing.T) {
	p := state.Placement{Medium: "warm", Location: "prod/db/a.dump", Hash: strings.Repeat("a", 64), HashAlg: "sha256"}
	enc := &state.PlacementEncryption{
		FormatID: "tink-aes256gcm-hkdf-1mb/v1", FormatVersion: 1, KeyID: "k0123456789abcdef",
		StoredHash: strings.Repeat("b", 64), StoredHashAlg: "sha256", StoredSize: 4096,
	}
	res, err := placement.VerifyCopy(context.Background(), nil, nil, transport.Medium{}, p, enc, placement.Content, time.Now())
	if !errors.Is(err, placement.ErrClassUnavailable) || !errors.Is(err, copycrypt.ErrUnknownSuite) {
		t.Fatalf("VerifyCopy over an unknown suite = %+v, %v; want ErrClassUnavailable wrapping ErrUnknownSuite", res, err)
	}
	if res.Passed {
		t.Fatal("a copy under an unknown suite was reported as passing")
	}

	// The control: the same record under age/v1 gets past the suite check
	// and stops at the next one, the missing key custody, which is a
	// different refusal.
	enc.FormatID = copycrypt.AgeX25519ID
	_, err = placement.VerifyCopy(context.Background(), nil, nil, transport.Medium{}, p, enc, placement.Content, time.Now())
	if errors.Is(err, copycrypt.ErrUnknownSuite) {
		t.Fatalf("an age/v1 record was refused as an unknown suite: %v", err)
	}
	if !errors.Is(err, placement.ErrKeyUnresolvable) {
		t.Fatalf("an age/v1 record with no key custody = %v, want ErrKeyUnresolvable", err)
	}
}
