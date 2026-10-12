package app

import (
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/revalidate"
)

// Wire is the scrub result as the API contract states it
// (apicontract.ScrubResult). `retnd scrub --json` prints exactly this, and
// any route that reports a scrub reuses the same type, so the CLI and the
// API cannot come to describe one pass in two shapes.
//
// Every list is non-nil, so a pass that found nothing says `[]` and not
// `null`: a reader branching on the first would otherwise have to branch on
// the second as well.
func (s ScrubSummary) Wire() apicontract.ScrubResult {
	out := apicontract.ScrubResult{
		Trigger: s.Trigger, Scope: s.Scope,
		Selected: int64(s.Selected), Checked: int64(s.Checked), Passed: int64(s.Passed), Failed: int64(s.Failed),
		CouldNotComplete: int64(s.CouldNotComplete), RequiresRestore: int64(s.RequiresRestore),
		SkippedHeld: int64(s.SkippedHeld), SkippedLeased: int64(s.SkippedLeased), Refused: int64(s.Refused),
		BytesRead: s.BytesRead, PassID: s.PassID,
		Sets: make([]apicontract.ScrubSetResult, 0, len(s.Sets)),
	}
	for _, sr := range s.Sets {
		set := apicontract.ScrubSetResult{
			BackupSetID: sr.Set.String(),
			Copies:      []apicontract.ScrubCopyOutcome{},
			Errors:      []apicontract.ScrubArtifactError{},
			Skipped:     []apicontract.ScrubSkip{},
		}
		if sr.Err != nil {
			set.Error = sr.Err.Error()
		}
		for _, f := range sr.Report.Findings {
			for _, c := range f.Copies {
				set.Copies = append(set.Copies, wireCopy(f.Artifact.String(), c))
			}
		}
		for _, e := range sr.Report.Errors {
			for _, c := range e.Copies {
				set.Copies = append(set.Copies, wireCopy(e.Artifact.String(), c))
			}
			set.Errors = append(set.Errors, apicontract.ScrubArtifactError{ArtifactID: e.Artifact.String(), Error: e.Err.Error()})
		}
		for _, sk := range sr.Report.Skipped {
			w := apicontract.ScrubSkip{Medium: sk.Medium, Reason: string(sk.Reason)}
			if sk.Artifact.Name != "" {
				w.ArtifactID = sk.Artifact.String()
			}
			set.Skipped = append(set.Skipped, w)
		}
		out.Sets = append(out.Sets, set)
	}
	return out
}

func wireCopy(artifact string, c revalidate.CopyResult) apicontract.ScrubCopyOutcome {
	return apicontract.ScrubCopyOutcome{
		ArtifactID: artifact, Medium: c.Medium, Verdict: string(c.Verdict),
		Class: string(c.Class), Reason: c.Reason, Refused: c.Refused,
	}
}
