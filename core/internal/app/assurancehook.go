package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
)

// Telling the operator that an assurance claim went wrong, or came back
// (EPIC Q, #978).
//
// # Through a script, not through a notifier
//
// A scrub that keeps failing, and a recovery test that fails, are things an
// operator has to hear about. This product already has a way to run what an
// operator wants done about an event: the hook scripts of EPIC L, with their
// custody checks, their shell verification and their hardened runner. So
// the notification IS an "after" stage: a directory of NAME.local.sh scripts
// the operator owns, run with the event in their environment. There is no
// new built-in alert kind, no webhook client and no mail code to maintain,
// and what happens next (page someone, post to a channel, open a ticket) is
// the operator's script and not this product's opinion.
//
// The directories are conventions under the workflow root, not configured
// stage directories, so an absent one means nobody asked and is silent:
//
//	assurance-after/sets/<source>/<set>   this set's scripts, run first
//	assurance-after/global                 every set's scripts, run after them
//
// The same unwinding order a backup's "after" stages use. A directory is
// deliberately NOT the backup set's own after_dir: that one holds the
// scripts that undo what a before hook did to the source machine, and
// running "unquiesce the database" because a scrub failed would be the
// notification doing damage.
//
// # What a script is told
//
// Ordinary variables, none of them RETND_-prefixed (those are the product's
// own facts about a run, and there is no run):
//
//	ASSURANCE_EVENT           scrub_failing, scrub_recovered,
//	                          recovery_test_failed or recovery_test_recovered
//	ASSURANCE_BACKUP_SET      source/set
//	ASSURANCE_SUMMARY         one sentence, safe to paste into a message
//	ASSURANCE_FAILING_COPIES  how many copies have crossed the threshold
//	                          (scrub events; 0 otherwise)
//	ASSURANCE_THRESHOLD       the consecutive-failure count that opens an
//	                          episode
//	ASSURANCE_ARTIFACT        the artifact a recovery test was about
//
// Every value is a single line of printable text, bounded.
//
// # Edges, not levels
//
// A script is run when an episode OPENS and when it CLOSES, once each, and
// not on every pass that finds the same failure standing. That is the
// property the built-in alerts had (exactly one alert per episode, a clear
// when it ends, a new one on recurrence) and it is kept because a script
// that pages someone every five minutes until they turn it off is a script
// they turn off.
//
// The record of which episodes are open is in memory, as the alert
// dispatcher's was: a restart forgets it, so a failure still standing after
// a restart is announced once more. That is the same trade, made for the
// same reason, and a durable record would be a new table to keep consistent
// with a derived fact.
const (
	// AssuranceHookGlobalDir is the stage directory run for every backup
	// set, relative to the workflow root.
	AssuranceHookGlobalDir = "assurance-after/global"
)

// AssuranceHookSetDir is a backup set's own stage directory.
func AssuranceHookSetDir(set model.BackupSetID) string {
	return "assurance-after/sets/" + set.Source + "/" + set.Set
}

// The events a script can be told about.
const (
	AssuranceScrubFailing          = "scrub_failing"
	AssuranceScrubRecovered        = "scrub_recovered"
	AssuranceRecoveryTestFailed    = "recovery_test_failed"
	AssuranceRecoveryTestRecovered = "recovery_test_recovered"
)

// assuranceEpisodes is which episodes are open, per set and kind.
type assuranceEpisodes struct {
	mu   sync.Mutex
	open map[string]bool
}

// AdoptAssuranceEpisodes carries what has been announced across a
// configuration reload, for AdoptAlerts' reason.
func (s *Service) AdoptAssuranceEpisodes(prev *Service) {
	if prev == nil {
		return
	}
	prev.assurance.mu.Lock()
	defer prev.assurance.mu.Unlock()
	s.assurance.mu.Lock()
	defer s.assurance.mu.Unlock()
	s.assurance.open = prev.assurance.open
}

// transition records whether the episode (set, kind) is now open and reports
// whether that is a change.
func (e *assuranceEpisodes) transition(set model.BackupSetID, kind string, open bool) (changed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.open == nil {
		e.open = map[string]bool{}
	}
	key := set.String() + "|" + kind
	was := e.open[key]
	if was == open {
		return false
	}
	if open {
		e.open[key] = true
	} else {
		delete(e.open, key)
	}

	return true
}

