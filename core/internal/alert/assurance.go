package alert

import "fmt"

// AssuranceSignal is what the assurance layer knows about one backup set,
// reduced to counts and flags. It deliberately has no field for a
// validator's output, a path or a copy's location: a notification leaves
// the process for a phone or a chat webhook, and the real defence against
// leaking operator-controlled text is that there is nowhere to put it.
type AssuranceSignal struct {
	BackupSet string

	// ScrubOverdue is how many copies are past their scrub cadence,
	// counting a never-scrubbed copy.
	ScrubOverdue int
	// ScrubFailing is how many copies failed their last scrub.
	ScrubFailing int

	// RecoveryTestOverdue says the policy requires a recovery test and the
	// last one is older than its cadence or absent.
	RecoveryTestOverdue bool
	// RecoveryTestFailing says the last recovery test did not pass.
	RecoveryTestFailing bool
	// ValidatorID names the validator, for the sentence only. It is
	// checked as a bare name and withheld otherwise.
	ValidatorID string

	// NoReadableCopy is how many artifacts have no copy that can be read
	// now.
	NoReadableCopy int
	// ArchiveOnly is how many artifacts have only an archived copy left.
	ArchiveOnly int
}

// AssuranceConditions turns signals into conditions: at most one per kind
// per backup set, in a fixed kind order, and none for a set with nothing
// wrong. Each resolves when the signal clears, and de-duplicates on
// (kind, set) like every other condition.
func AssuranceConditions(signals []AssuranceSignal) []Condition {
	var out []Condition
	for _, s := range signals {
		set := safeSetName(s.BackupSet)
		add := func(k Kind, detail string) {
			out = append(out, Condition{Kind: k, Scope: set, Detail: detail})
		}
		if s.ScrubOverdue > 0 {
			add(ScrubOverdue, fmt.Sprintf("Backup set %s has %d backup(s) that have not been re-read within their scrub cadence. Until they are, nothing says the stored bytes are still good.", set, s.ScrubOverdue))
		}
		if s.ScrubFailing > 0 {
			add(ScrubFailing, fmt.Sprintf("Backup set %s has %d backup(s) that failed their last re-read. The stored copy no longer matches what was recorded.", set, s.ScrubFailing))
		}
		if s.RecoveryTestOverdue {
			add(RecoveryTestOverdue, fmt.Sprintf("Backup set %s requires a recovery test and the last one is older than its cadence, or none has run.", set))
		}
		if s.RecoveryTestFailing {
			add(RecoveryTestFailing, fmt.Sprintf("The last recovery test for backup set %s did not pass (validator %s). The bytes were recovered but the check on them failed.", set, safeToken(s.ValidatorID)))
		}
		if s.NoReadableCopy > 0 {
			add(NoReadableCopy, fmt.Sprintf("Backup set %s has %d backup(s) with no copy that can be read right now.", set, s.NoReadableCopy))
		}
		if s.ArchiveOnly > 0 {
			add(ArchiveOnlyRecoverability, fmt.Sprintf("Backup set %s has %d backup(s) whose only surviving copy is in an archive tier. Recovering one needs a thaw first.", set, s.ArchiveOnly))
		}
	}
	return out
}
