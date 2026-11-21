package obs

import (
	"os"
	"strings"

	"github.com/retnd/retnd/core/envcompat"
)

// LevelFromEnv is how a deployment turns this sink up without a flag, a
// config key or a restart argument nobody can remember over a phone call:
// LOG_LEVEL=debug, or RETND_DEBUG=1 as the shortcut.
//
// It exists here, rather than at the one or two places that build a
// Logger, because issue #730's whole difficulty was that the two
// containers of one deployment answered "how loud am I" differently. The
// UI host read the environment (apps/common/webhost's own envLogLevel)
// and the engine hard-coded LevelInfo, so an operator who set LOG_LEVEL
// on both got the proxy's trace and none of the engine events the trace
// was supposed to be joined to.
//
// The two readers are deliberately not one shared helper: apps/ may
// import core/, never the reverse, and core/internal is unreachable from
// apps/ by construction. What keeps them honest is that they read the
// same variables with the same precedence and the same fallback,
// each covered by its own package's test, and that this comment and
// webhost's own say so. The one thing they DO share is core/envcompat,
// which is reachable from both and owns the deprecated names, because
// "one notice per name per process" is a property neither reader can
// hold on its own when `serve` runs both of them in one process.
//
// RETND_DEBUG wins over LOG_LEVEL because it is the shortcut an
// operator is told to set, and an unparseable LOG_LEVEL falls back to
// LevelInfo rather than refusing to start: a typo in a diagnostic knob
// must never take a backup host down.
//
// One DEPRECATED spelling of that shortcut is still read, RM_DEBUG, which
// comes from the first product name (#794). It is still honoured so an
// upgrade does not silently turn a diagnosing operator's logs back off,
// and it produces one deprecation notice per process the first time it is
// read. RETND_DEBUG is the only spelling this product writes or documents.
// The previous product name's spelling is not read at all.
func LevelFromEnv() Level {
	level, _ := ResolveLevel(LevelInfo)
	return level
}

// Where ResolveLevel's answer came from, reported by GET
// /settings/observability so "why is it not debug" is never a guess.
const (
	LevelSourceRetndDebug = "RETND_DEBUG"
	LevelSourceLogLevel   = "LOG_LEVEL"
	LevelSourceConfig     = "config"
)

// ResolveLevel ranks the three places a log floor can come from (FR-100):
// RETND_DEBUG=1 (and its deprecated alias RM_DEBUG=1) first, then a parseable
// LOG_LEVEL, then the configured floor (observability.logs.level). It returns
// the level and which of the three it came from.
//
// The environment wins because a diagnostic knob an operator reaches for
// during an incident must not need a config edit and a reload. An unset or
// unparseable LOG_LEVEL defers to the configured floor rather than to INFO,
// which is the same answer whenever nothing is configured (the floor
// defaults to info), so a deployment that never writes the block sees no
// change. "info" is a real LOG_LEVEL answer and beats a quieter floor.
func ResolveLevel(configured Level) (Level, string) {
	if debugShortcut() {
		return LevelDebug, LevelSourceRetndDebug
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		return LevelDebug, LevelSourceLogLevel
	case "info":
		return LevelInfo, LevelSourceLogLevel
	case "warn":
		return LevelWarn, LevelSourceLogLevel
	case "error":
		return LevelError, LevelSourceLogLevel
	default:
		return configured, LevelSourceConfig
	}
}

// debugEnv is the one-variable debug shortcut and the deprecated name it
// has had. Package-level so both this file's reader and its test
// name the same thing, and so webhost's copy can be compared against it
// by eye in a review.
var debugEnv = envcompat.Rename{
	Current: "RETND_DEBUG",
	Legacy:  []string{"RM_DEBUG"},
}

// debugShortcut reports whether the one-variable debug shortcut is set,
// under its own name or under its deprecated alias. Only the
// documented "1" counts, under every name: a knob whose typos mean
// something is a knob that surprises the operator reading it back.
//
// The two names are OR'd rather than ranked (envcompat.Any), which is
// the rule this knob has always had and the reason it exists: neither
// has ever had an "off" value, so a deployment that sets both, or the
// old one on one container and the new one on the other, gets debug
// either way. Ranking them would make an operator who asked for debug
// twice get INFO, which is a behaviour change and not a
// rename. The variables whose values mean something in both directions
// are ranked instead (core/internal/config's incremental-engine gate).
func debugShortcut() bool {
	return envcompat.Any(debugEnv, "1")
}
