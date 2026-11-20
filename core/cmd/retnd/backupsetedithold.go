package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/internal/config"
)

// cmdBackupSetEditHold is `retnd backup-set edit-hold
// <source/backup-set> [--take | --release] [--token T]`.
//
// # The gap this closes
//
// The three edit-hold routes have existed since #350 and nothing outside a
// browser could reach any of them. So a hold could be taken from the Web
// UI and released from nowhere else, and the one action an operator most
// needs from a terminal, giving back a hold somebody left behind, had no
// command at all. That also meant the docked terminal (#599) could print
// no equivalent command for it, which is the parity rule failing visibly
// rather than at an audit nobody runs.
//
// # Why this is routed, and refuses without a route
//
// A hold is a lease held IN MEMORY by the process that is serving this
// deployment (core/service's edithold.go says why it is deliberately not
// durable: a hold that survived a restart would leave a set paused after a
// crash with no way out but a second restart). So with nothing serving,
// there is no hold, and there is nothing on disk that could be consulted
// or changed. This refuses rather than answering "not held" from a world
// where the question has no meaning, for the same reason
// `activity --follow` refuses: an answer from the wrong place is worse
// than no answer, because it looks like an answer.
//
// # Report by default, take or release on a flag
//
// The default is a read, so an operator can see whether a set is held and
// what taking the hold interrupted before deciding to do anything about
// it. --take and --release are the writes, and they are flags rather than
// verbs because the three are the same question asked with and without an
// action: taking and releasing print the resulting state as well, so one
// invocation both acts and reports what it left behind.
//
// TAKING a hold had no command until #1253, on the reasoning that a hold
// protects an edit SESSION and a CLI edit is one `backup-set patch` that
// either runs or does not. A scripted edit of several patches is a
// session, and without a hold a cycle can run between two of them against
// a set that is half changed. --take is what the browser's edit form does
// when it opens: it takes the lease and reports what that stopped. The
// lease is short and is not renewed by anything here.
//
// # The holder token (#1606)
//
// --take prints the token the engine gave this holder. A script that edits
// for longer than the lease renews with --take --token T, which extends
// that holder's lease and nothing else, and gives the hold back with
// --release --token T, which leaves anybody else's hold on the same set
// (an operator's browser tab, say) in force. A renewal of a hold that was
// released or has lapsed is refused rather than taking the set again,
// because that unconditional re-take is how a late renewal used to put a
// released hold back; the refusal says to run --take without a token for a
// new hold. --release with no token gives back every hold on the set, as
// it always did, which is the operator's way to clear one somebody left
// behind.
func cmdBackupSetEditHold(args []string) int {
	fs, cfgPath := newFlagSet("backup-set edit-hold")
	take := fs.Bool("take", false,
		"take a hold and print its token: the cycle inside this backup set stops at its next safe boundary and the scheduler starts no new pass for it until every hold is released or has lapsed. With --token, renew that hold instead")
	release := fs.Bool("release", false,
		"give the hold back, so the scheduler may run this backup set again rather than waiting for the lease to lapse. With --token, only that hold; without, every hold on the set")
	token := fs.String("token", "",
		"the holder token --take printed: with --take it renews that hold (refused if it was released or has lapsed), with --release it gives back that hold alone")

	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return 2
	}
	// The verb itself is operands[0]: cmdBackupSet hands every handler
	// the whole argument list, so a flag written before the verb is not
	// silently dropped. See backupSetVerbs' own doc.
	if len(operands) != 2 || operands[0] != "edit-hold" {
		return usageError(`backup-set edit-hold: expected "edit-hold <source/backup-set>" and exactly one backup set id`)
	}
	id := operands[1]
	source, set, ok := splitBackupSetID(id)
	if !ok {
		return usageError("backup-set edit-hold: %q is not a backup set id; a backup set id is exactly source/name", id)
	}
	// Opposites, so neither is picked: a command that took and then
	// released, or the other way round, would do half of what was asked
	// and report the other half.
	if *take && *release {
		return usageError("backup-set edit-hold: --take and --release are opposites; pass one")
	}
	// An empty --token is a script whose variable was unset. Read as "no
	// token" it would take a new hold instead of renewing, or give back
	// every holder's hold instead of its own (#1606).
	tokenGiven := false
	fs.Visit(func(f *flag.Flag) { tokenGiven = tokenGiven || f.Name == "token" })
	if tokenGiven && *token == "" {
		return usageError("backup-set edit-hold: --token needs the token --take printed; an empty value is refused so it is never read as no token")
	}
	// A report does not act as anybody, so a token with nothing to do
	// would be silently ignored, and a script that meant --take --token
	// would read "held: true" off somebody else's hold.
	if *token != "" && !*take && !*release {
		return usageError("backup-set edit-hold: --token names the hold --take renews or --release gives back; pass it with one of them")
	}

	ctx := context.Background()
	mode, code := editHoldRoute(ctx, *cfgPath)
	if code >= 0 {
		return code
	}

	if *take && *token == "" {
		hold, err := mode.client.TakeBackupSetEditHold(ctx, source, set)
		if err != nil {
			return fail(fmt.Errorf("taking the edit hold on %s: %w", id, err))
		}
		printEditHoldTaken(hold)
	}
	if *take && *token != "" {
		hold, err := mode.client.RenewBackupSetEditHold(ctx, source, set, *token)
		var refused *apiclient.Error
		if errors.As(err, &refused) && refused.Code == apicontract.ErrorCodeEditHoldGone {
			return fail(fmt.Errorf("the edit hold on %s under token %s was released or has lapsed, so it was not renewed and nothing is held under it; run `%s backup-set edit-hold %s --take` without --token to take a new hold", id, *token, cliecho.Binary, id))
		}
		if err != nil {
			return fail(fmt.Errorf("renewing the edit hold on %s: %w", id, err))
		}
		printEditHoldTaken(hold)
	}
	if *release {
		if err := mode.client.ReleaseBackupSetEditHold(ctx, source, set, *token); err != nil {
			return fail(fmt.Errorf("releasing the edit hold on %s: %w", id, err))
		}
	}

	state, err := mode.client.GetBackupSetEditHold(ctx, source, set)
	if err != nil {
		return fail(mode.unanswered("whether "+id+" is held for editing", err))
	}
	printEditHold(state, *release)
	return 0
}

