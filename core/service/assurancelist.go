package service

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// The assurance listing: which backups the Dashboard's Assurance tile counts
// as due, overdue and degraded, and why (issue #1174). It is the terminal's
// twin of that tile (`retnd assurance list`) and a port of the web client's
// own composition, so the two name the same backups:
//
//   - the claims come from realAssurance.ts's contentClaim and recoveryClaim,
//     read off each copy's recorded fields, and are then rendered the way
//     claims.ts's renderClaim renders them (a pass with no time is unknown,
//     a content pass below the content class says which class it reached);
//   - the buckets come from claims.ts's bucketsOf, the one definition the
//     tile's counts and the Backups list's filter share;
//   - the reason is the tile row's own sentence, the claims' labels with the
//     age of each check.
//
// The two are held together by the vectors in testdata/assurance-listing,
// which this package's tests and a vitest (ui/shared/src/test/
// assurance-listing-vectors.test.ts) both assert. A change to either side
// that the other doesn't follow turns one of them red. Until the engine owns
// this classification (a route both read), that is what stops them drifting.

// ClaimState is a rendered claim, one of the constants below.
type ClaimState string

// The claim states. A content claim can be any of them but the recovery-only
// ones; a recovery claim is never existence_only or attested_only.
const (
	ClaimPass             ClaimState = "pass"
	ClaimExistenceOnly    ClaimState = "existence_only"
	ClaimAttestedOnly     ClaimState = "attested_only"
	ClaimOverdue          ClaimState = "overdue"
	ClaimFailed           ClaimState = "failed"
	ClaimNeverRun         ClaimState = "never_run"
	ClaimCouldNotComplete ClaimState = "could_not_complete"
	ClaimRequiresRestore  ClaimState = "requires_restore"
	ClaimNoReadableCopy   ClaimState = "no_readable_copy"
	ClaimRefusedForCost   ClaimState = "refused_for_cost"
	ClaimNeverConfigured  ClaimState = "never_configured"
	ClaimUnknown          ClaimState = "unknown"
)

// Bucket is one of the tile's counts.
type Bucket string

// Assurance listing buckets, in the order a row is sorted by.
const (
	BucketDegraded Bucket = "degraded"
	BucketOverdue  Bucket = "overdue"
	BucketDue      Bucket = "due"
	BucketUntested Bucket = "untested"
)

// AssuranceBuckets is every bucket, most urgent first. A fresh slice each
// call, so no caller can reorder it for the next.
func AssuranceBuckets() []Bucket {
	return []Bucket{BucketDegraded, BucketOverdue, BucketDue, BucketUntested}
}

// PolicyBasis is what is known about a set's recovery-test configuration,
// which is what a never-configured recovery claim says instead of a bare
// "not configured" (the tile's wording, #1141).
type PolicyBasis string

const (
	// PolicyDefault: the set follows the deployment's default policy.
	PolicyDefault PolicyBasis = "default"
	// PolicyOwn: the set has its own policy.
	PolicyOwn PolicyBasis = "own"
	// PolicyNone: the policy resolves to nothing. Also what a set missing
	// from the map reads as.
	PolicyNone PolicyBasis = "none"
	// PolicyUnavailable: the policy could not be read. Whether a test is
	// owed is then unknown, so a backup with no verdict of its own reads
	// "unknown" rather than "never run" and is not counted due.
	PolicyUnavailable PolicyBasis = "unavailable"
)

// SetRecoveryPolicy is the one thing a copy's record cannot say: whether the
// set's own policy turned recovery tests off, and where that came from.
type SetRecoveryPolicy struct {
	Off   bool
	Basis PolicyBasis
}

// AssuranceListing is one backup in the assurance listing.
type AssuranceListing struct {
	ArtifactID   string
	ArtifactName string
	BackupSetID  string
	// Buckets is empty for a backup nothing is owed on.
	Buckets  []Bucket
	Content  AssuranceClaim
	Recovery AssuranceClaim
	// Reason is the two claims in words, with the age of each that has one,
	// exactly as the tile's row says it.
	Reason string
}

