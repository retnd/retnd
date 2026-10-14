package service

import "github.com/retnd/retnd/core/apicontract"

// The wire objects behind `retnd storage --json`, `retnd medium backends
// --json` and `retnd medium configure --json` (#1154).
//
// Not written yet: these return zero values so the red tests compile and
// fail on what they assert rather than on a missing symbol.

// StorageWire is the contract's ListStorageStatusResponse.
func StorageWire(ManagerStorage, []StorageStatus) apicontract.ListStorageStatusResponse {
	return apicontract.ListStorageStatusResponse{}
}

// BackendsWire is the contract's ListBackendsResponse.
func BackendsWire(BackendCatalog) apicontract.ListBackendsResponse {
	return apicontract.ListBackendsResponse{}
}

// StorageMediumWire is the contract's StorageMediumSummary.
func StorageMediumWire(StorageMediumSummary) apicontract.StorageMediumSummary {
	return apicontract.StorageMediumSummary{}
}

// MediumPreflightWire is the contract's MediumPreflightResponse.
func MediumPreflightWire(MediumPreflight) apicontract.MediumPreflightResponse {
	return apicontract.MediumPreflightResponse{}
}
