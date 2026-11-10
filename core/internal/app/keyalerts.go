package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/secretref"
)

// keyAlertSource is what the alert pass needs from key custody: the active
// managed key (a registry read that opens nothing) and the condition builder.
// service.KeyCustody satisfies it by opening its *keycustody.Store for the
// second, which is the only call that creates the key directory.
type keyAlertSource interface {
	Active(ctx context.Context) (keycustody.KeyRecord, bool, error)
	AlertConditions(ctx context.Context, policies []keycustody.SetPolicy) ([]alert.Condition, []alert.Subject)
}

// externalKeyRegistrar is the capability that decides whether this build can
// write a copy under an external key reference at all: custody that can
// register an external key can, and the engine refuses outright for custody
// that cannot (placement.ExternalKeys, #1292). It is asked of s.Keys instead
// of being assumed, so the day custody gains the method, the alert for an
// external reference stops claiming a refusal and lets the probe speak, with
// no change here.
type externalKeyRegistrar interface {
	RegisterExternal(ctx context.Context, ref secretref.Ref, at time.Time) (keycustody.KeyRecord, error)
}

// externalKeySuites tells which suite an external reference's key belongs
// to without registering it (EPIC T, #1314). service.KeyCustody satisfies it;
// a custodian without it cannot say, and the pinned-suite check for an
// external set is then left to the engine's refusal.
type externalKeySuites interface {
	ExternalKeySuite(ctx context.Context, ref secretref.Ref) (string, error)
}

// externalKeyRisks is the capability that tells which external keys have lost
// every declared reference while ACTIVE copies still need them (#1304).
// service.KeyCustody satisfies it; it is asked of s.Keys so a custodian
// without it simply raises nothing.
type externalKeyRisks interface {
	ExternalKeysAtRisk(ctx context.Context) ([]health.ExternalKeyAtRisk, error)
	LastExternalKeysAtRisk() []health.ExternalKeyAtRisk
}

// ExternalKeyAlertConditions raises ENCRYPTION_KEY_UNRESOLVABLE for each
// alertable set holding ACTIVE copies under an external key no declared
// reference yields any more (#1304). It is a different fact from the required
// set's own condition (those copies already exist and are unreadable, not
// refused), so it is raised whatever the set's mode is, but a set that already
// carries a condition this pass (have) is not given a second one for the same
// kind and scope: the dispatcher keys on both.
//
// A look that fails marks every alertable set unevaluated, so a standing
// condition is not resolved by a pass that could not look.
func (s *Service) ExternalKeyAlertConditions(ctx context.Context, alertable map[model.BackupSetID]bool, have []alert.Condition) ([]alert.Condition, []alert.Subject) {
	risks, ok := s.Keys.(externalKeyRisks)
	if !ok {
		return nil, nil
	}
	couldNotLook := func() []alert.Subject {
		var out []alert.Subject
		for id, on := range alertable {
			if on {
				out = append(out, alert.Subject{Kind: alert.EncryptionKeyUnresolvable, Scope: id.String()})
			}
		}
		return out
	}
	if ctx.Err() != nil {
		return nil, couldNotLook()
	}
	list, err := risks.ExternalKeysAtRisk(ctx)
	if err != nil {
		return nil, couldNotLook()
	}
	taken := map[string]bool{}
	for _, c := range have {
		if c.Kind == alert.EncryptionKeyUnresolvable {
			taken[c.Scope] = true
		}
	}
	type atSet struct {
		keys   []string
		copies []string
	}
	bySet := map[string]*atSet{}
	var order []string
	for _, r := range list {
		for _, c := range r.Copies {
			set := c.Artifact.Set
			if !alertable[set] || taken[set.String()] {
				continue
			}
			a := bySet[set.String()]
			if a == nil {
				a = &atSet{}
				bySet[set.String()] = a
				order = append(order, set.String())
			}
			if len(a.keys) == 0 || a.keys[len(a.keys)-1] != r.KeyID {
				a.keys = append(a.keys, r.KeyID)
			}
			a.copies = append(a.copies, c.String())
		}
	}
	sort.Strings(order)
	var out []alert.Condition
	for _, set := range order {
		a := bySet[set]
		shown := a.copies
		more := ""
		if len(shown) > 10 {
			more = fmt.Sprintf(", and %d more", len(shown)-10)
			shown = shown[:10]
		}
		out = append(out, alert.Condition{
			Kind:  alert.EncryptionKeyUnresolvable,
			Scope: set,
			Detail: fmt.Sprintf(
				"Backup set %s holds %d ACTIVE encrypted copies under external key %s, and no declared key reference yields that key now, so they cannot be read, verified or restored: %s%s. "+
					"Declare a reference that yields the key again (keep a superseded key's reference declared for as long as copies record it). Nothing was deleted.",
				set, len(a.copies), strings.Join(a.keys, ", "), strings.Join(shown, ", "), more),
		})
	}
	return out, nil
}

