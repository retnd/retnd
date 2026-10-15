package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/recovery/budget"
	"github.com/retnd/retnd/core/internal/transport"
	"github.com/retnd/retnd/core/service"
)

// `retnd recover` (EPIC Q, #976, FR-67 and FR-68): hand an operator the
// retained bytes of one artifact.
//
// It is neither `restore` (FR-34's archival thaw, which asks a provider to
// make an object readable and writes nothing anywhere) nor `snapshot
// restore` (a kopia restore point extracted into a directory). It reads one
// chosen copy, checks all of it against the SHA-256 the journal recorded
// and only then writes it to exactly one target. It runs no application
// command: this product recovers the retained artifact, and putting the
// application back is the operator's.
//
// # The terminal mints and redeems in one step
//
// A grant exists so a browser can be handed a single-use link. Here the
// asker and the redeemer are one process, so the verb mints and redeems
// through the same service code the two routes use, and its refusals are
// the routes' refusals under the same codes. The audit row is still
// written, naming the actor "cli".

// recoverExitCodes gives every recovery refusal its own exit code, so a
// script can branch on what happened instead of on a sentence. They start
// above the four codes the usage text already publishes, and they are
// stable: a code is never reused and never renumbered.
//
// 34, 35 and 37 are deliberately absent. They were reserved for a per-pass
// bound, a validator-output bound and scheduled egress. FR-74 as amended says
// the first two defer or truncate and never refuse, and that scheduled egress
// is refused at the moment of spend by the placement ladder, so no operator
// command can hit any of them and a code nothing emits is a promise nobody
// keeps. They stay unallocated and are not to be reused (budget.Unused).
//
// The grant refusals (22 to 26) are reachable here only in the sense that
// the code table is shared; the terminal redeems its own grant at once.
// They are still distinct, because the same table is what the HTTP route's
// statuses are held against.
var recoverExitCodes = map[string]int{
	service.RecoveryCostNotAcknowledged: 10,
	service.RecoveryNoCopy:              11,
	service.RecoveryCopyNotFound:        12,
	service.RecoveryNotReadable:         13,
	service.RecoveryLocalCopyCorrupt:    14,
	service.RecoveryRemoteCopyCorrupt:   15,
	service.RecoveryNoDigest:            16,
	service.RecoveryNoSpace:             17,
	service.RecoveryBelowReserve:        budget.ExitCode(budget.StagingReserve),
	service.RecoveryUseSnapshotRestore:  19,
	service.RecoveryTargetInvalid:       20,
	service.RecoveryTargetExists:        21,
	service.RecoveryGrantUnknown:        22,
	service.RecoveryGrantUsed:           23,
	service.RecoveryGrantExpired:        24,
	service.RecoveryGrantSessionEnded:   25,
	service.RecoveryTooManyGrants:       26,
	service.RecoveryUnavailable:         27,
	service.RecoveryBusy:                28,
	service.RecoveryInterrupted:         29,
	service.RecoveryFailed:              30,
	service.RecoveryReadsBusy:           budget.ExitCode(budget.ConcurrentReads),
	service.RecoveryDestinationBusy:     budget.ExitCode(budget.DestinationReads),
	service.RecoveryDownloadsBusy:       budget.ExitCode(budget.DownloadStreams),

	// Issue #1191: the source host, in the two free numbers left at the
	// top of the block and the block's last one.
	service.RecoverySourceUnavailable:     38,
	service.RecoverySourceReadOnly:        39,
	service.RecoverySourceNotAcknowledged: 40,
}

// exitRecoverThawStarted: nothing was recovered, because the only copy is
// on an archive class and exactly one provider restore was started instead.
// Not 0, because a script that sees 0 expects bytes.
const exitRecoverThawStarted = 31

// exitRecoverPartial: a recovery of several backups finished and at least one
// was not recovered (failed its check, needs a thaw, was quarantined). Not 0,
// because a script that sees 0 expects every file; not a refusal code,
// because the files that could be recovered were.
const exitRecoverPartial = 32

