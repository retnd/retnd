package obs

import "log/slog"

// LogSchemaVersion is the version of the log line's field schema (FR-100).
// It is carried on the startup event as log_schema and, in the shipped
// Promtail and Alloy pipelines, as the schema stream label. It is never a
// per-line field: that would be bytes on every line of a high-volume stream
// to answer a question that changes once a year.
const LogSchemaVersion = "1"

// ReservedKeys is the field schema every line this package writes promises,
// in the same sense events.go promises its event names: a wire contract.
//
// time, level, msg and event are on every line. result, action and action_id
// are on a marked line (action.go). backup_set is on any line with a backup
// set in scope. Everything else on a line is a per-event attribute, which
// FR-100 allows to be added freely and which is never reserved.
//
// Adding, removing or renaming one is as breaking as renaming an event, so
// schema_test.go pins the list against literals typed out again and holds a
// golden line over the real output. The list is built from the same
// constants emit writes with, which is what keeps it from drifting from the
// line.
var ReservedKeys = []string{
	slog.TimeKey, slog.LevelKey, slog.MessageKey,
	fieldEvent, fieldResult, fieldAction, fieldActionID, fieldBackupSet,
}
