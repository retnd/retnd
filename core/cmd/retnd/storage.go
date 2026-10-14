package main

import (
	"context"
	"fmt"
	"os"

	"github.com/retnd/retnd/core/service"
)

// `retnd storage [--json]` (#1154, audit row 3): GET /api/v1/system/storage
// from a terminal.
//
// The capacity reading used to be browser-only. `status` prints each set's
// free bytes and nothing of the manager-wide gauge: the cap, which
// denominator the gauge is drawn against, the headroom before the next
// transfer is refused, which constraint binds, and this manager's own
// consumption. This prints both halves, through the same two service calls
// the handler makes, so a terminal and a dashboard cannot read different
// numbers off one volume.
//
// # Field names
//
// The API's own, so a script reading the text and a client reading the JSON
// share a vocabulary. Byte counts are exact, with a rounded figure beside
// them for a human.
//
// # Exit status
//
// 0 whenever the reading was taken, whatever level it found. This is a
// read, not a health check: `status` is the verb that exits non-zero when
// something is wrong, and a script that wants to branch on the level reads
// it from --json.
//
// # How it opens
//
// The way `settings assurance` does: this host's configuration and journal
// directly, never refused beside a running engine, with the log on stderr
// so stdout is the reading and nothing else.
func cmdStorage(args []string) int {
	fs, cfgPath := newFlagSet("storage")
	asJSON := fs.Bool("json", false, "emit JSON, the same body GET /api/v1/system/storage serves, instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("storage takes no argument; %q is one", operands[0])
	}

	ctx := context.Background()
	svc, cleanup, err := openBackupServiceLoggingTo(ctx, *cfgPath, readsConfig, os.Stderr)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	// All or nothing, as the handler is: half a reading would leave the
	// reader deciding what the missing half meant, which on a capacity
	// figure is the guess this verb exists to stop.
	manager, err := svc.ManagerStorage(ctx)
	if err != nil {
		return fail(fmt.Errorf("could not assess storage capacity: %w", err))
	}
	statuses, err := svc.ListStorageStatus(ctx)
	if err != nil {
		return fail(fmt.Errorf("could not assess storage capacity: %w", err))
	}

	if *asJSON {
		return printMediumJSON(service.StorageWire(manager, statuses))
	}
	printManagerStorage(manager)
	printStorageStatuses(statuses)
	return 0
}

func printManagerStorage(m service.ManagerStorage) {
	fmt.Println("manager")
	fmt.Printf("  known: %t\n", m.Known)
	if !m.Known {
		fmt.Printf("  unknown_reason: %s\n", m.UnknownReason)
		fmt.Printf("  measured_path: %s\n", m.MeasuredPath)
		fmt.Println("  no capacity reading could be taken, which is not the same as a volume with nothing on it")
		return
	}
	fmt.Printf("  measured_path: %s\n", m.MeasuredPath)
	fmt.Printf("  total_bytes: %s\n", bytesWithSize(m.TotalBytes))
	fmt.Printf("  free_bytes: %s\n", bytesWithSize(m.FreeBytes))
	fmt.Printf("  available_bytes: %s\n", bytesWithSize(m.AvailableBytes))
	if m.CatalogBytesKnown {
		fmt.Printf("  catalog_bytes: %s\n", bytesWithSize(m.CatalogBytes))
	} else {
		fmt.Println("  catalog_bytes: not known")
	}
	if m.OtherBytesKnown {
		fmt.Printf("  other_bytes: %s\n", bytesWithSize(m.OtherBytes))
	} else {
		fmt.Println("  other_bytes: not known")
	}
	if m.CapBytes == 0 {
		fmt.Println("  cap_bytes: 0 (no cap: the whole volume is the allowance)")
	} else {
		fmt.Printf("  cap_bytes: %s\n", bytesWithSize(m.CapBytes))
	}
	fmt.Printf("  denominator: %s\n", m.Denominator)
	fmt.Printf("  limit_bytes: %s\n", bytesWithSize(m.LimitBytes))
	fmt.Printf("  used_bytes: %s\n", bytesWithSize(m.UsedBytes))
	fmt.Printf("  headroom_bytes: %s\n", bytesWithSize(m.HeadroomBytes))
	fmt.Printf("  binding_constraint: %s\n", m.BindingConstraint)
	fmt.Printf("  warning_free_bytes: %s\n", bytesWithSize(m.WarningFreeBytes))
	fmt.Printf("  critical_free_bytes: %s\n", bytesWithSize(m.CriticalFreeBytes))
	fmt.Printf("  level: %s\n", m.Level)
}

func printStorageStatuses(statuses []service.StorageStatus) {
	fmt.Println("backup sets")
	if len(statuses) == 0 {
		fmt.Println("  none configured")
		return
	}
	for _, s := range statuses {
		fmt.Printf("%s\n", s.BackupSetID)
		fmt.Printf("  local_path: %s\n", s.LocalPath)
		fmt.Printf("  available: %t\n", s.Available)
		if !s.Available {
			fmt.Printf("  unavailable_reason: %s\n", s.UnavailableReason)
			continue
		}
		fmt.Printf("  total_bytes: %s\n", bytesWithSize(s.TotalBytes))
		fmt.Printf("  free_bytes: %s\n", bytesWithSize(s.FreeBytes))
		fmt.Printf("  available_bytes: %s\n", bytesWithSize(s.AvailableBytes))
		fmt.Printf("  warning_free_bytes: %s\n", bytesWithSize(s.WarningFreeBytes))
		fmt.Printf("  critical_free_bytes: %s\n", bytesWithSize(s.CriticalFreeBytes))
		fmt.Printf("  level: %s\n", s.Level)
	}
}

// bytesWithSize renders an exact byte count with a rounded figure beside it
// once it is big enough for the rounding to help a human. The exact count
// comes first and is what a script reads.
func bytesWithSize(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%d (%.1f %ciB)", n, float64(n)/float64(div), "KMGTPE"[exp])
}