func cmdRecover(args []string) int {
	fs, cfgPath := newFlagSet("recover")
	copySel := fs.String("copy", "", "medium id of the copy to recover (default: the best readable copy)")
	to := fs.String("to", "", "where the bytes go: source[:FOLDER] for the set's own SFTP host (the default), - for standard output, a name under the recovery staging root, or medium:ID[/KEY]")
	ack := fs.Bool("acknowledge-cost", false, "accept a chargeable retrieval (a billed download, or a provider restore first)")
	ackSource := fs.Bool("acknowledge-source-write", false, "say yes to writing onto the set's own source host (--to source, and the default), into a new folder there")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	follow := fs.Bool("follow", false, "print the recovery's stages on standard error as it passes through them; the exit status is the recovery's")
	set := fs.String("set", "", "recover every retained restore point of this backup set (source/backup-set) into the one destination")
	newestOnly := fs.Bool("newest-only", false, "with --set: only the newest restore point")
	since := fs.String("since", "", "with --set: only restore points updated at or after this date (YYYY-MM-DD or RFC 3339)")
	preflight := fs.Bool("preflight", false, "with --set or several ids: print what would be recovered, what would not and why, and whether it fits; copy nothing")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if *set != "" || len(operands) > 1 {
		return cmdRecoverMany(manyArgs{
			cfgPath: *cfgPath, set: *set, ids: operands, to: *to, copySel: *copySel, ack: *ack, asJSON: *asJSON,
			follow: *follow, newestOnly: *newestOnly, since: *since, preflight: *preflight, ackSource: *ackSource,
		})
	}
	if *newestOnly || *since != "" {
		return usageError("recover: --newest-only and --since need --set")
	}
	if *preflight {
		return usageError("recover: --preflight is for --set or several backup ids")
	}
	if len(operands) != 1 {
		return usageError("recover: expected <source/backup-set/artifact>")
	}
	if _, err := app.ParseArtifactID(operands[0]); err != nil {
		return usageError("recover: %v", err)
	}
	var target service.RecoveryTarget
	if *to != "" {
		var err error
		if target, err = parseRecoverTarget(*to); err != nil {
			return usageError("recover: %v", err)
		}
	}
	if *asJSON && target.Kind == "download" {
		return usageError("recover: --json cannot be combined with --to -, because standard output carries the bytes")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc, cleanup, err := openBackupServiceLoggingTo(ctx, *cfgPath, readsConfig, os.Stderr)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	// Issue #1191: with no --to, the backup goes back where it came from.
	if *to == "" {
		def, why := svc.DefaultRecoveryTarget(operands[0])
		if def.Kind == "" {
			return usageError("recover: --to is required here, because %s: - for standard output, a name under the recovery staging root, or medium:ID[/KEY] for another storage destination", why)
		}
		target = def
	}
	if target.Kind == "source" {
		target.Acknowledged = *ackSource
	}

	logStartup(ctx, obs.New(os.Stderr, obs.LevelFromEnv()), app.BuildVersionInfo(version, commit))

	req := service.RecoveryRequest{ArtifactID: operands[0], Copy: *copySel, Target: target, AcknowledgeCost: *ack}
	sess := service.RecoverySession{Actor: "cli"}
	var out io.Writer
	if target.Kind == "download" {
		out = os.Stdout
	}
	var (
		res  service.RecoveryResult
		thaw *service.RecoveryThaw
	)
	if *follow {
		followWhile(ctx, svc, opByFirstStart(operands[0]), os.Stderr, os.Stderr, func() {
			res, thaw, err = svc.RecoverNow(ctx, sess, req, out)
		})
	} else {
		res, thaw, err = svc.RecoverNow(ctx, sess, req, out)
	}
	if err != nil {
		return reportRecoverError(err, *asJSON)
	}
	if thaw != nil {
		printRecoverThaw(operands[0], res.Copy, thaw, *asJSON)
		return exitRecoverThawStarted
	}
	printRecoverResult(res, *asJSON)
	return exitOK
}

// parseRecoverTarget reads --to.
func parseRecoverTarget(s string) (service.RecoveryTarget, error) {
	switch {
	case s == "":
		return service.RecoveryTarget{}, errors.New("--to is empty")
	case s == "-":
		return service.RecoveryTarget{Kind: "download"}, nil
	case s == "source":
		return service.RecoveryTarget{Kind: "source"}, nil
	case strings.HasPrefix(s, "source:"):
		folder := strings.TrimPrefix(s, "source:")
		if folder == "" {
			return service.RecoveryTarget{}, errors.New("--to source: names no folder; write source, or source:" + transport.RecoveredFolderPrefix + "NAME")
		}
		return service.RecoveryTarget{Kind: "source", Path: folder}, nil
	case strings.HasPrefix(s, "medium:"):
		rest := strings.TrimPrefix(s, "medium:")
		id, key, _ := strings.Cut(rest, "/")
		if id == "" {
			return service.RecoveryTarget{}, errors.New("--to medium: names no destination; write medium:ID or medium:ID/KEY")
		}
		return service.RecoveryTarget{Kind: "storage", Medium: id, Key: key}, nil
	}
	return service.RecoveryTarget{Kind: "staging", Path: s}, nil
}

func reportRecoverError(err error, asJSON bool) int {
	re, ok := service.AsRecoveryError(err)
	if !ok {
		return fail(err)
	}
	if asJSON {
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		body.Error.Code, body.Error.Message = re.Code, re.Message
		_ = json.NewEncoder(os.Stdout).Encode(body)
	} else {
		fmt.Fprintln(os.Stderr, cliecho.Binary+":", re.Message)
	}
	if n, known := recoverExitCodes[re.Code]; known {
		return n
	}
	return recoverExitCodes[service.RecoveryFailed]
}

func printRecoverThaw(artifact, copyID string, thaw *service.RecoveryThaw, asJSON bool) {
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"artifact_id": artifact, "copy": copyID,
			"restore": map[string]string{"operation_id": thaw.OperationID, "wait": thaw.Wait, "billing": thaw.Billing},
		})
		return
	}
	fmt.Fprintf(os.Stderr, "nothing was recovered yet: the copy on %s is on an archive class, so a provider restore was started instead\n", copyID)
	fmt.Fprintf(os.Stderr, "operation:           %s\n", thaw.OperationID)
	if thaw.Wait != "" {
		fmt.Fprintf(os.Stderr, "how long:            %s\n", thaw.Wait)
	}
	if thaw.Billing != "" {
		fmt.Fprintf(os.Stderr, "cost:                %s\n", thaw.Billing)
	}
	fmt.Fprintf(os.Stderr, "run this command again once the copy is readable (`%s status`, or GET /api/v1/operations/%s)\n", cliecho.Binary, thaw.OperationID)
}

