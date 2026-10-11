package obs

import (
	"context"
	"log/slog"
	"time"
)

// FR-23 catalog additions for EPIC Q (Q1.4, #982): scrubbing, artifact
// recovery and recovery testing. Every name here is new. None of them
// renames or reshapes anything already shipped, and the stable-names test
// in assurance_events_test.go pins them the way events_test.go pins the
// older ones.
//
// # The pairing, and what each completion states
//
// Each long-running action is a start line and a completion line sharing an
// action id, in the shape the cycle and the workflow run already use. The
// completion always states a Result, so a client never infers an outcome
// from a missing error field.
//
// # Event volume is a decision, not a side effect
//
// The activity feed is append-only and cursor-paged, and a scrub can touch a
// thousand copies in one pass. So a scrub pass is two lines (start, end)
// plus one EventScrubCopy per copy that did NOT pass, and the caller caps
// those (service.MaxScrubCopyRows). A thousand healthy copies cost two
// rows. A recovery or recovery test is a start, one stage line per stage it
// enters, and an end.
const (
	// EventScrubStart and EventScrubEnd bracket one scrub pass over a
	// backup set.
	EventScrubStart = "scrub_start"
	EventScrubEnd   = "scrub_end"

	// EventScrubCopy records one copy a scrub pass could not pass: it
	// failed, it could not be read, or it was refused for cost. A passing
	// copy emits nothing, which is what keeps a large pass bounded.
	EventScrubCopy = "scrub_copy"

	// EventRecoveryStart, EventRecoveryStage and EventRecoveryEnd bracket
	// one artifact recovery and name the stage it is in.
	EventRecoveryStart = "recovery_start"
	EventRecoveryStage = "recovery_stage"
	EventRecoveryEnd   = "recovery_end"

	// EventRecoveryTestStart and EventRecoveryTestEnd bracket one recovery
	// test. Its stages are reported as EventRecoveryStage lines under the
	// recovery_test action.
	EventRecoveryTestStart = "recovery_test_start"
	EventRecoveryTestEnd   = "recovery_test_end"
)

// The action names the pairs above are bracketed under.
const (
	ActionScrub        = "scrub"
	ActionRecovery     = "recovery"
	ActionRecoveryTest = "recovery_test"
)

// RecoveryStage is one named step of a recovery or recovery test.
type RecoveryStage struct {
	// ID is the wire name carried in the "stage" field.
	ID string
	// Label is what a terminal and the browser both print.
	Label string
}

// RecoveryStages is the stage sequence, in order. It is the single list the
// CLI's --follow and the browser's follow both render from, so the two
// cannot disagree about a name or an order. "Restoring archive" is skipped
// when no thaw was requested, and Complete is always last.
var RecoveryStages = []RecoveryStage{
	{ID: "resolving_copy", Label: "Resolving copy"},
	{ID: "restoring_archive", Label: "Restoring archive"},
	{ID: "reading", Label: "Reading"},
	{ID: "verifying", Label: "Verifying"},
	{ID: "decrypting", Label: "Decrypting"},
	{ID: "writing_target", Label: "Writing target"},
	{ID: "validating", Label: "Validating"},
	{ID: "cleaning_temporary_files", Label: "Cleaning temporary files"},
	{ID: "complete", Label: "Complete"},
}

// StageLabel returns the label for a stage id, or "" for one this catalog
// does not name.
func StageLabel(id string) string {
	for _, s := range RecoveryStages {
		if s.ID == id {
			return s.Label
		}
	}
	return ""
}

// ScrubTotals is what a finished scrub pass measured.
type ScrubTotals struct {
	Checked   int
	Passed    int
	Failed    int
	Refused   int
	Unread    int
	Truncated int
	BytesRead int64
	Duration  time.Duration
}

// ScrubStart logs EventScrubStart for the pass runID.
func (l *Logger) ScrubStart(ctx context.Context, runID, backupSet string, copies int) {
	l.emitMarked(ctx, LevelInfo, mark{action: ActionScrub, actionID: runID},
		EventScrubStart, "scrub starting",
		slog.String("run_id", runID),
		slog.String("backup_set", backupSet),
		slog.Int("copies", copies),
	)
}

// ScrubEnd logs EventScrubEnd for the pass runID started, stating result.
// Truncated is how many non-passing copies were not given a line of their
// own, so a capped pass says that it was capped.
func (l *Logger) ScrubEnd(ctx context.Context, runID, backupSet string, t ScrubTotals, result Result) {
	l.emitMarked(ctx, result.Level(), mark{result: result, action: ActionScrub, actionID: runID},
		EventScrubEnd, "scrub finished",
		slog.String("run_id", runID),
		slog.String("backup_set", backupSet),
		slog.Int("checked", t.Checked),
		slog.Int("passed", t.Passed),
		slog.Int("failed", t.Failed),
		slog.Int("refused", t.Refused),
		slog.Int("unread", t.Unread),
		slog.Int("truncated", t.Truncated),
		slog.Int64("bytes_read", t.BytesRead),
		slog.Duration("duration", t.Duration),
	)
}

