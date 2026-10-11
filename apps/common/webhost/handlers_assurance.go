package webhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/retnd/retnd/core/service"
)

// EPIC Q, Q1.4 (#982): the assurance configuration and the scrub schedule.
//
// Three resources, all configuration or reads, none of them touching a
// backup datum, so the writes sit in the same CSRF-but-not-destructive tier
// as PATCH /settings and the workflow settings:
//
//	GET/PATCH        /settings/assurance
//	GET/PATCH/DELETE /backup-sets/{source}/{set}/assurance
//	GET              /assurance/due
//	POST             /backup-sets/{source}/{set}/recovery-test
//
// The set resource is PATCH plus DELETE rather than PUT. A per-set override
// merges field by field with the deployment's (a field the set does not name
// is inherited, so a global change still reaches it), which is a patch, and
// "go back to inheriting everything" cannot be a value on a request where an
// absent field already means "leave this alone", which is the DELETE.

const maxAssuranceBodyBytes = 1 << 12 // 4 KiB

type assuranceScrubBody struct {
	CadenceSeconds int64  `json:"cadence_seconds"`
	Strategy       string `json:"strategy"`
}

type assuranceRecoveryTestBody struct {
	CadenceSeconds int64  `json:"cadence_seconds"`
	Mode           string `json:"mode"`
	ValidatorID    string `json:"validator_id"`
}

type assuranceHostKnobBody struct {
	Name   string `json:"name"`
	PerSet bool   `json:"per_set"`
	Reason string `json:"reason"`
	Value  string `json:"value"`
}

type assuranceSettingsResponse struct {
	Configured             bool                      `json:"configured"`
	HostKnobs              []assuranceHostKnobBody   `json:"host_knobs"`
	MaxConcurrentFullReads int64                     `json:"max_concurrent_full_reads"`
	MaxPerDestination      int64                     `json:"max_per_destination"`
	RecoveryTest           assuranceRecoveryTestBody `json:"recovery_test"`
	Scrub                  assuranceScrubBody        `json:"scrub"`
	StagingRoot            string                    `json:"staging_root"`
	StagingTTLSeconds      int64                     `json:"staging_ttl_seconds"`
}

type updateAssuranceSettingsRequest struct {
	MaxConcurrentFullReads     *int64  `json:"max_concurrent_full_reads"`
	MaxPerDestination          *int64  `json:"max_per_destination"`
	RecoveryTestCadenceSeconds *int64  `json:"recovery_test_cadence_seconds"`
	RecoveryTestMode           *string `json:"recovery_test_mode"`
	RecoveryTestValidatorID    *string `json:"recovery_test_validator_id"`
	ScrubCadenceSeconds        *int64  `json:"scrub_cadence_seconds"`
	ScrubStrategy              *string `json:"scrub_strategy"`
	StagingRoot                *string `json:"staging_root"`
	StagingTTLSeconds          *int64  `json:"staging_ttl_seconds"`
}

func (r updateAssuranceSettingsRequest) namesNothing() bool {
	return r == updateAssuranceSettingsRequest{}
}

type backupSetAssuranceResponse struct {
	BackupSetID  string                    `json:"backup_set_id"`
	IsOverride   bool                      `json:"is_override"`
	RecoveryTest assuranceRecoveryTestBody `json:"recovery_test"`
	Scrub        assuranceScrubBody        `json:"scrub"`
}

type updateBackupSetAssuranceRequest struct {
	RecoveryTestCadenceSeconds *int64  `json:"recovery_test_cadence_seconds"`
	RecoveryTestMode           *string `json:"recovery_test_mode"`
	RecoveryTestValidatorID    *string `json:"recovery_test_validator_id"`
	ScrubCadenceSeconds        *int64  `json:"scrub_cadence_seconds"`
	ScrubStrategy              *string `json:"scrub_strategy"`
}

func (r updateBackupSetAssuranceRequest) namesNothing() bool {
	return r == updateBackupSetAssuranceRequest{}
}

type startRecoveryTestRequest struct {
	Acknowledged bool   `json:"acknowledged,omitempty"`
	ArtifactID   string `json:"artifact_id,omitempty"`
}

type recoveryTestStartedResponse struct {
	BackupSetID string `json:"backup_set_id"`
	OperationID string `json:"operation_id"`
}

type assuranceDueItemBody struct {
	ArtifactID     string `json:"artifact_id"`
	ArtifactName   string `json:"artifact_name"`
	BackupSetID    string `json:"backup_set_id"`
	Basis          string `json:"basis"`
	CadenceSeconds int64  `json:"cadence_seconds"`
	Due            bool   `json:"due"`
	LastAssuredAt  string `json:"last_assured_at,omitempty"`
	NextDueAt      string `json:"next_due_at"`
	Overdue        bool   `json:"overdue"`
}

type assuranceDueResponse struct {
	GeneratedAt string                 `json:"generated_at"`
	Items       []assuranceDueItemBody `json:"items"`
}

