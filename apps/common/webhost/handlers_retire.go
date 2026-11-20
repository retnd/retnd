package webhost

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/service"
)

// Issue #1528: retiring a FAILED backup whose source is gone, and the
// backups list's state filter and cursor. The rule is internal/app's; these
// handlers translate it, so the browser, the terminal and an agent read the
// same refusal and the same sentences.

// maxRetireBodyBytes bounds POST .../retire's body: four small fields.
const maxRetireBodyBytes = 8 << 10

// retireRequest is POST .../retire's optional body.
type retireRequest struct {
	SupersededBy        string `json:"superseded_by,omitempty"`
	NoSuccessor         bool   `json:"no_successor,omitempty"`
	AcceptSourcePresent bool   `json:"accept_source_present,omitempty"`
	Note                string `json:"note,omitempty"`
}

type retirementRecordResponse struct {
	RetiredAt     string `json:"retired_at"`
	ActorKind     string `json:"actor_kind"`
	Actor         string `json:"actor"`
	SourceCheck   string `json:"source_check"`
	SupersededBy  string `json:"superseded_by"`
	FailureReason string `json:"failure_reason"`
	Note          string `json:"note"`
}

type retireArtifactResponse struct {
	Artifact       string                   `json:"artifact"`
	State          string                   `json:"state"`
	Retirement     retirementRecordResponse `json:"retirement"`
	PartialRemoved bool                     `json:"partial_removed"`
}

type retirementSuccessorResponse struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	DiscoveredAt string `json:"discovered_at"`
}

type retirementRefusalResponse struct {
	Code            string `json:"code"`
	Sentence        string `json:"sentence"`
	OverridableWith string `json:"overridable_with"`
}

type retirementPreviewResponse struct {
	Artifact            string                       `json:"artifact"`
	State               string                       `json:"state"`
	FailureReason       string                       `json:"failure_reason"`
	SourceCheck         string                       `json:"source_check"`
	SourceCheckCategory string                       `json:"source_check_category"`
	SourceCheckSentence string                       `json:"source_check_sentence"`
	ProposedSuccessor   *retirementSuccessorResponse `json:"proposed_successor,omitempty"`
	// SuccessorCandidates is every newer good backup the dialog's "Replaced
	// by" may name, newest first (issue #1528's review).
	SuccessorCandidates []retirementSuccessorResponse `json:"successor_candidates"`
	Refusal             *retirementRefusalResponse    `json:"refusal,omitempty"`
	Effects             []string                      `json:"effects"`
	Retirement          *retirementRecordResponse     `json:"retirement,omitempty"`
	Command             string                        `json:"command"`
}

func toRetirementRecordResponse(r service.Retirement) retirementRecordResponse {
	return retirementRecordResponse{RetiredAt: r.RetiredAt.UTC().Format(time.RFC3339), ActorKind: r.ActorKind, Actor: r.Actor, SourceCheck: r.SourceCheck,
		SupersededBy: r.SupersededBy, FailureReason: r.FailureReason, Note: r.Note}
}

// retireRequestFrom reads the preview's query or the action's body into one
// request. The preview takes the same four fields as query parameters, so
// the dialog can ask "and with this acknowledgement?" before acting.
func retireRequestFromQuery(r *http.Request) service.RetireRequest {
	q := r.URL.Query()
	return service.RetireRequest{
		SupersededBy:        q.Get("superseded_by"),
		NoSuccessor:         q.Get("no_successor") == "true",
		AcceptSourcePresent: q.Get("accept_source_present") == "true",
	}
}

// retireCommand is the `retnd retire` line the action is equivalent to.
func retireCommand(r *http.Request, req retireRequest) string {
	body, _ := json.Marshal(req)
	return cliecho.Echo(cliecho.Action{
		Method: http.MethodPost, Route: "/backups/{source}/{set}/{name}/retire",
		Params: map[string]string{"source": chi.URLParam(r, "source"), "set": chi.URLParam(r, "set"), "name": chi.URLParam(r, "name")},
		Body:   body,
	}).Shell()
}

// previewRetirement is GET .../retire: what retiring this backup would do.
// Read-only, though it asks the source about the object.
func (h *handlers) previewRetirement(w http.ResponseWriter, r *http.Request) {
	req := retireRequestFromQuery(r)
	p, err := h.backend.PreviewRetirement(r.Context(), artifactIDFrom(r), req)
	if err != nil {
		h.writeRetireError(w, r, err, "failed to preview the retirement")
		return
	}
	resp := retirementPreviewResponse{
		Artifact: p.Artifact, State: p.State, FailureReason: p.FailureReason,
		SourceCheck: p.SourceCheck.Result, SourceCheckCategory: p.SourceCheck.Category, SourceCheckSentence: p.SourceCheck.Sentence,
		Effects:             append(make([]string, 0, len(p.Effects)), p.Effects...),
		SuccessorCandidates: make([]retirementSuccessorResponse, 0, len(p.SuccessorCandidates)),
		Command:             retireCommand(r, retireRequest{SupersededBy: req.SupersededBy, NoSuccessor: req.NoSuccessor, AcceptSourcePresent: req.AcceptSourcePresent}),
	}
	for _, c := range p.SuccessorCandidates {
		resp.SuccessorCandidates = append(resp.SuccessorCandidates, retirementSuccessorResponse{Name: c.Name, State: c.State, DiscoveredAt: c.DiscoveredAt.UTC().Format(time.RFC3339)})
	}
	if s := p.ProposedSuccessor; s != nil {
		resp.ProposedSuccessor = &retirementSuccessorResponse{Name: s.Name, State: s.State, DiscoveredAt: s.DiscoveredAt.UTC().Format(time.RFC3339)}
	}
	if f := p.Refusal; f != nil {
		resp.Refusal = &retirementRefusalResponse{Code: f.Code, Sentence: f.Sentence, OverridableWith: f.OverridableWith}
	}
	if p.Retirement != nil {
		rec := toRetirementRecordResponse(*p.Retirement)
		resp.Retirement = &rec
	}
	writeJSON(w, http.StatusOK, resp)
}

