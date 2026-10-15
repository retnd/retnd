package service

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The assurance listing: which backups the Dashboard's Assurance tile counts
// as due, overdue and degraded, and why (issue #1174). It is the terminal's
// twin of that tile (`retnd assurance list`) and a port of the web client's
// own composition, so the two name the same backups:
//
//   - the claims come from realAssurance.ts's contentClaim and recoveryClaim,
//     read off each copy's recorded fields;
//   - the buckets come from claims.ts's bucketsOf, the one definition the
//     tile's counts and the Backups list's filter share.
//
// A claim is one of: pass, overdue, failed, never_run, could_not_complete,
// requires_restore, no_readable_copy, refused_for_cost, never_configured,
// unknown. A backup can be in several buckets: a recovery test that never ran
// is both due and untested.

// Assurance listing buckets, in the order a row is sorted by.
const (
	BucketDegraded = "degraded"
	BucketOverdue  = "overdue"
	BucketDue      = "due"
	BucketUntested = "untested"
)

// AssuranceBuckets is every bucket name, most urgent first.
var AssuranceBuckets = []string{BucketDegraded, BucketOverdue, BucketDue, BucketUntested}

// AssuranceListing is one backup in the assurance listing.
type AssuranceListing struct {
	ArtifactID   string
	ArtifactName string
	BackupSetID  string
	// Buckets is empty for a backup nothing is owed on.
	Buckets  []string
	Content  AssuranceClaim
	Recovery AssuranceClaim
	// Reason is the two claims in words, with the age of each that has one.
	Reason string
}

// AssuranceClaim is one composed claim.
type AssuranceClaim struct {
	State string
	At    time.Time
	// Check is "builtin" for a recovery claim the built-in structure check
	// passed, "validator" for one a validator accepted.
	Check string
}

func liveCopies(a Artifact) []Placement {
	out := make([]Placement, 0, len(a.Placements))
	for _, p := range a.Placements {
		if p.Status != "DELETE_PENDING" {
			out = append(out, p)
		}
	}
	return out
}

func newestOf(ts ...time.Time) time.Time {
	var best time.Time
	for _, t := range ts {
		if t.After(best) {
			best = t
		}
	}
	return best
}

var classRank = map[string]int{"content": 3, "attested": 2, "existence": 1}

type dueFact struct{ overdue, never bool }

func contentClaimOf(a Artifact, d *dueFact) AssuranceClaim {
	if a.Quarantined {
		return AssuranceClaim{State: "failed"}
	}
	live := liveCopies(a)
	if len(live) == 0 {
		return AssuranceClaim{State: "never_run"}
	}
	var failedAt time.Time
	failed := false
	for _, p := range live {
		if p.ScrubState == "failed" {
			failed = true
			failedAt = newestOf(failedAt, firstNonZero(p.ScrubLastFailureAt, p.ScrubLastCheckedAt))
		}
	}
	if failed {
		return AssuranceClaim{State: "failed", At: failedAt}
	}
	var cls string
	var at time.Time
	for _, p := range live {
		if p.VerificationClass == "" || p.VerifiedAt.IsZero() {
			continue
		}
		if classRank[p.VerificationClass] > classRank[cls] {
			cls, at = p.VerificationClass, p.VerifiedAt
		}
	}
	if cls != "" {
		st := "pass"
		if d != nil && d.overdue && !d.never {
			st = "overdue"
		}
		if st == "pass" && cls == "existence" {
			st = "existence_only"
		} else if st == "pass" && cls == "attested" {
			st = "attested_only"
		}
		return AssuranceClaim{State: st, At: at}
	}
	var incompleteAt time.Time
	incomplete := false
	readable, restorable := false, false
	for _, p := range live {
		if p.ScrubState == "could_not_complete" {
			incomplete = true
			incompleteAt = newestOf(incompleteAt, firstNonZero(p.ScrubLastFailureAt, p.ScrubLastCheckedAt))
		}
		if p.Access == "immediate" {
			readable = true
		}
		if p.Access == "requires_restore" || p.Access == "restoring" {
			restorable = true
		}
	}
	if incomplete {
		return AssuranceClaim{State: "could_not_complete", At: incompleteAt}
	}
	if !readable {
		if restorable {
			return AssuranceClaim{State: "requires_restore"}
		}
		return AssuranceClaim{State: "no_readable_copy"}
	}
	return AssuranceClaim{State: "never_run"}
}

