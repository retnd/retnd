package keycustody

import (
	"context"
	"fmt"

	"github.com/retnd/retnd/core/internal/alert"
)

// AlertConditions is what the resolvability of each set's key means to the
// operator-facing notification path, in internal/alert's vocabulary. It
// produces exactly one Kind, alert.EncryptionKeyUnresolvable, for a set
// whose policy is `required` and whose key does not resolve right now.
//
// # Why this one fact is a condition and the bundle is not
//
// alert.Dispatcher's model is for a fact that is true for a while, stops
// being true on its own, and may become true again: it fires once, stays
// quiet while the fact holds, and re-alerts on a genuine recurrence. An
// unresolvable key is exactly that shape: the file comes back, the mode is
// corrected, the secrets manager answers again, and the condition is gone
// without anybody touching this product. An unconfirmed bundle is not: only
// a person can clear it, so as a condition it would ride the retry ladder
// forever, which no kind in the vocabulary does. It is health
// (Store.Health) instead.
//
// # What the sentence has to say
//
// That copies are being REFUSED, not lost. FR-66 says a `required` policy
// whose key cannot resolve refuses the affected copy before any source or
// medium deletion step, so the consequence of this condition is that new
// off-site copies for the set stop and the source is left alone. The
// message says so, names the set and the reference an operator has to go
// and fix, and names no material, which Probe cannot carry anyway.
//
// Sets whose policy is not `required` produce nothing, however broken
// their reference: `off` encrypts nothing, so an unresolvable key is not
// refusing anything.
func (s *Store) AlertConditions(ctx context.Context, policies []SetPolicy) ([]alert.Condition, []alert.Subject) {
	var (
		conditions  []alert.Condition
		unevaluated []alert.Subject
	)
	for _, p := range policies {
		if !p.Required {
			continue
		}
		if ctx.Err() != nil {
			unevaluated = append(unevaluated, alert.Subject{Kind: alert.EncryptionKeyUnresolvable, Scope: p.Set})
			continue
		}
		if p.Managed && p.Provider.Managed == "" && p.Provider.External.IsZero() {
			conditions = append(conditions, UnresolvableCondition(p.Set, "the managed key", "no managed key is active"))
			continue
		}
		probe := s.Resolvability(ctx, p.Provider)
		if probe.State == Resolves {
			continue
		}
		reason := probe.Reason
		if probe.State == NotDeclared {
			reason = "no key provider is declared for it"
		}
		conditions = append(conditions, UnresolvableCondition(p.Set, p.Provider.Describe(), reason))
	}
	return conditions, unevaluated
}

// UnresolvableCondition is the one sentence ENCRYPTION_KEY_UNRESOLVABLE says,
// for the two places that build it: Store.AlertConditions, for a key it probed,
// and app's key alert wiring, for the cases no probe is needed to know copies
// are refused (no custody configured, an external reference this build cannot
// write under). key describes the key the way Provider.Describe does and
// carries no material.
func UnresolvableCondition(set, key, reason string) alert.Condition {
	return alert.Condition{
		Kind:  alert.EncryptionKeyUnresolvable,
		Scope: set,
		Detail: fmt.Sprintf(
			"Backup set %s requires encrypted retained copies and its key (%s) cannot be resolved: %s. "+
				"New copies for this set are being refused until the key resolves again, and nothing is deleted at the source or on any medium while they are. "+
				"Copies already stored are unaffected.",
			set, key, reason),
	}
}
