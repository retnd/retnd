package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
)

// Issue #1528's two operations, translated for the layers outside core. The
// rule lives in internal/app (retire.go there); this file only carries its
// answers across the module boundary, so the API and the CLI read the same
// refusal, the same source check and the same sentences.

// RetireRefusal is a named reason a retirement was not made. It is an error,
// so the API layer can match it with errors.As and answer Code with
// HTTPStatus.
type RetireRefusal struct {
	Code            string
	Sentence        string
	OverridableWith string
}

func (r *RetireRefusal) Error() string { return r.Sentence }

// RetireRequest mirrors app.RetireRequest. ActorKind is "operator" or
// "agent"; Actor names who.
type RetireRequest struct {
	SupersededBy        string
	NoSuccessor         bool
	AcceptSourcePresent bool
	// AcceptUnverifiedCopy is issue #1594's acknowledgement: the row's own
	// copy at its final name could not be verified and the source is gone.
	AcceptUnverifiedCopy bool
	Note                 string
	ActorKind            string
	Actor                string
}

// RetryRefusal is a named reason a retry was not made (issue #1594's
// RETRY_OWN_COPY_UNVERIFIED), so the API layer can answer it by code with the
// engine's sentence.
type RetryRefusal struct {
	Code     string
	Sentence string
}

func (r *RetryRefusal) Error() string { return r.Sentence }

// RetirementSourceCheck is what the source said about the object just now.
type RetirementSourceCheck struct {
	Result   string
	Category string
	Sentence string
}

// RetirementSuccessor is the newer good backup a retirement names.
type RetirementSuccessor struct {
	Name         string
	State        string
	DiscoveredAt time.Time
}

// Retirement is one recorded retirement.
type Retirement struct {
	RetiredAt     time.Time
	ActorKind     string
	Actor         string
	SourceCheck   string
	SupersededBy  string
	FailureReason string
	Note          string
}

// RetirementPreview is what the retire dialog shows before anybody acts.
type RetirementPreview struct {
	Artifact          string
	State             string
	FailureReason     string
	SourceCheck       RetirementSourceCheck
	ProposedSuccessor *RetirementSuccessor
	// SuccessorCandidates is every newer good backup an operator may name
	// instead, newest first.
	SuccessorCandidates []RetirementSuccessor
	Refusal             *RetireRefusal
	Effects             []string
	Retirement          *Retirement
}

// RetireResult is what a successful retirement did.
type RetireResult struct {
	Artifact       string
	State          string
	Retirement     Retirement
	PartialRemoved bool
}

// ArtifactStates is the lifecycle vocabulary, for a caller outside core that
// has to validate a state filter without importing internal/lifecycle.
func ArtifactStates() []string {
	out := make([]string, 0, len(lifecycle.AllStates))
	for _, s := range lifecycle.AllStates {
		out = append(out, string(s))
	}
	return out
}

func toServiceRetirement(r state.Retirement) Retirement {
	return Retirement{RetiredAt: r.RetiredAt, ActorKind: r.ActorKind, Actor: r.Actor, SourceCheck: r.SourceCheck,
		SupersededBy: r.SupersededBy, FailureReason: r.FailureReason, Note: r.Note}
}

func toAppRetireRequest(req RetireRequest) app.RetireRequest {
	return app.RetireRequest{SupersededBy: req.SupersededBy, NoSuccessor: req.NoSuccessor, AcceptSourcePresent: req.AcceptSourcePresent,
		Note: req.Note, ActorKind: req.ActorKind, Actor: req.Actor}
}

// retireError maps the app layer's refusals onto this package's sentinels,
// shared by both operations so the preview and the action cannot drift.
func retireError(id string, err error) error {
	var refusal *app.RetireRefusal
	switch {
	case errors.Is(err, state.ErrArtifactNotFound):
		return fmt.Errorf("%w: %s", ErrArtifactNotFound, id)
	case errors.Is(err, app.ErrNotFailed):
		return fmt.Errorf("%w: %s", ErrArtifactNotFailed, id)
	case isUnconfiguredSet(err):
		return fmt.Errorf("%w: %s", ErrBackupSetNotFound, id)
	case errors.As(err, &refusal):
		return &RetireRefusal{Code: refusal.Code, Sentence: refusal.Sentence, OverridableWith: refusal.OverridableWith}
	}
	return fmt.Errorf("service: retiring %s: %w", id, err)
}

// PreviewRetirement answers what RetireArtifact would do, without doing it.
func (b *BackupService) PreviewRetirement(ctx context.Context, id string, req RetireRequest) (RetirementPreview, error) {
	artifactID, err := app.ParseArtifactID(id)
	if err != nil {
		return RetirementPreview{}, fmt.Errorf("%w: %s", ErrArtifactNotFound, id)
	}
	p, err := b.state.Load().inner.PreviewRetirement(ctx, artifactID, toAppRetireRequest(req))
	if err != nil {
		return RetirementPreview{}, retireError(id, err)
	}
	out := RetirementPreview{
		Artifact: id, State: string(p.State), FailureReason: p.FailureReason,
		SourceCheck: RetirementSourceCheck{Result: p.SourceCheck.Result, Category: p.SourceCheck.Category, Sentence: p.SourceCheck.Sentence},
		Effects:     p.Effects,
	}
	for _, c := range p.SuccessorCandidates {
		out.SuccessorCandidates = append(out.SuccessorCandidates, RetirementSuccessor{Name: c.Name, State: string(c.State), DiscoveredAt: c.DiscoveredAt})
	}
	if p.ProposedSuccessor != nil {
		out.ProposedSuccessor = &RetirementSuccessor{Name: p.ProposedSuccessor.Name, State: string(p.ProposedSuccessor.State),
			DiscoveredAt: p.ProposedSuccessor.DiscoveredAt}
	}
	if p.Refusal != nil {
		out.Refusal = &RetireRefusal{Code: p.Refusal.Code, Sentence: p.Refusal.Sentence, OverridableWith: p.Refusal.OverridableWith}
	}
	if p.Retirement != nil {
		r := toServiceRetirement(*p.Retirement)
		out.Retirement = &r
	}
	return out, nil
}

// RetireArtifact retires one FAILED backup (issue #1528).
func (b *BackupService) RetireArtifact(ctx context.Context, id string, req RetireRequest) (RetireResult, error) {
	artifactID, err := app.ParseArtifactID(id)
	if err != nil {
		return RetireResult{}, fmt.Errorf("%w: %s", ErrArtifactNotFound, id)
	}
	res, err := b.state.Load().inner.RetireArtifact(ctx, artifactID, toAppRetireRequest(req))
	if err != nil {
		return RetireResult{}, retireError(id, err)
	}
	return RetireResult{Artifact: id, State: string(lifecycle.Retired), Retirement: toServiceRetirement(res.Retirement),
		PartialRemoved: res.PartialRemoved}, nil
}