func printRecoverResult(res service.RecoveryResult, asJSON bool) {
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"artifact_id": res.ArtifactID, "copy": res.Copy,
			"target": map[string]string{"kind": res.Target.Kind, "path": res.Target.Path, "medium": res.Target.Medium, "key": res.Target.Key},
			"path":   res.Path, "key": res.Key, "bytes": res.Bytes, "sha256": res.SHA256,
		})
		return
	}
	// Standard output carries the bytes for --to -, so the summary goes to
	// standard error in that case and to standard output otherwise.
	dst := os.Stdout
	if res.Target.Kind == "download" {
		dst = os.Stderr
	}
	fmt.Fprintf(dst, "recovered:           %s\n", res.ArtifactID)
	fmt.Fprintf(dst, "copy:                %s\n", res.Copy)
	switch {
	case res.Path != "":
		fmt.Fprintf(dst, "path:                %s\n", res.Path)
	case res.Key != "":
		fmt.Fprintf(dst, "key:                 %s on %s\n", res.Key, res.Target.Medium)
	}
	if res.Target.Kind == "source" {
		fmt.Fprintf(dst, "on:                  the set's source host, in the new folder %s\n", res.Target.Path)
	}
	fmt.Fprintf(dst, "bytes:               %d\n", res.Bytes)
	if res.SHA256 != "" {
		fmt.Fprintf(dst, "sha256:              %s\n", res.SHA256)
	}
}
