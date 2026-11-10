package health

import (
	"time"

	"github.com/retnd/retnd/core/internal/model"
)

// This file is EPIC N's section of the FR-24 report (#990, FR-66): whether
// the keys that encrypt retained copies could be recovered if this host
// were lost.
//
// # Why this is health and not an alert
//
// "Managed encryption is on and nobody has confirmed holding a bundle for
// the active key" is a fact only a person can change. internal/alert's
// model is for facts that resolve on their own and may recur; a condition
// that can never resolve by itself would ride the 5m -> 1h retry ladder
// forever, which none of the kinds follows. So it is a section an operator
// sees every time they look, the same way Report.Workflow reports an
// unresolved recovery hold, and it is durable: the confirmation lives in the
// key registry, not in this process, so a restart reports the same thing.
//
// # Why the zero value encrypts nothing
//
// Most deployments encrypt no retained copies, and for them this section
// must cost nothing and change nothing. The zero value is "not enabled, no
// key, nothing to confirm, OK", which is what lets Report grow the field
// with no NewReport call site changing, exactly as WorkflowHealth did. The
// verdict depends on Enabled rather than on the emptiness of the fields
// beside it, so a deployment with keys minted and no set requiring them is
// not degraded, and a deployment that requires encryption and has no key
// yet is.

// KeyCustodyHealth is Report.KeyCustody: the recoverability of the keys
// behind encrypted retained copies. It carries ids, times and verdicts and
// has no field that could hold material; surfaces_test.go in
// internal/keycustody pins that structurally.
type KeyCustodyHealth struct {
	// Enabled says at least one backup set resolves to encryption
	// required, which is what makes the rest of this section a verdict
	// rather than a report of facts.
	Enabled bool

	// ActiveKeyID is the managed key new copies are encrypted under, empty
	// when there is none. Reported whether or not Enabled, because the key
	// exists either way.
	ActiveKeyID string

	// BundleConfirmed says a person confirmed holding a disaster-recovery
	// bundle for the ACTIVE key, and BundleConfirmedAt is when.
	BundleConfirmed   bool
	BundleConfirmedAt *time.Time

	// UnconfirmedKeyIDs is every key that is not retired and has no
	// confirmed bundle, the active one included. A superseded key still
	// guards every copy written under it, so a rotation that confirmed the
	// new key and never the old one is still a deployment one lost disk
	// away from unreadable copies.
	UnconfirmedKeyIDs []string

	// ExternalKeysAtRisk is every external key that ACTIVE copies record and
	// that no declared reference currently yields (#1304). An external key's
	// row stores no reference, so dropping a superseded one from the
	// configuration makes every copy under it unreadable, and this is where
	// that is said. It is reported whether or not Enabled, because the copies
	// are unreadable whatever the policy says now.
	ExternalKeysAtRisk []ExternalKeyAtRisk

	// Suites is one entry per encryption suite something in this deployment
	// still depends on: a suite that ACTIVE copies record or that a key which
	// is not retired belongs to (EPIC T, T2.1, #1316, FR-87). A suite nothing
	// depends on is not listed, so a deployment that encrypts nothing keeps
	// the zero section. It is reported whether or not Enabled, for the reason
	// ExternalKeysAtRisk is: the copies depend on their suite whatever the
	// policy says now.
	Suites []SuiteHealth

	// Unevaluated is one line saying why this section could not be computed
	// (the key registry could not be read), and empty when it was. The rest
	// of the report is unaffected; a section that could not be evaluated
	// reads as degraded rather than as nothing to report.
	Unevaluated string
}

// ExternalKeyAtRisk is one external key no declared reference yields and the
// ACTIVE copies that need it. It carries an id, the references' descriptions
// in secretref's own words and the copies; nothing that could hold material.
type ExternalKeyAtRisk struct {
	KeyID  string
	Reason string
	Copies []model.CopyRef
}

// SuiteHealth is one suite's line in the key custody section: its status
// and what still depends on it. It carries ids, counts and a version, never
// material.
//
// The status is the registry's word (supported, deprecated, retired) as a
// string, so this package stays a leaf; keycustody fills it from the
// registry and decides Degrading, which is the lifecycle's verdict for this
// suite with these counts. A health section never holds an alert: the state
// is durable and only a person changes it (a re-encryption, or a rotation to
// another suite), so it lives here, as ADR 0028 D3 argues, and no alert kind
// is added for it.
type SuiteHealth struct {
	ID     string
	Status string

	// ActiveCopies counts the ACTIVE copies whose record names this suite.
	ActiveCopies int

	// Keys counts the keys of this suite that are not retired, and
	// ActiveKey says one of them is the active key new copies go under.
	Keys      int
	ActiveKey bool

	// RetirementVersion is the version the retirement is announced for
	// (deprecated) or that retired the suite (retired), from the registry's
	// table; empty for a supported suite.
	RetirementVersion string

	// Degrading says this suite degrades the section: a deprecated suite
	// that copies or the active key are still under, or a retired suite that
	// any active copy still records. Reason is the sentence a surface prints
	// for it, empty when Degrading is false.
	Degrading bool
	Reason    string
}

// Degraded is FR-66's durable degraded fact: encryption is required
// somewhere and either no key can encrypt yet, or a key that guards copies
// has no confirmed bundle. A suite the lifecycle says is being left behind
// degrades it too, whatever Enabled says (#1316).
func (h KeyCustodyHealth) Degraded() bool {
	if h.Unevaluated != "" || len(h.ExternalKeysAtRisk) != 0 {
		return true
	}
	for _, s := range h.Suites {
		if s.Degrading {
			return true
		}
	}
	if !h.Enabled {
		return false
	}
	return h.ActiveKeyID == "" || !h.BundleConfirmed || len(h.UnconfirmedKeyIDs) != 0
}

// OK is the negation of Degraded, named for the surfaces that render a
// section as fine or not.
func (h KeyCustodyHealth) OK() bool { return !h.Degraded() }
