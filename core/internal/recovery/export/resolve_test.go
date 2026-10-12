package export

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/archive"
	"github.com/retnd/retnd/core/internal/placement"
)

func copyOn(medium, class string) Copy {
	return Copy{
		Medium:            medium,
		Local:             medium == "local",
		Active:            true,
		VerificationClass: class,
		Access:            archive.Immediate,
		Chargeable:        medium != "local",
	}
}

func TestResolvePrefersTheStrongestRecordedVerificationClass(t *testing.T) {
	copies := []Copy{
		copyOn("local", "existence"),
		copyOn("cold", "content"),
		copyOn("warm", "attested"),
	}
	got, err := Resolve(copies, ResolveRequest{Copy: BestAvailable, AcknowledgeCost: true})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Copy.Medium != "cold" {
		t.Fatalf("picked %q, want the content-verified copy %q", got.Copy.Medium, "cold")
	}
}

func TestResolveEmptySelectorMeansBestAvailable(t *testing.T) {
	got, err := Resolve([]Copy{copyOn("local", "content")}, ResolveRequest{})
	if err != nil || got.Copy.Medium != "local" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestResolveBreaksAClassTieTowardTheFreeCopyThenTheNewerVerification(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(48 * time.Hour)

	a, b := copyOn("a", "content"), copyOn("b", "content")
	a.VerifiedAt, b.VerifiedAt = &older, &newer
	got, err := Resolve([]Copy{a, b}, ResolveRequest{AcknowledgeCost: true})
	if err != nil || got.Copy.Medium != "b" {
		t.Fatalf("newer verification should win a tie: %+v, %v", got, err)
	}

	got, err = Resolve([]Copy{a, copyOn("local", "content")}, ResolveRequest{AcknowledgeCost: true})
	if err != nil || got.Copy.Medium != "local" {
		t.Fatalf("a free copy should win a class tie: %+v, %v", got, err)
	}
}

func TestResolveIsDeterministicOnAFullTie(t *testing.T) {
	x, y := copyOn("x", "content"), copyOn("y", "content")
	for i := 0; i < 20; i++ {
		got, err := Resolve([]Copy{y, x}, ResolveRequest{AcknowledgeCost: true})
		if err != nil || got.Copy.Medium != "x" {
			t.Fatalf("run %d: %+v, %v", i, got, err)
		}
	}
}

func TestResolveNeverReadsACopyTheJournalNoLongerWants(t *testing.T) {
	gone := copyOn("local", "content")
	gone.Active = false
	live := copyOn("cold", "existence")
	got, err := Resolve([]Copy{gone, live}, ResolveRequest{AcknowledgeCost: true})
	if err != nil || got.Copy.Medium != "cold" {
		t.Fatalf("an inactive copy was considered: %+v, %v", got, err)
	}
	if _, err := Resolve([]Copy{gone}, ResolveRequest{}); !errors.Is(err, ErrNoCopy) {
		t.Fatalf("only an inactive copy: want ErrNoCopy, got %v", err)
	}
}

func TestResolveNoCopiesAtAll(t *testing.T) {
	if _, err := Resolve(nil, ResolveRequest{}); !errors.Is(err, ErrNoCopy) {
		t.Fatalf("want ErrNoCopy, got %v", err)
	}
}

func TestResolveNamedCopyMustExist(t *testing.T) {
	_, err := Resolve([]Copy{copyOn("local", "content")}, ResolveRequest{Copy: "nowhere"})
	if !errors.Is(err, ErrCopyNotFound) {
		t.Fatalf("want ErrCopyNotFound, got %v", err)
	}
}

func TestResolveNamedCopyIsHonouredEvenWhenAStrongerOneExists(t *testing.T) {
	got, err := Resolve([]Copy{copyOn("local", "content"), copyOn("cold", "existence")},
		ResolveRequest{Copy: "cold", AcknowledgeCost: true})
	if err != nil || got.Copy.Medium != "cold" {
		t.Fatalf("explicit selector ignored: %+v, %v", got, err)
	}
}

func TestResolveRefusesAChargeableReadWithoutAcknowledgement(t *testing.T) {
	_, err := Resolve([]Copy{copyOn("cold", "content")}, ResolveRequest{})
	if !errors.Is(err, ErrCostNotAcknowledged) {
		t.Fatalf("want ErrCostNotAcknowledged, got %v", err)
	}
	var cr *CostRefusal
	if !errors.As(err, &cr) || cr.Medium != "cold" {
		t.Fatalf("refusal must name the copy: %v", err)
	}
	if _, err := Resolve([]Copy{copyOn("cold", "content")}, ResolveRequest{AcknowledgeCost: true}); err != nil {
		t.Fatalf("acknowledged read refused: %v", err)
	}
}

func TestResolveLocalCopyNeedsNoAcknowledgement(t *testing.T) {
	if _, err := Resolve([]Copy{copyOn("local", "")}, ResolveRequest{}); err != nil {
		t.Fatalf("a read off this host's disk costs nothing: %v", err)
	}
}

func TestResolveAFreeCopyIsChosenOverAChargeableOneWithoutAcknowledgement(t *testing.T) {
	got, err := Resolve([]Copy{copyOn("cold", "content"), copyOn("local", "existence")}, ResolveRequest{})
	if err != nil || got.Copy.Medium != "local" {
		t.Fatalf("without an acknowledgement the free copy is the only one allowed: %+v, %v", got, err)
	}
}

func TestResolveCostRefusalFollowsTheLadderRule(t *testing.T) {
	// The resolver defers to placement's own statement of what costs
	// egress, so a change there moves this refusal with it.
	if !placement.Content.CostsEgress() {
		t.Fatal("test premise: content verification costs egress")
	}
}

func archiveCopy(medium string) Copy {
	c := copyOn(medium, "")
	c.Access = archive.RequiresRestore
	c.StorageClass = "GLACIER"
	return c
}

func TestResolveArchiveOnlyIsRefusedForCostAndSaysSo(t *testing.T) {
	got, err := Resolve([]Copy{archiveCopy("deep")}, ResolveRequest{Copy: BestAvailable})
	if !errors.Is(err, ErrCostNotAcknowledged) {
		t.Fatalf("want a cost refusal, got %+v, %v", got, err)
	}
	if !strings.Contains(err.Error(), "restore") {
		t.Fatalf("the refusal must name what it costs: %v", err)
	}
	if got.NeedsThaw {
		t.Fatal("a refusal must not ask for a thaw")
	}
}

func TestResolveArchiveOnlyAcknowledgedAsksForExactlyOneThaw(t *testing.T) {
	got, err := Resolve([]Copy{archiveCopy("deep"), archiveCopy("deeper")}, ResolveRequest{AcknowledgeCost: true})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !got.NeedsThaw || got.Copy.Medium != "deep" {
		t.Fatalf("want one thaw of the first archive copy, got %+v", got)
	}
}

func TestResolveAReadableCopyBeatsAnArchiveOne(t *testing.T) {
	got, err := Resolve([]Copy{archiveCopy("deep"), copyOn("warm", "existence")}, ResolveRequest{AcknowledgeCost: true})
	if err != nil || got.Copy.Medium != "warm" || got.NeedsThaw {
		t.Fatalf("a readable copy should be used instead of paying for a thaw: %+v, %v", got, err)
	}
}

func TestResolveRestoringAndUnreachableAreNotReadableAndStartNothing(t *testing.T) {
	for _, st := range []archive.State{archive.Restoring, archive.Unreachable} {
		c := copyOn("cold", "content")
		c.Access = st
		got, err := Resolve([]Copy{c}, ResolveRequest{AcknowledgeCost: true})
		if !errors.Is(err, ErrNotReadable) {
			t.Fatalf("%s: want ErrNotReadable, got %+v, %v", st, got, err)
		}
		if got.NeedsThaw {
			t.Fatalf("%s: must not request a thaw", st)
		}
	}
}

func TestResolveRanksAnUnknownVerificationBelowAnyRecordedClassButStillRecoversIt(t *testing.T) {
	// A catalog rebuilt from sidecars knows nothing about when or how any
	// copy was last verified: every class is empty and every time is nil.
	unknown := copyOn("local", "")
	existence := copyOn("cold", "existence")

	got, err := Resolve([]Copy{unknown, existence}, ResolveRequest{AcknowledgeCost: true})
	if err != nil || got.Copy.Medium != "cold" {
		t.Fatalf("a copy that has achieved some class should outrank one that never has: %+v, %v", got, err)
	}
	got, err = Resolve([]Copy{unknown}, ResolveRequest{})
	if err != nil || got.Copy.Medium != "local" {
		t.Fatalf("a copy of unknown verification age must still be recoverable (the bytes are verified on the way out): %+v, %v", got, err)
	}
	got, err = Resolve([]Copy{unknown, existence}, ResolveRequest{})
	if err != nil || got.Copy.Medium != "local" {
		t.Fatalf("without the cost acknowledgement the free, unverified copy is the one allowed: %+v, %v", got, err)
	}
}
