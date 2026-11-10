package app

import (
	"context"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/config"
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
		policies  []keycustody.SetPolicy
		activeID  string
		activeErr error
		looked    bool
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
			policies = append(policies, keycustody.SetPolicy{Set: w.set, Required: true, Provider: keycustody.Provider{External: ref}})
		case kp.Managed:
			if !looked {
				looked = true
				rec, has, err := source.Active(ctx)
				activeErr = err
				if has {
					activeID = rec.ID
				}
			}
			if activeErr != nil {
				couldNotLook(w.set)
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
