package service

import "github.com/retnd/retnd/core/apicontract"

// SSHKeysToWire and SSHDiscoveryToWire are stubs until #1153 lands, so the
// tests written for them fail on an assertion and not on a build error.
func SSHKeysToWire([]SSHKeyListing) apicontract.ListSSHKeysResponse {
	return apicontract.ListSSHKeysResponse{}
}

func SSHDiscoveryToWire(SSHKeyDiscovery) apicontract.ListSSHKeyCandidatesResponse {
	return apicontract.ListSSHKeyCandidatesResponse{}
}
