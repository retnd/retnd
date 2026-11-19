package health

import (
	"testing"

	"github.com/retnd/retnd/core/internal/model"
)

// #1304: a dropped external reference leaves copies unreadable whatever the
// policy says now, so it degrades the section even when no set requires
// encryption any more. Written by reading; not run.
func TestAnExternalKeyNoReferenceYieldsDegradesTheSectionWhateverTheMode_1304(t *testing.T) {
	copyRef := model.CopyRef{Artifact: model.ArtifactID{Set: model.BackupSetID{Source: "production", Set: "pg"}, Name: "a.dump"}, Medium: "warm"}
	risk := ExternalKeyAtRisk{KeyID: "x0123456789abcdef", Copies: []model.CopyRef{copyRef}}

	for _, enabled := range []bool{false, true} {
		h := KeyCustodyHealth{Enabled: enabled, ActiveKeyID: "k1", BundleConfirmed: true, ExternalKeysAtRisk: []ExternalKeyAtRisk{risk}}
		if !h.Degraded() || h.OK() {
			t.Errorf("enabled=%v: an unreadable external key with a copy did not degrade the section", enabled)
		}
	}
	// Control: the same section without the risk is not degraded, so the
	// assertion above can fail.
	if (KeyCustodyHealth{Enabled: true, ActiveKeyID: "k1", BundleConfirmed: true}).Degraded() {
		t.Error("control: a confirmed, resolvable section reads as degraded")
	}
	if (KeyCustodyHealth{}).Degraded() {
		t.Error("the zero section reads as degraded")
	}
}