// AssuranceClaim is one composed claim.
type AssuranceClaim struct {
	State ClaimState
	At    time.Time
	// Check is "builtin" for a recovery claim the built-in structure check
	// passed, "validator" for one a validator accepted.
	Check string
}

// rendered is the claim as renderClaim names it, where a built-in pass is
// its own state.
func (c AssuranceClaim) rendered() string {
	if c.State == ClaimPass && c.Check == "builtin" {
		return "pass_builtin"
	}
	return string(c.State)
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

func newer(best, t time.Time) time.Time {
	if t.After(best) {
		return t
	}
	return best
}

func firstNonZero(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}

var classRank = map[string]int{"content": 3, "attested": 2, "existence": 1}

type dueFact struct{ overdue, never bool }

// copyContent is realAssurance.ts's copyContentOf: what each live copy has on
// record for the Content claim.
func copyContent(live []Placement) []string {
	out := make([]string, len(live))
	for i, p := range live {
		switch {
		case p.ScrubState == "failed":
			out[i] = "failed"
		case p.VerificationClass != "" && !p.VerifiedAt.IsZero():
			out[i] = p.VerificationClass
		case p.ScrubState == "could_not_complete":
			out[i] = "could_not_complete"
		default:
			out[i] = "unchecked"
		}
	}
	return out
}

func contentClaimOf(a Artifact, live []Placement, d *dueFact) AssuranceClaim {
	if a.Quarantined {
		return AssuranceClaim{State: ClaimFailed}
	}
	if len(live) == 0 {
		return AssuranceClaim{State: ClaimNeverRun}
	}
	var failedAt, incompleteAt time.Time
	failed, incomplete, readable, restorable := false, false, false, false
	var cls string
	var at time.Time
	for _, p := range live {
		switch p.ScrubState {
		case "failed":
			failed = true
			failedAt = newer(failedAt, firstNonZero(p.ScrubLastFailureAt, p.ScrubLastCheckedAt))
		case "could_not_complete":
			incomplete = true
			incompleteAt = newer(incompleteAt, firstNonZero(p.ScrubLastFailureAt, p.ScrubLastCheckedAt))
		}
		if p.VerificationClass != "" && !p.VerifiedAt.IsZero() && classRank[p.VerificationClass] > classRank[cls] {
			cls, at = p.VerificationClass, p.VerifiedAt
		}
		switch p.Access {
		case "immediate":
			readable = true
		case "requires_restore", "restoring":
			restorable = true
		}
	}
	switch {
	case failed:
		return AssuranceClaim{State: ClaimFailed, At: failedAt}
	case cls != "":
		if d != nil && d.overdue && !d.never {
			return AssuranceClaim{State: ClaimOverdue, At: at}
		}
		switch cls {
		case "existence":
			return AssuranceClaim{State: ClaimExistenceOnly, At: at}
		case "attested":
			return AssuranceClaim{State: ClaimAttestedOnly, At: at}
		}
		return AssuranceClaim{State: ClaimPass, At: at}
	case incomplete:
		return AssuranceClaim{State: ClaimCouldNotComplete, At: incompleteAt}
	case !readable && restorable:
		return AssuranceClaim{State: ClaimRequiresRestore}
	case !readable:
		return AssuranceClaim{State: ClaimNoReadableCopy}
	}
	return AssuranceClaim{State: ClaimNeverRun}
}

func recoveryClaimOf(live []Placement, policy SetRecoveryPolicy) AssuranceClaim {
	// The newest time each recorded state was seen at, in one pass.
	seen := map[string]bool{}
	when := map[string]time.Time{}
	for _, p := range live {
		if p.RecoveryState == "" {
			continue
		}
		seen[p.RecoveryState] = true
		when[p.RecoveryState] = newer(when[p.RecoveryState], p.RecoveryTestedAt)
	}
	claim := func(s ClaimState, from, check string) AssuranceClaim {
		at := when[from]
		// A pass with no time is not evidence of anything (renderClaim).
		if s == ClaimPass && at.IsZero() {
			return AssuranceClaim{State: ClaimUnknown}
		}
		return AssuranceClaim{State: s, At: at, Check: check}
	}
	switch {
	case seen["failed"]:
		return claim(ClaimFailed, "failed", "")
	case seen["could_not_complete"]:
		return claim(ClaimCouldNotComplete, "could_not_complete", "")
	case seen["passed"]:
		return claim(ClaimPass, "passed", "validator")
	case seen["readable"]:
		return claim(ClaimPass, "readable", "builtin")
	case seen["overdue"]:
		return claim(ClaimOverdue, "overdue", "")
	case seen["refused_for_cost"]:
		return claim(ClaimRefusedForCost, "refused_for_cost", "")
	case seen["not_configured"]:
		// The engine saying there is no validator to run is a fact about
		// the copy, whatever the policy read said.
		return AssuranceClaim{State: ClaimNeverConfigured}
	case policy.Basis == PolicyUnavailable:
		return AssuranceClaim{State: ClaimUnknown}
	case policy.Off:
		return AssuranceClaim{State: ClaimNeverConfigured}
	case seen["never_run"] || len(live) > 0:
		return AssuranceClaim{State: ClaimNeverRun}
	}
	return AssuranceClaim{State: ClaimUnknown}
}

func badClaim(s ClaimState) bool {
	return s == ClaimFailed || s == ClaimCouldNotComplete || s == ClaimNoReadableCopy
}

// bucketsOfClaims is claims.ts's bucketsOf, in this package's bucket order.
func bucketsOfClaims(content, recovery ClaimState) []Bucket {
	var out []Bucket
	contentDue := content == ClaimUnknown || content == ClaimNeverRun || content == ClaimOverdue
	recoveryDue := recovery == ClaimNeverRun || recovery == ClaimOverdue
	if badClaim(content) || badClaim(recovery) {
		out = append(out, BucketDegraded)
	}
	if content == ClaimOverdue || recovery == ClaimOverdue {
		out = append(out, BucketOverdue)
	}
	if contentDue || recoveryDue {
		out = append(out, BucketDue)
	}
	if recovery == ClaimNeverConfigured || recovery == ClaimNeverRun || recovery == ClaimUnknown {
		out = append(out, BucketUntested)
	}
	return out
}

// assuranceAge is claims.ts's ageOf: "just now", "5m ago", "26h ago", "3d ago".
func assuranceAge(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	s := max(int64(now.Sub(at)/time.Second), 0)
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

// The labels are claims.ts's LABELS, lower-cased the way the tile's row
// lower-cases them.
var contentWords = map[string]string{
	"pass": "content verified", "overdue": "overdue", "failed": "content check failed", "never_run": "never run",
	"could_not_complete": "could not complete", "requires_restore": "archived: thaw needed",
	"no_readable_copy": "no readable copy", "existence_only": "existence checked only", "attested_only": "attested only",
	"refused_for_cost": "skipped: read would cost egress", "unknown": "unknown",
}

var recoveryWords = map[string]string{
	"pass": "recovery test passed", "pass_builtin": "recovered, readable", "overdue": "overdue",
	"failed": "recovery test failed", "never_run": "never run", "could_not_complete": "could not complete",
	"refused_for_cost": "skipped: read would cost egress", "unknown": "unknown",
}

// neverConfiguredWords is claims.ts's neverConfiguredWording.
var neverConfiguredWords = map[PolicyBasis]string{
	PolicyDefault: "default configuration", PolicyOwn: "custom configuration",
	PolicyUnavailable: "configuration unavailable", PolicyNone: "not configured",
}

// withAge is the states renderClaim gives an age to.
func withAge(state string) bool {
	switch state {
	case "pass", "pass_builtin", "existence_only", "attested_only", "overdue", "failed", "could_not_complete":
		return true
	}
	return false
}

// contentPassWords is claims.ts's contentPassLabel: a content pass says when
// fewer than all of the backup's copies back it, and what the rest have.
func contentPassWords(copies []string) string {
	bare := contentWords["pass"]
	good := 0
	for _, c := range copies {
		if c == "content" {
			good++
		}
	}
	if good == len(copies) {
		return bare
	}
	var parts []string
	for _, w := range [][2]string{
		{"failed", "failed"}, {"could_not_complete", "could not complete"}, {"unchecked", "never checked"},
		{"existence", "existence only"}, {"attested", "attested only"},
	} {
		n := 0
		for _, c := range copies {
			if c == w[0] {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, w[1]))
		}
	}
	noun := " copies"
	if len(copies) == 1 {
		noun = " copy"
	}
	return fmt.Sprintf("%s · %d of %d%s, %s", bare, good, len(copies), noun, strings.Join(parts, ", "))
}

func claimWords(kind string, c AssuranceClaim, copies []string, basis PolicyBasis, now time.Time) string {
	state := c.rendered()
	var w string
	switch {
	case kind == "content" && state == "pass":
		w = contentPassWords(copies)
	case kind == "recovery" && state == "never_configured":
		w = neverConfiguredWords[basis]
		if w == "" {
			w = neverConfiguredWords[PolicyNone]
		}
	case kind == "content":
		w = contentWords[state]
	default:
		w = recoveryWords[state]
	}
	if w == "" {
		w = "unknown"
	}
	if withAge(state) {
		if age := assuranceAge(c.At, now); age != "" {
			w += " " + age
		}
	}
	return w
}

// ListAssurance classifies every backup the way the Dashboard's Assurance
// tile does. due is the scrub schedule (AssuranceDue); policies is each set's
// recovery-test policy, a set missing from it reading as PolicyNone with
// tests on. Rows are sorted degraded first, then overdue, then due, then the
// rest, each by id.
func ListAssurance(as []Artifact, due []AssuranceDueItem, policies map[string]SetRecoveryPolicy, now time.Time) []AssuranceListing {
	dueBy := make(map[string]*dueFact, len(due))
	for _, d := range due {
		dueBy[d.ArtifactID] = &dueFact{overdue: d.Overdue, never: d.LastAssuredAt.IsZero()}
	}
	type ranked struct {
		AssuranceListing
		rank int
	}
	order := AssuranceBuckets()
	rows := make([]ranked, 0, len(as))
	for _, a := range as {
		live := liveCopies(a)
		policy := policies[a.BackupSetID]
		if policy.Basis == "" {
			policy.Basis = PolicyNone
		}
		content := contentClaimOf(a, live, dueBy[a.ID])
		recovery := recoveryClaimOf(live, policy)
		buckets := bucketsOfClaims(content.State, recovery.State)
		rank := len(order)
		for i, b := range order {
			if slices.Contains(buckets, b) {
				rank = i
				break
			}
		}
		rows = append(rows, ranked{AssuranceListing{
			ArtifactID: a.ID, ArtifactName: a.Name, BackupSetID: a.BackupSetID,
			Buckets: buckets, Content: content, Recovery: recovery,
			Reason: "Content: " + claimWords("content", content, copyContent(live), policy.Basis, now) +
				". Recovery: " + claimWords("recovery", recovery, nil, policy.Basis, now) + ".",
		}, rank})
	}
	slices.SortStableFunc(rows, func(x, y ranked) int {
		if x.rank != y.rank {
			return x.rank - y.rank
		}
		return strings.Compare(x.ArtifactID, y.ArtifactID)
	})
	out := make([]AssuranceListing, len(rows))
	for i, r := range rows {
		out[i] = r.AssuranceListing
	}
	return out
}