// assuranceFacts is what an event tells a script.
type assuranceFacts struct {
	Event         string
	Summary       string
	FailingCopies int
	Artifact      string
}

// notifyAssurance runs the set's and the global assurance-after scripts for
// one event. It never fails the caller: a notification that could not be
// sent is logged, and the pass that raised it has already recorded its
// verdict.
func (s *Service) notifyAssurance(ctx context.Context, bs config.BackupSet, f assuranceFacts) {
	if s.Stages == nil {
		return
	}

	res, err := s.Stages.RunStage(context.WithoutCancel(ctx), StageRequest{
		Set:               bs,
		SetDir:            AssuranceHookSetDir(bs.ID),
		GlobalDir:         AssuranceHookGlobalDir,
		ContinueOnFailure: true,
		LocalOnly:         true,
		Vars: map[string]string{
			"ASSURANCE_EVENT":          f.Event,
			"ASSURANCE_BACKUP_SET":     bs.ID.String(),
			"ASSURANCE_SUMMARY":        stageValue(f.Summary, 300),
			"ASSURANCE_FAILING_COPIES": fmt.Sprint(f.FailingCopies),
			"ASSURANCE_THRESHOLD":      fmt.Sprint(health.ScrubFailureThreshold),
			"ASSURANCE_ARTIFACT":       stageValue(f.Artifact, 300),
		},
	})
	switch {
	case err != nil && !isNotConfigured(err):
		s.logger().Event(ctx, obs.LevelWarn, "assurance_hook_failed",
			"an assurance notification script could not be run: "+stageValue(err.Error(), 300))
	case err != nil, !res.Configured:
		// Nobody asked for notifications here. Silence is correct.
	default:
		for _, sc := range res.Scripts {
			if sc.State != "success" {
				s.logger().Event(ctx, obs.LevelWarn, "assurance_hook_failed",
					fmt.Sprintf("assurance notification script %s ended %s for %s", stageValue(sc.Name, 120), stageValue(sc.State, 40), bs.ID))
			}
		}
	}
}

func isNotConfigured(err error) bool {
	return err != nil && strings.Contains(err.Error(), ErrStageNotConfigured.Error())
}

// stageValue makes text a single printable line of at most max bytes, for an
// environment variable a script will print or paste.
func stageValue(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == utf8.RuneError, r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if len(out) <= max {
		return out
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}

	return out[:cut]
}

// settleScrubEpisodes looks at each set a scrub touched and announces an
// episode opening or closing. It runs after the verdicts are recorded and
// reads the journal, so it reports what is true now and not what this pass
// happened to see.
func (s *Service) settleScrubEpisodes(ctx context.Context, sets []config.BackupSet) {
	journal, ok := s.Journal.(scrubJournal)
	if !ok {
		return
	}
	for _, bs := range sets {
		records, err := journal.ListByBackupSet(ctx, bs.ID)
		if err != nil {
			s.logger().Error(ctx, "assurance-episodes", err)

			continue
		}
		failing := 0
		for _, rec := range records {
			for _, p := range rec.Placements {
				if p.Status == "ACTIVE" && p.ConsecutiveFailures >= health.ScrubFailureThreshold {
					failing++
				}
			}
		}
		if s.assurance.transition(bs.ID, "scrub", failing > 0) {
			if failing > 0 {
				s.notifyAssurance(ctx, bs, assuranceFacts{
					Event:         AssuranceScrubFailing,
					FailingCopies: failing,
					Summary: fmt.Sprintf("%d cop%s in backup set %s failed %d scheduled checks in a row, so the set cannot be relied on to restore until that is looked at. Nothing has been deleted.",
						failing, pluralY(failing), bs.ID, health.ScrubFailureThreshold),
				})
			} else {
				s.notifyAssurance(ctx, bs, assuranceFacts{
					Event:   AssuranceScrubRecovered,
					Summary: fmt.Sprintf("every copy in backup set %s is passing its scheduled check again", bs.ID),
				})
			}
		}
	}
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}

	return "ies"
}