func firstNonZero(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func recoveryClaimOf(a Artifact, policyOff bool) AssuranceClaim {
	live := liveCopies(a)
	when := func(state string) time.Time {
		var best time.Time
		for _, p := range live {
			if p.RecoveryState == state {
				best = newestOf(best, p.RecoveryTestedAt)
			}
		}
		return best
	}
	has := func(state string) bool {
		for _, p := range live {
			if p.RecoveryState == state {
				return true
			}
		}
		return false
	}
	switch {
	case has("failed"):
		return AssuranceClaim{State: "failed", At: when("failed")}
	case has("could_not_complete"):
		return AssuranceClaim{State: "could_not_complete", At: when("could_not_complete")}
	case has("passed"):
		return AssuranceClaim{State: "pass", At: when("passed"), Check: "validator"}
	case has("readable"):
		return AssuranceClaim{State: "pass", At: when("readable"), Check: "builtin"}
	case has("overdue"):
		return AssuranceClaim{State: "overdue", At: when("overdue")}
	case has("refused_for_cost"):
		return AssuranceClaim{State: "refused_for_cost", At: when("refused_for_cost")}
	case policyOff:
		return AssuranceClaim{State: "never_configured"}
	case has("not_configured"):
		return AssuranceClaim{State: "never_configured"}
	case has("never_run") || len(live) > 0:
		return AssuranceClaim{State: "never_run"}
	}
	return AssuranceClaim{State: "unknown"}
}

func badClaim(s string) bool {
	return s == "failed" || s == "could_not_complete" || s == "no_readable_copy" || s == "cleanup_failed"
}

// bucketsOfClaims is claims.ts's bucketsOf.
func bucketsOfClaims(content, recovery string) []string {
	var out []string
	contentDue := content == "unknown" || content == "never_run" || content == "overdue"
	recoveryDue := recovery == "never_run" || recovery == "overdue"
	if badClaim(content) || badClaim(recovery) {
		out = append(out, BucketDegraded)
	}
	if content == "overdue" || recovery == "overdue" {
		out = append(out, BucketOverdue)
	}
	if contentDue || recoveryDue {
		out = append(out, BucketDue)
	}
	if recovery == "never_configured" || recovery == "never_run" || recovery == "unknown" {
		out = append(out, BucketUntested)
	}
	return out
}

// AssuranceAge is how long ago, in the web client's words: "just now", "5m
// ago", "26h ago", "3d ago".
func AssuranceAge(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	s := int64(now.Sub(at) / time.Second)
	if s < 90 {
		return "just now"
	}
	m := s / 60
	if m < 90 {
		return fmt.Sprintf("%dm ago", m)
	}
	h := m / 60
	if h < 48 {
		return fmt.Sprintf("%dh ago", h)
	}
	return fmt.Sprintf("%dd ago", h/24)
}

var contentWords = map[string]string{
	"pass": "verified", "overdue": "overdue", "failed": "check failed", "never_run": "never run",
	"could_not_complete": "could not complete", "requires_restore": "archived, thaw needed",
	"no_readable_copy": "no readable copy", "existence_only": "existence checked only", "attested_only": "attested only",
}

var recoveryWords = map[string]string{
	"pass": "test passed", "overdue": "overdue", "failed": "test failed", "never_run": "never run",
	"could_not_complete": "could not complete", "refused_for_cost": "skipped, read would cost egress",
	"never_configured": "not configured", "unknown": "unknown",
}

func claimWords(words map[string]string, c AssuranceClaim, now time.Time) string {
	w := words[c.State]
	if w == "" {
		w = "unknown"
	}
	if c.State == "pass" && c.Check == "builtin" {
		w = "recovered, readable"
	}
	switch c.State {
	case "pass", "overdue", "failed", "could_not_complete":
		if age := AssuranceAge(c.At, now); age != "" {
			w += " " + age
		}
	}
	return w
}

// ListAssurance classifies every backup the way the Dashboard's Assurance
// tile does. due is the scrub schedule (AssuranceDue); recoveryOff names the
// sets whose own policy turned recovery tests off. Rows are sorted degraded
// first, then overdue, then due, then the rest, each by id.
func ListAssurance(as []Artifact, due []AssuranceDueItem, recoveryOff map[string]bool, now time.Time) []AssuranceListing {
	dueBy := make(map[string]*dueFact, len(due))
	for _, d := range due {
		dueBy[d.ArtifactID] = &dueFact{overdue: d.Overdue, never: d.LastAssuredAt.IsZero()}
	}
	out := make([]AssuranceListing, 0, len(as))
	for _, a := range as {
		content := contentClaimOf(a, dueBy[a.ID])
		recovery := recoveryClaimOf(a, recoveryOff[a.BackupSetID])
		out = append(out, AssuranceListing{
			ArtifactID: a.ID, ArtifactName: a.Name, BackupSetID: a.BackupSetID,
			Buckets: bucketsOfClaims(content.State, recovery.State), Content: content, Recovery: recovery,
			Reason: "Content: " + claimWords(contentWords, content, now) + ". Recovery: " + claimWords(recoveryWords, recovery, now) + ".",
		})
	}
	rank := func(l AssuranceListing) int {
		for i, b := range AssuranceBuckets {
			for _, x := range l.Buckets {
				if x == b {
					return i
				}
			}
		}
		return len(AssuranceBuckets)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return strings.Compare(out[i].ArtifactID, out[j].ArtifactID) < 0
	})
	return out
}