// KeyCustodyAlertConditions is FR-66's refusal reaching whoever is on the other
// end of the configured notifier (#1294): ENCRYPTION_KEY_UNRESOLVABLE for each
// alertable set whose copies the engine would refuse for want of a key. It
// sits in evaluateAlerts beside RepositoryAlertConditions, so the dispatcher's
// de-duplication, resolution on absence, AlertTick cadence and AdoptAlerts
// carry-over all apply as they do for every other kind.
//
// Which sets: alertable (not disabled) and config.BackupSet.EncryptsRetainedCopies,
// so a kopia set, whose copies are satisfied by its repository, never alerts.
// When no such set exists this returns nothing and asks custody nothing, so a
// deployment that encrypts nothing never grows a key directory beside its
// config (the "off is byte identical" invariant) and works on a read-only
// config mount.
//
// What each set is held to is the engine's own rule (placement activeKey):
//   - managed: the registry's active key, then its file. No active key is a
//     condition ("no managed key is active"). A registry that cannot be read is
//     "could not look": the set goes on the unevaluated list, never resolved by
//     omission, because a failed look that resolved a standing condition would
//     alert again on the next good pass about a fact that never changed.
//   - external: refused outright while custody cannot register an external key
//     (externalKeyRegistrar), which is said as a condition rather than left
//     silent, since silent while refusing is the one combination that is not
//     acceptable. Once custody can, the reference is handed to custody's probe.
//   - no custody that can be asked at all: the engine refuses that too, so it is
//     a condition.
func (s *Service) KeyCustodyAlertConditions(ctx context.Context, alertable map[model.BackupSetID]bool) ([]alert.Condition, []alert.Subject) {
	type wanted struct {
		set    string
		policy config.RetainedCopyEncryption
	}
	var sets []wanted
	for _, src := range s.Config.Sources {
		for _, bs := range src.BackupSets {
			if alertable[bs.ID] && bs.EncryptsRetainedCopies() {
				sets = append(sets, wanted{set: bs.ID.String(), policy: bs.RetainedCopyEncryption})
			}
		}
	}
	if len(sets) == 0 {
		return nil, nil
	}

	var (
		conditions  []alert.Condition
		unevaluated []alert.Subject
	)
	couldNotLook := func(set string) {
		unevaluated = append(unevaluated, alert.Subject{Kind: alert.EncryptionKeyUnresolvable, Scope: set})
	}
	if ctx.Err() != nil {
		for _, w := range sets {
			couldNotLook(w.set)
		}
		return nil, unevaluated
	}

	source, ok := s.Keys.(keyAlertSource)
	if !ok {
		for _, w := range sets {
			conditions = append(conditions, keycustody.UnresolvableCondition(w.set,
				providerDescription(w.policy, keycustody.Provider{External: w.policy.KeyProvider.ExternalRef()}),
				"no key custody is configured"))
		}
		return conditions, nil
	}
	_, canWriteExternal := s.Keys.(externalKeyRegistrar)

	var (
		policies    []keycustody.SetPolicy
		activeID    string
		activeSuite string
		activeErr   error
		looked      bool
	)
	for _, w := range sets {
		kp := w.policy.KeyProvider
		switch {
		case kp.External != nil:
			ref := kp.ExternalRef()
			if !canWriteExternal {
				conditions = append(conditions, keycustody.UnresolvableCondition(w.set,
					keycustody.Provider{External: ref}.Describe(),
					"this build cannot write copies under an external key reference yet"))
				continue
			}
			// A pinned suite the external key is not a key of: the engine
			// refuses every copy (FR-86), so the alert says why, under the
			// existing kind. A reference that does not resolve is the probe's
			// to report, so it falls through.
			if want, pinned := w.policy.PinnedSuite(); pinned {
				if es, ok := s.Keys.(externalKeySuites); ok {
					if have, err := es.ExternalKeySuite(ctx, ref); err == nil && have != want {
						conditions = append(conditions, keycustody.UnresolvableCondition(w.set,
							keycustody.Provider{External: ref}.Describe(),
							fmt.Sprintf("the deployment pins suite %s and the external key is a %s key; point the reference at a %s key", want, have, want)))
						continue
					}
				}
			}
			policies = append(policies, keycustody.SetPolicy{Set: w.set, Required: true, Provider: keycustody.Provider{External: ref}})
		case kp.Managed:
			if !looked {
				looked = true
				rec, has, err := source.Active(ctx)
				activeErr = err
				if has {
					activeID = rec.ID
					activeSuite = rec.SuiteID
					if activeSuite == "" {
						activeSuite = keycustody.AgeFormatID
					}
				}
			}
			if activeErr != nil {
				couldNotLook(w.set)
				continue
			}
			// A pinned suite with no active key of it is the engine's refusal
			// (EPIC T, #1314), said here with the same reason and the
			// command that clears it, under the existing kind.
			if want, pinned := w.policy.PinnedSuite(); pinned && activeSuite != want {
				conditions = append(conditions, keycustody.UnresolvableCondition(w.set, "managed key",
					fmt.Sprintf("no active key for suite %s; run `retnd encryption key rotate --suite %s`", want, want)))
				continue
			}
			policies = append(policies, keycustody.SetPolicy{Set: w.set, Required: true, Managed: true, Provider: keycustody.Provider{Managed: activeID}})
		default:
			// Names no provider. Validate refuses that at load, so a loaded
			// config never gets here; custody says "no key provider is declared".
			policies = append(policies, keycustody.SetPolicy{Set: w.set, Required: true})
		}
	}
	if len(policies) > 0 {
		c, u := source.AlertConditions(ctx, policies)
		conditions = append(conditions, c...)
		unevaluated = append(unevaluated, u...)
	}
	return conditions, unevaluated
}
