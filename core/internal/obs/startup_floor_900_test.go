package obs

import (
	"bytes"
	"context"
	"testing"
)

// The schema version is on the startup event and nowhere else on a line
// (docs/observability.md, spec decision 3), and the doc promises every
// process emits it once. A floor of warn or error used to swallow it: the
// service puts the configured floor on before any caller logs startup at
// INFO, so a deployment set to warn never said which schema it writes.
// Startup is written whatever the floor; nothing else is.

func TestStartupIsWrittenWhateverTheFloor_900(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*bytes.Buffer) *Logger
	}{
		{"built at warn", func(b *bytes.Buffer) *Logger { return New(b, LevelWarn) }},
		{"built at error", func(b *bytes.Buffer) *Logger { return New(b, LevelError) }},
		{"moved to warn after it was built", func(b *bytes.Buffer) *Logger { l := New(b, LevelInfo); l.SetFloor(LevelWarn); return l }},
		{"moved to error after it was built", func(b *bytes.Buffer) *Logger { l := New(b, LevelInfo); l.SetFloor(LevelError); return l }},
		{"a derived logger after the floor moved", func(b *bytes.Buffer) *Logger {
			l := New(b, LevelInfo)
			d := l.With("component", "daemon")
			l.SetFloor(LevelError)
			return d
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := tc.build(&buf)
			l.Startup(context.Background(), "1.2.3", "deadbeef", "go1.27")
			lines := decodeLines(t, &buf)
			if len(lines) != 1 {
				t.Fatalf("Startup wrote %d lines under a raised floor, want 1: the startup line is the only place log_schema is written (%q)", len(lines), buf.String())
			}
			got := lines[0]
			if got["event"] != EventStartup || got["log_schema"] != LogSchemaVersion || got["level"] != "INFO" {
				t.Errorf("startup line = %v, want event %q, log_schema %q at INFO", got, EventStartup, LogSchemaVersion)
			}
		})
	}
}

// The control: the floor still holds for everything else, so the startup
// line getting through is about startup and not about a floor that stopped
// working.
func TestStartupIsWrittenWhateverTheFloor_900_OnlyStartup(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelInfo)
	l.SetFloor(LevelWarn)
	ctx := context.Background()
	l.RcloneVersion(ctx, "v1.76.0")
	l.CycleStart(ctx, "c1")
	if buf.Len() != 0 {
		t.Fatalf("INFO events other than startup got past a warn floor: %s", buf.String())
	}
	l.Startup(ctx, "1.2.3", "deadbeef", "go1.27")
	if n := len(decodeLines(t, &buf)); n != 1 {
		t.Fatalf("got %d lines, want only the startup line: %s", n, buf.String())
	}
}