func (h *handlers) getAssuranceSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.backend.AssuranceSettings(r.Context())
	if err != nil {
		h.writeAssuranceError(w, r, err, "failed to read the assurance configuration")
		return
	}
	writeJSON(w, http.StatusOK, toAssuranceSettingsBody(s))
}

func (h *handlers) updateAssuranceSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAssuranceBodyBytes)
	var body updateAssuranceSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDecodeError(w, err, maxAssuranceBodyBytes)
		return
	}
	if body.namesNothing() {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "an assurance settings write must name at least one setting to change")
		return
	}
	req, err := toUpdateAssuranceSettingsRequest(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	s, err := h.backend.UpdateAssuranceSettings(r.Context(), req)
	if err != nil {
		h.writeAssuranceError(w, r, err, "failed to write the assurance configuration")
		return
	}
	writeJSON(w, http.StatusOK, toAssuranceSettingsBody(s))
}

func (h *handlers) getBackupSetAssurance(w http.ResponseWriter, r *http.Request) {
	s, err := h.backend.BackupSetAssurance(r.Context(), backupSetIDFromRoute(r))
	if err != nil {
		h.writeAssuranceError(w, r, err, "failed to read the backup set's assurance policy")
		return
	}
	writeJSON(w, http.StatusOK, toBackupSetAssuranceBody(s))
}

func (h *handlers) updateBackupSetAssurance(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAssuranceBodyBytes)
	var body updateBackupSetAssuranceRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDecodeError(w, err, maxAssuranceBodyBytes)
		return
	}
	if body.namesNothing() {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "an assurance write must name at least one setting to change")
		return
	}
	cad, err := assuranceSeconds(body.ScrubCadenceSeconds, "scrub_cadence_seconds")
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	rcad, err := assuranceSeconds(body.RecoveryTestCadenceSeconds, "recovery_test_cadence_seconds")
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	s, err := h.backend.UpdateBackupSetAssurance(r.Context(), backupSetIDFromRoute(r), service.UpdateBackupSetAssuranceRequest{
		ScrubCadence:            cad,
		ScrubStrategy:           body.ScrubStrategy,
		RecoveryTestMode:        body.RecoveryTestMode,
		RecoveryTestCadence:     rcad,
		RecoveryTestValidatorID: body.RecoveryTestValidatorID,
	})
	if err != nil {
		h.writeAssuranceError(w, r, err, "failed to write the backup set's assurance policy")
		return
	}
	writeJSON(w, http.StatusOK, toBackupSetAssuranceBody(s))
}

func (h *handlers) resetBackupSetAssurance(w http.ResponseWriter, r *http.Request) {
	s, err := h.backend.ResetBackupSetAssurance(r.Context(), backupSetIDFromRoute(r))
	if err != nil {
		h.writeAssuranceError(w, r, err, "failed to reset the backup set's assurance policy")
		return
	}
	writeJSON(w, http.StatusOK, toBackupSetAssuranceBody(s))
}

func (h *handlers) listAssuranceDue(w http.ResponseWriter, r *http.Request) {
	filter := service.AssuranceDueFilter{BackupSetID: r.URL.Query().Get("backup_set")}
	switch status := r.URL.Query().Get("status"); status {
	case "", "all":
	case "due":
		filter.DueOnly = true
	case "overdue":
		filter.OverdueOnly = true
	default:
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("status %q is not one of all, due or overdue", status))
		return
	}
	items, err := h.backend.AssuranceDue(r.Context(), filter)
	if err != nil {
		h.writeAssuranceError(w, r, err, "failed to compute the scrub schedule")
		return
	}
	out := assuranceDueResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Items: make([]assuranceDueItemBody, 0, len(items))}
	for _, it := range items {
		b := assuranceDueItemBody{
			ArtifactID: it.ArtifactID, ArtifactName: it.ArtifactName, BackupSetID: it.BackupSetID, Basis: it.Basis,
			CadenceSeconds: durationSeconds(it.Cadence), Due: it.Due, NextDueAt: it.NextDueAt.UTC().Format(time.RFC3339), Overdue: it.Overdue,
		}
		if !it.LastAssuredAt.IsZero() {
			b.LastAssuredAt = it.LastAssuredAt.UTC().Format(time.RFC3339)
		}
		out.Items = append(out.Items, b)
	}
	writeJSON(w, http.StatusOK, out)
}

// startRecoveryTest is the operator's way to run a recovery test now. It is a
// write in the same tier as the rest of this file (CSRF, no destructive gate):
// it reads one copy through the recovery engine and writes a claim on that
// copy, and deletes and overwrites nothing. It answers 202 at once, because
// a test reads a whole artifact and runs an operator's script, and the
// operation id in the answer is how the activity feed is followed.
func (h *handlers) startRecoveryTest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAssuranceBodyBytes)
	var body startRecoveryTestRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeDecodeError(w, err, maxAssuranceBodyBytes)
		return
	}
	started, err := h.backend.StartRecoveryTest(r.Context(), backupSetIDFromRoute(r), service.RecoveryTestOptions{
		ArtifactID: body.ArtifactID, AcknowledgeCost: body.Acknowledged,
	})
	if err != nil {
		h.writeAssuranceError(w, r, err, "failed to start the recovery test")
		return
	}
	writeJSON(w, http.StatusAccepted, recoveryTestStartedResponse{BackupSetID: started.BackupSetID, OperationID: started.OperationID})
}

