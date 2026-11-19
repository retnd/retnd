package apicontract

// The values RecoveryTarget.Kind takes.
//
// The contract document declares them as an enum, but the generated Go
// type is a plain string, so before #1253's review the recover builders in
// core/cliecho switched on literals and the canary corpus kept its own
// copy of the same four words. Both now read these, and
// TestRecoveryTargetKindsAreTheContractsEnum holds the slice to the
// document's enum, so a fifth kind added there fails here first rather
// than reaching no arm of either builder.
const (
	// RecoveryTargetDownload streams the bytes to the caller that
	// redeems the grant.
	RecoveryTargetDownload = "download"

	// RecoveryTargetStaging places the file under the recovery staging
	// root.
	RecoveryTargetStaging = "staging"

	// RecoveryTargetStorage uploads to another configured storage
	// destination.
	RecoveryTargetStorage = "storage"

	// RecoveryTargetSource writes into a new folder on the backup set's
	// own source host (#1191).
	RecoveryTargetSource = "source"
)

// RecoveryTargetKinds is every RecoveryTarget.Kind, in the contract's
// order.
var RecoveryTargetKinds = []string{
	RecoveryTargetDownload,
	RecoveryTargetStaging,
	RecoveryTargetStorage,
	RecoveryTargetSource,
}
