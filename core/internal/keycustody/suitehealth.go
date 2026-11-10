package keycustody

import (
	"fmt"
	"sort"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/health"
)

// SuiteHealth is health.KeyCustodyHealth.Suites (EPIC T, T2.1, #1316,
// FR-87): one line per suite that something in this deployment still
// depends on, with the lifecycle's verdict for it.
//
// activeCopies is the count of ACTIVE copies per recorded suite id (the
// journal's ActiveEncryptedCopiesBySuite), keys the registry's rows. suites is the
// registry (copycrypt.Suites()), handed in so a test can see exactly the
// table it injected. Nothing here reads key material or a copy's bytes; it
// is arithmetic over ids and counts, so a health build pays for two reads
// it already makes and nothing more.
//
// A suite nothing depends on is left out, which is what keeps the zero
// section for a deployment that encrypts nothing. A recorded id the registry
// does not know is listed with the status "unknown" and degrades: a copy
// this build cannot open is the loudest thing this section can say, and it
// is never read as plaintext.
func SuiteHealth(suites []copycrypt.Suite, activeCopies map[string]int, keys []KeyRecord) []health.SuiteHealth {
	type tally struct {
		keys   int
		active bool
	}
	byKey := map[string]tally{}
	for _, k := range keys {
		if k.Status == StatusRetired {
			continue
		}
		id := suiteOf(k) // a key from before migration 0020 is age/v1
		t := byKey[id]
		t.keys++
		t.active = t.active || k.Status == StatusActive
		byKey[id] = t
	}

	var out []health.SuiteHealth
	seen := map[string]bool{}
	for _, s := range suites {
		id := s.ID()
		seen[id] = true
		k := byKey[id]
		h := health.SuiteHealth{
			ID: id, Status: string(s.Status()), ActiveCopies: activeCopies[id],
			Keys: k.keys, ActiveKey: k.active, RetirementVersion: copycrypt.RetiredIn(s),
		}
		if h.ActiveCopies == 0 && h.Keys == 0 {
			continue
		}
		h.Degrading, h.Reason = suiteVerdict(s.Status(), h)
		out = append(out, h)
	}

	var unknown []string
	for id, n := range activeCopies {
		if !seen[id] && n > 0 {
			unknown = append(unknown, id)
		}
	}
	for id, k := range byKey {
		if !seen[id] && activeCopies[id] == 0 && k.keys > 0 {
			unknown = append(unknown, id)
		}
	}
	sort.Strings(unknown)
	for _, id := range unknown {
		k := byKey[id]
		out = append(out, health.SuiteHealth{
			ID: id, Status: "unknown", ActiveCopies: activeCopies[id], Keys: k.keys, ActiveKey: k.active,
			Degrading: true,
			Reason: fmt.Sprintf("%d active copies and %d keys record the suite %q, which this build does not know; they cannot be opened here, and nothing reads them as plaintext",
				activeCopies[id], k.keys, id),
		})
	}
	return out
}

// suiteVerdict is the lifecycle's health column for one suite: whether it
// degrades the section and the sentence that says why. Every status in the
// vocabulary has a case, which is also how the status guard in copycrypt
// sees each one consumed outside its package.
func suiteVerdict(status copycrypt.Status, h health.SuiteHealth) (bool, string) {
	announced := "no retirement version is announced yet"
	if h.RetirementVersion != "" {
		announced = "retirement announced for version " + h.RetirementVersion
	}
	switch status {
	case copycrypt.StatusSupported:
		return false, ""
	case copycrypt.StatusDeprecated:
		if h.ActiveCopies == 0 && !h.ActiveKey {
			return false, ""
		}
		under := fmt.Sprintf("%d active copies are", h.ActiveCopies)
		if h.ActiveKey {
			under = fmt.Sprintf("%d active copies and the active key are", h.ActiveCopies)
		}
		return true, fmt.Sprintf("%s under %s, a deprecated suite; %s", under, h.ID, announced)
	case copycrypt.StatusRetired:
		retired := "retired"
		if h.RetirementVersion != "" {
			retired = "retired in " + h.RetirementVersion
		}
		switch {
		case h.ActiveCopies > 0 && h.ActiveKey:
			return true, fmt.Sprintf("%d active copies and the active key are under %s, %s; the copies still read, new copies are refused until the key is rotated to another suite", h.ActiveCopies, h.ID, retired)
		case h.ActiveCopies > 0:
			return true, fmt.Sprintf("%d active copies record %s, %s; they still read, and re-encrypting them under the active suite clears this", h.ActiveCopies, h.ID, retired)
		case h.ActiveKey:
			return true, fmt.Sprintf("the active key is under %s, %s; new copies are refused until the key is rotated to another suite", h.ID, retired)
		default:
			return false, ""
		}
	default:
		return true, fmt.Sprintf("%s has the status %q, which this build gives no meaning; treated as unable to write", h.ID, status)
	}
}