func (h *handlers) writeAssuranceError(w http.ResponseWriter, r *http.Request, err error, internal string) {
	switch {
	case errors.Is(err, service.ErrBackupSetNotFound):
		h.logRefusal(r, http.StatusNotFound, "BACKUP_SET_NOT_FOUND",
			writeError(w, http.StatusNotFound, "BACKUP_SET_NOT_FOUND", "no such backup set"), err)
	case errors.Is(err, service.ErrArtifactNotFound):
		h.logRefusal(r, http.StatusNotFound, "ARTIFACT_NOT_FOUND",
			writeError(w, http.StatusNotFound, "ARTIFACT_NOT_FOUND", "no such backup"), err)
	case errors.Is(err, service.ErrRecoveryTestRunning):
		writeError(w, http.StatusConflict, "OPERATION_ALREADY_RUNNING", "a recovery test of this backup set is already running")
	case errors.Is(err, service.ErrInvalidRequest):
		// Safe to echo, on core/service's standing guarantee for this
		// sentinel: the text is built from config's own field descriptions
		// and the caller's own submitted values.
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case errors.Is(err, service.ErrConfigNotFileBacked):
		h.internalError(w, r, "INTERNAL", "this deployment has no configuration file to persist to", err)
	default:
		h.internalError(w, r, "INTERNAL", internal, err)
	}
}

// assuranceSeconds converts a whole-seconds request field, refusing a
// value too large to be a duration rather than letting it wrap.
func assuranceSeconds(v *int64, field string) (*time.Duration, error) {
	return checkedSecondsPointerToDurationFromInt64(v, field)
}

func toUpdateAssuranceSettingsRequest(b updateAssuranceSettingsRequest) (service.UpdateAssuranceSettingsRequest, error) {
	cad, err := assuranceSeconds(b.ScrubCadenceSeconds, "scrub_cadence_seconds")
	if err != nil {
		return service.UpdateAssuranceSettingsRequest{}, err
	}
	rcad, err := assuranceSeconds(b.RecoveryTestCadenceSeconds, "recovery_test_cadence_seconds")
	if err != nil {
		return service.UpdateAssuranceSettingsRequest{}, err
	}
	ttl, err := assuranceSeconds(b.StagingTTLSeconds, "staging_ttl_seconds")
	if err != nil {
		return service.UpdateAssuranceSettingsRequest{}, err
	}
	toInt := func(v *int64) *int {
		if v == nil {
			return nil
		}
		n := int(*v)
		return &n
	}
	return service.UpdateAssuranceSettingsRequest{
		ScrubCadence:            cad,
		ScrubStrategy:           b.ScrubStrategy,
		RecoveryTestMode:        b.RecoveryTestMode,
		RecoveryTestCadence:     rcad,
		RecoveryTestValidatorID: b.RecoveryTestValidatorID,
		StagingRoot:             b.StagingRoot,
		StagingTTL:              ttl,
		MaxConcurrentFullReads:  toInt(b.MaxConcurrentFullReads),
		MaxPerDestination:       toInt(b.MaxPerDestination),
	}, nil
}

func toAssurancePolicyBodies(p service.AssurancePolicy) (assuranceScrubBody, assuranceRecoveryTestBody) {
	return assuranceScrubBody{CadenceSeconds: durationSeconds(p.Scrub.Cadence), Strategy: p.Scrub.Strategy},
		assuranceRecoveryTestBody{CadenceSeconds: durationSeconds(p.RecoveryTest.Cadence), Mode: p.RecoveryTest.Mode, ValidatorID: p.RecoveryTest.ValidatorID}
}

func toAssuranceSettingsBody(s service.AssuranceSettings) assuranceSettingsResponse {
	scrub, rt := toAssurancePolicyBodies(s.Policy)
	knobs := make([]assuranceHostKnobBody, 0, len(s.HostKnobs))
	for _, k := range s.HostKnobs {
		knobs = append(knobs, assuranceHostKnobBody{Name: k.Name, PerSet: k.PerSet, Reason: k.Reason, Value: k.Value})
	}
	return assuranceSettingsResponse{
		Configured: s.Configured, HostKnobs: knobs,
		MaxConcurrentFullReads: int64(s.MaxConcurrentFullReads), MaxPerDestination: int64(s.MaxPerDestination),
		RecoveryTest: rt, Scrub: scrub, StagingRoot: s.StagingRoot, StagingTTLSeconds: durationSeconds(s.StagingTTL),
	}
}

func toBackupSetAssuranceBody(s service.BackupSetAssurance) backupSetAssuranceResponse {
	scrub, rt := toAssurancePolicyBodies(s.Policy)
	return backupSetAssuranceResponse{BackupSetID: s.BackupSetID, IsOverride: s.Override, RecoveryTest: rt, Scrub: scrub}
}
