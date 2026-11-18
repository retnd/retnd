package webhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1387: an agent's run_cycle or run_backup_set proposal records the
// configuration revision the engine held when it was proposed, so the
// approved replay is not refused for lack of one. Written red first.

// revisionBackend refuses what the real service refuses: a run with no
// revision is INVALID_REQUEST, and one that names another revision than the
// engine holds is stale. The shared fake accepts both, which is how the bug
// stayed hidden.
type revisionBackend struct{ *confirmationsBackend }

func (b *revisionBackend) check(action, rev string) error {
	switch {
	case rev == "":
		return fmt.Errorf("%w: %s request requires a configuration revision", service.ErrInvalidRequest, action)
	case rev != b.ConfigRevision():
		return fmt.Errorf("%w: request names %q", service.ErrConfigRevisionStale, rev)
	}
	return nil
}

func (b *revisionBackend) SubmitRunCycle(ctx context.Context, req service.RunCycleRequest) (service.Operation, error) {
	if err := b.check("run_cycle", req.ConfigRevision); err != nil {
		return service.Operation{}, err
	}
	return b.confirmationsBackend.SubmitRunCycle(ctx, req)
}

func (b *revisionBackend) SubmitRunBackupSet(ctx context.Context, req service.RunBackupSetRequest) (service.Operation, error) {
	if err := b.check("run_backup_set", req.ConfigRevision); err != nil {
		return service.Operation{}, err
	}
	return b.confirmationsBackend.SubmitRunBackupSet(ctx, req)
}

var revisionTools = fakeTools{
	"run_cycle":      {"POST", "/operations", "run_cycle", "operate"},
	"run_backup_set": {"POST", "/operations", "run_backup_set", "operate"},
}

func newRevisionRig(t *testing.T) (http.Handler, *revisionBackend) {
	t.Helper()
	b := &revisionBackend{&confirmationsBackend{
		backupSetFakeBackend: newBackupSetFakeBackend(),
		fakeAgentAccess:      newFakeAgentAccess(),
		fakeConfirmations:    newFakeConfirmations(),
	}}
	steps := &stubStepUp{user: "alice", password: stepUpPassword}
	return NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: b, Gate: NewProfileGate(profile.Generic), StepUp: steps,
		Recorder: &recordingBackend{}, AgentTools: revisionTools, BinaryVersion: "test", Commit: "test",
	}), b
}

// propose makes the tool call and returns the id of the confirmation it left.
func propose(t *testing.T, router http.Handler, tool, body string) string {
	t.Helper()
	rec := serve(router, toolCall(http.MethodPost, "/api/v1/operations", body, operateToken, tool))
	id := rec.Header().Get(ConfirmationIDHeader)
	if rec.Code != http.StatusConflict || id == "" {
		t.Fatalf("%s proposal = %d %s, want 409 with a confirmation id", tool, rec.Code, rec.Body.String())
	}
	return id
}

func approve(t *testing.T, router http.Handler, id string) apicontract.AgentConfirmation {
	t.Helper()
	rec := serve(router, session(http.MethodPost, "/api/v1/agent/confirmations/"+id+"/approve", "", stepUpPassword))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d %s; the approval stands whatever the replay does", rec.Code, rec.Body.String())
	}
	var got apicontract.AgentConfirmation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Execution == nil {
		t.Fatalf("approved confirmation = %s, %v", rec.Body.String(), err)
	}
	return got
}

var revisionBodies = map[string]string{
	"run_cycle":      `{"action":"run_cycle"}`,
	"run_backup_set": `{"action":"run_backup_set","backup_set_id":"production/postgres"}`,
}

// Falsify: record the body as sent, with no revision.
func TestAnApprovedRunWithNoRevisionSuppliedIsQueued(t *testing.T) {
	for tool, body := range revisionBodies {
		t.Run(tool, func(t *testing.T) {
			router, b := newRevisionRig(t)
			got := approve(t, router, propose(t, router, tool, body))
			if got.Execution.Status/100 != 2 || got.Execution.Code != "" || got.Execution.ExitCode != 0 {
				t.Errorf("execution = %+v, want a 2xx with exit code 0 as for retnd operation run", got.Execution)
			}
			if len(b.ops) != 1 {
				t.Fatalf("operations queued = %d, want 1", len(b.ops))
			}
			for _, op := range b.ops {
				if op.ConfigRevision != "rev-1" {
					t.Errorf("the operation ran against revision %q, want the one held at proposal, rev-1", op.ConfigRevision)
				}
			}
		})
	}
}

