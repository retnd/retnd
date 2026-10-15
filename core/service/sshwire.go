package service

import (
	"time"

	"github.com/retnd/retnd/core/apicontract"
)

// The key store and the key scan, as the API serves them (issue #1153).
//
// They live here, and not in the handlers beside the router, because two
// callers print the same object: the engine's GET /ssh-keys and
// GET /ssh/key-candidates, and `retnd ssh-key list --json` and
// `ssh-key discover --json` on a terminal with no engine to ask. A
// conversion each side wrote for itself is two answers that agree until the
// day somebody adds a field to one of them, which is how --json would stop
// being "the wire object". apps/common/webhost has a test that diffs its
// handlers against these.
//
// Nothing here can carry key material or a server-side path for a stored
// key: SSHKeyListing has neither, and the candidate row's path is the one
// the scan was built to report.

// SSHKeysToWire is the GET /ssh-keys body for a listing.
//
// imported_at is RFC 3339 in UTC, or "" when it is not known, and never
// omitted. used_by is never null: "used by nothing" is an answer, and a
// client that could not tell it from an absent field would render the two
// the same way.
func SSHKeysToWire(keys []SSHKeyListing) apicontract.ListSSHKeysResponse {
	out := apicontract.ListSSHKeysResponse{Keys: make([]apicontract.SSHKey, 0, len(keys))}
	for _, key := range keys {
		imported := ""
		if !key.ImportedAt.IsZero() {
			imported = key.ImportedAt.UTC().Format(time.RFC3339)
		}
		usedBy := key.UsedBy
		if usedBy == nil {
			usedBy = []string{}
		}
		out.Keys = append(out.Keys, apicontract.SSHKey{
			ID:                  key.ID,
			Algorithm:           key.Algorithm,
			Fingerprint:         key.Fingerprint,
			PublicKey:           key.PublicKey,
			ImportedAt:          imported,
			PassphraseProtected: key.PassphraseProtected,
			Problem:             key.Problem,
			UsedBy:              usedBy,
		})
	}
	return out
}

// SSHDiscoveryToWire is the GET /ssh/key-candidates body for a scan. The
// locations travel beside the candidates, so an empty candidate list always
// arrives with the places that were looked in.
func SSHDiscoveryToWire(found SSHKeyDiscovery) apicontract.ListSSHKeyCandidatesResponse {
	out := apicontract.ListSSHKeyCandidatesResponse{
		Locations:  make([]apicontract.SSHKeyDiscoveryLocation, 0, len(found.Locations)),
		Candidates: make([]apicontract.SSHKeyCandidate, 0, len(found.Candidates)),
	}
	for _, loc := range found.Locations {
		out.Locations = append(out.Locations, apicontract.SSHKeyDiscoveryLocation{
			Path: loc.Path, Kind: loc.Kind, Found: loc.Found, Problem: loc.Problem,
		})
	}
	for _, c := range found.Candidates {
		out.Candidates = append(out.Candidates, apicontract.SSHKeyCandidate{
			ID: c.ID, Path: c.Path, Location: c.Location,
			Algorithm: c.Algorithm, Fingerprint: c.Fingerprint, PublicKey: c.PublicKey,
			Mode: c.Mode, InStore: c.InStore, InStoreID: c.InStoreID,
			Selectable: c.Selectable, Reason: c.Reason,
		})
	}
	return out
}