// ScrubCopy logs EventScrubCopy: one copy the pass run runID could not
// pass. reason is the service's own sentence, never a validator's output.
func (l *Logger) ScrubCopy(ctx context.Context, runID, backupSet, artifact, medium, outcome, reason string) {
	l.emitMarked(ctx, LevelWarn, mark{result: ResultWarn},
		EventScrubCopy, "scrub could not pass a copy",
		slog.String("run_id", runID),
		slog.String("backup_set", backupSet),
		slog.String("artifact", artifact),
		slog.String("medium", medium),
		slog.String("outcome", outcome),
		slog.String("reason", cleanReason(reason)),
	)
}

// RecoveryStart logs EventRecoveryStart for the recovery opID.
func (l *Logger) RecoveryStart(ctx context.Context, opID, backupSet, artifact string) {
	l.emitMarked(ctx, LevelInfo, mark{action: ActionRecovery, actionID: opID},
		EventRecoveryStart, "recovery starting",
		slog.String("op_id", opID),
		slog.String("backup_set", backupSet),
		slog.String("artifact", artifact),
	)
}

// RecoveryStage logs EventRecoveryStage: the recovery opID entered the
// stage with this id. An id the catalog does not name is logged as given
// with an empty label rather than dropped.
func (l *Logger) RecoveryStage(ctx context.Context, opID, backupSet, stageID string) {
	l.emitMarked(ctx, LevelInfo, mark{action: ActionRecovery, actionID: opID},
		EventRecoveryStage, "recovery stage",
		slog.String("op_id", opID),
		slog.String("backup_set", backupSet),
		slog.String("stage", stageID),
		slog.String("stage_label", StageLabel(stageID)),
	)
}

// RecoveryEnd logs EventRecoveryEnd for the recovery opID started.
func (l *Logger) RecoveryEnd(ctx context.Context, opID, backupSet, artifact string, bytes int64, d time.Duration, result Result, reason string) {
	attrs := []slog.Attr{
		slog.String("op_id", opID),
		slog.String("backup_set", backupSet),
		slog.String("artifact", artifact),
		slog.Int64("bytes", bytes),
		slog.Duration("duration", d),
	}
	if reason != "" {
		attrs = append(attrs, slog.String("reason", cleanReason(reason)))
	}
	l.emitMarked(ctx, result.Level(), mark{result: result, action: ActionRecovery, actionID: opID},
		EventRecoveryEnd, "recovery finished", attrs...)
}

// RecoveryTestStart logs EventRecoveryTestStart. The validator is named by
// id only; see validatorRef.
func (l *Logger) RecoveryTestStart(ctx context.Context, opID, backupSet, artifact, validatorID string) {
	l.emitMarked(ctx, LevelInfo, mark{action: ActionRecoveryTest, actionID: opID},
		EventRecoveryTestStart, "recovery test starting",
		slog.String("op_id", opID),
		slog.String("backup_set", backupSet),
		slog.String("artifact", artifact),
		slog.String("validator", validatorRef(validatorID)),
	)
}

// RecoveryTestEnd logs EventRecoveryTestEnd for the test opID started.
func (l *Logger) RecoveryTestEnd(ctx context.Context, opID, backupSet, artifact, validatorID string, d time.Duration, result Result, reason string) {
	attrs := []slog.Attr{
		slog.String("op_id", opID),
		slog.String("backup_set", backupSet),
		slog.String("artifact", artifact),
		slog.String("validator", validatorRef(validatorID)),
		slog.Duration("duration", d),
	}
	if reason != "" {
		attrs = append(attrs, slog.String("reason", cleanReason(reason)))
	}
	l.emitMarked(ctx, result.Level(), mark{result: result, action: ActionRecoveryTest, actionID: opID},
		EventRecoveryTestEnd, "recovery test finished", attrs...)
}

// validatorRef renders a validator reference for the feed. It is an id, a
// bare name, never a path: anything with a separator, a space or a control
// byte, or longer than 64 bytes, is withheld, because validator identity
// reaches a terminal and an operator's validator is untrusted input.
func validatorRef(id string) string {
	if id == "" || len(id) > 64 {
		return "(withheld)"
	}
	for _, r := range id {
		ok := r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return "(withheld)"
		}
	}
	return id
}

// maxReasonBytes bounds a reason on the feed.
const maxReasonBytes = 200

// cleanReason makes a reason safe for a terminal: control and non-printing
// bytes become spaces and the length is bounded. Reasons are the service's
// own sentences, but a caller can fold a validator's words into one, and
// validator output is untrusted terminal input.
func cleanReason(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			r = ' '
		}
		out = append(out, r)
		if len(out) >= maxReasonBytes {
			break
		}
	}
	return string(out)
}
