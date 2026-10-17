package service

import (
	"time"

	"github.com/retnd/retnd/core/apicontract"
)

// The custom validator bodies as the API serves them (#1203), here rather
// than in the web host so `retnd assurance validators --json` prints the same
// objects through the same projection.

// CustomValidatorWire is the API body for one validator, used by the route and by
// the CLI's --json.
func CustomValidatorWire(v CustomValidator) apicontract.CustomValidator {
	out := apicontract.CustomValidator{
		Name: v.Name, Pattern: v.Pattern, Anchored: v.Anchored, BackupSetID: v.BackupSetID, Scope: v.Scope,
		ScriptFile: v.ScriptFile, ScriptPresent: v.ScriptPresent, ScriptSha256: v.ScriptSHA256, ScriptSizeBytes: v.ScriptSize,
		PinnedSha256: v.ScriptPinnedSHA256, ScriptState: v.ScriptState,
		MatchCount: int64(v.Matches), MatchSets: append([]string{}, v.MatchSets...), ExampleName: v.ExampleName,
	}
	if !v.UploadedAt.IsZero() {
		out.UploadedAt = v.UploadedAt.UTC().Format(time.RFC3339)
	}
	if v.LastResult != nil {
		out.LastResult = &apicontract.CustomValidatorResult{State: v.LastResult.State, At: v.LastResult.At.UTC().Format(time.RFC3339), ArtifactID: v.LastResult.Artifact}
	}
	return out
}

// CustomValidatorsWire is the list body.
func CustomValidatorsWire(l CustomValidators) apicontract.ListCustomValidatorsResponse {
	out := apicontract.ListCustomValidatorsResponse{
		Validators: make([]apicontract.CustomValidator, 0, len(l.Validators)),
		Directory:  l.Directory, WorkflowRoot: l.WorkflowRoot, Available: l.Available, UnavailableReason: l.UnavailableReason,
		DirectoryRefused: l.DirectoryRefused,
		RunnerConfigured: l.RunnerConfigured, MaxValidators: int64(l.MaxValidators), MaxScriptBytes: l.MaxScriptBytes,
		MaxPatternBytes: int64(l.MaxPatternBytes),
		Ambiguities:     make([]apicontract.CustomValidatorAmbiguity, 0, len(l.Ambiguities)),
	}
	for _, a := range l.Ambiguities {
		out.Ambiguities = append(out.Ambiguities, apicontract.CustomValidatorAmbiguity{
			BackupSetID: a.BackupSetID, Validators: append([]string{}, a.Validators...), Example: a.Example,
		})
	}
	for _, v := range l.Validators {
		out.Validators = append(out.Validators, CustomValidatorWire(v))
	}
	if r := l.Resolution; r != nil {
		out.Resolution = &apicontract.CustomValidatorResolution{
			ArtifactID: r.ArtifactID, Validator: r.Validator, Ambiguous: append([]string{}, r.Ambiguous...), Fallback: r.Fallback,
		}
	}
	return out
}

// CustomValidatorPreviewWire is the preview body.
func CustomValidatorPreviewWire(p CustomValidatorPreview) apicontract.CustomValidatorPreview {
	out := apicontract.CustomValidatorPreview{
		Valid: p.Valid, Error: p.Error, Anchored: p.Anchored, MatchCount: int64(p.Matches),
		MatchSets: append([]string{}, p.MatchSets...), Sample: append([]string{}, p.Sample...),
		Conflicts: make([]apicontract.CustomValidatorConflict, 0, len(p.Conflicts)),
	}
	for _, c := range p.Conflicts {
		out.Conflicts = append(out.Conflicts, apicontract.CustomValidatorConflict{Validator: c.Validator, Example: c.Example})
	}
	return out
}

// CustomValidatorPatternsWire is the suggestions body.
func CustomValidatorPatternsWire(p CustomValidatorPatterns) apicontract.CustomValidatorPatternsResponse {
	out := apicontract.CustomValidatorPatternsResponse{BackupSetID: p.BackupSetID, Patterns: make([]apicontract.CustomValidatorPattern, 0, len(p.Patterns))}
	for _, s := range p.Patterns {
		out.Patterns = append(out.Patterns, apicontract.CustomValidatorPattern{
			Pattern: s.Pattern, Example: s.Example, MatchCount: int64(s.Matches), MatchSets: append([]string{}, s.MatchSets...), Validator: s.Validator,
		})
	}
	return out
}

// ValidatorScriptWire is a script body.
func ValidatorScriptWire(s ValidatorScript) apicontract.ValidatorScriptResponse {
	return apicontract.ValidatorScriptResponse{FileName: s.FileName, Sha256: s.SHA256, SizeBytes: s.SizeBytes, Content: s.Content}
}
