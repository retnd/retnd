package health_test

import (
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/keycustody"
)

// EPIC T, T2.1 (#1316, FR-87): the key custody section's suite lines. A
// deprecated suite that copies or the active key are under degrades the
// section with the count and the announced retirement version; a retired
// suite degrades it while any active copy records it; a deployment that
// encrypts nothing keeps the zero section. The suites are the registry's
// test hook over the real default adapter, so the production table is never
// edited.
//
// These are external tests (package health_test) because the lines are
// built by keycustody, which imports health.
//
// Written by reading; not run (the suites run on the release PR).

func section(suites []copycrypt.Suite, copies map[string]int, keys []keycustody.KeyRecord) health.KeyCustodyHealth {
	return health.KeyCustodyHealth{
		Enabled: true, ActiveKeyID: "k1", BundleConfirmed: true,
		Suites: keycustody.SuiteHealth(suites, copies, keys),
	}
}

func line(t *testing.T, h health.KeyCustodyHealth, id string) health.SuiteHealth {
	t.Helper()
	for _, s := range h.Suites {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no suite line for %q in %+v", id, h.Suites)
	return health.SuiteHealth{}
}

func TestADeprecatedSuiteDegradesWithCounts(t *testing.T) {
	dep := copycrypt.InjectForTest(t, copycrypt.Default(), copycrypt.TestSuitePrefix+"deprecated/v1", copycrypt.StatusDeprecated, "0.10.0")
	keys := []keycustody.KeyRecord{{ID: "k1", SuiteID: dep.ID(), Status: keycustody.StatusActive}}

	h := section(copycrypt.Suites(), map[string]int{dep.ID(): 3}, keys)
	got := line(t, h, dep.ID())
	if got.Status != string(copycrypt.StatusDeprecated) || got.ActiveCopies != 3 || got.Keys != 1 || !got.ActiveKey || got.RetirementVersion != "0.10.0" {
		t.Errorf("the deprecated suite's line is %+v", got)
	}
	if !got.Degrading || !h.Degraded() || h.OK() {
		t.Fatalf("a deprecated suite with 3 active copies and the active key did not degrade the section: %+v", got)
	}
	for _, want := range []string{"3 active copies and the active key are under", dep.ID(), "deprecated suite", "retirement announced for version 0.10.0"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("the reason %q does not say %q", got.Reason, want)
		}
	}

	// Control: the same suite supported is quiet, so the assertion above can
	// fail for the status and nothing else.
	dep.SetStatus(copycrypt.StatusSupported, "")
	if h := section(copycrypt.Suites(), map[string]int{dep.ID(): 3}, keys); h.Degraded() {
		t.Errorf("control: a supported suite with copies degraded the section: %+v", h.Suites)
	}

	// A deprecated suite nothing is under any more says nothing.
	dep.SetStatus(copycrypt.StatusDeprecated, "0.10.0")
	if h := section(copycrypt.Suites(), nil, nil); h.Degraded() || len(h.Suites) != 0 {
		t.Errorf("a deprecated suite with no copies and no key degraded or listed: %+v", h.Suites)
	}
}

func TestARetiredSuiteDegradesWhileCopiesRecordIt(t *testing.T) {
	ret := copycrypt.InjectForTest(t, copycrypt.Default(), copycrypt.TestSuitePrefix+"retired/v1", copycrypt.StatusRetired, "0.9.0")
	// The active key is under the default suite: the operator rotated away,
	// and two copies under the retired suite are still ACTIVE.
	keys := []keycustody.KeyRecord{
		{ID: "k-old", SuiteID: ret.ID(), Status: keycustody.StatusSuperseded},
		{ID: "k1", SuiteID: copycrypt.Default().ID(), Status: keycustody.StatusActive},
	}

	h := section(copycrypt.Suites(), map[string]int{ret.ID(): 2, copycrypt.Default().ID(): 5}, keys)
	got := line(t, h, ret.ID())
	if !got.Degrading || !h.Degraded() {
		t.Fatalf("a retired suite that 2 active copies record did not degrade the section: %+v", got)
	}
	if got.ActiveCopies != 2 || got.ActiveKey || got.RetirementVersion != "0.9.0" {
		t.Errorf("the retired suite's line is %+v", got)
	}
	for _, want := range []string{"2 active copies record", ret.ID(), "retired in 0.9.0", "still read"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("the reason %q does not say %q", got.Reason, want)
		}
	}
	if d := line(t, h, copycrypt.Default().ID()); d.Degrading {
		t.Errorf("the default suite's line degrades: %+v", d)
	}

	// Once the last copy under it is re-encrypted the section is quiet again,
	// with the superseded key still counted.
	h = section(copycrypt.Suites(), map[string]int{copycrypt.Default().ID(): 7}, keys)
	if h.Degraded() {
		t.Errorf("a retired suite with no active copy still degrades the section: %+v", h.Suites)
	}

	// An active key under a retired suite is the write refusal's state and
	// degrades on its own.
	h = section(copycrypt.Suites(), nil, []keycustody.KeyRecord{{ID: "k-old", SuiteID: ret.ID(), Status: keycustody.StatusActive}})
	if got := line(t, h, ret.ID()); !got.Degrading || !strings.Contains(got.Reason, "new copies are refused") {
		t.Errorf("an active key under a retired suite did not degrade with the refusal: %+v", got)
	}
}

func TestNoEncryptionReadsQuiet(t *testing.T) {
	h := health.KeyCustodyHealth{Suites: keycustody.SuiteHealth(copycrypt.Suites(), nil, nil)}
	if len(h.Suites) != 0 {
		t.Fatalf("a deployment with no copies and no keys lists suites: %+v", h.Suites)
	}
	if h.Degraded() || !h.OK() {
		t.Fatal("a deployment that encrypts nothing reads as degraded")
	}
	// The zero section is exactly that: nothing to say, nothing wrong.
	if (health.KeyCustodyHealth{}).Degraded() {
		t.Fatal("the zero section reads as degraded")
	}
}

// A recorded id the registry does not know is never quiet and never read as
// plaintext: it is listed as unknown and degrades.
func TestACopyUnderAnUnknownSuiteDegradesAndIsNamed(t *testing.T) {
	h := section(copycrypt.Suites(), map[string]int{"tink-aes256gcm-hkdf-1mb/v1": 1}, nil)
	got := line(t, h, "tink-aes256gcm-hkdf-1mb/v1")
	if got.Status != "unknown" || !got.Degrading || !strings.Contains(got.Reason, "does not know") {
		t.Errorf("a copy under an unknown suite reads %+v", got)
	}
}
