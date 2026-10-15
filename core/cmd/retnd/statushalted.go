package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/service"
)

// printHalted is issue #1146: a set the manager could not connect to has to
// look stopped from the terminal, because until it goes STALE the verdict
// above it reads like a slow set.
//
// The timestamp is when the refusal was LAST observed (the journal replaces
// the row on every refused cycle), so the line says "last refused" and not
// "since": the first refusal is not recorded and claiming it would be a
// guess dressed as a fact.
func printHalted(bs health.BackupSetHealth) {
	if bs.HaltReason == "" {
		return
	}
	line := "  halted: " + bs.HaltReason
	if !bs.HaltObservedAt.IsZero() {
		line += " (last refused " + formatActivityTime(bs.HaltObservedAt.UTC()) + ")"
	}
	fmt.Println(line)
	switch bs.HaltReason {
	case state.HaltHostKeyChanged:
		fmt.Println("    the host's SSH key no longer matches the one on record, which can mean somebody is intercepting the connection. Nothing is being backed up.")
		fmt.Println("    if you changed the key yourself, `" + cliecho.Binary + " backup-set patch " + bs.Set.String() + " --host <host> --trust-host-key --acknowledge-host-key-change` trusts the new one.")
	case state.HaltAuthenticationFailed:
		fmt.Println("    the host answered and rejected the login, so no session was opened and nothing is being backed up.")
		fmt.Println("    check the account and the credential this set uses on the remote; `" + cliecho.Binary + " backup-set test-connection " + bs.Set.String() + "` tries it again.")
	case state.HaltKeyPermissions:
		fmt.Println("    the manager never tried the host: the key_file's mode on disk no longer matches what it was written with, so nothing is being backed up.")
		fmt.Println("    fix the permissions on the key_file this set names; `" + cliecho.Binary + " backup-set test-connection " + bs.Set.String() + "` tries again.")
	}
}

// statusProcess is the process half of `status --json`. The API keeps it
// out of GET /system/health on purpose (it is GET /system/version's), so
// the CLI, which has no other verb that reports both, adds it beside the
// wire object under its own key.
type statusProcess struct {
	BinaryVersion string `json:"binary_version"`
	RcloneVersion string `json:"rclone_version"`
}

// statusReportJSON is apicontract.HealthResponse with `process` added, so a
// script that already reads GET /system/health reads this unchanged.
type statusReportJSON struct {
	apicontract.HealthResponse
	Process statusProcess `json:"process"`
}

// printStatusJSON emits the health report and keeps status's exit
// convention: 0 only when every set is HEALTHY.
func printStatusJSON(report health.Report) int {
	out := statusReportJSON{
		HealthResponse: apicontract.HealthResponse{
			GeneratedAt: formatActivityTime(report.GeneratedAt),
			BackupSets:  make([]apicontract.BackupSetHealth, 0, len(report.BackupSets)),
		},
		Process: statusProcess{
			BinaryVersion: report.Process.BinaryVersion,
			RcloneVersion: report.Process.RcloneVersion,
		},
	}
	healthy := true
	for _, bs := range report.BackupSets {
		if !bs.State.OK() {
			healthy = false
		}
		out.BackupSets = append(out.BackupSets, wireBackupSetHealth(service.BackupSetHealthFromReport(bs)))
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fail(err)
	}
	if !healthy {
		return 1
	}
	return 0
}

// wireBackupSetHealth is apps/common/webhost's toBackupSetHealthResponse,
// which is unexported in another module, so this is the second copy of a
// mapping the contract test in statushalted_1146_test.go keeps honest.
func wireBackupSetHealth(bs service.BackupSetHealth) apicontract.BackupSetHealth {
	return apicontract.BackupSetHealth{
		BackupSetID:           bs.BackupSetID,
		SourceName:            bs.SourceName,
		SetName:               bs.SetName,
		State:                 bs.State,
		Reason:                bs.Reason,
		HaltReason:            bs.HaltReason,
		NewestGoodBackupAt:    formatActivityTime(bs.NewestGoodBackupAt),
		LastCompletedBackupAt: formatActivityTime(bs.LastCompletedBackupAt),
		StaleAfterSeconds:     int64(bs.StaleAfter.Seconds()),
		CurrentTransfers:      bs.CurrentTransfers,
		PendingDeletes:        bs.PendingDeletes,
		Failures:              bs.Failures,
		QuarantinedCount:      bs.QuarantinedCount,
		QuarantinedLostCount:  bs.QuarantinedLostCount,

		ReinstatedRemoteRetainedCount: bs.ReinstatedRemoteRetainedCount,
		ReadOnlyRetainedCount:         bs.ReadOnlyRetainedCount,
		FreeBytes:                     bs.FreeBytes,
		FreeBytesKnown:                bs.FreeBytesKnown,
		TotalBytes:                    bs.TotalBytes,
		StorageLevel:                  bs.StorageLevel,

		AwayFromHome:                 bs.Placement.AwayFromHome,
		AwayFromHomeOldestAgeSeconds: int64(bs.Placement.OldestAwayFromHomeAge.Seconds()),
		UnconfirmedLocation:          bs.Placement.UnconfirmedLocation,
		OpenMoves:                    bs.Placement.OpenMoves,
		OpenMoveOldestAgeSeconds:     int64(bs.Placement.OldestOpenMoveAge.Seconds()),
		FailedMoves:                  bs.Placement.FailedMoves,
		FailedMoveOldestAgeSeconds:   int64(bs.Placement.OldestFailedMoveAge.Seconds()),
		FailedMoveReason:             bs.Placement.FailedMoveReason,
	}
}
