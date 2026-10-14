package service

import "github.com/retnd/retnd/core/apicontract"

// ArtifactWire is the contract's Artifact for one backup.
func ArtifactWire(a Artifact) apicontract.Artifact {
	return apicontract.Artifact{}
}

// ArtifactsWire is the contract's ListArtifactsResponse for a listing.
func ArtifactsWire(as []Artifact) apicontract.ListArtifactsResponse {
	return apicontract.ListArtifactsResponse{}
}