// printEditHoldTaken says what taking or renewing the hold did: the lease
// it placed, the token that names it, and what it stopped. "nothing"
// rather than nothing at all, for the contract's own reason: a client
// never claims to have stopped something it did not, and a line that is
// simply absent reads as either.
func printEditHoldTaken(hold apicontract.BackupSetEditHold) {
	fmt.Printf("taken until %s\n", hold.ExpiresAt)
	fmt.Printf("  token: %s\n", hold.Token)
	if hold.Stopped == nil {
		fmt.Printf("  stopped: nothing\n")
		return
	}
	fmt.Printf("  stopped: %s (%s)\n", hold.Stopped.Artifact, hold.Stopped.Stage)
}

// printEditHold is the report, and it says what is true rather than what
// was asked for.
//
// After a --release it prints the state read back from the engine, not an
// assumed one: a release that succeeded and was immediately followed by
// somebody else taking the hold is a real sequence, and a command that
// printed "held: false" from its own optimism would be lying about the
// deployment at the moment it was asked.
func printEditHold(state apicontract.BackupSetEditHoldState, released bool) {
	if released {
		fmt.Println("released")
	}
	fmt.Printf("%s\n", state.BackupSetID)
	fmt.Printf("  held: %v\n", state.Held)
	if state.ExpiresAt != "" {
		fmt.Printf("  expires_at: %s\n", state.ExpiresAt)
	}
	// Absent rather than zeroed, on purpose: the contract makes `running`
	// null when no cycle is inside this set, and printing an empty
	// artifact and an empty stage would read as a pass that is running and
	// has not said what it is doing.
	if state.Running == nil {
		fmt.Printf("  running: nothing\n")
		return
	}
	fmt.Printf("  running: %s (%s)\n", state.Running.Artifact, state.Running.Stage)
}

// editHoldRoute is the engine an edit hold is read from: remote from a
// terminal with no deployment of its own (remote.go), otherwise the same
// door every read goes through, which announces which world this answer is
// about and refuses the three disagreements before anything is printed. It
// takes no claim, which is right here: an edit hold is not configuration, so
// there is nothing on disk for a claim to protect.
func editHoldRoute(ctx context.Context, cfgPath string) (readDecision, int) {
	remote, err := engineOnlyRemote(ctx, cfgPath, false)
	if err != nil {
		return readDecision{}, fail(err)
	}
	if remote != nil {
		return remote.decision(), -1
	}
	cfg, err := config.LoadAndValidate(cfgPath)
	if err != nil {
		return readDecision{}, fail(fmt.Errorf("config: %w", err))
	}
	mode, err := enterReadMode(ctx, cfgPath, cfg, os.Stderr)
	if err != nil {
		return readDecision{}, fail(err)
	}
	if !mode.attached() {
		return readDecision{}, fail(noEngineRefusal("an edit hold is held in memory by the process serving this deployment, so there is nothing to report and nothing to release without a route to it (with nothing serving at all there is no hold, because a hold does not survive the process that took it)"))
	}
	return mode, -1
}