// retireArtifact is POST .../retire: retire one FAILED backup.
//
// An agent's approved replay may not carry either acknowledgement. The
// proposer already refuses them before a person is asked (the catalogue's
// AgentMayNotSet); this is the same refusal again, so a replay that somehow
// carried one still cannot act on it.
func (h *handlers) retireArtifact(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRetireBodyBytes)
	var req retireRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeDecodeError(w, err, maxRetireBodyBytes)
		return
	}
	sreq := service.RetireRequest{SupersededBy: req.SupersededBy, NoSuccessor: req.NoSuccessor,
		AcceptSourcePresent: req.AcceptSourcePresent, Note: req.Note, ActorKind: "operator"}
	if p, ok := agentReplayFrom(r.Context()); ok {
		if req.NoSuccessor || req.AcceptSourcePresent {
			writeError(w, http.StatusForbidden, "AGENT_RETIRE_ACK_REFUSED",
				"an agent may propose only the retirement the engine itself would make; the acknowledgements are a person's")
			return
		}
		sreq.ActorKind, sreq.Actor = "agent", "agent:"+p.Name
	} else {
		sreq.Actor = actorFromContext(r.Context())
	}
	res, err := h.backend.RetireArtifact(r.Context(), artifactIDFrom(r), sreq)
	if err != nil {
		h.writeRetireError(w, r, err, "failed to retire the backup")
		return
	}
	writeJSON(w, http.StatusOK, retireArtifactResponse{Artifact: res.Artifact, State: res.State,
		Retirement: toRetirementRecordResponse(res.Retirement), PartialRemoved: res.PartialRemoved})
}

// writeRetireError answers a retirement refusal with its own code: every one
// is a 409 an operator can act on, and the sentence is the engine's.
func (h *handlers) writeRetireError(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	var refusal *service.RetireRefusal
	switch {
	case errors.As(err, &refusal):
		// Spelled out code by code: every one is declared on this route in
		// the contract, and a code the engine grows later is an internal
		// error here until it is declared, never a silent new wire value.
		switch refusal.Code {
		case "RETIRE_OWN_COPY_AT_FINAL_NAME":
			writeError(w, http.StatusConflict, "RETIRE_OWN_COPY_AT_FINAL_NAME", refusal.Sentence)
		case "RETIRE_SOURCE_PRESENT":
			writeError(w, http.StatusConflict, "RETIRE_SOURCE_PRESENT", refusal.Sentence)
		case "RETIRE_SOURCE_UNCONFIRMED":
			writeError(w, http.StatusConflict, "RETIRE_SOURCE_UNCONFIRMED", refusal.Sentence)
		case "RETIRE_NO_SUCCESSOR":
			writeError(w, http.StatusConflict, "RETIRE_NO_SUCCESSOR", refusal.Sentence)
		case "RETIRE_SUCCESSOR_INVALID":
			writeError(w, http.StatusConflict, "RETIRE_SUCCESSOR_INVALID", refusal.Sentence)
		default:
			h.internalError(w, r, "INTERNAL", fallback, err)
		}
	case errors.Is(err, service.ErrArtifactNotFailed):
		writeError(w, http.StatusConflict, "ARTIFACT_NOT_FAILED", "only a failed backup can be retired")
	default:
		h.writeArtifactActionError(w, r, err, fallback)
	}
}

// --- the backups list's filter and cursor ---

// maxArtifactPage caps one page of GET /backups or GET /quarantine. A larger
// limit is clamped, not refused, as GET /activity does.
const maxArtifactPage = 5000

// pageArtifacts applies the state filter and, when limit is set, keyset
// paging over the (source, set, name) order, returning the page and the
// cursor for the next one (empty when there is none). Without a limit the
// list and its order are exactly what they were before issue #1528.
func pageArtifacts(r *http.Request, all []service.Artifact) ([]service.Artifact, string, bool) {
	q := r.URL.Query()
	var states map[string]bool
	if raw := q.Get("state"); raw != "" {
		known := map[string]bool{}
		for _, s := range service.ArtifactStates() {
			known[s] = true
		}
		states = map[string]bool{}
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if !known[s] {
				return nil, "", false
			}
			states[s] = true
		}
	}
	out := all
	if states != nil {
		out = make([]service.Artifact, 0, len(all))
		for _, a := range all {
			if states[a.State] {
				out = append(out, a)
			}
		}
	}
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, "", false
		}
		limit = n
	}
	if limit <= 0 {
		return out, "", true
	}
	if limit > maxArtifactPage {
		limit = maxArtifactPage
	}
	sorted := append([]service.Artifact(nil), out...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	if raw := q.Get("before"); raw != "" {
		after, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return nil, "", false
		}
		i := sort.Search(len(sorted), func(i int) bool { return sorted[i].ID > string(after) })
		sorted = sorted[i:]
	}
	if len(sorted) <= limit {
		return sorted, "", true
	}
	page := sorted[:limit]
	return page, base64.RawURLEncoding.EncodeToString([]byte(page[len(page)-1].ID)), true
}