// Falsify: stamp the revision at approval time instead of proposal time.
func TestAConfigChangeBetweenProposeAndApproveIsRefusedAsStale(t *testing.T) {
	for tool, body := range revisionBodies {
		t.Run(tool, func(t *testing.T) {
			router, b := newRevisionRig(t)
			id := propose(t, router, tool, body)
			b.configRevision = "rev-2"
			got := approve(t, router, id)
			if got.Execution.Status != http.StatusConflict || got.Execution.Code != "CONFIG_REVISION_STALE" || got.Execution.ExitCode != 1 {
				t.Errorf("execution = %+v, want 409 CONFIG_REVISION_STALE with exit code 1, not a 400", got.Execution)
			}
			if len(b.ops) != 0 {
				t.Errorf("a stale replay queued %d operations", len(b.ops))
			}
		})
	}
}

// Falsify: overwrite the revision an agent supplied.
func TestARevisionTheAgentSuppliedIsKept(t *testing.T) {
	router, b := newRevisionRig(t)
	id := propose(t, router, "run_cycle", `{"action":"run_cycle","config_revision":"rev-0"}`)
	if !strings.Contains(string(b.records[id].Body), `"rev-0"`) {
		t.Errorf("recorded body %s lost the agent's revision", b.records[id].Body)
	}
	got := approve(t, router, id)
	if got.Execution.Code != "CONFIG_REVISION_STALE" || len(b.ops) != 0 {
		t.Errorf("execution = %+v, operations %d, want the agent's own stale revision refused", got.Execution, len(b.ops))
	}

	id = propose(t, router, "run_cycle", `{"action":"run_cycle","config_revision":"rev-1"}`)
	got = approve(t, router, id)
	if got.Execution.Status/100 != 2 || len(b.ops) != 1 {
		t.Errorf("execution = %+v, operations %d, want the agent's current revision accepted", got.Execution, len(b.ops))
	}
}

// Falsify: rebuild the replay body at approval, or drop the agent's other fields.
func TestTheStampedRecordIsWhatIsReplayedAndKeepsTheAgentsFields(t *testing.T) {
	router, b := newRevisionRig(t)
	id := propose(t, router, "run_backup_set", revisionBodies["run_backup_set"])
	var rec struct {
		Action, BackupSetID, ConfigRevision string
	}
	stored := b.records[id].Body
	var m map[string]string
	if err := json.Unmarshal(stored, &m); err != nil {
		t.Fatalf("recorded body %s: %v", stored, err)
	}
	rec.Action, rec.BackupSetID, rec.ConfigRevision = m["action"], m["backup_set_id"], m["config_revision"]
	if rec.Action != "run_backup_set" || rec.BackupSetID != "production/postgres" || rec.ConfigRevision != "rev-1" {
		t.Errorf("recorded body %s, want the agent's fields plus config_revision rev-1", stored)
	}
	approve(t, router, id)
	if string(b.records[id].Body) != string(stored) {
		t.Errorf("approval changed the record: %s became %s", stored, b.records[id].Body)
	}
	if b.lastRunBackupSet.ConfigRevision != "rev-1" || b.lastRunBackupSet.BackupSetID != "production/postgres" {
		t.Errorf("the replay ran %+v, want the record's own fields", b.lastRunBackupSet)
	}
}

// Falsify: leave the revision out of what the person is shown.
func TestTheConfirmationViewShowsTheRevisionItWasRecordedAgainst(t *testing.T) {
	router, _ := newRevisionRig(t)
	id := propose(t, router, "run_cycle", revisionBodies["run_cycle"])
	rec := serve(router, session(http.MethodGet, "/api/v1/agent/confirmations/"+id, "", ""))
	var view struct {
		ConfigRevision string `json:"config_revision"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &view) != nil || view.ConfigRevision != "rev-1" {
		t.Errorf("view = %d %s, want config_revision rev-1", rec.Code, rec.Body.String())
	}
}

// Falsify: let the approve request carry a body.
func TestApprovingStillRefusesABody(t *testing.T) {
	router, b := newRevisionRig(t)
	id := propose(t, router, "run_cycle", revisionBodies["run_cycle"])
	rec := serve(router, session(http.MethodPost, "/api/v1/agent/confirmations/"+id+"/approve", `{"config_revision":"rev-1"}`, stepUpPassword))
	if rec.Code != http.StatusBadRequest || responseErrorCode(rec.Body.String()) != "AGENT_CONFIRMATION_BODY_SUBSTITUTED" || len(b.ops) != 0 {
		t.Errorf("approve with a body = %d %s, operations %d", rec.Code, rec.Body.String(), len(b.ops))
	}
}
