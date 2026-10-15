package service

import "time"

// AssuranceListing is one backup in the assurance listing.
type AssuranceListing struct {
	ArtifactID   string
	ArtifactName string
	BackupSetID  string
	Buckets      []string
	Content      AssuranceClaim
	Recovery     AssuranceClaim
	Reason       string
}

// AssuranceClaim is one composed claim.
type AssuranceClaim struct {
	State string
	At    time.Time
}

// ListAssurance classifies every backup (stub).
func ListAssurance(as []Artifact, due []AssuranceDueItem, recoveryOff map[string]bool, now time.Time) []AssuranceListing {
	return nil
}
